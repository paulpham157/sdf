package summary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/samplepath"
)

const (
	opencodeSession = "ses_sampleopencode0001"
	piSession       = "2026-01-01T00-00-00-000Z_sample-pi-session"
	freeModel       = "recursive/flint-v1.0:free"
)

func loadSignal(t *testing.T, signal, name string) []record.Record {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(samplepath.Dir(), name))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := otlp.Decode(signal, body, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func opencodeRows(t *testing.T) []record.Record {
	t.Helper()
	return append(loadSignal(t, "traces", "opencode_traces.json"), loadSignal(t, "logs", "opencode_logs.json")...)
}

func piRows(t *testing.T) []record.Record {
	t.Helper()
	return append(append(loadSignal(t, "traces", "pi_traces.json"), loadSignal(t, "logs", "pi_logs.json")...), loadSignal(t, "metrics", "pi_metrics.json")...)
}

func TestFixturesAreMarkedSampleAndHaveNoSecrets(t *testing.T) {
	for _, name := range []string{"opencode_traces.json", "opencode_logs.json", "pi_traces.json", "pi_logs.json", "pi_metrics.json"} {
		text, err := os.ReadFile(filepath.Join(samplepath.Dir(), name))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(text))
		if !strings.Contains(lower, "sdf.fixture") || !strings.Contains(lower, "sample") {
			t.Fatal(name)
		}
		if strings.Contains(lower, "api_key") || strings.Contains(lower, "authorization") || strings.Contains(lower, "hello from") {
			t.Fatal(name)
		}
		var payload map[string]any
		if err := json.Unmarshal(text, &payload); err != nil {
			t.Fatal(err)
		}
		var blobs []any
		for _, key := range []string{"resourceSpans", "resourceLogs", "resourceMetrics"} {
			if found, ok := payload[key].([]any); ok {
				blobs = found
			}
		}
		resource := blobs[0].(map[string]any)["resource"].(map[string]any)
		values := map[string]any{}
		for _, raw := range resource["attributes"].([]any) {
			item := raw.(map[string]any)
			values[item["key"].(string)] = item["value"].(map[string]any)["stringValue"]
		}
		if values["sdf.fixture"] != "sample" {
			t.Fatal(name, values)
		}
	}
}

