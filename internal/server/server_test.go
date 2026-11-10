package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	"github.com/paulpham157/sdf/internal/cli"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/samplepath"
	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
)

var posts = [][2]string{
	{"/v1/metrics", "claude_metrics.json"},
	{"/v1/logs", "claude_logs.json"},
	{"/v1/traces", "claude_traces.json"},
	{"/v1/metrics", "codex_metrics.json"},
	{"/v1/logs", "codex_logs.json"},
}

func TestSampleFilesAreMarked(t *testing.T) {
	dir := samplepath.Dir()
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(readme, []byte("hand-written sample")) {
		t.Fatal("samples README does not describe the fixtures")
	}
	for _, item := range posts {
		payload := readJSON(t, filepath.Join(dir, item[1]))
		blobs := firstBlob(payload)
		if len(blobs) == 0 {
			t.Fatalf("%s has no resources", item[1])
		}
		resource, _ := blobs[0].(map[string]any)["resource"].(map[string]any)
		attrs, _ := resource["attributes"].([]any)
		fixture := map[string]string{}
		for _, raw := range attrs {
			item := raw.(map[string]any)
			value, _ := item["value"].(map[string]any)
			if s, ok := value["stringValue"].(string); ok {
				fixture[item["key"].(string)] = s
			}
		}
		if fixture["sdf.fixture"] != "sample" {
			t.Fatalf("%s fixture=%q", item[1], fixture["sdf.fixture"])
		}
	}
}

func TestReceiverStoresSamples(t *testing.T) {
	base, db := start(t)
	health := get(t, base+"/health")
	if health.status != 200 || strings.TrimSpace(health.body) != `{"status":"ok"}` {
		t.Fatalf("health %+v", health)
	}
	dir := samplepath.Dir()
	for _, item := range posts {
		body, err := os.ReadFile(filepath.Join(dir, item[1]))
		if err != nil {
			t.Fatal(err)
		}
		resp := post(t, base+item[0], "application/json", body)
		if resp.status != 200 || resp.body != "{}" {
			t.Fatalf("%s %+v", item[1], resp)
		}
	}
	rows := mustLoad(t, db)
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, row := range rows {
		if row.Resource["sdf.fixture"] != "sample" {
			t.Fatalf("fixture %#v", row.Resource["sdf.fixture"])
		}
	}
	foundUnscoped := false
	for _, row := range rows {
		if row.Name != nil && *row.Name == "codex.websocket.event" && row.SessionID == nil {
			foundUnscoped = true
		}
	}
	if !foundUnscoped {
		t.Fatal("unscoped codex counter was not stored")
	}
	report := summary.Summarize(rows, false)
	sessions := map[string]summary.SessionSummary{}
	for _, item := range report.Sessions {
		sessions[item.SessionID] = item
	}
	if len(sessions) != 2 || sessions["sample-claude-1"].SessionID == "" || sessions["sample-codex-conv-1"].SessionID == "" {
		t.Fatalf("sessions %#v", report.Sessions)
	}
	if len(report.Unattributed) != 0 {
		t.Fatalf("unattributed %#v", report.Unattributed)
	}
	claude := sessions["sample-claude-1"]
	if claude.Tool != "claude-code" || strings.Join(claude.ServiceNames, ",") != "claude-code" {
		t.Fatalf("claude identity %+v", claude)
	}
	if strings.Join(claude.Models, ",") != "claude-sonnet-5" {
		t.Fatalf("models %#v", claude.Models)
	}
	if claude.DurationMS == nil || *claude.DurationMS != 2000 || deref(claude.DurationSource) != "traces claude_code.interaction" {
		t.Fatalf("duration %v %s", claude.DurationMS, deref(claude.DurationSource))
	}
	assertTokens(t, claude.Tokens, map[string]float64{"input": 100, "output": 40, "cacheRead": 10, "cacheCreation": 4})
	if deref(claude.TokensSource) != "metrics claude_code.token.usage" {
		t.Fatal(deref(claude.TokensSource))
	}
	if claude.CostUSD == nil || *claude.CostUSD != 0.5 || deref(claude.CostSource) != "metrics claude_code.cost.usage" {
		t.Fatalf("cost %v %s", claude.CostUSD, deref(claude.CostSource))
	}
	if claude.Traces == 0 || claude.Logs == 0 || claude.Metrics == 0 {
		t.Fatalf("signals %+v", claude)
	}
	if len(claude.ToolCalls) != 2 {
		t.Fatalf("calls %+v", claude.ToolCalls)
	}
	if deref(claude.ToolCalls[0].Name) != "Bash" || deref(claude.ToolCalls[0].CallID) != "toolu_bash" || *claude.ToolCalls[0].DurationMS != 250 {
		t.Fatalf("bash call %+v", claude.ToolCalls[0])
	}
	if deref(claude.ToolCalls[1].Name) != "Read" || deref(claude.ToolCalls[1].CallID) != "toolu_read" || *claude.ToolCalls[1].DurationMS != 100 {
		t.Fatalf("read call %+v", claude.ToolCalls[1])
	}
	if !strings.Contains(claude.ToolCalls[0].Source, "logs claude_code.tool_result") {
		t.Fatal(claude.ToolCalls[0].Source)
	}
	if !spanNamed(claude.Spans, "claude_code.interaction") {
		t.Fatalf("spans %+v", claude.Spans)
	}
	codex := sessions["sample-codex-conv-1"]
	if codex.Tool != "codex" || strings.Join(codex.Models, ",") != "gpt-5.1" {
		t.Fatalf("codex %+v", codex)
	}
	if codex.DurationMS == nil || *codex.DurationMS != 2500 || deref(codex.DurationSource) != "metrics codex.turn.e2e_duration_ms" {
		t.Fatalf("codex duration %v %s", codex.DurationMS, deref(codex.DurationSource))
	}
	assertTokens(t, codex.Tokens, map[string]float64{"input": 200, "output": 30})
	if deref(codex.TokensSource) != "metrics codex.turn.token_usage" {
		t.Fatal(deref(codex.TokensSource))
	}
	if codex.CostUSD == nil || !near(*codex.CostUSD, 0.005) || deref(codex.CostSource) != "metrics codex.turn.cost_microusd" {
		t.Fatalf("codex cost %v %s", codex.CostUSD, deref(codex.CostSource))
	}
	if len(codex.ToolCalls) != 1 || deref(codex.ToolCalls[0].Name) != "shell" || deref(codex.ToolCalls[0].CallID) != "call-1" || deref(codex.ToolCalls[0].Success) != "true" || *codex.ToolCalls[0].DurationMS != 90 {
		t.Fatalf("codex call %+v", codex.ToolCalls)
	}
}

