package server_test

import (
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/store"
)

const testOTLPToken = "test-otlp-token"

var emptyTracesJSON = []byte(`{"resourceSpans":[]}`)

func TestTokenUnsetAcceptsHTTPAndGRPC(t *testing.T) {
	base, db, grpcAddr := startBoth(t)
	resp := postBytes(t, base+"/v1/traces", "application/json", emptyTracesJSON)
	if resp.status != 200 {
		t.Fatalf("unset HTTP %+v", resp)
	}
	if err := exportGRPCAuth(t, grpcAddr, ""); err != nil {
		t.Fatal(err)
	}
	if rows := mustLoad(t, db); len(rows) != 1 {
		t.Fatalf("rows %d", len(rows))
	}
}

func TestTokenAcceptsCorrectBearerAndRejectsMissingOrWrong(t *testing.T) {
	logBuf := &bytes.Buffer{}
	base, db, grpcAddr := startAuth(t, testOTLPToken, logBuf)

	health := get(t, base+"/health")
	if health.status != 200 || strings.TrimSpace(health.body) != `{"status":"ok"}` {
		t.Fatalf("health without token %+v", health)
	}

	missing := postBytes(t, base+"/v1/traces", "application/json", emptyTracesJSON)
	if missing.status != 401 || missing.body != "unauthorized" {
		t.Fatalf("missing HTTP %+v", missing)
	}
	if got := wwwAuthenticate(t, base+"/v1/traces", "", emptyTracesJSON); got != "Bearer" {
		t.Fatalf("missing WWW-Authenticate %q", got)
	}

	wrong := postAuth(t, base+"/v1/traces", "Bearer wrong-token", emptyTracesJSON)
	if wrong.status != 401 || wrong.body != "unauthorized" {
		t.Fatalf("wrong HTTP %+v", wrong)
	}
	if got := wwwAuthenticate(t, base+"/v1/traces", "Bearer wrong-token", emptyTracesJSON); got != "Bearer" {
		t.Fatalf("wrong WWW-Authenticate %q", got)
	}

	prefix := postAuth(t, base+"/v1/traces", "Bearer "+testOTLPToken+"-extra", emptyTracesJSON)
	if prefix.status != 401 {
		t.Fatalf("prefix HTTP %+v", prefix)
	}

	ok := postAuth(t, base+"/v1/traces", "Bearer "+testOTLPToken, emptyTracesJSON)
	if ok.status != 200 {
		t.Fatalf("correct HTTP %+v", ok)
	}
	if got := wwwAuthenticate(t, base+"/v1/traces", "Bearer "+testOTLPToken, emptyTracesJSON); got != "" {
		t.Fatalf("200 WWW-Authenticate %q", got)
	}

	lower := postAuth(t, base+"/v1/metrics", "bearer "+testOTLPToken, []byte(`{"resourceMetrics":[]}`))
	if lower.status != 200 {
		t.Fatalf("lowercase scheme %+v", lower)
	}

	sessionMissing := postBytes(t, base+"/v1/sessions", "application/json", []byte(`{"tool":"pi","session_id":"s1","edge":"open"}`))
	if sessionMissing.status != 401 {
		t.Fatalf("session missing %+v", sessionMissing)
	}
	sessionOK := postAuth(t, base+"/v1/sessions", "Bearer "+testOTLPToken, []byte(`{"tool":"pi","session_id":"s1","edge":"open"}`))
	if sessionOK.status != 200 {
		t.Fatalf("session correct %+v", sessionOK)
	}

	if err := exportGRPCAuth(t, grpcAddr, "Bearer "+testOTLPToken); err != nil {
		t.Fatal(err)
	}
	err := exportGRPCAuth(t, grpcAddr, "")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing gRPC %v", err)
	}
	err = exportGRPCAuth(t, grpcAddr, "Bearer wrong-token")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong gRPC %v", err)
	}
	err = exportGRPCAuthValues(t, grpcAddr, "Bearer "+testOTLPToken, "Bearer "+testOTLPToken)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("multiple gRPC authorization values %v", err)
	}

	if rows := mustLoad(t, db); len(rows) != 2 {
		// one authorized gRPC span and one authorized session event
		t.Fatalf("stored %d rows: %+v", len(rows), rows)
	}

	logged := logBuf.String()
	if strings.Contains(logged, testOTLPToken) || strings.Contains(logged, "wrong-token") {
		t.Fatalf("token appeared in logs: %q", logged)
	}
	if !strings.Contains(logged, `POST "/v1/traces" 401`) || !strings.Contains(logged, `POST "/v1/traces" 200`) {
		t.Fatalf("expected request lines in logs: %q", logged)
	}
}

func TestTokenRejectsGRPCMetricsAndLogsWithoutBearer(t *testing.T) {
	_, _, grpcAddr := startAuth(t, testOTLPToken, io.Discard)
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = colmetricspb.NewMetricsServiceClient(conn).Export(t.Context(), &colmetricspb.ExportMetricsServiceRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("metrics %v", err)
	}
	_, err = collogspb.NewLogsServiceClient(conn).Export(t.Context(), &collogspb.ExportLogsServiceRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("logs %v", err)
	}
}

func startAuth(t *testing.T, token string, logBuf io.Writer) (string, *store.Store, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{
		Host:     "127.0.0.1",
		HTTPPort: 0,
		GRPCPort: 0,
		Store:    db,
		Quiet:    false,
		Token:    token,
		Log:      logBuf,
	}
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
	return base, db, srv.GRPCAddress()
}

func postAuth(t *testing.T, url, authorization string, body []byte) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authorization)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, string(got)}
}

func exportGRPCAuth(t *testing.T, addr, authorization string) error {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := coltracepb.NewTraceServiceClient(conn)
	ctx := t.Context()
	if authorization != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", authorization)
	}
	_, err = client.Export(ctx, &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: resource("pi", ""),
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:           bytes.Repeat([]byte{0x11}, 16),
					SpanId:            bytes.Repeat([]byte{0x22}, 8),
					Name:              "invoke_agent pi",
					StartTimeUnixNano: 1_000_000_000,
					EndTimeUnixNano:   2_000_000_000,
					Attributes: []*commonpb.KeyValue{
						kv("pi.session.id", "grpc-auth-session"),
						kv("sdf.fixture", "sample"),
					},
				}},
			}},
		}},
	})
	return err
}

func wwwAuthenticate(t *testing.T, url, authorization string, body []byte) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Header.Get("WWW-Authenticate")
}

func exportGRPCAuthValues(t *testing.T, addr string, values ...string) error {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := coltracepb.NewTraceServiceClient(conn)
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.MD{"authorization": values})
	_, err = client.Export(ctx, &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: resource("pi", ""),
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:           bytes.Repeat([]byte{0x11}, 16),
					SpanId:            bytes.Repeat([]byte{0x22}, 8),
					Name:              "invoke_agent pi",
					StartTimeUnixNano: 1_000_000_000,
					EndTimeUnixNano:   2_000_000_000,
					Attributes: []*commonpb.KeyValue{
						kv("pi.session.id", "grpc-auth-session"),
						kv("sdf.fixture", "sample"),
					},
				}},
			}},
		}},
	})
	return err
}
