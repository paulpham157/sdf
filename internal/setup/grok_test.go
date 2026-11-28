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
	for _, key := range []string{"GROK_HOME", "GROK_SESSION_ID", "GROK_EXTERNAL_OTEL", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_LOG_USER_PROMPTS", "OTEL_LOG_ASSISTANT_RESPONSES", "OTEL_LOG_TOOL_DETAILS", "OTEL_LOG_TOOL_CONTENT"} {
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
		"export OTEL_EXPORTER_OTLP_ENDPOINT='http://127.0.0.1:4318'\n",
		`export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $SDF_OTLP_TOKEN"`,
		"[telemetry]\notel_enabled = true\n",
		`otel_endpoint = "http://127.0.0.1:4318"`,
		"not written",
		"24-monitoring-usage.md",
		"custom model and no xAI login",
		"GROK_INTERNAL_OTLP_TRACES_ENDPOINT",
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

// The [telemetry] table sdf writes pins all four otel_log_* gates off, so a
// config.toml written by sdf never turns content export on by omission
// (otel_log_assistant_responses follows otel_log_user_prompts when unset).
func TestGrokConfigPinsOtelLogGatesOff(t *testing.T) {
	clearGrokEnv(t)
	home := t.TempDir()
	res, err := Run(grokReq(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	body, err := os.ReadFile(filepath.Join(home, ".grok", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	gates := []string{"otel_log_user_prompts", "otel_log_assistant_responses", "otel_log_tool_details", "otel_log_tool_content"}
	seen := map[string]int{}
	inTelemetry := false
	for _, line := range strings.Split(string(body), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") {
			inTelemetry = trim == "[telemetry]"
			continue
		}
		key, value, ok := strings.Cut(trim, "=")
		if !ok || strings.HasPrefix(trim, "#") {
			continue
		}
		key = strings.TrimSpace(key)
		if strings.HasPrefix(key, "otel_log_") {
			if !inTelemetry {
				t.Fatalf("%s is outside [telemetry]:\n%s", key, body)
			}
			if strings.TrimSpace(value) != "false" {
				t.Fatalf("%s = %s, want false:\n%s", key, strings.TrimSpace(value), body)
			}
			seen[key]++
		}
	}
	for _, gate := range gates {
		if seen[gate] != 1 {
			t.Fatalf("%s appears %d times, want once:\n%s", gate, seen[gate], body)
		}
	}
	if len(seen) != len(gates) {
		t.Fatalf("unexpected otel_log_* keys %v", seen)
	}
	// The dry run prints the same table.
	dry, err := Run(grokReq(t.TempDir(), false))
	if err != nil {
		t.Fatal(err)
	}
	for _, gate := range gates {
		if !strings.Contains(dry.Text, gate+" = false\n") {
			t.Fatalf("dry run is missing %s = false:\n%s", gate, dry.Text)
		}
	}
}

// grokSessionHookCommand single-quotes the receiver URL with shQuote.
// checkEndpoint already keeps --host inert, so this pins the second layer:
// a URL that reached the hook with shell syntax in it (a quote, $(...), a
// backtick, ;) stays one curl argument and runs nothing.
func TestGrokHookQuotesTheURL(t *testing.T) {
	clearGrokEnv(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	url := "http://127.0.0.1:1/v1/sessions?a='$(touch " + marker + ")`touch " + marker + "`;touch " + marker
	cmd := grokSessionHookCommand("open", url)
	if !strings.Contains(cmd, " "+shQuote(url)+" >/dev/null") {
		t.Fatalf("URL is not single-quoted in\n%s", cmd)
	}
	plain := grokSessionHookCommand("open", "http://127.0.0.1:4318/v1/sessions")
	if !strings.Contains(plain, " 'http://127.0.0.1:4318/v1/sessions' >/dev/null") {
		t.Fatalf("URL is not single-quoted in\n%s", plain)
	}
	runHook(t, cmd, `{"sessionId":"s1","source":"startup","cwd":"/tmp"}`)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the quoted URL ran a command (marker exists, err=%v)", err)
	}
}

// Every TOML form that sets the top-level telemetry key counts as an
// existing table. A line scan saw only "[telemetry]" and "[telemetry.",
// so --write appended a second [telemetry] to the other forms, which Grok
// then failed to parse (duplicate key; checked with grok inspect 1.0.92).
func TestGrokExistingTelemetryInAnyTOMLFormIsLeftAlone(t *testing.T) {
	forms := map[string]string{
		"table":         "model = \"keep\"\n\n[telemetry]\notel_enabled = false\n",
		"spaced header": "[ telemetry ]\notel_enabled = false\n",
		"tabbed header": "[\ttelemetry\t]\notel_enabled = false\n",
		"sub table":     "[telemetry.extra]\nx = 1\n",
		"spaced sub":    "[ telemetry . extra ]\nx = 1\n",
		"inline":        "model = \"keep\"\ntelemetry = { otel_enabled = false }\n",
		"dotted":        "model = \"keep\"\ntelemetry.otel_enabled = false\n",
		"spaced dotted": "telemetry . otel_enabled = false\n",
		"quoted key":    "\"telemetry\".otel_enabled = false\n",
		"quoted header": "['telemetry']\notel_enabled = false\n",
	}
	for name, body := range forms {
		t.Run(name, func(t *testing.T) {
			clearGrokEnv(t)
			home := t.TempDir()
			config := filepath.Join(home, ".grok", "config.toml")
			if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			res, err := Run(grokReq(home, true))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(res.Text, "already has a [telemetry] table") {
				t.Fatalf("not reported as an existing table:\n%s", res.Text)
			}
			if got := mustReadString(t, config); got != body {
				t.Fatalf("config.toml changed:\n%s", got)
			}
			if _, err := os.Stat(config + ".sdf-bak"); !os.IsNotExist(err) {
				t.Fatalf("left a backup (err=%v)", err)
			}
		})
	}
}

// Text that only mentions telemetry (a comment, a string, another key, a
// nested table) is not the top-level key, so the table is appended, and the
// result still parses.
func TestGrokTelemetryLookalikesStillGetTheTable(t *testing.T) {
	forms := map[string]string{
		"comment":      "# [telemetry]\nmodel = \"keep\"\n",
		"string":       "note = \"[telemetry]\"\n",
		"multiline":    "note = '''\n[telemetry]\n'''\n",
		"prefix key":   "telemetry_extra = 1\n",
		"nested table": "[profile.telemetry]\nx = 1\n",
	}
	for name, body := range forms {
		t.Run(name, func(t *testing.T) {
			clearGrokEnv(t)
			home := t.TempDir()
			config := filepath.Join(home, ".grok", "config.toml")
			if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			res, err := Run(grokReq(home, true))
			if err != nil || !res.Wrote {
				t.Fatal(err, res.Text)
			}
			got := mustReadString(t, config)
			if !strings.HasPrefix(got, body) || !strings.Contains(got, "\n[telemetry]\notel_enabled = true\n") {
				t.Fatalf("table not appended:\n%s", got)
			}
			if has, err := grokHasTelemetry([]byte(got)); err != nil || !has {
				t.Fatalf("written file: has=%t err=%v\n%s", has, err, got)
			}
		})
	}
}

// A config.toml that is not valid TOML is refused before anything is
// written: Grok cannot load it, and appending to it cannot be checked.
func TestGrokInvalidTOMLIsRefused(t *testing.T) {
	clearGrokEnv(t)
	home := t.TempDir()
	config := filepath.Join(home, ".grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "model = [\n"
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(grokReq(home, true))
	if err == nil || !strings.Contains(err.Error(), "is not valid TOML") || !strings.Contains(err.Error(), "it was not changed") {
		t.Fatalf("err = %v", err)
	}
	if got := mustReadString(t, config); got != body {
		t.Fatalf("config.toml changed:\n%s", got)
	}
	for _, p := range []string{config + ".sdf-bak", filepath.Join(home, ".grok", "hooks", "sdf-session.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s exists (err=%v)", p, err)
		}
	}
}

// The gate warnings follow Grok's env_bool: 1/true/yes/on (any case, trimmed)
// turn a gate on over the pinned file; 0/false/no/off/"" and unknown values
// do not (an unknown value is ignored and the file's false applies).
// OTEL_LOG_TOOL_DETAILS is warned about too, as metadata rather than bodies.
func TestGrokGateWarningsMatchGrokParsing(t *testing.T) {
	for _, gate := range grokContentGates {
		for _, v := range []string{"1", "true", "TRUE", " yes ", "On"} {
			clearGrokEnv(t)
			t.Setenv(gate, v)
			res, err := Run(grokReq(t.TempDir(), false))
			if err != nil {
				t.Fatal(err)
			}
			want := "warning: " + gate + "=" + strings.TrimSpace(v) + " "
			if !strings.Contains(res.Text, want) {
				t.Fatalf("%s=%q: missing %q in\n%s", gate, v, want, res.Text)
			}
			if gate == "OTEL_LOG_TOOL_DETAILS" && !strings.Contains(res.Text, "tool metadata") {
				t.Fatalf("details warning should say metadata:\n%s", res.Text)
			}
		}
		for _, v := range []string{"", "0", "false", "FALSE", "no", " off ", "Off", "2", "enabled"} {
			clearGrokEnv(t)
			t.Setenv(gate, v)
			res, err := Run(grokReq(t.TempDir(), false))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(res.Text, "warning: "+gate) {
				t.Fatalf("%s=%q is off in Grok but warned:\n%s", gate, v, res.Text)
			}
		}
	}
	if len(grokContentGates) != 4 {
		t.Fatalf("grokContentGates = %v, want the four OTEL_LOG_* gates", grokContentGates)
	}
}