func TestProtobufCumulativeCounter(t *testing.T) {
	base, db := start(t)
	postBytes(t, base+"/v1/metrics", "application/x-protobuf", counter(10, 1000))
	resp := postBytes(t, base+"/v1/metrics", "application/x-protobuf", counter(25, 2000))
	if resp.status != 200 || resp.body != "" {
		t.Fatalf("%+v", resp)
	}
	sessions := summary.Summarize(mustLoad(t, db), false).Sessions
	if len(sessions) != 1 || sessions[0].SessionID != "proto-session" {
		t.Fatalf("%+v", sessions)
	}
	assertTokens(t, sessions[0].Tokens, map[string]float64{"input": 25})

	report := summary.Summarize(mustLoad(t, db), false)
	filtered := summary.Report{Sessions: []summary.SessionSummary{}, Unattributed: []summary.Unattributed{}}
	for _, item := range report.Sessions {
		if item.SessionID == "proto-session" {
			filtered.Sessions = append(filtered.Sessions, item)
		}
	}
	var buf bytes.Buffer
	if err := summary.WriteJSON(&buf, filtered); err != nil {
		t.Fatal(err)
	}
	var printed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &printed); err != nil {
		t.Fatal(err, buf.String())
	}
	sessionsJSON := printed["sessions"].([]any)
	first := sessionsJSON[0].(map[string]any)
	if first["session_id"] != "proto-session" || first["cost_usd"] != nil {
		t.Fatalf("%v", first)
	}
}

