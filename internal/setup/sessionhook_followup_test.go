package setup

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sdfHookForms reads testdata/sdf_session_hooks.tsv: every session hook
// command an sdf build on main has written, as tool and command.
func sdfHookForms(t *testing.T) [][2]string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "sdf_session_hooks.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][2]string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tool, command, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("bad line %q", line)
		}
		out = append(out, [2]string{tool, command})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// legacyHook is the first form of sdf's hook for tool and edge, posting to
// http://127.0.0.1:4318/v1/sessions with an unquoted URL.
func legacyHook(t *testing.T, tool, edge string) string {
	t.Helper()
	for _, form := range sdfHookForms(t) {
		if form[0] == tool && strings.Contains(form[1], `"edge":"`+edge+`"`) && strings.HasSuffix(form[1], ` http://127.0.0.1:4318/v1/sessions >/dev/null 2>&1 || true`) {
			return form[1]
		}
	}
	t.Fatalf("no legacy %s %s hook in testdata", tool, edge)
	return ""
}

func cmdHook(command string) map[string]any {
	return map[string]any{"type": "command", "command": command}
}

func TestSdfHookDetectionMatchesEveryFormSdfWrote(t *testing.T) {
	forms := sdfHookForms(t)
	// Five generations on main (d066acd, 85485e8, a10a807, 0c150fb, 57239b0)
	// x 2 tools x 2 edges x 2 URLs (loopback, other host) = 40 rows. 0c150fb
	// and 57239b0 write the same non-loopback command (no token code), so 36
	// of them are distinct.
	distinct := map[[2]string]bool{}
	for _, form := range forms {
		distinct[form] = true
	}
	if len(forms) != 40 || len(distinct) != 36 {
		t.Fatalf("testdata has %d forms, %d distinct; want 40 and 36", len(forms), len(distinct))
	}
	for _, form := range forms {
		other := "codex"
		if form[0] == "codex" {
			other = "claude-code"
		}
		if !isSdfSessionHook(cmdHook(form[1]), form[0]) {
			t.Errorf("%s hook sdf wrote is not recognized: %s", form[0], form[1])
		}
		if isSdfSessionHook(cmdHook(form[1]), other) {
			t.Errorf("%s hook recognized as %s's", form[0], other)
		}
	}
	for _, url := range []string{"http://127.0.0.1:4318/v1/sessions", "http://localhost:9999/v1/sessions", "http://[::1]:4318/v1/sessions", "http://collector.lan:4318/v1/sessions"} {
		for event, command := range claudeHooks(url) {
			if !isSdfSessionHook(cmdHook(command), "claude-code") {
				t.Errorf("current claude %s hook for %s not recognized", event, url)
			}
		}
		for event, command := range codexHookCommands(url) {
			if !isSdfSessionHook(cmdHook(command), "codex") {
				t.Errorf("current codex %s hook for %s not recognized", event, url)
			}
		}
	}
}

// Review: detection matched any curl to /v1/sessions naming the tool, on any
// host, or any command mentioning CLAUDE_HOOK_EDGE, so a user's own hook was
// rewritten. Only sdf's exact command text matches now.
func TestSdfHookDetectionLeavesLookalikeUserHooks(t *testing.T) {
	sdf := claudeHooks("http://127.0.0.1:4318/v1/sessions")["SessionStart"]
	users := []string{
		`curl -sf -X POST --data '{"tool":"claude-code","edge":"open"}' https://audit.example.com/v1/sessions`,
		`curl -s http://10.0.0.5:8080/v1/sessions -d "{\"tool\":\"claude-code\"}"`,
		`CLAUDE_HOOK_EDGE=open curl -sf http://localhost:4318/v1/sessions`,
		`CLAUDE_HOOK_EDGE=close CLAUDE_HOOK_TOOL=claude-code sh -c 'curl -sf -X POST --data @- http://localhost:4318/v1/sessions'`,
		sdf + "; echo mine",
		"echo mine; " + sdf,
		strings.Replace(sdf, "'http://127.0.0.1:4318/v1/sessions'", "'https://127.0.0.1:4318/v1/sessions'", 1),
		strings.Replace(sdf, "'http://127.0.0.1:4318/v1/sessions'", "'http://127.0.0.1:4318/v1/sessions?x=1'", 1),
		strings.Replace(sdf, "'http://127.0.0.1:4318/v1/sessions'", "'http://127.0.0.1:4318/v1/sessions' -o /tmp/x", 1),
		strings.Replace(legacyHook(t, "claude-code", "open"), " http://127.0.0.1:4318/v1/sessions ", " http://127.0.0.1:4318/v1/sessions;id ", 1),
		strings.Replace(sdf, "-m 1", "-m 5", 1),
	}
	for _, command := range users {
		if isSdfSessionHook(cmdHook(command), "claude-code") {
			t.Errorf("user hook treated as sdf's: %s", command)
		}
	}
	if isSdfSessionHook(map[string]any{"type": "prompt", "command": sdf}, "claude-code") {
		t.Error("a non-command hook was treated as sdf's")
	}

	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	writeSettings(t, path, `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "curl -sf -X POST --data '{\"tool\":\"claude-code\",\"edge\":\"open\"}' https://audit.example.com/v1/sessions"}]}]}}`)
	claudeInit(t, home, 4318)
	sameList(t, "SessionStart", eventCommands(t, path, "SessionStart"), []string{
		`|curl -sf -X POST --data '{"tool":"claude-code","edge":"open"}' https://audit.example.com/v1/sessions`,
		"|sdf http://127.0.0.1:4318/v1/sessions",
	})
}

