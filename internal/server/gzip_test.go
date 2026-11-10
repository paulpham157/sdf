package server_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/summary"
)

func TestGzipJSONAndProtobuf(t *testing.T) {
	base, db := start(t)
	trace := []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"opencode"}},{"key":"sdf.fixture","value":{"stringValue":"sample"}}]},"scopeSpans":[{"spans":[{"name":"Tool.execute","traceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","spanId":"bbbbbbbbbbbbbbbb","startTimeUnixNano":"1000000","endTimeUnixNano":"2000000","status":{"code":"STATUS_CODE_ERROR","message":"write failed"},"attributes":[{"key":"session.id","value":{"stringValue":"gzip-session"}},{"key":"gen_ai.tool.name","value":{"stringValue":"write"}},{"key":"gen_ai.tool.call.id","value":{"stringValue":"call_gz"}}]}]}]}]}`)
	resp := postEncoded(t, base+"/v1/traces", "application/json", gzipBytes(t, trace), "gzip")
	if resp.status != 200 || resp.body != "{}" {
		t.Fatalf("json gzip %+v", resp)
	}
	logs := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"pi"}},{"key":"sdf.fixture","value":{"stringValue":"sample"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"10","body":{"stringValue":"pi session gzip-log started"},"attributes":[{"key":"event.name","value":{"stringValue":"pi.session.start"}},{"key":"pi.session.id","value":{"stringValue":"gzip-log"}},{"key":"gen_ai.system","value":{"stringValue":"pi"}}]}]}]}]}`)
	resp = postEncoded(t, base+"/v1/logs", "application/json", gzipBytes(t, logs), "gzip")
	if resp.status != 200 || resp.body != "{}" {
		t.Fatalf("logs gzip %+v", resp)
	}
	resp = postEncoded(t, base+"/v1/metrics", "application/x-protobuf", gzipBytes(t, counter(9, 3000)), "gzip")
	if resp.status != 200 || resp.body != "" {
		t.Fatalf("metrics gzip %+v", resp)
	}

	rows := mustLoad(t, db)
	found := false
	for _, row := range rows {
		if deref(row.Name) != "Tool.execute" {
			continue
		}
		found = true
		if row.StatusCode != "ERROR" || deref(row.StatusMessage) != "write failed" || deref(row.SessionID) != "gzip-session" {
			t.Fatalf("stored %+v %s", row.StatusCode, deref(row.StatusMessage))
		}
	}
	if !found {
		t.Fatal("tool span missing")
	}
	report := summary.Summarize(rows, false)
	var session summary.SessionSummary
	for _, item := range report.Sessions {
		if item.SessionID == "gzip-session" {
			session = item
		}
	}
	if len(session.ToolCalls) != 1 || deref(session.ToolCalls[0].Success) != "false" {
		t.Fatalf("calls %+v", session.ToolCalls)
	}
	if session.Spans[0].StatusCode != "ERROR" || deref(session.Spans[0].StatusMessage) != "write failed" {
		t.Fatalf("span %+v", session.Spans[0])
	}
	text := summary.Format(summary.Report{Sessions: []summary.SessionSummary{session}, Unattributed: []summary.Unattributed{}})
	if !strings.Contains(text, "status=ERROR (write failed)") || !strings.Contains(text, "success=false (traces status)") {
		t.Fatalf("%s", text)
	}
}

