package setup

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func grokReq(home string, write bool) Request {
	return Request{Agent: "grok", Home: home, Write: write, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}}
}

func clearGrokEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"GROK_HOME", "GROK_EXTERNAL_OTEL", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_LOG_USER_PROMPTS", "OTEL_LOG_ASSISTANT_RESPONSES", "OTEL_LOG_TOOL_CONTENT"} {
		t.Setenv(key, "")
	}
}

func TestGrokPrintDoesNotWrite(t *testing.T) {
	clearGrokEnv(t)
	home := t.TempDir()
	res, err := Run(grokReq(home, false))
	if err != nil || res.Wrote {
		t.Fatal(err, res.Text)
	}
	for _, want := range []string{
		"export GROK_EXTERNAL_OTEL='1'",
		"export OTEL_METRICS_EXPORTER='otlp'",
		"export OTEL_LOGS_EXPORTER='otlp'",
		"export OTEL_EXPORTER_OTLP_PROTOCOL='http/protobuf'",
		"export OTEL_EXPORTER_OTLP_ENDPOINT='http://127.0.0.1:4318'",
		`export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $SDF_OTLP_TOKEN"`,
		"[telemetry]\notel_enabled = true\n",
		`otel_endpoint = "http://127.0.0.1:4318"`,
		"not written",
		"24-monitoring-usage.md",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("missing %q in\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "%20") {
		t.Fatal("Grok does not percent-decode the header")
	}
	if _, err := os.Stat(filepath.Join(home, ".grok")); !os.IsNotExist(err) {
		t.Fatal("print created ~/.grok")
	}
}

func TestGrokWriteCreatesConfigAndHookAndIsIdempotent(t *testing.T) {
	clearGrokEnv(t)
	home := t.TempDir()
	res, err := Run(grokReq(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	config := mustReadFile(t, filepath.Join(home, ".grok", "config.toml"))
	if config != grokBlock(Endpoint{Host: "127.0.0.1", HTTPPort: 4318}) {
		t.Fatalf("config %q", config)
	}
	hookPath := filepath.Join(home, ".grok", "hooks", "sdf-session.json")
	var root map[string]any
	if err := json.Unmarshal([]byte(mustReadFile(t, hookPath)), &root); err != nil {
		t.Fatal(err)
	}
	hooks := root["hooks"].(map[string]any)
	for _, event := range []string{"SessionStart", "SessionEnd"} {
		groups := hooks[event].([]any)
		cmd := groups[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
		if !strings.Contains(cmd, `"tool":"grok-cli"`) || !strings.Contains(cmd, "http://127.0.0.1:4318/v1/sessions") {
			t.Fatalf("%s %s", event, cmd)
		}
	}
	again, err := Run(grokReq(home, true))
	if err != nil || again.Wrote || !strings.Contains(again.Text, "already has these values") {
		t.Fatalf("second run: %v %s", err, again.Text)
	}
	got, err := Configured(grokReq(home, false))
	if err != nil || !strings.Contains(got, "points at http://127.0.0.1:4318: true") || !strings.Contains(got, "sdf-session.json: true") {
		t.Fatal(err, got)
	}
}

func TestGrokWriteMergesAnExistingTelemetryTable(t *testing.T) {
	clearGrokEnv(t)
	home := t.TempDir()
	path := filepath.Join(home, ".grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "# mine\n[models]\ndefault = \"grok-4.5\"\n\n[telemetry]\ntrace_upload = false\notel_endpoint = \"https://collector.corp.example:4318\" # corp\notel_log_user_prompts = true\n\n[cli]\nauto_update = true\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(grokReq(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	if !strings.Contains(res.Text, `replacing telemetry.otel_endpoint = "https://collector.corp.example:4318"`) {
		t.Fatal(res.Text)
	}
	if !strings.Contains(res.Text, "telemetry.otel_log_user_prompts = true exports content") {
		t.Fatal(res.Text)
	}
	if backup := mustReadFile(t, path+".sdf-bak"); backup != original {
		t.Fatalf("backup %q", backup)
	}
	want := "# mine\n[models]\ndefault = \"grok-4.5\"\n\n[telemetry]\notel_enabled = true\notel_metrics_exporter = \"otlp\"\notel_logs_exporter = \"otlp\"\notel_protocol = \"http/protobuf\"\ntrace_upload = false\notel_endpoint = \"http://127.0.0.1:4318\"\notel_log_user_prompts = true\n\n[cli]\nauto_update = true\n"
	if got := mustReadFile(t, path); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	again, err := Run(grokReq(home, true))
	if err != nil || again.Wrote {
		t.Fatalf("second run: %v %s", err, again.Text)
	}
}

func TestGrokWriteAppendsATableAfterOtherTables(t *testing.T) {
	clearGrokEnv(t)
	home := t.TempDir()
	path := filepath.Join(home, ".grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"grok-4.5\""), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(grokReq(home, true)); err != nil {
		t.Fatal(err)
	}
	want := "[models]\ndefault = \"grok-4.5\"\n\n" + grokBlock(Endpoint{Host: "127.0.0.1", HTTPPort: 4318})
	if got := mustReadFile(t, path); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestGrokWriteRefusesWhatItCannotMerge(t *testing.T) {
	clearGrokEnv(t)
	for _, body := range []string{
		"telemetry.otel_enabled = false\n",
		"telemetry = { otel_enabled = false }\n",
		"[telemetry]\na = 1\n[telemetry]\nb = 2\n",
		"[telemetry.extra]\na = 1\n",
		"[agent]\nprompt = \"\"\"\nhi\n\"\"\"\n",
		"[telemetry]\notel_enabled = false\notel_enabled = true\n",
	} {
		home := t.TempDir()
		path := filepath.Join(home, ".grok", "config.toml")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Run(grokReq(home, true))
		if err == nil || !strings.Contains(err.Error(), "it was not changed") {
			t.Fatalf("%q: %v", body, err)
		}
		if got := mustReadFile(t, path); got != body {
			t.Fatalf("%q changed to %q", body, got)
		}
		if _, err := os.Stat(filepath.Join(home, ".grok", "hooks")); !os.IsNotExist(err) {
			t.Fatalf("%q: hook written after a refused merge", body)
		}
	}
}

func TestGrokHonorsGrokHomeAndWarnsOnContentEnv(t *testing.T) {
	clearGrokEnv(t)
	home := t.TempDir()
	custom := filepath.Join(t.TempDir(), "gh")
	t.Setenv("GROK_HOME", custom)
	t.Setenv("OTEL_LOG_USER_PROMPTS", "1")
	res, err := Run(grokReq(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	if !strings.Contains(res.Text, "warning: OTEL_LOG_USER_PROMPTS=1") {
		t.Fatal(res.Text)
	}
	for _, p := range []string{filepath.Join(custom, "config.toml"), filepath.Join(custom, "hooks", "sdf-session.json")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".grok")); !os.IsNotExist(err) {
		t.Fatal("wrote ~/.grok while GROK_HOME was set")
	}
}

func TestGrokHookMergeKeepsOtherEntries(t *testing.T) {
	clearGrokEnv(t)
	home := t.TempDir()
	hookPath := filepath.Join(home, ".grok", "hooks", "sdf-session.json")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o700); err != nil {
		t.Fatal(err)
	}
	old := grokHookCommand("open", "http://127.0.0.1:9999/v1/sessions")
	existing := `{"note":"keep","hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"echo mine"},{"type":"command","command":` + jsonString(old) + `}]}]}}`
	if err := os.WriteFile(hookPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(grokReq(home, true)); err != nil {
		t.Fatal(err)
	}
	text := mustReadFile(t, hookPath)
	if !strings.Contains(text, `"note": "keep"`) || !strings.Contains(text, "echo mine") || strings.Contains(text, "9999") || strings.Count(text, "grok-cli") != 2 {
		t.Fatal(text)
	}
}

func TestGrokHookCommandPostsAndSkipsAChildSession(t *testing.T) {
	var posts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		posts = append(posts, string(body))
		w.WriteHeader(200)
	}))
	defer srv.Close()
	// Field order and aliases follow HookEventEnvelope.to_hook_json in
	// grok-build xai-grok-hooks/src/event.rs (serde_json, sorted keys).
	start := `{"cwd":"/repo","hookEventName":"session_start","hook_event_name":"SessionStart","sessionId":"0199a1b2-c3d4","session_id":"0199a1b2-c3d4","source":"startup","timestamp":"2026-11-23T07:00:00Z","workspaceRoot":"/repo"}`
	runHook(t, grokHookCommand("open", srv.URL), start)
	end := `{"cwd":"/repo","hookEventName":"session_end","hook_event_name":"SessionEnd","reason":"exit","sessionId":"0199a1b2-c3d4","session_id":"0199a1b2-c3d4","timestamp":"2026-11-23T07:05:00Z","turnCount":3,"workspaceRoot":"/repo"}`
	runHook(t, grokHookCommand("close", srv.URL), end)
	child := `{"hookEventName":"session_end","reason":"exit","sessionId":"child-1","session_id":"child-1","subagentType":"explore"}`
	runHook(t, grokHookCommand("close", srv.URL), child)
	if len(posts) != 2 {
		t.Fatalf("posts %q", posts)
	}
	if posts[0] != `{"tool":"grok-cli","session_id":"0199a1b2-c3d4","edge":"open","reason":"startup"}` {
		t.Fatal(posts[0])
	}
	if posts[1] != `{"tool":"grok-cli","session_id":"0199a1b2-c3d4","edge":"close","reason":"exit"}` {
		t.Fatal(posts[1])
	}
}

func TestClaudeHookSkipsWhenGrokRunsIt(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(200)
	}))
	defer srv.Close()
	command := claudeHooks(srv.URL)["SessionStart"]
	in := `{"session_id":"abc123","source":"startup"}`
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "GROK_HOOK_EVENT=session_start")
	cmd.Stdin = strings.NewReader(in)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if posts != 0 {
		t.Fatalf("claude hook posted %d times under Grok", posts)
	}
	cmd = exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "GROK_HOOK_EVENT=")
	cmd.Stdin = strings.NewReader(in)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if posts != 1 {
		t.Fatalf("claude hook posted %d times under Claude Code", posts)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