// Review: SessionStart: null was replaced with sdf's array; it is refused,
// and the file is left as it was, like any other non-array value.
func TestHookMergeRefusesNullEvent(t *testing.T) {
	for _, body := range []string{
		`{"hooks": {"SessionStart": null, "PreToolUse": []}}`,
		`{"hooks": {"SessionEnd": null}}`,
		`{"hooks": null}`,
	} {
		home := t.TempDir()
		path := filepath.Join(home, ".claude", "settings.json")
		writeSettings(t, path, body)
		if res, err := Run(Request{Agent: "claude-code", Home: home, Write: true}); err == nil || !strings.Contains(err.Error(), "null") {
			t.Fatalf("claude %s: err %v\n%s", body, err, res.Text)
		}
		if got, _ := os.ReadFile(path); string(got) != body {
			t.Fatalf("claude file changed: %s", got)
		}
		if matches, _ := filepath.Glob(path + ".sdf-bak*"); len(matches) != 0 {
			t.Fatalf("backup left: %v", matches)
		}

		codexHome := filepath.Join(home, "codex")
		hooks := filepath.Join(codexHome, "hooks.json")
		writeSettings(t, hooks, body)
		if res, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true}); err == nil || !strings.Contains(err.Error(), "null") {
			t.Fatalf("codex %s: err %v\n%s", body, err, res.Text)
		}
		if got, _ := os.ReadFile(hooks); string(got) != body {
			t.Fatalf("codex hooks.json changed: %s", got)
		}
	}
}

// Review: with an [otel] table already in config.toml, codex returned before
// the hooks.json merge, so a user who set [otel] up by hand got no hook.
func TestCodexInitMergesHooksWhenOtelTableExists(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	config := filepath.Join(codexHome, "config.toml")
	hooks := filepath.Join(codexHome, "hooks.json")
	original := "model = \"keep\"\n\n[otel]\nexporter = \"none\"\n"
	writeSettings(t, config, original)
	writeSettings(t, hooks, `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo stop"}]}]}}`)
	req := Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}}
	res, err := Run(req)
	if err != nil || !res.Wrote || !strings.Contains(res.Text, "already has an [otel] table") || !strings.Contains(res.Text, "wrote "+hooks) {
		t.Fatal(err, res.Text)
	}
	if got, _ := os.ReadFile(config); string(got) != original {
		t.Fatalf("config.toml changed: %s", got)
	}
	sameList(t, "SessionStart", eventCommands(t, hooks, "SessionStart"), []string{"|sdf http://127.0.0.1:4318/v1/sessions"})
	sameList(t, "SessionEnd", eventCommands(t, hooks, "SessionEnd"), []string{"|sdf http://127.0.0.1:4318/v1/sessions"})
	sameList(t, "Stop", eventCommands(t, hooks, "Stop"), []string{"|echo stop"})
	if again, err := Run(req); err != nil || again.Wrote || !strings.Contains(again.Text, "already has these values") {
		t.Fatal(err, again.Text)
	}

	// No hooks.json yet: it is created.
	fresh := filepath.Join(home, "fresh")
	writeSettings(t, filepath.Join(fresh, "config.toml"), original)
	req.CodexHome = fresh
	if res, err := Run(req); err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	sameList(t, "fresh SessionStart", eventCommands(t, filepath.Join(fresh, "hooks.json"), "SessionStart"), []string{"|sdf http://127.0.0.1:4318/v1/sessions"})
}

