// Package demo pushes one synthetic coding-agent session through the real
// local receiver, so a new user can see a summary without an agent, an API
// key, or a network connection.
//
// The session is shaped like Claude Code telemetry. Every resource carries
// sdf.fixture=demo so it is never mistaken for a live capture. Traces and logs
// go over OTLP/HTTP JSON and metrics go over OTLP/gRPC, to the same listeners
// `sdf serve` opens. The summary is then read back from the SQLite store.
package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
)

// Model is the model the synthetic session reports. It is in the bundled
// price table, so the computed cost is known.
const Model = "claude-sonnet-5"

// Options configures one demo run.
type Options struct {
	// DBPath is the SQLite file the receiver writes. Required.
	DBPath string
	// Host, HTTPPort, and GRPCPort are the receiver listeners. Port 0 picks
	// a free port, so the demo does not collide with a running `sdf serve`.
	Host     string
	HTTPPort int
	GRPCPort int
	// SessionID names the synthetic session. Empty uses sdf-demo-<unix>.
	SessionID string
	// Now anchors the timestamps. Zero uses time.Now.
	Now time.Time
	// Log receives one line per step. Nil discards them.
	Log io.Writer
}

// Result is what the demo sent and what the summary made of it.
type Result struct {
	SessionID string
	HTTPAddr  string
	GRPCAddr  string
	Report    summary.Report
}

// Run starts a receiver, sends the synthetic session, stops the receiver,
// and summarizes the store with the bundled price table.
func Run(ctx context.Context, opt Options) (Result, error) {
	if opt.DBPath == "" {
		return Result{}, fmt.Errorf("demo needs a database path")
	}
	logw := opt.Log
	if logw == nil {
		logw = io.Discard
	}
	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}
	session := opt.SessionID
	if session == "" {
		session = "sdf-demo-" + strconv.FormatInt(now.Unix(), 10)
	}
	host := opt.Host
	if host == "" {
		host = "127.0.0.1"
	}

	db, err := store.Open(opt.DBPath)
	if err != nil {
		return Result{}, err
	}
	srv := &server.Server{Host: host, HTTPPort: opt.HTTPPort, GRPCPort: opt.GRPCPort, Store: db, Quiet: true}
	if err := srv.Start(); err != nil {
		_ = db.Close()
		return Result{}, fmt.Errorf("start receiver: %w", err)
	}
	res := Result{SessionID: session, HTTPAddr: srv.HTTPAddress(), GRPCAddr: srv.GRPCAddress()}
	fmt.Fprintf(logw, "receiver: OTLP/HTTP http://%s, OTLP/gRPC %s, db %s\n", res.HTTPAddr, res.GRPCAddr, opt.DBPath)

	sendErr := send(ctx, logw, res, session, now)
	srv.Shutdown()
	if sendErr != nil {
		_ = db.Close()
		return res, sendErr
	}
	records, err := db.Load()
	_ = db.Close()
	if err != nil {
		return res, err
	}
	report := summary.SummarizeWith(records, false, pricing.Bundled())
	outcome.Attach(&report, outcome.Options{})
	filtered := report.Sessions[:0:0]
	for _, item := range report.Sessions {
		if item.SessionID == session {
			filtered = append(filtered, item)
		}
	}
	rolled := outcome.CostPerAccepted(filtered)
	res.Report = summary.Report{Sessions: filtered, Unattributed: []summary.Unattributed{}, CostPerAccepted: &rolled}
	if len(filtered) != 1 {
		return res, fmt.Errorf("demo session %s not found in the summary", session)
	}
	return res, nil
}

func send(ctx context.Context, logw io.Writer, res Result, session string, now time.Time) error {
	client := &http.Client{Timeout: 10 * time.Second}
	base := "http://" + res.HTTPAddr
	if err := getHealth(ctx, client, base+"/health"); err != nil {
		return err
	}
	fmt.Fprintln(logw, "sent:     GET /health -> ok")
	payloads := Payloads(session, now)
	for _, signal := range []string{"traces", "logs"} {
		if err := postJSON(ctx, client, base+"/v1/"+signal, payloads[signal]); err != nil {
			return fmt.Errorf("post %s: %w", signal, err)
		}
		fmt.Fprintf(logw, "sent:     %s over OTLP/HTTP JSON -> 200\n", signal)
	}
	if err := exportMetricsGRPC(ctx, res.GRPCAddr, payloads["metrics"]); err != nil {
		return fmt.Errorf("export metrics: %w", err)
	}
	fmt.Fprintln(logw, "sent:     metrics over OTLP/gRPC -> ok")
	return nil
}

func getHealth(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health: status %d", resp.StatusCode)
	}
	return nil
}

func postJSON(ctx context.Context, client *http.Client, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, bytes.TrimSpace(text))
	}
	return nil
}