func TestCLISummaryJSON(t *testing.T) {
	base, db := start(t)
	postBytes(t, base+"/v1/metrics", "application/x-protobuf", counter(25, 2000))
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := cli.Main([]string{"summary", "--db", db.Path(), "--session", "proto-session", "--json"})
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if code != 0 {
		t.Fatal(code, string(out))
	}
	var printed map[string]any
	if err := json.Unmarshal(out, &printed); err != nil {
		t.Fatal(err, string(out))
	}
	first := printed["sessions"].([]any)[0].(map[string]any)
	if first["session_id"] != "proto-session" || first["cost_usd"] != nil {
		t.Fatalf("%s", out)
	}
}

func TestProtobufSpanIDsAreHex(t *testing.T) {
	base, db := start(t)
	span := &tracepb.Span{
		TraceId:           bytes.Repeat([]byte{0xab}, 16),
		SpanId:            bytes.Repeat([]byte{0xcd}, 8),
		ParentSpanId:      bytes.Repeat([]byte{0xef}, 8),
		Name:              "claude_code.interaction",
		StartTimeUnixNano: 1_000_000_000,
		EndTimeUnixNano:   2_000_000_000,
		Attributes: []*commonpb.KeyValue{
			kv("session.id", "span-session"),
			kv("sdf.fixture", "sample"),
		},
	}
	payload := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: resource("claude-code", "span-session"),
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{span},
			}},
		}},
	}
	raw, err := proto.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	resp := postBytes(t, base+"/v1/traces", "application/x-protobuf", raw)
	if resp.status != 200 || resp.body != "" {
		t.Fatalf("%+v", resp)
	}
	stored := mustLoad(t, db)
	if deref(stored[0].TraceID) != strings.Repeat("ab", 16) || deref(stored[0].SpanID) != strings.Repeat("cd", 8) || deref(stored[0].ParentSpanID) != strings.Repeat("ef", 8) {
		t.Fatalf("ids %s %s %s", deref(stored[0].TraceID), deref(stored[0].SpanID), deref(stored[0].ParentSpanID))
	}
}

func TestMalformedPayloadIsRejected(t *testing.T) {
	base, db := start(t)
	resp := postBytes(t, base+"/v1/logs", "application/json", []byte("not-otlp"))
	if resp.status != 400 {
		t.Fatalf("%+v", resp)
	}
	if rows := mustLoad(t, db); len(rows) != 0 {
		t.Fatalf("%d rows", len(rows))
	}
}

