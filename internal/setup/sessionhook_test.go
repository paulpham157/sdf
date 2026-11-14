package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeInitInstallsTheSessionHook(t *testing.T) {
	home := t.TempDir()
	res, err := Run(Request{Agent: "claude-code", Home: home, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	body, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "SessionStart") || !strings.Contains(text, "SessionEnd") || !strings.Contains(text, "http://127.0.0.1:4318/v1/sessions") {
		t.Fatal(text)
	}
	if !strings.Contains(text, `"CLAUDE_CODE_ENABLE_TELEMETRY": "1"`) {
		t.Fatal("exporter env missing")
	}
	again, err := Run(Request{Agent: "claude-code", Home: home, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || again.Wrote {
		t.Fatalf("second run changed the file: %v %s", err, again.Text)
	}
}

func TestCodexInitInstallsTheSessionHook(t *testing.T) {
	home := t.TempDir()
	res, err := Run(Request{Agent: "codex", Home: home, CodexHome: filepath.Join(home, "codex"), Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	body, err := os.ReadFile(filepath.Join(home, "codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "SessionStart") || !strings.Contains(text, "thread_id") || !strings.Contains(text, "/v1/sessions") {
		t.Fatal(text)
	}
	before, err := os.ReadFile(filepath.Join(home, "codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Request{Agent: "codex", Home: home, CodexHome: filepath.Join(home, "codex"), Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(home, "codex", "hooks.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("hooks.json changed on the second run")
	}
}

func TestOpenCodeInitInstallsTheSessionPlugin(t *testing.T) {
	home := t.TempDir()
	res, err := Run(Request{Agent: "opencode", Home: home, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	body, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "plugins", "sdf-session.js"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "session.created") || strings.Contains(text, "session.deleted") || !strings.Contains(text, `"opencode"`) {
		t.Fatal(text)
	}
}

func TestPiInitInstallsTheSessionExtension(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	res, err := Run(Request{Agent: "pi", Home: home, ProjectDir: dir, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".pi", "extensions", "sdf-session.js"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "getSessionId") || !strings.Contains(text, "session_shutdown") || !strings.Contains(text, `"reload"`) {
		t.Fatal(text)
	}
	if _, err := os.Stat(filepath.Join(home, ".pi", "agent", "extensions", "sdf-session.js")); !os.IsNotExist(err) {
		t.Fatal("project install wrote the global extension")
	}
}
