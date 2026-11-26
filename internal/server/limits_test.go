package server_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/paulpham157/sdf/internal/netlimit"
	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/store"
)

func TestOversizeBodyIs413WithoutRetryAfter(t *testing.T) {
	srv, db := startLimited(t, netlimit.Limits{MaxBodyBytes: 100, MaxInFlightBytes: 100})
	body := strings.Repeat("a", 101)
	resp := exchange(t, srv.HTTPAddress(), rawRequest("/v1/traces", srv.HTTPAddress(), map[string]string{
		"Content-Type":   "application/json",
		"Content-Length": "101",
	}, body))
	if resp.StatusCode != 413 || resp.body != "payload too large" || resp.Header.Get("Retry-After") != "" {
		t.Fatalf("%d %q retry %q", resp.StatusCode, resp.body, resp.Header.Get("Retry-After"))
	}
	// ReadResponse folds "Connection: close" into Response.Close.
	if !resp.Close {
		t.Fatal("oversize response left the connection open")
	}
	if rows := mustLoad(t, db); len(rows) != 0 {
		t.Fatalf("stored %d", len(rows))
	}
}

func TestTooManyConnectionsIs503(t *testing.T) {
	srv, _ := startLimited(t, netlimit.Limits{
		MaxConns:      1,
		MaxConnsPerIP: 1,
		ReadTimeout:   5 * time.Second,
	})
	hold := holdContinue(t, srv, 32)
	// The listener writes the rejection as soon as it accepts, before
	// reading a request. Writing first races with that close.
	resp := readOne(t, dial(t, srv.HTTPAddress(), 3*time.Second))
	if resp.StatusCode != 503 || resp.body != "too many connections" || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("%d %q retry %q", resp.StatusCode, resp.body, resp.Header.Get("Retry-After"))
	}
	hold.Close()
}

func TestTooManyConnectionsFromOneAddressIs429(t *testing.T) {
	srv, _ := startLimited(t, netlimit.Limits{
		MaxConns:      4,
		MaxConnsPerIP: 1,
		ReadTimeout:   5 * time.Second,
	})
	hold := holdContinue(t, srv, 32)
	resp := readOne(t, dial(t, srv.HTTPAddress(), 3*time.Second))
	if resp.StatusCode != 429 || resp.body != "too many connections from this address" || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("%d %q retry %q", resp.StatusCode, resp.body, resp.Header.Get("Retry-After"))
	}
	hold.Close()
}

func TestSlowHeadersAreClosed(t *testing.T) {
	srv, _ := startLimited(t, netlimit.Limits{ReadHeaderTimeout: 250 * time.Millisecond})
	c := dial(t, srv.HTTPAddress(), 3*time.Second)
	if _, err := c.Write([]byte("POST /v1/traces HTTP/1.1\r\n")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	one := make([]byte, 1)
	_, err := c.Read(one)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("slow headers received a byte")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("headers held the connection for %s", elapsed)
	}
}

func TestSlowBodyIs503AndReleases(t *testing.T) {
	srv, db := startLimited(t, netlimit.Limits{
		MaxBodyBytes:     64,
		MaxInFlightBytes: 64,
		ReadTimeout:      250 * time.Millisecond,
	})
	addr := srv.HTTPAddress()
	c := dial(t, addr, 3*time.Second)
	req := rawRequest("/v1/traces", addr, map[string]string{
		"Content-Type":   "application/json",
		"Content-Length": "64",
	}, "x")
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	resp := readOne(t, c)
	if resp.StatusCode != 503 || resp.body != "read timeout" || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("%d %q retry %q", resp.StatusCode, resp.body, resp.Header.Get("Retry-After"))
	}
	// The reservation is dropped when the handler returns, which is
	// before this response is flushed. A second body of the same size
	// fits only if that happened.
	again := exchange(t, addr, rawRequest("/v1/traces", addr, map[string]string{
		"Content-Type": "application/json",
	}, strings.Repeat("x", 64)))
	if again.StatusCode == 503 {
		t.Fatalf("bytes still held: %d %q", again.StatusCode, again.body)
	}
	if again.StatusCode != 400 {
		t.Fatalf("after timeout %d %q", again.StatusCode, again.body)
	}
	ok := exchange(t, addr, rawRequest("/v1/traces", addr, map[string]string{
		"Content-Type": "application/json",
	}, `{"resourceSpans":[]}`))
	if ok.StatusCode != 200 || ok.body != "{}" {
		t.Fatalf("recovered %d %q", ok.StatusCode, ok.body)
	}
	if rows := mustLoad(t, db); len(rows) != 0 {
		t.Fatalf("stored %d", len(rows))
	}
}