func TestChunkedBodies(t *testing.T) {
	base, db := start(t)
	dir := samplepath.Dir()
	chunkPosts := [][2]string{
		{"/v1/traces", "opencode_traces.json"},
		{"/v1/logs", "opencode_logs.json"},
		{"/v1/traces", "pi_traces.json"},
		{"/v1/logs", "pi_logs.json"},
		{"/v1/metrics", "pi_metrics.json"},
	}
	for _, item := range chunkPosts {
		body, err := os.ReadFile(filepath.Join(dir, item[1]))
		if err != nil {
			t.Fatal(err)
		}
		status, resp := postChunked(t, base, item[0], body, "application/json", "chunked", true, "")
		if status != 200 || string(resp) != "{}" {
			t.Fatalf("%s %d %q", item[1], status, resp)
		}
	}
	status, resp := postChunked(t, base, "/v1/traces", []byte(`{"resourceSpans":[]}`), "application/json", "chunked", true, "Content-Length: 0\r\n")
	if status != 200 || string(resp) != "{}" {
		t.Fatalf("content-length override %d %q", status, resp)
	}
	rows := mustLoad(t, db)
	opencode := map[string]bool{}
	for _, row := range rows {
		if deref(row.ServiceName) == "opencode" && row.Signal == "traces" {
			opencode[deref(row.SessionID)] = true
		}
	}
	if len(opencode) != 2 || !opencode["ses_sampleopencode0001"] || !opencode[""] {
		t.Fatalf("opencode sessions %#v", opencode)
	}
	var piLogs int
	for _, row := range rows {
		if deref(row.Name) == "pi.session.start" {
			piLogs++
			if deref(row.SessionID) != "2026-01-01T00-00-00-000Z_sample-pi-session" {
				t.Fatal(deref(row.SessionID))
			}
			if _, ok := row.Attributes["session.id"]; ok {
				t.Fatal("pi log should not have session.id")
			}
		}
	}
	if piLogs != 1 {
		t.Fatal(piLogs)
	}
	var piMetrics int
	for _, row := range rows {
		if deref(row.Name) == "gen_ai.client.token.usage" {
			piMetrics++
			if row.SessionID != nil {
				t.Fatal("pi metric stored a session id")
			}
		}
	}
	if piMetrics == 0 {
		t.Fatal("no pi token metrics")
	}
	report := summary.Summarize(rows, false)
	sessions := map[string]summary.SessionSummary{}
	for _, item := range report.Sessions {
		sessions[item.SessionID] = item
	}
	oc := sessions["ses_sampleopencode0001"]
	if oc.Tool != "opencode" {
		t.Fatal(oc.Tool)
	}
	in, _ := oc.Tokens.Get("input_tokens")
	out, _ := oc.Tokens.Get("output_tokens")
	if in != 5822 || out != 306 || oc.Logs != 0 {
		t.Fatalf("opencode tokens %v logs %d", oc.Tokens, oc.Logs)
	}
	pi := sessions["2026-01-01T00-00-00-000Z_sample-pi-session"]
	if pi.Tool != "pi" || pi.Metrics != 6 || pi.Logs != 2 {
		t.Fatalf("pi %+v", pi)
	}
	assertTokens(t, pi.Tokens, map[string]float64{"input_tokens": 1245, "output_tokens": 832})

	var stdout bytes.Buffer
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := cli.Main([]string{"summary", "--db", db.Path(), "--json", "--session", pi.SessionID})
	_ = w.Close()
	os.Stdout = old
	got, _ := io.ReadAll(r)
	stdout.Write(got)
	if code != 0 {
		t.Fatal(code, stdout.String())
	}
	var printed map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &printed); err != nil {
		t.Fatal(err, stdout.String())
	}
	calls := printed["sessions"].([]any)[0].(map[string]any)["tool_calls"].([]any)
	var names []string
	for i, raw := range calls {
		call := raw.(map[string]any)
		names = append(names, call["name"].(string))
		if i == 0 && (call["success"] != "true" || call["call_id"] != "call_sample_bash_1") {
			t.Fatalf("%v", call)
		}
	}
	if strings.Join(names, ",") != "bash,read,bash" {
		t.Fatal(names)
	}

	if status, body := postChunked(t, base, "/v1/traces", chunkTrace(), "application/x-protobuf", "chunked", true, ""); status != 200 || len(body) != 0 {
		t.Fatalf("proto trace %d %q", status, body)
	}
	if status, body := postChunked(t, base, "/v1/metrics", counter(4, 3000), "application/x-protobuf", "chunked", true, ""); status != 200 || len(body) != 0 {
		t.Fatalf("proto metric %d %q", status, body)
	}
	if status, body := postChunked(t, base, "/v1/logs", piLog(), "application/x-protobuf", "chunked", true, ""); status != 200 || len(body) != 0 {
		t.Fatalf("proto log %d %q", status, body)
	}
	found := false
	for _, row := range mustLoad(t, db) {
		if deref(row.ServiceName) == "pi" && deref(row.Name) == "chat sample-model" {
			found = true
		}
	}
	if !found {
		t.Fatal("chunked protobuf span was not stored")
	}
}

func TestBadChunkedBodies(t *testing.T) {
	base, db := start(t)
	status, body := postChunked(t, base, "/v1/traces", []byte("zzzz\r\n"), "application/json", "chunked", false, "")
	if status != 400 || string(body) != "invalid chunk size" {
		t.Fatalf("%d %q", status, body)
	}
	status, body = postChunked(t, base, "/v1/metrics", []byte("7a1201\r\n"), "application/json", "chunked", false, "")
	if status != 413 || string(body) != "payload too large" {
		t.Fatalf("%d %q", status, body)
	}
	status, body = postChunked(t, base, "/v1/logs", []byte("{}"), "application/json", "gzip, chunked", false, "")
	if status != 400 || string(body) != "unsupported transfer encoding" {
		t.Fatalf("%d %q", status, body)
	}
	if rows := mustLoad(t, db); len(rows) != 0 {
		t.Fatalf("%d rows", len(rows))
	}
}

