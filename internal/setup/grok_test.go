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
	for _, key := range []string{"GROK_HOME", "GROK_SESSION_ID", "GROK_EXTERNAL_OTEL", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_LOG_USER_PROMPTS", "OTEL_LOG_ASSISTANT_RESPONSES", "OTEL_LOG_TOOL_CONTENT"} {
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
		"export GROK_EXTERNAL_OTEL=1",
		"export OTEL_METRICS_EXPORTER=otlp",
		"export OTEL_LOGS_EXPORTER=otlp",
		"export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318",
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
		t.Fatal("the printed header should use the literal space, which every Grok version accepts")
	}
	if _, err := os.Stat(filepath.Join(home, ".grok")); !os.IsNotExist(err) {
		t.Fatal("print created ~/.grok")
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

func TestGrokHookCommandPostsAndSkipsAChildSession(t *testing.T) {
	clearGrokEnv(t)
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
	runHook(t, grokSessionHookCommand("open", srv.URL), start)
	end := `{"cwd":"/repo","hookEventName":"session_end","hook_event_name":"SessionEnd","reason":"exit","sessionId":"0199a1b2-c3d4","session_id":"0199a1b2-c3d4","timestamp":"2026-11-23T07:05:00Z","turnCount":3,"workspaceRoot":"/repo"}`
	runHook(t, grokSessionHookCommand("close", srv.URL), end)
	child := `{"hookEventName":"session_end","reason":"exit","sessionId":"child-1","session_id":"child-1","subagentType":"explore"}`
	runHook(t, grokSessionHookCommand("close", srv.URL), child)
	if len(posts) != 2 {
		t.Fatalf("posts %q", posts)
	}
	if posts[0] != `{"tool":"grok-cli","session_id":"0199a1b2-c3d4","edge":"open","reason":"startup","cwd":"/repo"}` {
		t.Fatal(posts[0])
	}
	if posts[1] != `{"tool":"grok-cli","session_id":"0199a1b2-c3d4","edge":"close","reason":"exit","cwd":"/repo"}` {
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

func TestGrokHookCommandSendsTheWorkingDirectory(t *testing.T) {
	clearGrokEnv(t)
	var posts []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("payload is not JSON: %v %s", err, body)
		}
		posts = append(posts, got)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	odd := `/tmp/my repo/it's "quoted" \\ back\slash $HOME %s`
	envelope := func(fields map[string]any) string {
		fields["session_id"] = "s1"
		fields["sessionId"] = "s1"
		fields["source"] = "startup"
		fields["hook_event_name"] = "SessionStart"
		// A decoy: another value that contains the key name, escaped.
		fields["transcriptPath"] = `x"workspaceRoot":"/decoy"`
		b, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cmd := grokSessionHookCommand("open", srv.URL)
	runHook(t, cmd, envelope(map[string]any{"cwd": "/tmp/my repo/sub", "workspaceRoot": odd}))
	runHook(t, cmd, envelope(map[string]any{"cwd": "/only/cwd dir"}))
	runHook(t, cmd, envelope(map[string]any{}))
	if len(posts) != 3 {
		t.Fatalf("posts %v", posts)
	}
	if posts[0]["cwd"] != odd {
		t.Fatalf("workspaceRoot: got %q want %q", posts[0]["cwd"], odd)
	}
	if posts[1]["cwd"] != "/only/cwd dir" {
		t.Fatalf("cwd fallback: %q", posts[1]["cwd"])
	}
	if posts[2]["cwd"] != "" || posts[2]["session_id"] != "s1" {
		t.Fatalf("no directory: %v", posts[2])
	}
}