func TestIdleConnectionCloses(t *testing.T) {
	srv, _ := startLimited(t, netlimit.Limits{IdleTimeout: 250 * time.Millisecond})
	c := dial(t, srv.HTTPAddress(), 3*time.Second)
	if _, err := c.Write([]byte(rawRequest("/health", srv.HTTPAddress(), nil, ""))); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || string(body) != `{"status":"ok"}` {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
	start := time.Now()
	_, err = br.ReadByte()
	if err == nil {
		t.Fatal("idle connection stayed open and sent a byte")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("idle connection held for %s", time.Since(start))
	}
}

func TestInFlightBytesRejectAndRelease(t *testing.T) {
	srv, db := startLimited(t, netlimit.Limits{
		MaxConns:         8,
		MaxBodyBytes:     800,
		MaxInFlightBytes: 800,
		ReadTimeout:      5 * time.Second,
	})
	addr := srv.HTTPAddress()
	hold := holdContinue(t, srv, 800)
	over := exchange(t, addr, rawRequest("/v1/traces", addr, map[string]string{
		"Content-Type":   "application/json",
		"Content-Length": "800",
	}, ""))
	if over.StatusCode != 503 || over.body != "inflight limit" || over.Header.Get("Retry-After") != "1" {
		t.Fatalf("%d %q retry %q", over.StatusCode, over.body, over.Header.Get("Retry-After"))
	}
	hold.Close()
	var ok rawResponse
	deadline := time.Now().Add(time.Second)
	for {
		ok = exchange(t, addr, rawRequest("/v1/traces", addr, map[string]string{
			"Content-Type": "application/json",
		}, `{"resourceSpans":[]}`))
		if ok.StatusCode == 200 && ok.body == "{}" {
			break
		}
		if ok.StatusCode != 503 || time.Now().After(deadline) {
			t.Fatalf("after release %d %q", ok.StatusCode, ok.body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rows := mustLoad(t, db); len(rows) != 0 {
		t.Fatalf("stored %d", len(rows))
	}
}

func TestPerTokenCapIs429AndBadTokenStays401(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{
		Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true, Token: "t",
		Limits: netlimit.Limits{
			MaxConns:         8,
			MaxConnsPerToken: 1,
			MaxBodyBytes:     2000,
			MaxInFlightBytes: 8000,
			ReadTimeout:      5 * time.Second,
		},
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Shutdown()
		_ = db.Close()
	})
	addr := srv.HTTPAddress()
	hold := holdContinueAuth(t, srv, 40, "Bearer t")
	bad := exchange(t, addr, rawRequest("/v1/traces", addr, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer no",
	}, `{"resourceSpans":[]}`))
	if bad.StatusCode != 401 || bad.body != "unauthorized" {
		t.Fatalf("bad token %d %q", bad.StatusCode, bad.body)
	}
	second := exchange(t, addr, rawRequest("/v1/traces", addr, map[string]string{
		"Authorization":  "Bearer t",
		"Content-Type":   "application/json",
		"Content-Length": "20",
	}, ""))
	if second.StatusCode != 429 || second.body != "too many requests" || second.Header.Get("Retry-After") != "1" {
		t.Fatalf("%d %q retry %q", second.StatusCode, second.body, second.Header.Get("Retry-After"))
	}
	hold.Close()
	deadline := time.Now().Add(time.Second)
	for {
		ok := exchange(t, addr, rawRequest("/v1/traces", addr, map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer t",
		}, `{"resourceSpans":[]}`))
		if ok.StatusCode == 200 && ok.body == "{}" {
			break
		}
		if ok.StatusCode != 429 || time.Now().After(deadline) {
			t.Fatalf("after release %d %q", ok.StatusCode, ok.body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rows := mustLoad(t, db); len(rows) != 0 {
		t.Fatalf("stored %d", len(rows))
	}
}

func TestGRPCMessageSizeIsResourceExhaustedWithoutRetry(t *testing.T) {
	const cap = 2048
	srv, _ := startLimited(t, netlimit.Limits{MaxBodyBytes: cap, MaxInFlightBytes: cap})
	client := traceClient(t, srv.GRPCAddress())
	small := spanReq(0)
	if n := proto.Size(small); n > cap {
		t.Fatalf("fixture is %d bytes", n)
	}
	big := spanReq(cap)
	if n := proto.Size(big); n <= cap {
		t.Fatalf("oversize fixture is %d bytes", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.Export(ctx, small); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Export(ctx, small); err != nil {
		t.Fatalf("second small export, reservation was not released: %v", err)
	}
	_, err := client.Export(ctx, big)
	assertExhausted(t, err, false)
	if _, err := client.Export(ctx, small); err != nil {
		t.Fatalf("after the oversized message: %v", err)
	}
}

func TestGRPCInFlightIsResourceExhaustedWithRetry(t *testing.T) {
	const cap = 2048
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{
		Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true,
		Limits: netlimit.Limits{MaxBodyBytes: cap, MaxInFlightBytes: cap, ReadTimeout: 5 * time.Second},
	}
	release := make(chan struct{})
	var once sync.Once
	entered := make(chan struct{})
	srv.SetBlockInsert(func(ctx context.Context) error {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		srv.Shutdown()
		_ = db.Close()
	})
	small := spanReq(0)
	if proto.Size(small) > cap {
		t.Fatalf("fixture is %d bytes", proto.Size(small))
	}
	client := traceClient(t, srv.GRPCAddress())
	errc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Export(ctx, small)
		errc <- err
	}()
	select {
	case <-entered:
	case err := <-errc:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("first export did not reach the commit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = client.Export(ctx, small)
	assertExhausted(t, err, true)
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if _, err := client.Export(ctx, small); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

func TestGRPCSlowPrefaceIsClosed(t *testing.T) {
	srv, _ := startLimited(t, netlimit.Limits{ReadHeaderTimeout: 250 * time.Millisecond})
	c := dial(t, srv.GRPCAddress(), 3*time.Second)
	// The server writes its HTTP/2 settings before it reads the client
	// preface. The connection must still close once that read times out.
	start := time.Now()
	buf := make([]byte, 64)
	for {
		_, err := c.Read(buf)
		if err != nil {
			if time.Since(start) > 2*time.Second {
				t.Fatalf("silent connection held for %s: %v", time.Since(start), err)
			}
			return
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("silent connection stayed open")
		}
	}
}

func TestGRPCTooManyConnectionsIsClosed(t *testing.T) {
	srv, _ := startLimited(t, netlimit.Limits{
		MaxConns:          1,
		MaxConnsPerIP:     1,
		ReadHeaderTimeout: 5 * time.Second,
	})
	first := dial(t, srv.GRPCAddress(), 3*time.Second)
	defer first.Close()
	second := dial(t, srv.GRPCAddress(), time.Second)
	buf := make([]byte, 64)
	start := time.Now()
	n, err := second.Read(buf)
	if time.Since(start) > time.Second {
		t.Fatalf("over-limit gRPC connection held for %s", time.Since(start))
	}
	if err == nil || n > 0 {
		t.Fatalf("over-limit gRPC connection wrote %q (%v)", buf[:n], err)
	}
}

func TestGRPCShrinksTheReservationBeforeCommit(t *testing.T) {
	const body = 2000
	const inflight = 6000
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{
		Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true,
		Limits: netlimit.Limits{MaxBodyBytes: body, MaxInFlightBytes: inflight, ReadTimeout: 5 * time.Second},
	}
	release := make(chan struct{})
	var once sync.Once
	entered := make(chan struct{})
	srv.SetBlockInsert(func(ctx context.Context) error {
		var first bool
		once.Do(func() {
			first = true
			close(entered)
		})
		if !first {
			return nil
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		srv.Shutdown()
		_ = db.Close()
	})
	small := spanReq(0)
	n := proto.Size(small)
	// The gRPC share is half the budget (3000). A reservation that stays
	// at the body cap (2000) leaves no room for a second one. Shrinking
	// to the encoded size does.
	if int64(n)+int64(body) > inflight/2 {
		t.Fatalf("fixture %d leaves no room to tell shrink from a full hold", n)
	}
	client := traceClient(t, srv.GRPCAddress())
	errc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Export(ctx, small)
		errc <- err
	}()
	select {
	case <-entered:
	case err := <-errc:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("first export did not reach the commit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.Export(ctx, small); err != nil {
		t.Fatalf("second export while the first is committing: %v", err)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestRefusedRequestDoesNotHoldTheByteBudget(t *testing.T) {
	const cap = 1000
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{
		Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true, Token: "t",
		Limits: netlimit.Limits{
			MaxConns:         16,
			MaxConnsPerIP:    16,
			MaxBodyBytes:     cap,
			MaxInFlightBytes: cap,
			ReadTimeout:      5 * time.Second,
		},
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Shutdown()
		_ = db.Close()
	})
	addr := srv.HTTPAddress()
	cases := []struct {
		name    string
		headers string
	}{
		{name: "origin", headers: "Host: " + addr + "\r\nOrigin: http://evil.example\r\n"},
		{name: "host", headers: "Host: evil.example\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := dial(t, addr, 2*time.Second)
			req := "POST /v1/traces HTTP/1.1\r\n" + tc.headers +
				"Content-Type: application/json\r\nContent-Length: " + itoa(cap) + "\r\n\r\n"
			if _, err := bad.Write([]byte(req)); err != nil {
				t.Fatal(err)
			}
			goodc := make(chan rawResponse, 1)
			errc := make(chan error, 1)
			go func() {
				c, err := net.DialTimeout("tcp", addr, time.Second)
				if err != nil {
					errc <- err
					return
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				if _, err := c.Write([]byte(rawRequest("/v1/traces", addr, map[string]string{
					"Content-Type":  "application/json",
					"Authorization": "Bearer t",
				}, `{"resourceSpans":[]}`))); err != nil {
					errc <- err
					return
				}
				got, err := readRaw(c)
				if err != nil {
					errc <- err
					return
				}
				goodc <- got
			}()
			resp := readOne(t, bad)
			if resp.StatusCode != 403 || resp.body != "forbidden" || !resp.Close {
				t.Fatalf("refused %d %q close %v", resp.StatusCode, resp.body, resp.Close)
			}
			select {
			case err := <-errc:
				t.Fatal(err)
			case good := <-goodc:
				if good.StatusCode != 200 || good.body != "{}" {
					t.Fatalf("authenticated export %d %q", good.StatusCode, good.body)
				}
			}
		})
	}
}

func TestTokenClosesUnauthenticatedResponses(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{
		Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true, Token: "t",
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Shutdown()
		_ = db.Close()
	})
	addr := srv.HTTPAddress()
	health := exchange(t, addr, rawRequest("/health", addr, nil, ""))
	if health.StatusCode != 200 || !health.Close {
		t.Fatalf("health %d close %v", health.StatusCode, health.Close)
	}
	missing := exchange(t, addr, rawRequest("/nope", addr, nil, ""))
	if missing.StatusCode != 404 || !missing.Close {
		t.Fatalf("404 %d close %v", missing.StatusCode, missing.Close)
	}
	ok := exchange(t, addr, rawRequest("/v1/traces", addr, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer t",
	}, `{"resourceSpans":[]}`))
	if ok.StatusCode != 200 || ok.Close {
		t.Fatalf("authenticated %d close %v body %q", ok.StatusCode, ok.Close, ok.body)
	}
}

func TestShutdownWrites200AfterCommit(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{
		Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true,
	}
	entered := make(chan struct{})
	srv.SetBlockAfterInsert(func(ctx context.Context) {
		close(entered)
		if ctx != nil {
			<-ctx.Done()
		}
	})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Shutdown()
		_ = db.Close()
	})
	addr := srv.HTTPAddress()
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"pi"}},{"key":"sdf.fixture","value":{"stringValue":"sample"}}]},"scopeSpans":[{"spans":[{"name":"n","traceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","spanId":"bbbbbbbbbbbbbbbb","startTimeUnixNano":"10","endTimeUnixNano":"20"}]}]}]}`
	errc := make(chan error, 1)
	resc := make(chan rawResponse, 1)
	go func() {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			errc <- err
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write([]byte(rawRequest("/v1/traces", addr, map[string]string{
			"Content-Type": "application/json",
		}, body))); err != nil {
			errc <- err
			return
		}
		got, err := readRaw(c)
		if err != nil {
			errc <- err
			return
		}
		resc <- got
	}()
	select {
	case <-entered:
	case err := <-errc:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("export did not commit")
	}
	srv.Shutdown()
	select {
	case err := <-errc:
		t.Fatal(err)
	case resp := <-resc:
		if resp.StatusCode != 200 || resp.body != "{}" {
			t.Fatalf("%d %q", resp.StatusCode, resp.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no response after shutdown")
	}
	if rows := mustLoad(t, db); len(rows) != 1 {
		t.Fatalf("stored %d", len(rows))
	}
}

func startLimited(t *testing.T, lim netlimit.Limits) (*server.Server, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{
		Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true,
		Limits: lim,
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Shutdown()
		_ = db.Close()
	})
	return srv, db
}

func spanReq(pad int) *coltracepb.ExportTraceServiceRequest {
	attrs := []*commonpb.KeyValue{kv("sdf.fixture", "sample")}
	if pad > 0 {
		attrs = append(attrs, kv("pad", strings.Repeat("a", pad)))
	}
	return &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: resource("pi", ""),
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:    bytes.Repeat([]byte{0x11}, 16),
					SpanId:     bytes.Repeat([]byte{0x22}, 8),
					Name:       "n",
					Attributes: attrs,
				}},
			}},
		}},
	}
}

func traceClient(t *testing.T, addr string) coltracepb.TraceServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return coltracepb.NewTraceServiceClient(conn)
}

func assertExhausted(t *testing.T, err error, wantRetry bool) {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.ResourceExhausted {
		t.Fatalf("got %v", err)
	}
	var delay time.Duration
	found := false
	for _, d := range st.Details() {
		info, ok := d.(*errdetails.RetryInfo)
		if !ok {
			continue
		}
		found = true
		delay = info.GetRetryDelay().AsDuration()
	}
	if found != wantRetry {
		t.Fatalf("retry info present=%v (%v), want %v", found, st.Details(), wantRetry)
	}
	if wantRetry && delay != time.Second {
		t.Fatalf("retry delay %s", delay)
	}
}

type rawResponse struct {
	StatusCode int
	Header     http.Header
	Close      bool
	body       string
}

func holdContinue(t *testing.T, srv *server.Server, n int) net.Conn {
	t.Helper()
	return holdContinueAuth(t, srv, n, "")
}

func holdContinueAuth(t *testing.T, srv *server.Server, n int, auth string) net.Conn {
	t.Helper()
	c := dial(t, srv.HTTPAddress(), 8*time.Second)
	hdr := map[string]string{
		"Content-Type":   "application/json",
		"Content-Length": itoa(n),
		"Expect":         "100-continue",
	}
	if auth != "" {
		hdr["Authorization"] = auth
	}
	if _, err := c.Write([]byte(rawRequest("/v1/traces", srv.HTTPAddress(), hdr, ""))); err != nil {
		t.Fatal(err)
	}
	resp := readOne(t, c)
	if resp.StatusCode != 100 {
		t.Fatalf("continue %d %q", resp.StatusCode, resp.body)
	}
	return c
}

func exchange(t *testing.T, addr, req string) rawResponse {
	t.Helper()
	c := dial(t, addr, 3*time.Second)
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	return readOne(t, c)
}

func readOne(t *testing.T, c net.Conn) rawResponse {
	t.Helper()
	resp, err := readRaw(c)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readRaw(c net.Conn) (rawResponse, error) {
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return rawResponse{}, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return rawResponse{}, err
	}
	return rawResponse{StatusCode: resp.StatusCode, Header: resp.Header, Close: resp.Close, body: string(body)}, nil
}

func dial(t *testing.T, addr string, wait time.Duration) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(wait))
	return c
}

func rawRequest(path, host string, headers map[string]string, body string) string {
	var b strings.Builder
	method := "POST"
	if path == "/health" {
		method = "GET"
	}
	b.WriteString(method + " " + path + " HTTP/1.1\r\nHost: " + host + "\r\n")
	if body != "" {
		if headers == nil {
			headers = map[string]string{}
		}
		if _, ok := headers["Content-Length"]; !ok {
			headers["Content-Length"] = itoa(len(body))
		}
	}
	for k, v := range headers {
		b.WriteString(k + ": " + v + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(body)
	return b.String()
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