func TestGRPCExport(t *testing.T) {
	_, db, grpcAddr := startBoth(t)
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := coltracepb.NewTraceServiceClient(conn)
	_, err = client.Export(t.Context(), &coltracepb.ExportTraceServiceRequest{
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
						kv("pi.session.id", "grpc-session"),
						kv("sdf.fixture", "sample"),
					},
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := mustLoad(t, db)
	if len(rows) != 1 || deref(rows[0].SessionID) != "grpc-session" || deref(rows[0].TraceID) != strings.Repeat("11", 16) {
		t.Fatalf("%+v", rows)
	}
	metrics := colmetricspb.NewMetricsServiceClient(conn)
	_, err = metrics.Export(t.Context(), &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: resource("pi", ""),
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "gen_ai.client.token.usage",
					Unit: "{token}",
					Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
						AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
						DataPoints: []*metricspb.NumberDataPoint{{
							TimeUnixNano: 1_500_000_000,
							Value:        &metricspb.NumberDataPoint_AsInt{AsInt: 10},
							Attributes: []*commonpb.KeyValue{
								kv("gen_ai.token.type", "input"),
							},
						}},
					}},
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	report := summary.Summarize(mustLoad(t, db), false)
	if len(report.Sessions) != 1 || report.Sessions[0].Metrics != 1 {
		t.Fatalf("%+v unattributed %+v", report.Sessions, report.Unattributed)
	}
	got, _ := report.Sessions[0].Tokens.Get("input")
	if got != 10 || deref(report.Sessions[0].TokensSource) != "metrics gen_ai.client.token.usage" {
		t.Fatalf("tokens %v source %s", report.Sessions[0].Tokens, deref(report.Sessions[0].TokensSource))
	}
}

func start(t *testing.T) (string, *store.Store) {
	t.Helper()
	base, db, _ := startBoth(t)
	return base, db
}

func startBoth(t *testing.T) (string, *store.Store, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true}
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

type response struct {
	status int
	body   string
}

func get(t *testing.T, url string) response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, string(body)}
}

func post(t *testing.T, url, contentType string, body []byte) response {
	t.Helper()
	return postBytes(t, url, contentType, body)
}

func postBytes(t *testing.T, url, contentType string, body []byte) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, string(got)}
}

func postChunked(t *testing.T, base, path string, payload []byte, contentType, transfer string, frame bool, extra string) (int, []byte) {
	t.Helper()
	host := strings.TrimPrefix(base, "http://")
	hostName, port, err := net.SplitHostPort(host)
	if err != nil {
		t.Fatal(err)
	}
	framed := payload
	if frame {
		framed = encodeChunked(payload)
	}
	request := "POST " + path + " HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Content-Type: " + contentType + "\r\n" +
		"Transfer-Encoding: " + transfer + "\r\n" +
		extra +
		"Connection: close\r\n\r\n"
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(hostName, port), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(append([]byte(request), framed...)); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	header, rest, ok := bytes.Cut(data, []byte("\r\n\r\n"))
	if !ok {
		t.Fatalf("no header in %q", data)
	}
	statusLine := bytes.SplitN(header, []byte("\r\n"), 2)[0]
	fields := bytes.Fields(statusLine)
	if len(fields) < 2 {
		t.Fatalf("status %q", statusLine)
	}
	status, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		t.Fatal(err)
	}
	var length *int
	for _, line := range bytes.Split(header, []byte("\r\n"))[1:] {
		if bytes.HasPrefix(bytes.ToLower(line), []byte("content-length:")) {
			n, err := strconv.Atoi(strings.TrimSpace(string(line[len("content-length:"):])))
			if err != nil {
				t.Fatal(err)
			}
			length = &n
		}
	}
	body := rest
	if length != nil && *length <= len(rest) {
		body = rest[:*length]
	}
	return status, body
}

