package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/httphost"
	"github.com/paulpham157/sdf/internal/netlimit"
	"github.com/paulpham157/sdf/internal/otlp"
)

const (
	maxChunkLine = 4096
	maxTrailers  = 65_536
	maxHeaders   = 1 << 20
)

var signals = map[string]string{
	"/v1/traces":  "traces",
	"/v1/metrics": "metrics",
	"/v1/logs":    "logs",
}

type bodyStatus struct {
	status  int
	message string
}

func (e *bodyStatus) Error() string { return e.message }

func (s *Server) serveHTTP(ln net.Listener) {
	defer s.httpWG.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s.httpWG.Add(1)
		go func(c net.Conn) {
			defer s.httpWG.Done()
			s.handleConn(c)
		}(conn)
	}
}

// maxRequestsPerConn and maxConnAge bound a keep-alive connection.
// A client that sends another request just inside the idle timeout would
// otherwise hold a slot for as long as it wants. On loopback the
// per-address cap is the whole listener, so that slot is the global cap.
const (
	maxRequestsPerConn = 1000
	maxConnAge         = 10 * time.Minute
)

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	defer s.untrack(conn)
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	started := time.Now()
	first := true
	var n int
	for {
		if err := s.trackReading(conn); err != nil {
			return
		}
		if err := s.armHeaderRead(conn, reader, first); err != nil {
			return
		}
		first = false
		n++
		closeAfter, err := s.handleRequest(conn, reader, writer, n, started)
		if err != nil {
			return
		}
		if err := writer.Flush(); err != nil {
			return
		}
		if closeAfter {
			return
		}
	}
}

// armHeaderRead sets the deadline for the next request's headers.
// The first request on a connection has ReadHeaderTimeout from the start.
// A later request may sit idle for IdleTimeout; once its first byte
// arrives, the rest of the headers have ReadHeaderTimeout. The deadline
// is not refreshed per byte, so a slow drip cannot hold the connection.
func (s *Server) armHeaderRead(conn net.Conn, reader *bufio.Reader, first bool) error {
	headerWait := s.lim.ReadHeaderTimeout
	if headerWait <= 0 {
		headerWait = netlimit.DefaultReadHeaderTimeout
	}
	if first {
		return conn.SetReadDeadline(time.Now().Add(headerWait))
	}
	idle := s.lim.IdleTimeout
	if idle <= 0 {
		idle = netlimit.DefaultIdleTimeout
	}
	if err := conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
		return err
	}
	if _, err := reader.ReadByte(); err != nil {
		return err
	}
	if err := reader.UnreadByte(); err != nil {
		return err
	}
	return conn.SetReadDeadline(time.Now().Add(headerWait))
}