func TestOpencodeSessionUsesSpanTokensWithoutSummingThePrefix(t *testing.T) {
	report := Summarize(opencodeRows(t), false)
	if len(report.Sessions) != 1 || report.Sessions[0].SessionID != opencodeSession {
		t.Fatalf("%+v", report.Sessions)
	}
	session := report.Sessions[0]
	in, _ := session.Tokens.Get("input_tokens")
	out, _ := session.Tokens.Get("output_tokens")
	cache, _ := session.Tokens.Get("cache_read.input_tokens")
	if session.Tool != "opencode" || strings.Join(session.ServiceNames, ",") != "opencode" || strings.Join(session.Models, ",") != freeModel {
		t.Fatalf("%+v", session)
	}
	if session.Traces != 13 || session.Logs != 0 || session.Metrics != 0 {
		t.Fatalf("signals %d %d %d", session.Traces, session.Logs, session.Metrics)
	}
	if session.DurationMS == nil || !near(*session.DurationMS, 3437.29) || deref(session.DurationSource) != "traces SessionRunner.drain" {
		t.Fatalf("duration %v %s", session.DurationMS, deref(session.DurationSource))
	}
	if in != 5822 || in == 5601+5822 || out != 212+94 || cache != 5760 || cache == 256+5760 {
		t.Fatalf("tokens %v", session.Tokens.Vals)
	}
	if deref(session.TokensSource) != "traces SessionStep.attempt" || !strings.Contains(deref(session.TokensMethod), "max") || session.CostUSD != nil {
		t.Fatalf("source %s method %s cost %v", deref(session.TokensSource), deref(session.TokensMethod), session.CostUSD)
	}
	if len(session.ToolCalls) != 1 || deref(session.ToolCalls[0].Name) != "write" || deref(session.ToolCalls[0].CallID) != "call_samplewrite0001" || deref(session.ToolCalls[0].Success) != "unknown" || session.ToolCalls[0].DurationMS == nil || !near(*session.ToolCalls[0].DurationMS, 3.602) || session.ToolCalls[0].Source != "traces Tool.execute" {
		t.Fatalf("%+v", session.ToolCalls)
	}
	if session.SpansHidden != 4 {
		t.Fatal(session.SpansHidden)
	}
	shown := names(session.Spans)
	for _, name := range []string{"SessionRunner.drain", "SessionRunner.runStep", "SessionStep.attempt", "POST", "Tool.execute", "FileWrite.write"} {
		if !shown[name] {
			t.Fatal("missing", name, shown)
		}
	}
	for _, name := range []string{"LLM.compile", "OpenAIChat.fromRequest", "FileWrite.commit", "Plugin.load", "FileSystem.readFileStringSafe"} {
		if shown[name] {
			t.Fatal("shown", name)
		}
	}
	attempt := session.Spans[0].Children[0].Children[0]
	if attempt.Name != "SessionStep.attempt" {
		t.Fatal(attempt.Name)
	}
	var kids []string
	for _, child := range attempt.Children {
		kids = append(kids, child.Name)
	}
	if strings.Join(kids, ",") != "POST,Tool.execute" || deref(attempt.Children[1].ToolName) != "write" || attempt.Children[1].Children[0].Name != "FileWrite.write" {
		t.Fatalf("%+v", attempt)
	}
	reasons := map[string]string{}
	for _, item := range session.Missing {
		reasons[item.Signal] = item.Reason
	}
	if !strings.Contains(reasons["logs"], "session.id") || !strings.Contains(reasons["logs"], "per-session") || reasons["metrics"] != "OpenCode v2 does not export OTLP metrics." || reasons["traces"] != "" {
		t.Fatal(reasons)
	}
	if len(report.Unattributed) != 1 || report.Unattributed[0].Tool != "opencode" || report.Unattributed[0].Traces != 1 || report.Unattributed[0].Logs != 1 {
		t.Fatalf("%+v", report.Unattributed)
	}
	text := Format(report)
	for _, want := range []string{"tool: opencode", "Tool.execute write call_id=call_samplewrite0001", "success=unknown", "input_tokens=5822"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
}

func TestPiSessionSumsTurnTokensAndDoesNotAddTheMetric(t *testing.T) {
	report := Summarize(piRows(t), false)
	if len(report.Sessions) != 1 || report.Sessions[0].SessionID != piSession {
		t.Fatalf("%+v", report.Sessions)
	}
	session := report.Sessions[0]
	if session.Tool != "pi" || strings.Join(session.ServiceNames, ",") != "pi" || strings.Join(session.Models, ",") != freeModel {
		t.Fatalf("%+v models %#v", session.Tool, session.Models)
	}
	if session.Traces != 13 || session.Logs != 2 || session.Metrics != 6 {
		t.Fatalf("signals %d %d %d", session.Traces, session.Logs, session.Metrics)
	}
	if session.DurationMS == nil || !near(*session.DurationMS, 7252.42) || deref(session.DurationSource) != "traces invoke_agent" {
		t.Fatalf("duration %v %s", session.DurationMS, deref(session.DurationSource))
	}
	in, _ := session.Tokens.Get("input_tokens")
	out, _ := session.Tokens.Get("output_tokens")
	if in != 1245 || out != 832 || in == 975 {
		t.Fatalf("%v", session.Tokens.Vals)
	}
	if deref(session.TokensSource) != "traces chat" || !strings.Contains(deref(session.TokensMethod), "additive") || !strings.Contains(deref(session.TokensMethod), "gen_ai.client.token.usage") {
		t.Fatalf("%s %s", deref(session.TokensSource), deref(session.TokensMethod))
	}
	if _, ok := session.Tokens.Get("reasoning_tokens"); ok {
		t.Fatal("reasoning included")
	}
	if _, ok := session.Tokens.Get("cache_read_input_tokens"); ok {
		t.Fatal("cache included")
	}
	if session.CostUSD == nil || *session.CostUSD != 0 || deref(session.CostSource) != "traces chat pi.cost.usd" {
		t.Fatalf("cost %v %s", session.CostUSD, deref(session.CostSource))
	}
	type call struct {
		name     string
		id       string
		success  string
		duration float64
	}
	var calls []call
	for _, item := range session.ToolCalls {
		calls = append(calls, call{deref(item.Name), deref(item.CallID), deref(item.Success), derefFloat(item.DurationMS)})
	}
	want := []call{
		{"bash", "call_sample_bash_1", "true", 10.643},
		{"read", "call_sample_read_1", "true", 5.28},
		{"bash", "call_sample_bash_2", "true", 5.815},
	}
	if len(calls) != len(want) {
		t.Fatalf("%+v", session.ToolCalls)
	}
	for i := range want {
		if calls[i].name != want[i].name || calls[i].id != want[i].id || calls[i].success != want[i].success || !near(calls[i].duration, want[i].duration) {
			t.Fatalf("got %+v want %+v", calls[i], want[i])
		}
	}
	var inputMetric, outputMetric *MetricLine
	for i := range session.MetricLines {
		line := &session.MetricLines[i]
		if line.Name != "gen_ai.client.token.usage" || line.Detail == nil {
			continue
		}
		if strings.Contains(*line.Detail, "input") {
			inputMetric = line
		}
		if strings.Contains(*line.Detail, "output") {
			outputMetric = line
		}
		if line.Value == 9999 {
			t.Fatal("outside metric was attributed")
		}
	}
	if inputMetric == nil || outputMetric == nil || inputMetric.Value != in || outputMetric.Value != out {
		t.Fatalf("%+v", session.MetricLines)
	}
	if session.SpansHidden != 1 {
		t.Fatal(session.SpansHidden)
	}
	shown := names(session.Spans)
	for _, name := range []string{"invoke_agent pi", "pi.turn", "chat " + freeModel, "execute_tool bash", "execute_tool read"} {
		if !shown[name] {
			t.Fatal("missing", name, shown)
		}
	}
	if shown["resolve_tools"] {
		t.Fatal("resolve_tools shown")
	}
	root := session.Spans[0]
	if root.Name != "invoke_agent pi" || len(root.Children) != 4 {
		t.Fatalf("%s children %d", root.Name, len(root.Children))
	}
	for _, child := range root.Children {
		if child.Name != "pi.turn" {
			t.Fatal(child.Name)
		}
	}
	var turnKids []string
	for _, child := range root.Children[0].Children {
		turnKids = append(turnKids, child.Name)
	}
	if strings.Join(turnKids, ",") != "chat "+freeModel+",execute_tool bash" || deref(root.Children[0].Children[1].Success) != "true" {
		t.Fatalf("%v", turnKids)
	}
	reasons := map[string]string{}
	for _, item := range session.Missing {
		reasons[item.Signal] = item.Reason
	}
	if !strings.Contains(reasons["log catalog"], "session start and end") || reasons["logs"] != "" || reasons["metrics"] != "" {
		t.Fatal(reasons)
	}
	if report.Unattributed[0].Tool != "pi" {
		t.Fatal(report.Unattributed)
	}
	found := false
	for _, name := range report.Unattributed[0].Names {
		if strings.HasPrefix(name, "gen_ai.client.token.usage=9999") {
			found = true
		}
	}
	if !found {
		t.Fatal(report.Unattributed[0].Names)
	}
	text := Format(report)
	for _, want := range []string{
		"tool: pi",
		"execute_tool bash call_id=call_sample_bash_1",
		"success=true (traces)",
		"bash call_id=call_sample_bash_1 success=true",
		"pi.session.start x1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
	logs := piRows(t)
	var logRows []record.Record
	for _, row := range logs {
		if row.Signal == "logs" {
			logRows = append(logRows, row)
		}
	}
	if deref(logRows[0].SessionID) != piSession {
		t.Fatal(deref(logRows[0].SessionID))
	}
	if _, ok := logRows[0].Attributes["session.id"]; ok {
		t.Fatal("session.id on pi log")
	}
	if _, ok := logRows[0].Attributes["conversation.id"]; ok {
		t.Fatal("conversation.id on pi log")
	}
	var hidden record.Record
	for _, row := range piRows(t) {
		if deref(row.Name) == "resolve_tools" {
			hidden = row
		}
	}
	if _, ok := hidden.Attributes["session.id"]; ok || deref(hidden.SessionID) != piSession {
		t.Fatalf("%v %s", hidden.Attributes, deref(hidden.SessionID))
	}
	drain := loadSignal(t, "traces", "opencode_traces.json")
	if deref(drain[0].SpanID) != "a100000000000001" || deref(drain[0].TraceID) != strings.Repeat("1", 32) {
		t.Fatalf("span id %s trace %s", deref(drain[0].SpanID), deref(drain[0].TraceID))
	}
}

func TestPiToolErrorFlagInvertsAndSystemNameIsEnough(t *testing.T) {
	errored := record.Record{
		Signal: "traces", ServiceName: record.Str("pi"), SessionID: record.Str("pi-error"), Name: record.Str("execute_tool bash"),
		StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(2_000_000),
		Attributes: map[string]any{"gen_ai.tool.name": "bash", "gen_ai.tool.call.id": "call_err", "pi.tool.is_error": true},
		Resource:   map[string]any{},
	}
	call := Summarize([]record.Record{errored}, false).Sessions[0].ToolCalls[0]
	if deref(call.Success) != "false" || deref(call.Name) != "bash" {
		t.Fatalf("%+v", call)
	}
	onlySystem := record.Record{
		Signal: "logs", Name: record.Str("pi.session.start"), SessionID: record.Str("from-system"), TimeUnixNano: record.Int64(1),
		Attributes: map[string]any{"gen_ai.system": "pi", "event.name": "pi.session.start"},
		Resource:   map[string]any{},
	}
	if ToolOf(onlySystem) != "pi" || Summarize([]record.Record{onlySystem}, false).Sessions[0].Tool != "pi" {
		t.Fatal("system name")
	}
}

func TestPiMetricsFallBackWhenChatSpansOmitUsage(t *testing.T) {
	span := record.Record{
		ID: 1, Signal: "traces", ServiceName: record.Str("pi"), SessionID: record.Str("pi-metric-only"), Name: record.Str("invoke_agent pi"),
		StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(5_000_000_000), Attributes: map[string]any{"gen_ai.system": "pi"}, Resource: map[string]any{},
	}
	inside := record.Record{
		ID: 2, Signal: "metrics", ServiceName: record.Str("pi"), Name: record.Str("gen_ai.client.token.usage"),
		TimeUnixNano: record.Int64(1_000_000_000), Value: record.Float(1245), MetricTemporality: record.Str("delta"), MetricKind: record.Str("sum"),
		Attributes: map[string]any{"gen_ai.token.type": "input"}, Resource: map[string]any{},
	}
	outside := record.Record{
		ID: 3, Signal: "metrics", ServiceName: record.Str("pi"), Name: record.Str("gen_ai.client.token.usage"),
		TimeUnixNano: record.Int64(20_000_000_000), Value: record.Float(50), MetricTemporality: record.Str("delta"), MetricKind: record.Str("sum"),
		Attributes: map[string]any{"gen_ai.token.type": "input"}, Resource: map[string]any{},
	}
	report := Summarize([]record.Record{span, inside, outside}, false)
	got, _ := report.Sessions[0].Tokens.Get("input")
	if got != 1245 || deref(report.Sessions[0].TokensSource) != "metrics gen_ai.client.token.usage" || report.Unattributed[0].Metrics != 1 {
		t.Fatalf("%v %s unattributed %+v", report.Sessions[0].Tokens.Vals, deref(report.Sessions[0].TokensSource), report.Unattributed)
	}
	found := false
	for _, item := range Summarize([]record.Record{span}, false).Sessions[0].Missing {
		if strings.Contains(item.Reason, "time window") {
			found = true
		}
	}
	if !found {
		t.Fatal("time window reason")
	}
}

func TestSessionIDAcceptsPiAndGenAIConversationKeys(t *testing.T) {
	trace, err := otlp.Decode("traces", []byte(`{
		"resourceSpans": [{
			"resource": {"attributes": [
				{"key": "service.name", "value": {"stringValue": "pi"}},
				{"key": "sdf.fixture", "value": {"stringValue": "sample"}}
			]},
			"scopeSpans": [{"spans": [{
				"name": "invoke_agent pi",
				"traceId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"spanId": "bbbbbbbbbbbbbbbb",
				"startTimeUnixNano": "1",
				"endTimeUnixNano": "2",
				"attributes": [{"key": "gen_ai.conversation.id", "value": {"stringValue": "from-gen-ai"}}]
			}]}]
		}]
	}`), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if deref(trace[0].SessionID) != "from-gen-ai" {
		t.Fatal(deref(trace[0].SessionID))
	}
	log, err := otlp.Decode("logs", []byte(`{
		"resourceLogs": [{
			"resource": {"attributes": [{"key": "sdf.fixture", "value": {"stringValue": "sample"}}]},
			"scopeLogs": [{"logRecords": [{
				"timeUnixNano": "1",
				"body": {"stringValue": "pi session from-pi started"},
				"attributes": [
					{"key": "event.name", "value": {"stringValue": "pi.session.start"}},
					{"key": "pi.session.id", "value": {"stringValue": "from-pi"}},
					{"key": "gen_ai.system", "value": {"stringValue": "pi"}}
				]
			}]}]
		}]
	}`), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if deref(log[0].SessionID) != "from-pi" || deref(log[0].Name) != "pi.session.start" || ToolOf(log[0]) != "pi" {
		t.Fatalf("%+v", log[0])
	}
}

func TestOpencodeAndPiSessionsStaySeparate(t *testing.T) {
	report := Summarize(append(opencodeRows(t), piRows(t)...), false)
	var ids []string
	for _, item := range report.Sessions {
		ids = append(ids, item.SessionID)
	}
	if strings.Join(ids, ",") != opencodeSession+","+piSession {
		t.Fatal(ids)
	}
	var tools []string
	for _, item := range report.Unattributed {
		tools = append(tools, item.Tool)
	}
	if strings.Join(tools, ",") != "opencode,pi" {
		t.Fatal(tools)
	}
}

func near(got, want float64) bool {
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-6
}

func derefFloat(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