func exportMetricsGRPC(ctx context.Context, addr string, body []byte) error {
	var req colmetricspb.ExportMetricsServiceRequest
	if err := protojson.Unmarshal(body, &req); err != nil {
		return err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = colmetricspb.NewMetricsServiceClient(conn).Export(ctx, &req)
	return err
}

// Payloads returns the OTLP JSON bodies for traces, logs, and metrics of one
// synthetic session that starts 42 seconds before now.
func Payloads(session string, now time.Time) map[string][]byte {
	start := now.Add(-42 * time.Second)
	at := func(d time.Duration) string { return strconv.FormatInt(start.Add(d).UnixNano(), 10) }
	resource := map[string]any{"attributes": []any{
		str("service.name", "claude-code"),
		str("session.id", session),
		str("sdf.fixture", "demo"),
	}}
	sid := str("session.id", session)
	model := str("model", Model)

	span := func(name string, from, to time.Duration, attrs ...any) map[string]any {
		return map[string]any{
			"traceId":           "5d4f0000000000000000000000000001",
			"spanId":            fmt.Sprintf("%016x", len(name)*1000+int(from/time.Millisecond)),
			"name":              name,
			"startTimeUnixNano": at(from),
			"endTimeUnixNano":   at(to),
			"attributes":        append([]any{sid}, attrs...),
		}
	}
	tool := func(name, id string, from, to time.Duration) map[string]any {
		return span("claude_code.tool", from, to, str("tool_name", name), str("tool_use_id", id))
	}
	traces := map[string]any{"resourceSpans": []any{map[string]any{
		"resource": resource,
		"scopeSpans": []any{map[string]any{"spans": []any{
			span("claude_code.interaction", 0, 42*time.Second, model, str("span.type", "claude_code.interaction")),
			tool("Read", "toolu_demo_read", 3*time.Second, 3200*time.Millisecond),
			tool("Edit", "toolu_demo_edit", 15*time.Second, 15400*time.Millisecond),
			tool("Bash", "toolu_demo_bash", 28*time.Second, 35*time.Second),
		}}},
	}}}

	logRec := func(d time.Duration, event string, attrs ...any) map[string]any {
		return map[string]any{
			"timeUnixNano": at(d),
			"body":         map[string]any{"stringValue": "claude_code." + event},
			"attributes":   append([]any{str("event.name", event), sid}, attrs...),
		}
	}
	toolResult := func(d time.Duration, name, id string, ms int64) map[string]any {
		return logRec(d, "tool_result", str("tool_name", name), str("tool_use_id", id), str("success", "true"), num("duration_ms", ms))
	}
	logs := map[string]any{"resourceLogs": []any{map[string]any{
		"resource": resource,
		"scopeLogs": []any{map[string]any{"logRecords": []any{
			logRec(1*time.Second, "user_prompt", num("prompt_length", 64)),
			logRec(2*time.Second, "api_request", model, num("input_tokens", 1800), num("output_tokens", 220), num("duration_ms", 2100)),
			toolResult(3200*time.Millisecond, "Read", "toolu_demo_read", 200),
			toolResult(15400*time.Millisecond, "Edit", "toolu_demo_edit", 400),
			toolResult(35*time.Second, "Bash", "toolu_demo_bash", 7000),
			logRec(38*time.Second, "api_request", model, num("input_tokens", 2400), num("output_tokens", 260), num("duration_ms", 2600)),
		}}},
	}}}

	point := func(value any, attrs ...any) map[string]any {
		p := map[string]any{"timeUnixNano": at(40 * time.Second), "attributes": append(attrs, sid)}
		switch v := value.(type) {
		case int64:
			p["asInt"] = strconv.FormatInt(v, 10)
		case float64:
			p["asDouble"] = v
		}
		return p
	}
	sum := func(name, unit string, points ...any) map[string]any {
		return map[string]any{"name": name, "unit": unit, "sum": map[string]any{
			"aggregationTemporality": "AGGREGATION_TEMPORALITY_DELTA",
			"isMonotonic":            true,
			"dataPoints":             points,
		}}
	}
	metrics := map[string]any{"resourceMetrics": []any{map[string]any{
		"resource": resource,
		"scopeMetrics": []any{map[string]any{"metrics": []any{
			sum("claude_code.token.usage", "tokens",
				point(int64(4200), str("type", "input"), model),
				point(int64(480), str("type", "output"), model),
				point(int64(12000), str("type", "cacheRead"), model),
			),
			sum("claude_code.cost.usage", "USD", point(0.0236, model)),
			sum("claude_code.active_time.total", "s", point(31.0, str("type", "cli"))),
		}}},
	}}}

	out := map[string][]byte{}
	for name, body := range map[string]any{"traces": traces, "logs": logs, "metrics": metrics} {
		encoded, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		out[name] = encoded
	}
	return out
}

func str(key, value string) map[string]any {
	return map[string]any{"key": key, "value": map[string]any{"stringValue": value}}
}

func num(key string, value int64) map[string]any {
	return map[string]any{"key": key, "value": map[string]any{"intValue": strconv.FormatInt(value, 10)}}
}