func encodeChunked(payload []byte) []byte {
	var out []byte
	step := 9
	first := true
	for offset := 0; offset < len(payload); offset += step {
		end := offset + step
		if end > len(payload) {
			end = len(payload)
		}
		part := payload[offset:end]
		size := strconv.FormatInt(int64(len(part)), 16)
		if first {
			size = sprintf04(len(part))
		}
		extension := ""
		if first {
			extension = ";sample=1"
		}
		first = false
		out = append(out, []byte(size+extension+"\r\n")...)
		out = append(out, part...)
		out = append(out, []byte("\r\n")...)
	}
	out = append(out, []byte("0\r\nfixture: sample\r\n\r\n")...)
	return out
}

func sprintf04(n int) string {
	s := strconv.FormatInt(int64(n), 16)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

func counter(value int, timeNano uint64) []byte {
	req := &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kv("service.name", "claude-code"),
				kv("session.id", "proto-session"),
				kv("sdf.fixture", "sample"),
			}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "claude_code.token.usage",
					Unit: "tokens",
					Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
						AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
						IsMonotonic:            true,
						DataPoints: []*metricspb.NumberDataPoint{{
							TimeUnixNano: timeNano,
							Value:        &metricspb.NumberDataPoint_AsInt{AsInt: int64(value)},
							Attributes: []*commonpb.KeyValue{
								kv("type", "input"),
								kv("session.id", "proto-session"),
								kv("sdf.fixture", "sample"),
							},
						}},
					}},
				}},
			}},
		}},
	}
	raw, err := proto.Marshal(req)
	if err != nil {
		panic(err)
	}
	return raw
}

func chunkTrace() []byte {
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: resource("pi", ""),
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:           bytes.Repeat([]byte{0x12}, 16),
					SpanId:            bytes.Repeat([]byte{0x34}, 8),
					Name:              "chat sample-model",
					StartTimeUnixNano: 10,
					EndTimeUnixNano:   20,
					Attributes: []*commonpb.KeyValue{
						kv("session.id", "proto-chunk-session"),
						kv("gen_ai.request.model", "sample-model"),
						kv("sdf.fixture", "sample"),
					},
				}},
			}},
		}},
	}
	raw, _ := proto.Marshal(req)
	return raw
}

func piLog() []byte {
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: resource("pi", ""),
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: 10,
					Body:         &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "pi session proto-chunk-session started"}},
					Attributes: []*commonpb.KeyValue{
						kv("event.name", "pi.session.start"),
						kv("pi.session.id", "proto-chunk-session"),
						kv("gen_ai.system", "pi"),
						kv("sdf.fixture", "sample"),
					},
				}},
			}},
		}},
	}
	raw, _ := proto.Marshal(req)
	return raw
}

func resource(service, session string) *resourcepb.Resource {
	attrs := []*commonpb.KeyValue{kv("service.name", service), kv("sdf.fixture", "sample")}
	if session != "" {
		attrs = append(attrs, kv("session.id", session))
	}
	return &resourcepb.Resource{Attributes: attrs}
}

func kv(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func mustLoad(t *testing.T, db *store.Store) []record.Record {
	t.Helper()
	rows, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func firstBlob(payload map[string]any) []any {
	for _, key := range []string{"resourceMetrics", "resourceLogs", "resourceSpans"} {
		if blobs, ok := payload[key].([]any); ok {
			return blobs
		}
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func assertTokens(t *testing.T, got summary.OrderedFloats, want map[string]float64) {
	t.Helper()
	if got.Len() != len(want) {
		t.Fatalf("tokens %#v want %#v", got.Vals, want)
	}
	for key, value := range want {
		have, ok := got.Get(key)
		if !ok || have != value {
			t.Fatalf("token %s=%v want %v in %#v", key, have, value, got.Vals)
		}
	}
}

func spanNamed(nodes []summary.SpanNode, name string) bool {
	for _, node := range nodes {
		if node.Name == name || spanNamed(node.Children, name) {
			return true
		}
	}
	return false
}

func near(got, want float64) bool {
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}
