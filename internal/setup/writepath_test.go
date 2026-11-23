package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The read-only tests use mode 0400. A non-root user cannot open that file
// for writing; root can, so checkWritable also refuses a file with no write
// bit at all. Both paths run here whatever the test user is.

func endpoint() Endpoint { return Endpoint{Host: "127.0.0.1", HTTPPort: 4318, GRPCPort: 4317} }

func writeFileMode(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertUntouched(t *testing.T, path, body string) {
	t.Helper()
	if got := mustReadString(t, path); got != body {
		t.Fatalf("%s changed: %s", path, got)
	}
	backups, _ := filepath.Glob(path + ".sdf-bak*")
	if len(backups) != 0 {
		t.Fatalf("backup written for a refused file: %v", backups)
	}
}

func TestReadOnlySettingsAreRefused(t *testing.T) {
	cases := []struct {
		agent string
		file  func(home string) string
	}{
		{"gemini", func(h string) string { return filepath.Join(h, ".gemini", "settings.json") }},
		{"claude-code", func(h string) string { return filepath.Join(h, ".claude", "settings.json") }},
		{"pi", func(h string) string { return filepath.Join(h, ".pi", "settings.json") }},
		{"codex", func(h string) string { return filepath.Join(h, ".codex", "config.toml") }},
		{"opencode", func(h string) string { return filepath.Join(h, ".config", "sdf", "opencode.env") }},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			home := t.TempDir()
			path := tc.file(home)
			body := "{}\n"
			if tc.agent == "codex" {
				body = "model = \"o3\"\n"
			}
			if tc.agent == "opencode" {
				body = "# old\n"
			}
			writeFileMode(t, path, body, 0o400)
			req := Request{Agent: tc.agent, Home: home, ProjectDir: home, CodexHome: filepath.Join(home, ".codex"), Write: true, Endpoint: endpoint()}
			_, err := Run(req)
			if err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("want a read-only error, got %v", err)
			}
			assertUntouched(t, path, body)
			if info, _ := os.Stat(path); info.Mode().Perm() != 0o400 {
				t.Fatalf("mode changed to %v", info.Mode())
			}
		})
	}
}

func TestReadOnlySecondFileLeavesTheFirstAlone(t *testing.T) {
	t.Run("pi extension", func(t *testing.T) {
		home := t.TempDir()
		settings := filepath.Join(home, ".pi", "settings.json")
		writeFileMode(t, settings, "{}\n", 0o600)
		ext := filepath.Join(home, ".pi", "extensions", "sdf-session.js")
		writeFileMode(t, ext, "// old\n", 0o400)
		_, err := Run(Request{Agent: "pi", Home: home, ProjectDir: home, Write: true, Endpoint: endpoint()})
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatal(err)
		}
		assertUntouched(t, settings, "{}\n")
		assertUntouched(t, ext, "// old\n")
	})
	t.Run("opencode plugin", func(t *testing.T) {
		home := t.TempDir()
		plugin := filepath.Join(home, ".config", "opencode", "plugins", "sdf-session.js")
		writeFileMode(t, plugin, "// old\n", 0o400)
		_, err := Run(Request{Agent: "opencode", Home: home, ProjectDir: home, Write: true, Endpoint: endpoint()})
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(home, ".config", "sdf", "opencode.env")); !os.IsNotExist(err) {
			t.Fatal("opencode.env written before the plugin was refused")
		}
		assertUntouched(t, plugin, "// old\n")
	})
	t.Run("codex hooks.json", func(t *testing.T) {
		home := t.TempDir()
		config := filepath.Join(home, ".codex", "config.toml")
		writeFileMode(t, config, "model = \"o3\"\n", 0o600)
		hooks := filepath.Join(home, ".codex", "hooks.json")
		writeFileMode(t, hooks, "{}\n", 0o400)
		_, err := Run(Request{Agent: "codex", Home: home, CodexHome: filepath.Join(home, ".codex"), Write: true, Endpoint: endpoint()})
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatal(err)
		}
		assertUntouched(t, config, "model = \"o3\"\n")
		assertUntouched(t, hooks, "{}\n")
	})
}

func TestUnwritableForThisUserIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open any file for writing; the read-only tests cover root")
	}
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "settings.json")
	// Group and other may write, the owner may not: the mode-bit check
	// passes, and the write-only open is what refuses it.
	writeFileMode(t, path, "{}\n", 0o466)
	_, err := Run(geminiRequest(home, true))
	if err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatal(err)
	}
	assertUntouched(t, path, "{}\n")
}

