package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func qwenRequest(home string, write bool) Request {
	return Request{
		Agent: "qwen", Home: home, ProjectDir: home, Write: write,
		Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318, GRPCPort: 4317},
	}
}

func clearQwenEnv(t *testing.T) {
	t.Helper()
	keys := append([]string{qwenEnabledEnv, qwenEndpointEnv, qwenProtocolEnv, qwenPromptsEnv, qwenOutfileEnv, qwenSensEnv, qwenOTELEnv, "QWEN_HOME"}, qwenSignalEnv...)
	for _, key := range keys {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	dir := t.TempDir()
	t.Setenv("QWEN_CODE_SYSTEM_SETTINGS_PATH", filepath.Join(dir, "system.json"))
	t.Setenv("QWEN_CODE_SYSTEM_DEFAULTS_PATH", filepath.Join(dir, "system-defaults.json"))
}

func writeQwen(t *testing.T, home, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(home, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustContain(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
}

const qwenOriginal = `{
    "mcpServers": {
        "db": {
            "command": "db-mcp",
            "env": {
                "TOKEN": "s3cret&<tag>"
            }
        }
    },
    "telemetry": {
        "target": "local",
        "logPrompts": true,
        "otlpEndpoint": "http://collector:4317",
        "resourceAttributes": {
            "team": "a&b"
        }
    },
    "model": {
        "name": "qwen3-coder-plus"
    },
    "big": 123456789012345678901234567890
}
`

func TestQwenPrintShowsOnlyTelemetryAndDoesNotWrite(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	path := writeQwen(t, home, qwenOriginal, 0o600)
	res, err := Run(qwenRequest(home, false))
	if err != nil || res.Wrote {
		t.Fatal(err, res.Text)
	}
	mustContain(t, res.Text,
		`"logPrompts": false`, `"otlpEndpoint": "http://127.0.0.1:4318"`, `"otlpProtocol": "http"`, `"enabled": true`,
		"other keys, including 2 other telemetry keys, are kept as they are and not shown",
		`warning: telemetry.logPrompts was true; sdf init qwen sets it to false`,
		`warning: replacing telemetry.otlpEndpoint="http://collector:4317"`,
		"not written (pass --write)")
	if strings.Contains(res.Text, "s3cret") || strings.Contains(res.Text, "mcpServers") || strings.Contains(res.Text, "resourceAttributes") || strings.Contains(res.Text, `"target"`) {
		t.Fatalf("dry run printed other settings\n%s", res.Text)
	}
	if body, _ := os.ReadFile(path); string(body) != qwenOriginal {
		t.Fatal("dry run changed the file")
	}
}

func TestQwenWriteMergesInPlaceAndBacksUp(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	path := writeQwen(t, home, qwenOriginal, 0o640)
	res, err := Run(qwenRequest(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	body, _ := os.ReadFile(path)
	want := `{
    "mcpServers": {
        "db": {
            "command": "db-mcp",
            "env": {
                "TOKEN": "s3cret&<tag>"
            }
        }
    },
    "telemetry": {
        "target": "local",
        "logPrompts": false,
        "otlpEndpoint": "http://127.0.0.1:4318",
        "resourceAttributes": {
            "team": "a&b"
        },
        "enabled": true,
        "otlpProtocol": "http"
    },
    "model": {
        "name": "qwen3-coder-plus"
    },
    "big": 123456789012345678901234567890,
    "hooks": {
        "Stop": [
            {
                "hooks": [
                    {
                        "type": "command",
                        "command": "command -v sdf >/dev/null 2>&1 || exit 0; sdf hook stop --agent qwen || exit 0",
                        "timeout": 2
                    }
                ]
            }
        ]
    }
}
`
	if string(body) != want {
		t.Fatalf("got\n%s", body)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	if backup, _ := os.ReadFile(path + ".sdf-bak"); string(backup) != qwenOriginal {
		t.Fatalf("backup %q", backup)
	}
	mustContain(t, res.Text, "wrote "+path, "backup "+path+".sdf-bak")
	again, err := Run(qwenRequest(home, true))
	if err != nil || again.Wrote || !strings.Contains(again.Text, "already has these values") {
		t.Fatal(err, again.Text)
	}
}

func TestQwenWriteFreshFile(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	if _, err := Run(qwenRequest(home, true)); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(home, ".qwen", "settings.json"))
	want := "{\n  \"telemetry\": {\n    \"enabled\": true,\n    \"otlpEndpoint\": \"http://127.0.0.1:4318\",\n    \"otlpProtocol\": \"http\",\n    \"logPrompts\": false\n  },\n  \"hooks\": {\n    \"Stop\": [\n      {\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"command -v sdf >/dev/null 2>&1 || exit 0; sdf hook stop --agent qwen || exit 0\",\n            \"timeout\": 2\n          }\n        ]\n      }\n    ]\n  }\n}\n"
	if string(body) != want {
		t.Fatalf("got\n%s", body)
	}
}

func TestQwenHonorsQwenHome(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	t.Setenv("QWEN_HOME", "~/custom-qwen")
	if _, err := Run(qwenRequest(home, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "custom-qwen", "settings.json")); err != nil {
		t.Fatal(err)
	}
}

func TestQwenRefusesFilesItCannotRewriteSafely(t *testing.T) {
	clearQwenEnv(t)
	for name, body := range map[string]string{
		"comments":       "{\n  // mine\n  \"model\": {}\n}\n",
		"invalid":        "{ \"model\": ",
		"array":          "[1, 2]",
		"telemetry type": `{"telemetry": "on"}`,
	} {
		home := t.TempDir()
		path := writeQwen(t, home, body, 0o600)
		res, err := Run(qwenRequest(home, true))
		if err == nil || !strings.Contains(err.Error(), "was not changed") || res.Wrote || !strings.Contains(res.Text, "was not changed") {
			t.Fatalf("%s --write: %v %s", name, err, res.Text)
		}
		// The dry run explains the same thing and succeeds.
		if dry, err := Run(qwenRequest(home, false)); err != nil || !strings.Contains(dry.Text, "was not changed") {
			t.Fatalf("%s dry run: %v %s", name, err, dry.Text)
		}
		if got, _ := os.ReadFile(path); string(got) != body {
			t.Fatalf("%s: file changed", name)
		}
		if name == "comments" && !strings.Contains(res.Text, "it has comments") {
			t.Fatal(res.Text)
		}
	}
}

func TestQwenRefusesReadOnlyTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open a 0466 file it does not own for writing")
	}
	clearQwenEnv(t)
	home := t.TempDir()
	// Group and other may write, the owner may not: the mode-bit check
	// passes and the write-only open refuses it. A file with no write bit
	// (0400) is covered by TestQwenReadOnlyTargetIsRefusedForRootToo.
	path := writeQwen(t, home, "{}\n", 0o466)
	if err := os.Chmod(path, 0o466); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(qwenRequest(home, true)); err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(path); string(body) != "{}\n" {
		t.Fatal("read-only file replaced")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("left behind %v", entries)
	}
}

func TestQwenWritesThroughSymlinks(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	dotfiles := filepath.Join(home, "dotfiles", "qwen")
	if err := os.MkdirAll(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	// Stow-style: a relative link to a file that does not exist yet.
	if err := os.Symlink("../dotfiles/qwen/settings.json", link); err != nil {
		t.Fatal(err)
	}
	res, err := Run(qwenRequest(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	target := filepath.Join(dotfiles, "settings.json")
	if body, err := os.ReadFile(target); err != nil || !strings.Contains(string(body), `"logPrompts": false`) {
		t.Fatal(err, string(body))
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link replaced", err)
	}
	// Second write through the now-resolving link backs up the target.
	if err := os.WriteFile(target, []byte("{\"model\": {}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = Run(qwenRequest(home, true))
	if err != nil || !strings.Contains(res.Text, "backup "+target+".sdf-bak") || !strings.Contains(res.Text, "is a symlink") {
		t.Fatal(err, res.Text)
	}
}

func TestQwenKeepsDifferentExistingBackup(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	path := writeQwen(t, home, "{}\n", 0o600)
	if err := os.WriteFile(path+".sdf-bak", []byte("older\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(qwenRequest(home, true))
	if err != nil {
		t.Fatal(err)
	}
	if old, _ := os.ReadFile(path + ".sdf-bak"); string(old) != "older\n" {
		t.Fatal("existing backup clobbered")
	}
	if b1, _ := os.ReadFile(path + ".sdf-bak.1"); string(b1) != "{}\n" {
		t.Fatalf("backup.1 %q", b1)
	}
	mustContain(t, res.Text, "backup "+path+".sdf-bak.1")
}

func TestQwenFailedWriteRemovesBackup(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	path := writeQwen(t, home, "{}\n", 0o600)
	qwenRename = func(string, string) error { return os.ErrPermission }
	t.Cleanup(func() { qwenRename = os.Rename })
	if _, err := Run(qwenRequest(home, true)); err == nil {
		t.Fatal("expected failure")
	}
	if body, _ := os.ReadFile(path); string(body) != "{}\n" {
		t.Fatal("file changed")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("left behind %v", entries)
	}
}

func TestQwenContentAndOverrideWarnings(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	path := writeQwen(t, home, `{"telemetry": {"includeSensitiveSpanAttributes": true, "outfile": ".qwen/t.log", "otlpLogsEndpoint": "http://x/v1/logs"}}`, 0o600)
	t.Setenv(qwenOTELEnv, "http://elsewhere:4318")
	t.Setenv(qwenPromptsEnv, "1")
	t.Setenv(qwenEnabledEnv, "no")
	res, err := Run(qwenRequest(home, true))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, res.Text,
		"telemetry.includeSensitiveSpanAttributes was true; set to false",
		`telemetry.outfile=".qwen/t.log" is set and turns OTLP export off; kept`,
		`telemetry.otlpLogsEndpoint="http://x/v1/logs" overrides otlpEndpoint`,
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://elsewhere:4318 overrides telemetry.otlpEndpoint",
		"QWEN_TELEMETRY_LOG_PROMPTS=1 turns prompt logging back on",
		"QWEN_TELEMETRY_ENABLED=no turns telemetry off")
	body, _ := os.ReadFile(path)
	mustContain(t, string(body), `"includeSensitiveSpanAttributes": false`, `"outfile": ".qwen/t.log"`)
}

func TestQwenKeepsBOM(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	path := writeQwen(t, home, "\xEF\xBB\xBF{}\n", 0o600)
	if _, err := Run(qwenRequest(home, true)); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(body), "\xEF\xBB\xBF{\n") {
		t.Fatalf("%q", body)
	}
}

func TestQwenCheckReportsLayersAndPrecedence(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	project := t.TempDir()
	writeQwen(t, home, `{"telemetry": {"enabled": true, "otlpEndpoint": "http://127.0.0.1:4318", "otlpProtocol": "http", "logPrompts": false}}`, 0o600)
	req := qwenRequest(home, false)
	req.ProjectDir = project
	got, err := Configured(req)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, got, "qwen points at http://127.0.0.1:4318 over http: true", "logPrompts=false (user settings)")

	// A system file overrides the user file.
	system := os.Getenv("QWEN_CODE_SYSTEM_SETTINGS_PATH")
	if err := os.WriteFile(system, []byte(`{"telemetry": {"otlpEndpoint": "http://corp:4318"}, "security": {"folderTrust": {"enabled": true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A workspace file sets logPrompts back on; it is subject to folder trust.
	ws := filepath.Join(project, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(ws), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws, []byte("{\n  // project\n  \"telemetry\": {\"logPrompts\": true}\n}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ = Configured(req)
	mustContain(t, got,
		`otlpEndpoint="http://corp:4318" (system settings)`,
		"logPrompts=true (workspace settings)",
		"applies only if this folder is trusted",
		"qwen points at http://127.0.0.1:4318 over http: false",
		"qwen warning: logPrompts is on")

	// Environment beats every file.
	t.Setenv(qwenEndpointEnv, "http://127.0.0.1:4318")
	t.Setenv(qwenPromptsEnv, "false")
	got, _ = Configured(req)
	mustContain(t, got,
		`otlpEndpoint="http://127.0.0.1:4318" (QWEN_TELEMETRY_OTLP_ENDPOINT)`,
		"logPrompts=false (QWEN_TELEMETRY_LOG_PROMPTS)",
		"qwen points at http://127.0.0.1:4318 over http: true",
		"CLI flags")
}

func TestQwenCheckDefaults(t *testing.T) {
	clearQwenEnv(t)
	got, err := Configured(qwenRequest(t.TempDir(), false))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, got, "enabled=false (default)", `otlpProtocol="grpc" (default)`, "logPrompts=true (default)", "over http: false", "qwen warning: logPrompts is on")
}

func TestQwenPrintHidesUnmanagedTelemetryKeys(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	writeQwen(t, home, `{"telemetry": {"otlpHeaders": {"Authorization": "Bearer tok-SECRET"}, "userId": "me@example.com", "includeSensitiveSpanAttributes": true}}`, 0o600)
	res, err := Run(qwenRequest(home, false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "tok-SECRET") || strings.Contains(res.Text, `"otlpHeaders"`) || strings.Contains(res.Text, "me@example.com") {
		t.Fatalf("unmanaged telemetry keys printed\n%s", res.Text)
	}
	mustContain(t, res.Text, `"includeSensitiveSpanAttributes": false`, "including 2 other telemetry keys")
}

func qwenLink(t *testing.T, home, dest string) string {
	t.Helper()
	link := filepath.Join(home, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dest, link); err != nil {
		t.Fatal(err)
	}
	return link
}

func TestQwenRefusesSymlinkToNonJSON(t *testing.T) {
	clearQwenEnv(t)
	for _, exists := range []bool{true, false} {
		home := t.TempDir()
		bashrc := filepath.Join(home, ".bashrc")
		if exists {
			if err := os.WriteFile(bashrc, []byte("export PATH=$PATH:~/bin\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		qwenLink(t, home, "../.bashrc")
		for _, write := range []bool{false, true} {
			res, err := Run(qwenRequest(home, write))
			if err == nil || !strings.Contains(err.Error(), "not a .json file") || res.Wrote {
				t.Fatalf("exists=%v write=%v: %v %s", exists, write, err, res.Text)
			}
		}
		body, err := os.ReadFile(bashrc)
		if exists && string(body) != "export PATH=$PATH:~/bin\n" {
			t.Fatal(".bashrc changed")
		}
		if !exists && !os.IsNotExist(err) {
			t.Fatal(".bashrc created")
		}
		if matches, _ := filepath.Glob(bashrc + "*"); len(matches) > 1 || (!exists && len(matches) > 0) {
			t.Fatalf("left files: %v", matches)
		}
	}
}

func TestQwenDanglingSymlinkDoesNotCreateDirectoriesOutside(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	qwenLink(t, home, "../elsewhere/deep/settings.json")
	res, err := Run(qwenRequest(home, true))
	if err == nil || !strings.Contains(err.Error(), "does not exist and is outside") || res.Wrote {
		t.Fatal(err, res.Text)
	}
	if _, err := os.Stat(filepath.Join(home, "elsewhere")); !os.IsNotExist(err) {
		t.Fatal("created a directory outside ~/.qwen")
	}
	// Once the directory exists, the stow-style target is created there.
	if err := os.MkdirAll(filepath.Join(home, "elsewhere", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if res, err := Run(qwenRequest(home, true)); err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
}

func TestQwenDanglingSymlinkInsideSettingsDirCreatesParents(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	qwenLink(t, home, "profiles/work/settings.json")
	res, err := Run(qwenRequest(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	info, err := os.Stat(filepath.Join(home, ".qwen", "profiles", "work"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal(err, info)
	}
	// A directory symlink inside ~/.qwen that leaves it does not count as
	// inside.
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".qwen"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "out"), filepath.Join(home, ".qwen", "escape")); err != nil {
		t.Fatal(err)
	}
	qwenLink(t, home, "escape/new/settings.json")
	if res, err := Run(qwenRequest(home, true)); err == nil || res.Wrote {
		t.Fatal("followed a directory symlink out of ~/.qwen", res.Text)
	}
	if _, err := os.Stat(filepath.Join(home, "out")); !os.IsNotExist(err) {
		t.Fatal("created a directory outside ~/.qwen")
	}
}