func TestCodexDryRunMentionsHooksMerge(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	hooks := filepath.Join(codexHome, "hooks.json")
	res, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !strings.Contains(res.Text, "merges sdf's SessionStart and SessionEnd hooks into "+hooks) {
		t.Fatal(err, res.Text)
	}
	if _, err := os.Stat(hooks); !os.IsNotExist(err) {
		t.Fatal("dry run wrote hooks.json")
	}
	writeSettings(t, hooks, `{"hooks": {"SessionStart": null}}`)
	res, err = Run(Request{Agent: "codex", Home: home, CodexHome: codexHome})
	if err != nil || !strings.Contains(res.Text, "--write will not change "+hooks) {
		t.Fatal(err, res.Text)
	}
}

// The merge keeps &, <, > unescaped and numbers digit for digit, in the
// file's key order (the shared jsonfile encoder), for Claude and Codex.
func TestHookMergeKeepsTextNumbersAndOrder(t *testing.T) {
	body := `{
  "zeta": 12345678901234567890,
  "ratio": 1.50,
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "a && b > /tmp/x < /dev/null"}]}]
  },
  "alpha": "x&y"
}
`
	home := t.TempDir()
	claudePath := filepath.Join(home, ".claude", "settings.json")
	writeSettings(t, claudePath, body)
	claudeInit(t, home, 4318)
	codexHome := filepath.Join(home, "codex")
	codexPath := filepath.Join(codexHome, "hooks.json")
	writeSettings(t, codexPath, body)
	if res, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}}); err != nil {
		t.Fatal(err, res.Text)
	}
	for _, path := range []string{claudePath, codexPath} {
		got, _ := os.ReadFile(path)
		text := string(got)
		for _, want := range []string{`"zeta": 12345678901234567890`, `"ratio": 1.50`, `"a && b > /tmp/x < /dev/null"`, `"alpha": "x&y"`, `>/dev/null 2>&1 || true`} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lost %s:\n%s", path, want, text)
			}
		}
		if strings.Contains(text, `\u0026`) || strings.Contains(text, `\u003e`) || strings.Contains(text, `\u003c`) {
			t.Errorf("%s has HTML escapes", path)
		}
		if z, h, a := strings.Index(text, `"zeta"`), strings.Index(text, `"hooks"`), strings.Index(text, `"alpha"`); !(z < h && h < a) {
			t.Errorf("%s key order changed:\n%s", path, text)
		}
	}
}

// Review: with no [otel] table, a corrupt or null hooks.json was only found
// after [otel] had been appended to config.toml, leaving the write half done.
// The hooks.json merge is planned first, so both files stay as they were.
func TestCodexInitRefusedHooksLeavesConfigUntouched(t *testing.T) {
	for name, hooksBody := range map[string]string{
		"corrupt":       `{"hooks": {`,
		"null hooks":    `{"hooks": null}`,
		"null event":    `{"hooks": {"SessionEnd": null}}`,
		"not an object": `{"hooks": []}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			codexHome := filepath.Join(home, "codex")
			config := filepath.Join(codexHome, "config.toml")
			hooks := filepath.Join(codexHome, "hooks.json")
			original := "model = \"keep\"\n"
			writeSettings(t, config, original)
			writeSettings(t, hooks, hooksBody)
			res, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
			if err == nil {
				t.Fatalf("hooks.json %s was accepted:\n%s", hooksBody, res.Text)
			}
			if got, _ := os.ReadFile(config); string(got) != original {
				t.Fatalf("config.toml changed:\n%s", got)
			}
			if got, _ := os.ReadFile(hooks); string(got) != hooksBody {
				t.Fatalf("hooks.json changed:\n%s", got)
			}
			for _, p := range []string{config, hooks} {
				if matches, _ := filepath.Glob(p + ".sdf-bak*"); len(matches) != 0 {
					t.Fatalf("backup left: %v", matches)
				}
			}
		})
	}
}
