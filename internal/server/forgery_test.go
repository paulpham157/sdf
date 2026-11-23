package server_test

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/store"
)

// jsonLog is one valid OTLP/JSON log. A receiver that stores it has accepted
// the request; a refusal must leave the database empty.
const jsonLog = `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"pi"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"10","body":{"stringValue":"kept"},"attributes":[{"key":"pi.session.id","value":{"stringValue":"json-kept"}}]}]}]}]}`

func TestForgedOTLPPostsAreRefusedAndStoreNothing(t *testing.T) {
	logs := piLog()
	cases := []struct {
		name     string
		path     string
		body     []byte
		ctype    string
		omitType bool
		origin   string
		host     string
		encoding string
		want     int // 415 for media type; 403 or 421 both count as refused when want is 403
	}{
		{name: "text/plain protobuf logs", path: "/v1/logs", body: logs, ctype: "text/plain", want: 415},
		{name: "text/plain charset protobuf logs", path: "/v1/logs", body: logs, ctype: "text/plain; charset=utf-8", want: 415},
		{name: "missing content type protobuf logs", path: "/v1/logs", body: logs, omitType: true, want: 415},
		{name: "multipart protobuf logs", path: "/v1/logs", body: logs, ctype: "multipart/form-data", want: 415},
		{name: "form protobuf logs", path: "/v1/logs", body: logs, ctype: "application/x-www-form-urlencoded", want: 415},
		{name: "text/plain protobuf traces", path: "/v1/traces", body: chunkTrace(), ctype: "text/plain", want: 415},
		{name: "text/plain protobuf metrics", path: "/v1/metrics", body: counter(1, 1000), ctype: "text/plain", want: 415},
		{name: "gzip text/plain protobuf logs", path: "/v1/logs", body: nil, ctype: "text/plain", encoding: "gzip", want: 415},
		{name: "origin protobuf logs", path: "/v1/logs", body: logs, ctype: "application/x-protobuf", origin: "https://evil.example", want: 403},
		{name: "origin json logs", path: "/v1/logs", body: []byte(jsonLog), ctype: "application/json", origin: "https://evil.example", want: 403},
		{name: "null origin protobuf logs", path: "/v1/logs", body: logs, ctype: "application/x-protobuf", origin: "null", want: 403},
		{name: "rebound host protobuf logs", path: "/v1/logs", body: logs, ctype: "application/x-protobuf", host: "attacker.example", want: 403},
		{name: "rebound host with port", path: "/v1/logs", body: logs, ctype: "application/x-protobuf", host: "attacker.example:4318", want: 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, db := start(t)
			body := tc.body
			if tc.encoding == "gzip" {
				body = gzipBytes(t, logs)
			}
			host := tc.host
			if host == "attacker.example:4318" {
				_, port := splitBase(t, base)
				host = net.JoinHostPort("attacker.example", port)
			}
			resp := postForged(t, base, forgedPost{
				path: tc.path, body: body, contentType: tc.ctype, omitType: tc.omitType,
				origin: tc.origin, host: host, encoding: tc.encoding,
			})
			if !refusedStatus(tc.want, resp.status) {
				t.Fatalf("status %d body %q, want %d", resp.status, resp.body, tc.want)
			}
			if n := len(mustLoad(t, db)); n != 0 {
				t.Fatalf("stored %d rows", n)
			}
		})
	}
}

func TestSessionHookForgeryIsRefusedAndStoresNothing(t *testing.T) {
	const event = `{"tool":"pi","session_id":"forged-session","edge":"open"}`
	t.Run("text/plain", func(t *testing.T) {
		base, db := start(t)
		resp := postForged(t, base, forgedPost{path: "/v1/sessions", body: []byte(event), contentType: "text/plain"})
		if resp.status != 415 {
			t.Fatalf("status %d body %q", resp.status, resp.body)
		}
		if n := len(mustLoad(t, db)); n != 0 {
			t.Fatalf("stored %d rows", n)
		}
	})
	t.Run("origin", func(t *testing.T) {
		base, db := start(t)
		resp := postForged(t, base, forgedPost{
			path: "/v1/sessions", body: []byte(event), contentType: "application/json", origin: "https://evil.example",
		})
		if !refusedStatus(403, resp.status) {
			t.Fatalf("status %d body %q", resp.status, resp.body)
		}
		if n := len(mustLoad(t, db)); n != 0 {
			t.Fatalf("stored %d rows", n)
		}
	})
}

