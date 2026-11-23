package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const userSettings = `{
  "hooks": {
    "SessionStart": [
      {"matcher": "startup", "hooks": [{"type": "command", "command": "echo user-start-1"}]},
      {"hooks": [{"type": "command", "command": "echo user-start-2"}]}
    ],
    "SessionEnd": [
      {"matcher": "logout", "hooks": [{"type": "command", "command": "echo user-end"}]}
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "echo pre"}]}
    ]
  }
}
`

func claudeInit(t *testing.T, home string, port int) Result {
	t.Helper()
	res, err := Run(Request{Agent: "claude-code", Home: home, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: port}})
	if err != nil {
		t.Fatal(err, res.Text)
	}
	return res
}

func writeSettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// eventCommands lists every command under hooks.<event>, group by group, with
// the group's matcher in front.
func eventCommands(t *testing.T, path, event string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, g := range root.Hooks[event] {
		for _, h := range g.Hooks {
			cmd := h.Command
			if isSdfSessionHook(map[string]any{"type": "command", "command": cmd}, "claude-code") || isSdfSessionHook(map[string]any{"type": "command", "command": cmd}, "codex") {
				cmd = "sdf"
				if i := strings.Index(h.Command, "http://"); i >= 0 {
					cmd += " " + strings.TrimRight(strings.Fields(h.Command[i:])[0], `'"`)
				}
			}
			out = append(out, g.Matcher+"|"+cmd)
		}
	}
	return out
}

func sameList(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s:\ngot  %q\nwant %q", what, got, want)
	}
}

func TestClaudeInitKeepsUserHooksAndOrder(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	writeSettings(t, path, userSettings)
	if res := claudeInit(t, home, 4318); !res.Wrote {
		t.Fatal(res.Text)
	}
	sameList(t, "SessionStart", eventCommands(t, path, "SessionStart"), []string{
		"startup|echo user-start-1", "|echo user-start-2", "|sdf http://127.0.0.1:4318/v1/sessions",
	})
	sameList(t, "SessionEnd", eventCommands(t, path, "SessionEnd"), []string{
		"logout|echo user-end", "|sdf http://127.0.0.1:4318/v1/sessions",
	})
	sameList(t, "PreToolUse", eventCommands(t, path, "PreToolUse"), []string{"Bash|echo pre"})
}

func TestClaudeInitRerunIsANoOp(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	writeSettings(t, path, userSettings)
	claudeInit(t, home, 4318)
	before, _ := os.ReadFile(path)
	if res := claudeInit(t, home, 4318); res.Wrote || !strings.Contains(res.Text, "already has these values") {
		t.Fatal(res.Text)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("second run changed settings.json")
	}
	if n := len(eventCommands(t, path, "SessionStart")); n != 3 {
		t.Fatalf("SessionStart has %d hooks after a rerun", n)
	}
}

// An sdf hook from an earlier init (another port here, and the first command
// form sdf wrote) is updated where it is, inside the user's
// matcher group, and a duplicate sdf copy is removed with the group it emptied.
func TestClaudeInitUpdatesAnOldSdfHookInPlace(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	old := sessionHookCommand("claude-code", "open", "http://127.0.0.1:9999/v1/sessions")
	legacy := legacyHook(t, "claude-code", "close")
	settings := map[string]any{"hooks": map[string]any{
		"SessionStart": []any{
			map[string]any{"matcher": "startup", "hooks": []any{
				map[string]any{"type": "command", "command": "echo user-start"},
				map[string]any{"type": "command", "command": old},
			}},
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": old}}},
			map[string]any{"matcher": "resume", "hooks": []any{map[string]any{"type": "command", "command": "echo resume"}}},
		},
		"SessionEnd": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": legacy}}},
			map[string]any{"matcher": "clear", "hooks": []any{map[string]any{"type": "command", "command": "echo clear"}}},
		},
	}}
	body, _ := json.MarshalIndent(settings, "", "  ")
	writeSettings(t, path, string(body))
	if res := claudeInit(t, home, 4318); !res.Wrote {
		t.Fatal(res.Text)
	}
	sameList(t, "SessionStart", eventCommands(t, path, "SessionStart"), []string{
		"startup|echo user-start", "startup|sdf http://127.0.0.1:4318/v1/sessions", "resume|echo resume",
	})
	sameList(t, "SessionEnd", eventCommands(t, path, "SessionEnd"), []string{
		"|sdf http://127.0.0.1:4318/v1/sessions", "clear|echo clear",
	})
	if res := claudeInit(t, home, 4318); res.Wrote {
		t.Fatal("rerun after the update changed the file")
	}
}

func TestClaudeInitDoesNotTakeOverAUserCurlHook(t *testing.T) {
	user := map[string]any{"type": "command", "command": "curl -sf https://example.com/v1/sessions"}
	if isSdfSessionHook(user, "claude-code") {
		t.Fatal("a user curl to /v1/sessions without the sdf payload was treated as sdf's")
	}
	if isSdfSessionHook(map[string]any{"type": "command", "command": sessionHookCommand("codex", "open", "http://127.0.0.1:4318/v1/sessions")}, "claude-code") {
		t.Fatal("a codex hook was treated as claude-code's")
	}
}

func TestClaudeInitRefusesANonArrayEvent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	writeSettings(t, path, `{"hooks": {"SessionStart": "nope"}}`)
	if _, err := Run(Request{Agent: "claude-code", Home: home, Write: true}); err == nil || !strings.Contains(err.Error(), "not an array") {
		t.Fatalf("err %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != `{"hooks": {"SessionStart": "nope"}}` {
		t.Fatalf("file changed: %s", got)
	}
}

func TestCodexInitKeepsUserHooks(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	path := filepath.Join(codexHome, "hooks.json")
	old := codexHookCommands("http://127.0.0.1:9999/v1/sessions")["SessionEnd"]
	existing := map[string]any{
		"other": true,
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"matcher": "startup", "hooks": []any{map[string]any{"type": "command", "command": "echo mine"}}}},
			"SessionEnd": []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": old}}},
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo bye"}}},
			},
			"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo stop"}}}},
		},
	}
	body, _ := json.MarshalIndent(existing, "", "  ")
	writeSettings(t, path, string(body))
	run := func() {
		if res, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}}); err != nil {
			t.Fatal(err, res.Text)
		}
	}
	run()
	sameList(t, "SessionStart", eventCommands(t, path, "SessionStart"), []string{"startup|echo mine", "|sdf http://127.0.0.1:4318/v1/sessions"})
	sameList(t, "SessionEnd", eventCommands(t, path, "SessionEnd"), []string{"|sdf http://127.0.0.1:4318/v1/sessions", "|echo bye"})
	sameList(t, "Stop", eventCommands(t, path, "Stop"), []string{"|echo stop"})
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `"other": true`) || !strings.Contains(string(got), "thread_id") {
		t.Fatal(string(got))
	}
	// The rerun finds the [otel] table the first run appended; hooks.json
	// is still merged, and does not change.
	run()
	again, _ := os.ReadFile(path)
	if string(again) != string(got) {
		t.Fatal("rerun changed hooks.json")
	}
}