func TestDanglingSymlinkCreatesItsTarget(t *testing.T) {
	home := t.TempDir()
	dotfiles := filepath.Join(home, "dotfiles", "gemini")
	if err := os.MkdirAll(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// Relative, like GNU stow makes them, and the target does not exist yet.
	if err := os.Symlink(filepath.Join("..", "dotfiles", "gemini", "settings.json"), path); err != nil {
		t.Fatal(err)
	}
	res, err := Run(geminiRequest(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced", err)
	}
	created := filepath.Join(dotfiles, "settings.json")
	if text := mustReadString(t, created); !strings.Contains(text, `"enabled": true`) {
		t.Fatal(text)
	}
	if info, _ := os.Stat(created); info.Mode().Perm() != 0o600 {
		t.Fatalf("new target mode %v", info.Mode())
	}
	if backups, _ := filepath.Glob(path + ".sdf-bak*"); len(backups) != 0 {
		t.Fatal(backups)
	}
}

func TestCodexConfigWriteIsAtomicAndCleansUp(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	config := filepath.Join(codexHome, "config.toml")
	original := "model = \"o3\"\n"
	writeFileMode(t, config, original, 0o640)
	renameFile = func(string, string) error { return errors.New("disk full") }
	t.Cleanup(func() { renameFile = os.Rename })
	req := Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: endpoint()}
	if _, err := Run(req); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatal(err)
	}
	assertUntouched(t, config, original)
	entries, _ := os.ReadDir(codexHome)
	if len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
	renameFile = os.Rename
	res, err := Run(req)
	if err != nil || !strings.Contains(res.Text, "backup ") {
		t.Fatal(err, res.Text)
	}
	if text := mustReadString(t, config); !strings.HasPrefix(text, original) || !strings.Contains(text, "[otel]") {
		t.Fatal(text)
	}
	if info, _ := os.Stat(config); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", info.Mode())
	}
}

func TestHardLinkIsReported(t *testing.T) {
	home := t.TempDir()
	path := writeGeminiSettings(t, home, `{"theme":"x"}`, 0o600)
	other := filepath.Join(home, "linked.json")
	if err := os.Link(path, other); err != nil {
		t.Skip("hard links unsupported:", err)
	}
	res, err := Run(geminiRequest(home, true))
	if err != nil || !strings.Contains(res.Text, "had 2 hard links") {
		t.Fatal(err, res.Text)
	}
	if mustReadString(t, other) != `{"theme":"x"}` {
		t.Fatal("the other link changed")
	}
}

func TestDryRunsShowOnlyManagedKeys(t *testing.T) {
	const secret = "sk-SECRET-123"
	t.Run("gemini", func(t *testing.T) {
		home := t.TempDir()
		writeGeminiSettings(t, home, `{"mcpServers":{"gh":{"env":{"GITHUB_TOKEN":"`+secret+`"}}},"telemetry":{"otlpHeaders":{"Authorization":"`+secret+`"}}}`, 0o600)
		res, err := Run(geminiRequest(home, false))
		if err != nil || strings.Contains(res.Text, secret) || strings.Contains(res.Text, "otlpHeaders") || !strings.Contains(res.Text, `"otlpEndpoint": "http://127.0.0.1:4318"`) {
			t.Fatal(err, res.Text)
		}
	})
	t.Run("claude-code", func(t *testing.T) {
		home := t.TempDir()
		writeFileMode(t, filepath.Join(home, ".claude", "settings.json"), `{"env":{"ANTHROPIC_API_KEY":"`+secret+`"},"apiKeyHelper":"`+secret+`"}`, 0o600)
		res, err := Run(Request{Agent: "claude-code", Home: home, Endpoint: endpoint()})
		if err != nil || res.Wrote || strings.Contains(res.Text, secret) || !strings.Contains(res.Text, "sdf would set:") || !strings.Contains(res.Text, `"CLAUDE_CODE_ENABLE_TELEMETRY": "1"`) || !strings.Contains(res.Text, `"SessionStart"`) {
			t.Fatal(err, res.Text)
		}
	})
	t.Run("pi", func(t *testing.T) {
		home := t.TempDir()
		writeFileMode(t, filepath.Join(home, ".pi", "settings.json"), `{"providers":{"x":{"apiKey":"`+secret+`"}}}`, 0o600)
		res, err := Run(Request{Agent: "pi", Home: home, ProjectDir: home, Endpoint: endpoint()})
		if err != nil || res.Wrote || strings.Contains(res.Text, secret) || !strings.Contains(res.Text, `"otel"`) {
			t.Fatal(err, res.Text)
		}
	})
}