func (s *Server) handleRequest(conn net.Conn, reader *bufio.Reader, writer *bufio.Writer, requests int, started time.Time) (bool, error) {
	method, path, headers, err := readHeaders(reader)
	if err != nil {
		return true, err
	}
	bodyWait := s.lim.ReadTimeout
	if bodyWait <= 0 {
		bodyWait = netlimit.DefaultReadTimeout
	}
	if err := conn.SetReadDeadline(time.Now().Add(bodyWait)); err != nil {
		return true, err
	}
	closeAfter := connectionClose(headers)
	if requests >= maxRequestsPerConn || time.Since(started) >= maxConnAge {
		closeAfter = true
	}
	path = strings.SplitN(path, "?", 2)[0]
	needsAuth := s.requiresAuth(method, path)
	authed := needsAuth && s.authorizedBearer(headerGet(headers, "Authorization"))
	// With a token set, an unauthenticated response does not stay open.
	// Health checks and 404s would otherwise hold a connection up to the
	// idle timeout, and on loopback that is the whole listener.
	if s.Token != "" && !authed {
		closeAfter = true
	}
	// Host and Origin are checked before the bearer token. A matching token
	// does not make a browser post or a rebound Host acceptable. Neither
	// refusal reads the body or reserves budget.
	if status, msg, refuse := s.refusal(headers); refuse {
		return s.refuse(conn, reader, writer, method, path, status, msg)
	}
	if needsAuth && !authed {
		s.reply(conn, writer, 401, "text/plain", []byte("unauthorized"), true)
		s.logRequest(method, path, 401)
		return true, nil
	}
	var releaseToken func()
	if needsAuth {
		rel, ok := s.tokens.Acquire()
		if !ok {
			s.reply(conn, writer, 429, "text/plain", []byte(msgTooManyToken), true)
			s.logRequest(method, path, 429)
			return true, nil
		}
		releaseToken = rel
	}
	if releaseToken != nil {
		defer releaseToken()
	}
	hold := s.budget.Hold()
	defer hold.Release()
	switch method {
	case "GET":
		if path != "/health" {
			s.reply(conn, writer, 404, "text/plain", []byte("not found"), closeAfter)
			s.logRequest(method, path, 404)
			return closeAfter, nil
		}
		s.reply(conn, writer, 200, "application/json", []byte(`{"status":"ok"}`), closeAfter)
		s.logRequest(method, path, 200)
		return closeAfter, nil
	case "POST":
		if path != "/v1/sessions" {
			if _, ok := signals[path]; !ok {
				s.reply(conn, writer, 404, "text/plain", []byte("not found"), closeAfter)
				s.logRequest(method, path, 404)
				return closeAfter, nil
			}
		}
		var plan bodyPlan
		if err := s.acceptBody(headers, hold, &plan); err != nil {
			return s.writeBodyError(conn, writer, method, path, err, closeAfter)
		}
		if expect100(headers) {
			if err := s.writeContinue(conn, writer); err != nil {
				return true, err
			}
		}
		if path == "/v1/sessions" {
			return s.handleSession(conn, reader, writer, headers, method, path, closeAfter, &plan, hold)
		}
		signal := signals[path]
		body, err := s.readBody(reader, &plan, hold)
		if err != nil {
			return s.writeBodyError(conn, writer, method, path, err, closeAfter)
		}
		contentType := headerGet(headers, "Content-Type")
		media, ok := otlp.HTTPMedia(contentType)
		if !ok {
			s.reply(conn, writer, 415, "text/plain", []byte("unsupported media type"), closeAfter)
			s.logRequest(method, path, 415)
			return closeAfter, nil
		}
		body, err = s.decodeContentEncoding(body, headers, hold)
		if err != nil {
			return s.writeBodyError(conn, writer, method, path, err, closeAfter)
		}
		if len(body) > s.lim.MaxBodyBytes {
			s.reply(conn, writer, 413, "text/plain", []byte(msgTooLarge), true)
			s.logRequest(method, path, 413)
			return true, nil
		}
		records, err := otlp.DecodeLimited(signal, body, contentType, s.lim.MaxBodyBytes)
		if err != nil {
			s.reply(conn, writer, 400, "text/plain", []byte("could not parse OTLP payload"), closeAfter)
			s.logRequest(method, path, 400)
			return closeAfter, nil
		}
		// Commit before the 200. The body is "{}" or empty protobuf with
		// partial_success unset, which is full success. A cap is a 4xx or
		// 5xx above, never a 200, so the client retries it.
		if err := s.beginHTTPCommit(conn); err != nil {
			s.reply(conn, writer, 503, "text/plain", []byte(msgStopping), true)
			s.logRequest(method, path, 503)
			return true, nil
		}
		if err := s.insertRecords(records); err != nil {
			s.reply(conn, writer, 500, "text/plain", []byte("store error"), true)
			s.logRequest(method, path, 500)
			return true, nil
		}
		if s.blockAfterInsert != nil {
			s.blockAfterInsert(s.ctx)
		}
		if media == "application/json" {
			s.reply(conn, writer, 200, "application/json", []byte("{}"), closeAfter)
		} else {
			s.reply(conn, writer, 200, "application/x-protobuf", []byte{}, closeAfter)
		}
		s.logRequest(method, path, 200)
		return closeAfter, nil
	default:
		s.reply(conn, writer, 501, "text/plain", []byte("unsupported method"), true)
		return true, nil
	}
}

// refusal is a browser or rebinding request. Origin is refused on every
// bind. Host is refused only while the listener is loopback, because a
// wider bind is an address the exporter was given and this process does
// not know which name that client will send.
func (s *Server) refusal(headers []header) (int, string, bool) {
	if httphost.Loopback(s.httpHost) && !httphost.Allowed(headerGet(headers, "Host"), s.httpPort, s.httpHost) {
		return 403, "forbidden", true
	}
	if headerPresent(headers, "Origin") {
		return 403, "forbidden", true
	}
	return 0, "", false
}

// refuseDrain is how much of an unread body a refusal may drop.
// It is not charged to the byte budget and it is not allocated from
// Content-Length. A larger body is left unread and the connection closes.
const refuseDrain = 64 << 10

