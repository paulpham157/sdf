package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGeminiSettings(t *testing.T, home, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustReadString(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func assertOrder(t *testing.T, text string, parts ...string) {
	t.Helper()
	at := -1
	for _, part := range parts {
		i := strings.Index(text, part)
		if i < 0 {
			t.Fatalf("missing %q\n%s", part, text)
		}
		if i <= at {
			t.Fatalf("%q is out of order\n%s", part, text)
		}
		at = i
	}
}

func TestGeminiWriteKeepsKeyOrderHTMLAndBigInts(t *testing.T) {
	home := t.TempDir()
	original := `{
    "zeta": {"b": 1, "a": 2},
    "mcpServers": {"x": {"url": "https://h/?a=1&b=<2>", "timeout": 12345678901234567890}},
    "list": [{"y": 1, "x": 2}],
    "telemetry": {"traces": true, "useCollector": false},
    "alpha": 1.50
}
`
	path := writeGeminiSettings(t, home, original, 0o600)
	res, err := Run(geminiRequest(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	text := mustReadString(t, path)
	assertOrder(t, text, `"zeta"`, `"b": 1`, `"a": 2`, `"mcpServers"`, `"list"`, `"y": 1`, `"x": 2`, `"telemetry"`, `"traces": true`, `"useCollector": false`,
		`"enabled": true`, `"logPrompts": false`, `"otlpEndpoint"`, `"otlpProtocol"`, `"target"`, `"alpha"`)
	for _, want := range []string{`"https://h/?a=1&b=<2>"`, `12345678901234567890`, `1.50`, "\n    \"zeta\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, `\u0026`) || strings.Contains(text, `\u003c`) {
		t.Fatal(text)
	}
	if !json.Valid([]byte(text)) {
		t.Fatal(text)
	}
	if mustReadString(t, path+".sdf-bak") != original {
		t.Fatal("backup differs")
	}
}

func TestGeminiWriteKeepsFileMode(t *testing.T) {
	home := t.TempDir()
	path := writeGeminiSettings(t, home, `{"theme":"x"}`, 0o640)
	if _, err := Run(geminiRequest(home, true)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatal(err, info.Mode())
	}
	if info, err := os.Stat(path + ".sdf-bak"); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatal(err, info.Mode())
	}
}

func TestGeminiWriteThroughSymlink(t *testing.T) {
	home := t.TempDir()
	dotfiles := filepath.Join(t.TempDir(), "dotfiles")
	if err := os.MkdirAll(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dotfiles, "gemini-settings.json")
	original := `{"theme":"x"}`
	if err := os.WriteFile(real, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(geminiRequest(home, true)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced", err)
	}
	if text := mustReadString(t, real); !strings.Contains(text, `"otlpEndpoint": "http://127.0.0.1:4318"`) {
		t.Fatal(text)
	}
	if info, err := os.Stat(real); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatal(err, info.Mode())
	}
	if mustReadString(t, path+".sdf-bak") != original {
		t.Fatal("backup differs")
	}
	if info, err := os.Lstat(path + ".sdf-bak"); err != nil || !info.Mode().IsRegular() {
		t.Fatal("backup is not a regular file", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dotfiles, ".*sdf-tmp*"))
	if len(leftovers) != 0 {
		t.Fatal(leftovers)
	}
}

func TestGeminiWriteNeverClobbersABackup(t *testing.T) {
	home := t.TempDir()
	path := writeGeminiSettings(t, home, `{"theme":"x"}`, 0o600)
	if err := os.WriteFile(path+".sdf-bak", []byte("older backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(geminiRequest(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	if mustReadString(t, path+".sdf-bak") != "older backup" {
		t.Fatal("existing .sdf-bak was overwritten")
	}
	req := geminiRequest(home, true)
	req.Endpoint.HTTPPort = 5318
	if _, err := Run(req); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(path + ".sdf-bak.*")
	if len(backups) != 2 {
		t.Fatalf("want two timestamped backups, got %v", backups)
	}
	contents := map[string]bool{}
	for _, b := range backups {
		contents[mustReadString(t, b)] = true
	}
	if !contents[`{"theme":"x"}`] || len(contents) != 2 {
		t.Fatal(contents)
	}
	if !strings.Contains(res.Text, "backup "+path+".sdf-bak.") {
		t.Fatal(res.Text)
	}
}

func TestGeminiFailedWriteCleansUp(t *testing.T) {
	home := t.TempDir()
	original := `{"theme":"x"}`
	path := writeGeminiSettings(t, home, original, 0o600)
	renameFile = func(string, string) error { return errors.New("disk full") }
	t.Cleanup(func() { renameFile = os.Rename })
	if _, err := Run(geminiRequest(home, true)); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatal(err)
	}
	if mustReadString(t, path) != original {
		t.Fatal("original changed")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("leftovers: %v", names)
	}
}

func TestGeminiDryRunWarnsAndPrintsMergedJSON(t *testing.T) {
	home := t.TempDir()
	original := `{"theme":"a&b","telemetry":{"target":"gcp","otlpEndpoint":"http://localhost:4317","otlpProtocol":"grpc","logPrompts":true}}`
	path := writeGeminiSettings(t, home, original, 0o600)
	res, err := Run(geminiRequest(home, false))
	if err != nil || res.Wrote {
		t.Fatal(err, res.Text)
	}
	for _, want := range []string{
		`telemetry.target "gcp" -> "local"`,
		`telemetry.otlpEndpoint "http://localhost:4317" -> "http://127.0.0.1:4318"`,
		`telemetry.otlpProtocol "grpc" -> "http"`,
		"telemetry.logPrompts=true",
		"sdf would set:",
		`"logPrompts": true`,
		`"enabled": true`,
		"1 other top-level key(s)",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("missing %q\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "theme") {
		t.Fatalf("dry run printed a key sdf does not manage\n%s", res.Text)
	}
	if mustReadString(t, path) != original {
		t.Fatal("dry run changed the file")
	}
	if _, err := os.Stat(path + ".sdf-bak"); !os.IsNotExist(err) {
		t.Fatal("dry run wrote a backup")
	}
}

func TestGeminiCheckWarnsAboutOverrides(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	system := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(system, []byte(`{"telemetry":{"enabled":false,"otlpEndpoint":"http://collector:4317"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeGeminiSettings(t, project, `{"telemetry":{"outfile":"t.log"}}`, 0o600)
	t.Setenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH", system)
	t.Setenv("GEMINI_TELEMETRY_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4318")
	req := geminiRequest(home, false)
	req.ProjectDir = project
	text, err := Configured(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"warning: OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318 overrides",
		"warning: system settings " + system,
		"enabled=false",
		`otlpEndpoint="http://collector:4317"`,
		"only when this folder is trusted",
		`outfile="t.log"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	t.Setenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH", filepath.Join(t.TempDir(), "missing.json"))
	req.ProjectDir = t.TempDir()
	text, _ = Configured(req)
	if strings.Contains(text, "warning:") {
		t.Fatal(text)
	}
}

func TestMergeIntoComparesJSONValues(t *testing.T) {
	dst := map[string]any{"interval": json.Number("10000"), "hooks": []any{map[string]any{"a": "b"}}}
	src := map[string]any{"interval": 10000, "hooks": []map[string]string{{"a": "b"}}}
	if mergeInto(dst, src) {
		t.Fatal("equal values reported as a change")
	}
	if !mergeInto(dst, map[string]any{"interval": 5000}) || dst["interval"] != 5000 {
		t.Fatal(dst)
	}
	if !mergeInto(dst, map[string]any{"enabled": false}) || dst["enabled"] != false {
		t.Fatal("a new false value was not added")
	}
}
