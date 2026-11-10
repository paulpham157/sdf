package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

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
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		closeAfter, err := s.handleRequest(conn, reader, writer)
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

func (s *Server) handleRequest(conn net.Conn, reader *bufio.Reader, writer *bufio.Writer) (bool, error) {
	_ = conn.SetReadDeadline(time.Time{})
	method, path, headers, err := readHeaders(reader)
	if err != nil {
		return true, err
	}
	closeAfter := connectionClose(headers)
	path = strings.SplitN(path, "?", 2)[0]
	if expect100(headers) {
		fmt.Fprintf(writer, "HTTP/1.1 100 Continue\r\n\r\n")
		if err := writer.Flush(); err != nil {
			return true, err
		}
	}
	switch method {
	case "GET":
		if path != "/health" {
			writeResponse(writer, 404, "text/plain", []byte("not found"), closeAfter)
			s.logRequest(method, path, 404)
			return closeAfter, nil
		}
		writeResponse(writer, 200, "application/json", []byte(`{"status":"ok"}`), closeAfter)
		s.logRequest(method, path, 200)
		return closeAfter, nil
	case "POST":
		signal, ok := signals[path]
		if !ok {
			writeResponse(writer, 404, "text/plain", []byte("not found"), closeAfter)
			s.logRequest(method, path, 404)
			return closeAfter, nil
		}
		body, err := readBody(reader, headers)
		if err != nil {
			status := 400
			msg := "invalid chunked body"
			if bs, ok := err.(*bodyStatus); ok {
				status = bs.status
				msg = bs.message
				if bs.status == 400 && (strings.Contains(bs.message, "chunk") || bs.message == "incomplete body" || bs.message == "unsupported transfer encoding") {
					closeAfter = true
				}
				if bs.message == "invalid chunk size" || bs.message == "invalid chunked body" || bs.message == "incomplete chunked body" || bs.message == "payload too large" || bs.message == "unsupported transfer encoding" || bs.message == "incomplete body" {
					closeAfter = true
				}
			}
			writeResponse(writer, status, "text/plain", []byte(msg), closeAfter)
			s.logRequest(method, path, status)
			return true, nil
		}
		contentType := headerGet(headers, "Content-Type")
		records, err := otlp.Decode(signal, body, contentType)
		if err != nil {
			writeResponse(writer, 400, "text/plain", []byte("could not parse OTLP payload"), closeAfter)
			s.logRequest(method, path, 400)
			return closeAfter, nil
		}
		if _, err := s.Store.Insert(records); err != nil {
			writeResponse(writer, 500, "text/plain", []byte("store error"), true)
			s.logRequest(method, path, 500)
			return true, nil
		}
		if isJSON(contentType) {
			writeResponse(writer, 200, "application/json", []byte("{}"), closeAfter)
		} else {
			writeResponse(writer, 200, "application/x-protobuf", []byte{}, closeAfter)
		}
		s.logRequest(method, path, 200)
		return closeAfter, nil
	default:
		writeResponse(writer, 501, "text/plain", []byte("unsupported method"), true)
		return true, nil
	}
}

func (s *Server) logRequest(method, path string, status int) {
	if s.Quiet {
		return
	}
	fmt.Fprintf(s.log, "%s %s %d\n", method, path, status)
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

func connectionClose(headers []header) bool {
	return strings.EqualFold(strings.TrimSpace(headerGet(headers, "Connection")), "close")
}

func expect100(headers []header) bool {
	return strings.EqualFold(strings.TrimSpace(headerGet(headers, "Expect")), "100-continue")
}

func readBody(r *bufio.Reader, headers []header) ([]byte, error) {
	var encodings []string
	for _, part := range strings.Split(headerGet(headers, "Transfer-Encoding"), ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			encodings = append(encodings, part)
		}
	}
	if len(encodings) > 0 {
		if len(encodings) != 1 || encodings[0] != "chunked" {
			return nil, &bodyStatus{400, "unsupported transfer encoding"}
		}
		return readChunked(r)
	}
	lengthText := headerGet(headers, "Content-Length")
	if strings.TrimSpace(lengthText) == "" {
		lengthText = "0"
	}
	length, err := strconv.Atoi(strings.TrimSpace(lengthText))
	if err != nil {
		return nil, &bodyStatus{400, "invalid content length"}
	}
	if length < 0 || length > otlp.MaxBodyBytes {
		return nil, &bodyStatus{413, "payload too large"}
	}
	if length == 0 {
		return []byte{}, nil
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, &bodyStatus{400, "incomplete body"}
	}
	return body, nil
}

func readChunked(r *bufio.Reader) ([]byte, error) {
	var body []byte
	for {
		line, err := readLineLimited(r, maxChunkLine)
		if err != nil {
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
		if size > otlp.MaxBodyBytes || int64(len(body))+size > otlp.MaxBodyBytes {
			return nil, &bodyStatus{413, "payload too large"}
		}
		chunk := make([]byte, int(size))
		if _, err := io.ReadFull(r, chunk); err != nil {
			return nil, &bodyStatus{400, "incomplete chunked body"}
		}
		body = append(body, chunk...)
		delim := make([]byte, 2)
		if _, err := io.ReadFull(r, delim); err != nil || string(delim) != "\r\n" {
			return nil, &bodyStatus{400, "invalid chunked body"}
		}
	}
}

func discardTrailers(r *bufio.Reader) error {
	seen := 0
	for {
		line, err := readLineLimited(r, maxChunkLine)
		if err != nil {
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

func writeResponse(w *bufio.Writer, status int, contentType string, body []byte, closeConn bool) {
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\n", status, reason(status))
	fmt.Fprintf(w, "Content-Type: %s\r\n", contentType)
	fmt.Fprintf(w, "Content-Length: %d\r\n", len(body))
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
	case 400:
		return "Bad Request"
	case 404:
		return "Not Found"
	case 413:
		return "Content Too Large"
	case 500:
		return "Internal Server Error"
	case 501:
		return "Not Implemented"
	default:
		return "Error"
	}
}

func isJSON(contentType string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	return strings.ToLower(strings.TrimSpace(media)) == "application/json"
}