// refuse answers a forbidden Host or Origin. It does not reserve budget
// and does not allocate the body: a large Content-Length with no body
// would otherwise hold the in-flight cap until the read timeout. A small
// amount already in the buffer is discarded so a client that sent a short
// body can still read the response. The connection is then closed.
func (s *Server) refuse(conn net.Conn, reader *bufio.Reader, writer *bufio.Writer, method, path string, status int, msg string) (bool, error) {
	s.reply(conn, writer, status, "text/plain", []byte(msg), true)
	s.logRequest(method, path, status)
	// Send the refusal before the short drain, so a client that already
	// wrote a small body can read it while the rest is discarded.
	if err := writer.Flush(); err != nil {
		return true, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	_, _ = io.CopyN(io.Discard, reader, refuseDrain)
	return true, nil
}

func (s *Server) writeBodyError(conn net.Conn, writer *bufio.Writer, method, path string, err error, closeAfter bool) (bool, error) {
	status := 400
	msg := "invalid chunked body"
	if bs, ok := err.(*bodyStatus); ok {
		status = bs.status
		msg = bs.message
		if bs.status == 400 && (strings.Contains(bs.message, "chunk") || bs.message == "incomplete body" || bs.message == "unsupported transfer encoding" || bs.message == "invalid gzip body") {
			closeAfter = true
		}
		if bs.message == "invalid chunk size" || bs.message == "invalid chunked body" || bs.message == "incomplete chunked body" || bs.message == msgTooLarge || bs.message == "unsupported transfer encoding" || bs.message == "incomplete body" {
			closeAfter = true
		}
		if bs.status == 413 || bs.status == 429 || bs.status == 503 {
			closeAfter = true
		}
	} else if isTimeout(err) {
		status = 503
		msg = msgReadTimeout
		closeAfter = true
	}
	s.reply(conn, writer, status, "text/plain", []byte(msg), closeAfter)
	s.logRequest(method, path, status)
	// Close only when the response says Connection: close. A 415 still has a
	// fully read body, and closing without that header makes the client reuse
	// the socket and see a reset.
	return closeAfter, nil
}

func (s *Server) decodeContentEncoding(body []byte, headers []header, hold *netlimit.Hold) ([]byte, error) {
	var codings []string
	for _, h := range headers {
		if !strings.EqualFold(h.name, "Content-Encoding") {
			continue
		}
		for _, part := range strings.Split(h.value, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part != "" {
				codings = append(codings, part)
			}
		}
	}
	// One gzip layer is the encoding exporters send. More than one
	// non-identity coding is rejected so a stack of gzip members cannot
	// expand on the way in. identity is ignored.
	var compressed int
	for _, coding := range codings {
		if coding != "identity" {
			compressed++
		}
	}
	if compressed > 1 {
		return nil, &bodyStatus{415, "unsupported content encoding"}
	}
	// Content-Encoding lists codings in the order they were applied.
	for i := len(codings) - 1; i >= 0; i-- {
		switch codings[i] {
		case "identity":
			continue
		case "gzip":
			next, err := s.gunzipLimited(body, hold)
			if err != nil {
				return nil, err
			}
			body = next
		default:
			return nil, &bodyStatus{415, "unsupported content encoding"}
		}
	}
	return body, nil
}

// gunzipLimited inflates body and counts the inflated bytes on hold.
// The compressed bytes stay reserved until inflation finishes, so the
// peak is both buffers. A failure leaves the hold for the caller to
// release. The scratch buffer is small and is not part of the budget.
func (s *Server) gunzipLimited(body []byte, hold *netlimit.Hold) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, &bodyStatus{400, "invalid gzip body"}
	}
	defer zr.Close()
	var out []byte
	buf := make([]byte, 8192)
	for {
		n, err := zr.Read(buf)
		if n > 0 {
			if int64(len(out))+int64(n) > int64(s.lim.MaxBodyBytes) {
				return nil, &bodyStatus{413, msgTooLarge}
			}
			if !hold.Add(int64(n)) {
				return nil, &bodyStatus{503, msgInFlight}
			}
			out = append(out, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, &bodyStatus{400, "invalid gzip body"}
		}
	}
	hold.Sub(int64(len(body)))
	if out == nil {
		out = []byte{}
	}
	return out, nil
}

func (s *Server) logRequest(method, path string, status int) {
	if s.Quiet {
		return
	}
	fmt.Fprintf(s.log, "%q %q %d\n", method, path, status)
}