func TestHealthWithOriginIsRefused(t *testing.T) {
	base, db := start(t)
	req, err := http.NewRequest(http.MethodGet, base+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !refusedStatus(403, resp.StatusCode) {
		t.Fatalf("status %d body %q", resp.StatusCode, got)
	}
	if n := len(mustLoad(t, db)); n != 0 {
		t.Fatalf("stored %d rows", n)
	}
}

func TestProtobufAndJSONStillStore(t *testing.T) {
	base, db := start(t)
	resp := postForged(t, base, forgedPost{path: "/v1/logs", body: piLog(), contentType: "application/x-protobuf"})
	if resp.status != 200 {
		t.Fatalf("protobuf %d %q", resp.status, resp.body)
	}
	resp = postForged(t, base, forgedPost{path: "/v1/logs", body: []byte(jsonLog), contentType: "application/json"})
	if resp.status != 200 || resp.body != "{}" {
		t.Fatalf("json %d %q", resp.status, resp.body)
	}
	resp = postForged(t, base, forgedPost{path: "/v1/logs", body: []byte(jsonLog), contentType: "application/json; charset=utf-8"})
	if resp.status != 200 {
		t.Fatalf("charset %d %q", resp.status, resp.body)
	}
	resp = postForged(t, base, forgedPost{path: "/v1/logs", body: piLog(), contentType: "Application/X-Protobuf"})
	if resp.status != 200 {
		t.Fatalf("case %d %q", resp.status, resp.body)
	}
	rows := mustLoad(t, db)
	if len(rows) < 2 {
		t.Fatalf("stored %d rows", len(rows))
	}
	var protoHit, jsonHit bool
	for _, row := range rows {
		switch deref(row.SessionID) {
		case "proto-chunk-session":
			protoHit = true
		case "json-kept":
			jsonHit = true
		}
	}
	if !protoHit || !jsonHit {
		t.Fatalf("protobuf stored %v json stored %v", protoHit, jsonHit)
	}
}

func TestLoopbackHostStillAcceptsExporterPosts(t *testing.T) {
	base, db := start(t)
	host, port := splitBase(t, base)
	for _, h := range []string{
		net.JoinHostPort(host, port),
		host,
		net.JoinHostPort("localhost", port),
		"localhost",
		net.JoinHostPort("::1", port),
	} {
		resp := postForged(t, base, forgedPost{
			path: "/v1/logs", body: piLog(), contentType: "application/x-protobuf", host: h,
		})
		if resp.status != 200 {
			t.Fatalf("host %q status %d body %q", h, resp.status, resp.body)
		}
	}
	if n := len(mustLoad(t, db)); n == 0 {
		t.Fatal("loopback hosts stored nothing")
	}
}

func TestNonLoopbackReceiverAcceptsForeignHost(t *testing.T) {
	base, db := startOn(t, "0.0.0.0")
	resp := postForged(t, base, forgedPost{
		path: "/v1/logs", body: piLog(), contentType: "application/x-protobuf", host: "attacker.example",
	})
	if resp.status != 200 {
		t.Fatalf("status %d body %q", resp.status, resp.body)
	}
	if n := len(mustLoad(t, db)); n == 0 {
		t.Fatal("wide bind stored nothing")
	}
}

type forgedPost struct {
	path        string
	body        []byte
	contentType string
	omitType    bool
	origin      string
	host        string
	encoding    string
}

func postForged(t *testing.T, base string, p forgedPost) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+p.path, bytes.NewReader(p.body))
	if err != nil {
		t.Fatal(err)
	}
	// NewRequest does not set a content type. Set one unless the case omits it,
	// and do not let a nil body header sneak in as octet-stream.
	req.Header.Del("Content-Type")
	if !p.omitType {
		req.Header.Set("Content-Type", p.contentType)
	}
	if p.origin != "" {
		req.Header.Set("Origin", p.origin)
	}
	if p.encoding != "" {
		req.Header.Set("Content-Encoding", p.encoding)
	}
	if p.host != "" {
		req.Host = p.host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, string(got)}
}

func refusedStatus(want, got int) bool {
	if want == 403 {
		return got == 403 || got == 421
	}
	return got == want
}

func splitBase(t *testing.T, base string) (string, string) {
	t.Helper()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func startOn(t *testing.T, host string) (string, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{Host: host, HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Shutdown()
		_ = db.Close()
	})
	base := "http://" + srv.HTTPAddress()
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(base + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not become ready", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return base, db
}
