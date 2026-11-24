package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitStopHookIsIdempotentAndKeepsSessionEdges(t *testing.T) {
	home := t.TempDir()
	ep := Endpoint{Host: "127.0.0.1", HTTPPort: 4318}

	claude := filepath.Join(home, ".claude", "settings.json")
	if _, err := Run(Request{Agent: "claude-code", Home: home, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	before := readStopFile(t, claude)
	assertOneStop(t, before, "claude-code", "Stop")
	if !strings.Contains(before, "SessionStart") || !strings.Contains(before, "SessionEnd") || !strings.Contains(before, "/v1/sessions") {
		t.Fatalf("session edges missing:\n%s", before)
	}
	if _, err := Run(Request{Agent: "claude-code", Home: home, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	if after := readStopFile(t, claude); after != before {
		t.Fatalf("claude settings changed on the second init")
	}

	codexHome := filepath.Join(home, "codex")
	codex := filepath.Join(codexHome, "hooks.json")
	if _, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	before = readStopFile(t, codex)
	assertOneStop(t, before, "codex", "Stop")
	if !strings.Contains(before, "SessionStart") || !strings.Contains(before, "SessionEnd") || !strings.Contains(before, "thread_id") {
		t.Fatalf("codex session edges missing:\n%s", before)
	}
	if _, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	if after := readStopFile(t, codex); after != before {
		t.Fatalf("codex hooks.json changed on the second init")
	}

	t.Setenv("GROK_HOME", filepath.Join(home, "grok-home"))
	grok := filepath.Join(home, "grok-home", "hooks", "sdf-session.json")
	if _, err := Run(Request{Agent: "grok", Home: home, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	before = readStopFile(t, grok)
	assertOneStop(t, before, "grok", "Stop")
	if !strings.Contains(before, "SessionStart") || !strings.Contains(before, "SessionEnd") || !strings.Contains(before, "GROK_SESSION_ID") {
		t.Fatalf("grok session edges missing:\n%s", before)
	}
	if _, err := Run(Request{Agent: "grok", Home: home, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	if after := readStopFile(t, grok); after != before {
		t.Fatalf("grok hooks changed on the second init")
	}

	project := t.TempDir()
	pi := filepath.Join(project, ".pi", "extensions", "sdf-session.js")
	if _, err := Run(Request{Agent: "pi", Home: home, ProjectDir: project, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	before = readStopFile(t, pi)
	if strings.Count(before, "agent_before_settle") != 1 || !strings.Contains(before, `sdf hook stop --agent pi`) || !strings.Contains(before, "session_start") || !strings.Contains(before, "session_shutdown") {
		t.Fatalf("pi extension:\n%s", before)
	}
	if _, err := Run(Request{Agent: "pi", Home: home, ProjectDir: project, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	if after := readStopFile(t, pi); after != before {
		t.Fatalf("pi extension changed on the second init")
	}

	t.Setenv("QWEN_HOME", "")
	if _, err := Run(Request{Agent: "qwen", Home: home, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	qwen := filepath.Join(home, ".qwen", "settings.json")
	before = readStopFile(t, qwen)
	assertOneStop(t, before, "qwen", "Stop")
	if !strings.Contains(before, `"telemetry"`) || !strings.Contains(before, `"enabled": true`) {
		t.Fatalf("qwen telemetry missing:\n%s", before)
	}
	if _, err := Run(Request{Agent: "qwen", Home: home, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	if after := readStopFile(t, qwen); after != before {
		t.Fatalf("qwen settings changed on the second init")
	}

	if _, err := Run(Request{Agent: "gemini", Home: home, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	gemini := filepath.Join(home, ".gemini", "settings.json")
	before = readStopFile(t, gemini)
	assertOneStop(t, before, "gemini", "AfterAgent")
	if !strings.Contains(before, `"timeout": 2000`) {
		t.Fatalf("gemini AfterAgent timeout is milliseconds:\n%s", before)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(before), &root); err != nil {
		t.Fatal(err)
	}
	tel, _ := root["telemetry"].(map[string]any)
	if tel["enabled"] != true || tel["otlpEndpoint"] != "http://127.0.0.1:4318" {
		t.Fatalf("telemetry %#v", tel)
	}
	if _, err := Run(Request{Agent: "gemini", Home: home, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	if after := readStopFile(t, gemini); after != before {
		t.Fatalf("gemini settings changed on the second init")
	}
}

func TestInitDoesNotInventAStopHookForOpenCodeOrCopilot(t *testing.T) {
	home := t.TempDir()
	ep := Endpoint{Host: "127.0.0.1", HTTPPort: 4318}
	if _, err := Run(Request{Agent: "opencode", Home: home, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	plugin := readStopFile(t, filepath.Join(home, ".config", "opencode", "plugins", "sdf-session.js"))
	if strings.Contains(plugin, "hook stop") || strings.Contains(plugin, "agent_before_settle") || !strings.Contains(plugin, "session.created") {
		t.Fatalf("opencode plugin:\n%s", plugin)
	}
	if _, err := Run(Request{Agent: "copilot", Home: home, Write: true, Endpoint: ep}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".copilot", "hooks")); !os.IsNotExist(err) {
		t.Fatalf("copilot hooks dir: %v", err)
	}
}

func assertOneStop(t *testing.T, body, agent, event string) {
	t.Helper()
	needle := "sdf hook stop --agent " + agent
	if strings.Count(body, needle) != 1 {
		t.Fatalf("%s stop command count %d\n%s", agent, strings.Count(body, needle), body)
	}
	if !strings.Contains(body, `"`+event+`"`) {
		t.Fatalf("%s event %s missing\n%s", agent, event, body)
	}
}

func readStopFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(bytes.TrimSpace(b))
}