func readHeaders(r *bufio.Reader) (method, target string, headers []header, err error) {
	line, err := readLineLimited(r, maxHeaders)
	if err != nil {
		return "", "", nil, err
	}
	if !strings.HasSuffix(line, "\n") {
		return "", "", nil, io.ErrUnexpectedEOF
	}
	parts := strings.Fields(strings.TrimRight(line, "\r\n"))
	if len(parts) < 2 {
		return "", "", nil, io.ErrUnexpectedEOF
	}
	method, target = parts[0], parts[1]
	total := len(line)
	for {
		line, err = readLineLimited(r, maxChunkLine)
		if err != nil {
			return "", "", nil, err
		}
		total += len(line)
		if total > maxHeaders {
			return "", "", nil, io.ErrUnexpectedEOF
		}
		if line == "\r\n" || line == "\n" {
			return method, target, headers, nil
		}
		if !strings.HasSuffix(line, "\n") {
			return "", "", nil, io.ErrUnexpectedEOF
		}
		trimmed := strings.TrimRight(line, "\r\n")
		name, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		headers = append(headers, header{name: name, value: strings.TrimSpace(value)})
	}
}

type header struct {
	name  string
	value string
}

func headerGet(headers []header, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.name, name) {
			return h.value
		}
	}
	return ""
}

func headerPresent(headers []header, name string) bool {
	for _, h := range headers {
		if strings.EqualFold(h.name, name) {
			return true
		}
	}
	return false
}

func connectionClose(headers []header) bool {
	return strings.EqualFold(strings.TrimSpace(headerGet(headers, "Connection")), "close")
}

func expect100(headers []header) bool {
	return strings.EqualFold(strings.TrimSpace(headerGet(headers, "Expect")), "100-continue")
}

// bodyPlan is the framing decision for one request. acceptBody reserves
// a known Content-Length before any byte of the body is buffered.
// Chunked bodies reserve each chunk as it is read.
type bodyPlan struct {
	chunked bool
	length  int
}

func bodyFraming(headers []header) (chunked bool, length int, err error) {
	var encodings []string
	for _, part := range strings.Split(headerGet(headers, "Transfer-Encoding"), ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			encodings = append(encodings, part)
		}
	}
	if len(encodings) > 0 {
		if len(encodings) != 1 || encodings[0] != "chunked" {
			return false, 0, &bodyStatus{400, "unsupported transfer encoding"}
		}
		return true, 0, nil
	}
	lengthText := headerGet(headers, "Content-Length")
	if strings.TrimSpace(lengthText) == "" {
		lengthText = "0"
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(lengthText))
	if convErr != nil {
		return false, 0, &bodyStatus{400, "invalid content length"}
	}
	return false, n, nil
}

func (s *Server) acceptBody(headers []header, hold *netlimit.Hold, plan *bodyPlan) error {
	chunked, length, err := bodyFraming(headers)
	if err != nil {
		return err
	}
	plan.chunked = chunked
	plan.length = length
	if chunked {
		return nil
	}
	if length < 0 || length > s.lim.MaxBodyBytes {
		return &bodyStatus{413, msgTooLarge}
	}
	if length > 0 && !hold.Add(int64(length)) {
		return &bodyStatus{503, msgInFlight}
	}
	return nil
}

func (s *Server) readBody(r *bufio.Reader, plan *bodyPlan, hold *netlimit.Hold) ([]byte, error) {
	if plan.chunked {
		return s.readChunked(r, hold)
	}
	if plan.length == 0 {
		return []byte{}, nil
	}
	body := make([]byte, plan.length)
	if _, err := io.ReadFull(r, body); err != nil {
		if isTimeout(err) {
			return nil, &bodyStatus{503, msgReadTimeout}
		}
		return nil, &bodyStatus{400, "incomplete body"}
	}
	return body, nil
}