func TestGzipChunkedBody(t *testing.T) {
	base, db := start(t)
	payload := gzipBytes(t, []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"pi"}},{"key":"sdf.fixture","value":{"stringValue":"sample"}}]},"scopeSpans":[{"spans":[{"name":"invoke_agent pi","startTimeUnixNano":"10","endTimeUnixNano":"20","status":{"code":"STATUS_CODE_OK"},"attributes":[{"key":"session.id","value":{"stringValue":"chunk-gzip"}}]}]}]}]}`))
	status, body := postChunked(t, base, "/v1/traces", payload, "application/json", "chunked", true, "Content-Encoding: gzip\r\n")
	if status != 200 || string(body) != "{}" {
		t.Fatalf("%d %q", status, body)
	}
	rows := mustLoad(t, db)
	if len(rows) != 1 || rows[0].StatusCode != "OK" || deref(rows[0].SessionID) != "chunk-gzip" {
		t.Fatalf("%+v %s", rows[0].StatusCode, deref(rows[0].SessionID))
	}
}

func TestGzipDecompressedSizeIsLimited(t *testing.T) {
	base, db := start(t)
	fat := bytes.Repeat([]byte("x"), otlp.MaxBodyBytes+1)
	status, body := postChunked(t, base, "/v1/traces", gzipBytes(t, fat), "application/json", "chunked", true, "Content-Encoding: gzip\r\n")
	if status != 413 || string(body) != "payload too large" {
		t.Fatalf("%d %q", status, body)
	}
	resp := postEncoded(t, base+"/v1/logs", "application/json", gzipBytes(t, fat), "gzip")
	if resp.status != 413 || resp.body != "payload too large" {
		t.Fatalf("content-length %+v", resp)
	}
	if rows := mustLoad(t, db); len(rows) != 0 {
		t.Fatalf("%d rows", len(rows))
	}
}

func TestUnsupportedAndInvalidContentEncoding(t *testing.T) {
	base, db := start(t)
	resp := postEncoded(t, base+"/v1/metrics", "application/json", []byte(`{"resourceMetrics":[]}`), "br")
	if resp.status != 415 || resp.body != "unsupported content encoding" {
		t.Fatalf("%+v", resp)
	}
	resp = postEncoded(t, base+"/v1/traces", "application/json", []byte("not-gzip"), "gzip")
	if resp.status != 400 || resp.body != "invalid gzip body" {
		t.Fatalf("%+v", resp)
	}
	resp = postEncoded(t, base+"/v1/traces", "application/json", []byte(`{"resourceSpans":[]}`), "identity")
	if resp.status != 200 {
		t.Fatalf("identity %+v", resp)
	}
	if rows := mustLoad(t, db); len(rows) != 0 {
		t.Fatalf("%d rows", len(rows))
	}
}

func TestGRPCGzipAndSpanStatus(t *testing.T) {
	_, db, addr := startBoth(t)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := coltracepb.NewTraceServiceClient(conn)
	_, err = client.Export(t.Context(), &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: resource("claude-code", "grpc-gzip"),
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:           bytes.Repeat([]byte{0xab}, 16),
					SpanId:            bytes.Repeat([]byte{0xcd}, 8),
					Name:              "claude_code.tool",
					StartTimeUnixNano: 1_000_000,
					EndTimeUnixNano:   2_000_000,
					Status: &tracepb.Status{
						Code:    tracepb.Status_STATUS_CODE_ERROR,
						Message: "blocked",
					},
					Attributes: []*commonpb.KeyValue{
						kv("session.id", "grpc-gzip"),
						kv("tool_name", "Bash"),
						kv("tool_use_id", "toolu_grpc"),
						kv("sdf.fixture", "sample"),
					},
				}},
			}},
		}},
	}, grpc.UseCompressor("gzip"))
	if err != nil {
		t.Fatal(err)
	}
	rows := mustLoad(t, db)
	if len(rows) != 1 || rows[0].StatusCode != "ERROR" || deref(rows[0].StatusMessage) != "blocked" {
		t.Fatalf("%s %s", rows[0].StatusCode, deref(rows[0].StatusMessage))
	}
	session := summary.Summarize(rows, false).Sessions[0]
	if deref(session.ToolCalls[0].Success) != "false" || session.Spans[0].StatusCode != "ERROR" {
		t.Fatalf("%+v %+v", session.ToolCalls, session.Spans)
	}
	var buf bytes.Buffer
	if err := summary.WriteJSON(&buf, summary.Summarize(rows, false)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"status_code": "ERROR"`)) || !bytes.Contains(buf.Bytes(), []byte(`"status_message": "blocked"`)) {
		t.Fatalf("%s", buf.String())
	}
}

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func postEncoded(t *testing.T, url, contentType string, body []byte, encoding string) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, string(got)}
}