func (s *Server) readChunked(r *bufio.Reader, hold *netlimit.Hold) ([]byte, error) {
	var body []byte
	max := int64(s.lim.MaxBodyBytes)
	for {
		line, err := readLineLimited(r, maxChunkLine)
		if err != nil {
			if isTimeout(err) {
				return nil, &bodyStatus{503, msgReadTimeout}
			}
			return nil, &bodyStatus{400, "invalid chunked body"}
		}
		if !strings.HasSuffix(line, "\n") {
			return nil, &bodyStatus{400, "invalid chunked body"}
		}
		sizeToken := strings.TrimSpace(strings.SplitN(strings.TrimRight(line, "\r\n"), ";", 2)[0])
		size, err := strconv.ParseInt(sizeToken, 16, 64)
		if err != nil {
			return nil, &bodyStatus{400, "invalid chunk size"}
		}
		if size < 0 {
			return nil, &bodyStatus{400, "invalid chunk size"}
		}
		if size == 0 {
			if err := discardTrailers(r); err != nil {
				return nil, err
			}
			return body, nil
		}
		if size > max || int64(len(body)) > max-size {
			return nil, &bodyStatus{413, msgTooLarge}
		}
		if !hold.Add(size) {
			return nil, &bodyStatus{503, msgInFlight}
		}
		chunk := make([]byte, int(size))
		if _, err := io.ReadFull(r, chunk); err != nil {
			if isTimeout(err) {
				return nil, &bodyStatus{503, msgReadTimeout}
			}
			return nil, &bodyStatus{400, "incomplete chunked body"}
		}
		body = append(body, chunk...)
		delim := make([]byte, 2)
		if _, err := io.ReadFull(r, delim); err != nil || string(delim) != "\r\n" {
			if isTimeout(err) {
				return nil, &bodyStatus{503, msgReadTimeout}
			}
			return nil, &bodyStatus{400, "invalid chunked body"}
		}
	}
}

func discardTrailers(r *bufio.Reader) error {
	seen := 0
	for {
		line, err := readLineLimited(r, maxChunkLine)
		if err != nil {
			if isTimeout(err) {
				return &bodyStatus{503, msgReadTimeout}
			}
			return &bodyStatus{400, "invalid chunked body"}
		}
		seen += len(line)
		if seen > maxTrailers || !strings.HasSuffix(line, "\n") {
			return &bodyStatus{400, "invalid chunked body"}
		}
		if line == "\r\n" || line == "\n" {
			return nil
		}
	}
}

func readLineLimited(r *bufio.Reader, max int) (string, error) {
	var buf []byte
	for len(buf) < max {
		b, err := r.ReadByte()
		if err != nil {
			if len(buf) > 0 && err == io.EOF {
				return string(buf), nil
			}
			return string(buf), err
		}
		buf = append(buf, b)
		if b == '\n' {
			return string(buf), nil
		}
	}
	return string(buf), nil
}

func (s *Server) writeContinue(conn net.Conn, w *bufio.Writer) error {
	budget := s.lim.ReadTimeout
	if budget <= 0 {
		budget = netlimit.DefaultReadTimeout
	}
	_ = conn.SetWriteDeadline(time.Now().Add(budget))
	if _, err := fmt.Fprintf(w, "HTTP/1.1 100 Continue\r\n\r\n"); err != nil {
		return err
	}
	return w.Flush()
}

func (s *Server) reply(conn net.Conn, w *bufio.Writer, status int, contentType string, body []byte, closeConn bool) {
	budget := s.lim.ReadTimeout
	if budget <= 0 {
		budget = netlimit.DefaultReadTimeout
	}
	if conn != nil {
		_ = conn.SetWriteDeadline(time.Now().Add(budget))
	}
	writeResponse(w, status, contentType, body, closeConn)
}

func writeResponse(w *bufio.Writer, status int, contentType string, body []byte, closeConn bool) {
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\n", status, reason(status))
	fmt.Fprintf(w, "Content-Type: %s\r\n", contentType)
	fmt.Fprintf(w, "Content-Length: %d\r\n", len(body))
	if status == 401 {
		fmt.Fprintf(w, "WWW-Authenticate: Bearer\r\n")
	}
	// 429 and 503 are the OTLP retryable overload statuses. Retry-After
	// is delay-seconds. 413 is not retryable and does not get the header.
	if status == 429 || status == 503 {
		fmt.Fprintf(w, "Retry-After: %d\r\n", int(netlimit.RetryAfter/time.Second))
	}
	if closeConn {
		fmt.Fprintf(w, "Connection: close\r\n")
	}
	fmt.Fprintf(w, "\r\n")
	_, _ = w.Write(body)
}

func reason(status int) string {
	switch status {
	case 200:
		return "OK"
	case 401:
		return "Unauthorized"
	case 400:
		return "Bad Request"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 413:
		return "Content Too Large"
	case 415:
		return "Unsupported Media Type"
	case 429:
		return "Too Many Requests"
	case 500:
		return "Internal Server Error"
	case 503:
		return "Service Unavailable"
	case 501:
		return "Not Implemented"
	default:
		return "Error"
	}
}
