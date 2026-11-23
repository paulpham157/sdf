package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func copilotRequest(home string, write bool) Request {
	return Request{
		Agent: "copilot", Home: home, ProjectDir: home, Write: write,
		Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318, GRPCPort: 4317},
	}
}

func clearCopilotEnv(t *testing.T) {
	t.Helper()
	for _, key := range append(append([]string{copilotEndpointKey, copilotProtocolKey, copilotCaptureKey, copilotLegacyKey}, copilotTLSVars...), copilotOverrides...) {
		t.Setenv(key, "")
	}
}

func TestCopilotPrintDoesNotWrite(t *testing.T) {
	clearCopilotEnv(t)
	home := t.TempDir()
	res, err := Run(copilotRequest(home, false))
	if err != nil || res.Wrote {
		t.Fatal(err, res.Wrote)
	}
	for _, want := range []string{
		"export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318",
		"export OTEL_EXPORTER_OTLP_PROTOCOL=http/json",
		"export OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=NO_CONTENT",
		"not written",
		filepath.Join(home, ".config", "sdf", "copilot.env"),
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("missing %q\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "warning:") {
		t.Fatalf("unexpected warning\n%s", res.Text)
	}
	if _, err := os.Stat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
		t.Fatal("print wrote ~/.config")
	}
	alias := copilotRequest(home, false)
	alias.Agent = "copilot-cli"
	if _, err := Run(alias); err != nil {
		t.Fatal(err)
	}
}

func TestCopilotWarnsWhenTLSVariablesBlockHTTP(t *testing.T) {
	clearCopilotEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_CLIENT_KEY", "/tmp/key.pem")
	res, err := Run(copilotRequest(t.TempDir(), false))
	if err != nil || !strings.Contains(res.Text, "warning: OTEL_EXPORTER_OTLP_CLIENT_KEY is set. Copilot CLI drops export") {
		t.Fatal(err, res.Text)
	}
	got, err := Configured(copilotRequest(t.TempDir(), false))
	if err != nil || !strings.Contains(got, "copilot warning: OTEL_EXPORTER_OTLP_CLIENT_KEY is set") {
		t.Fatal(err, got)
	}
}

func TestCopilotWriteMergesEnvAndBacksUp(t *testing.T) {
	clearCopilotEnv(t)
	home := t.TempDir()
	path := filepath.Join(home, ".config", "sdf", "copilot.env")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := "# mine\nexport OTEL_EXPORTER_OTLP_HEADERS=x-team=blue\nexport OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318\nexport OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=SPAN_ONLY\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	req := copilotRequest(home, true)
	req.Endpoint.HTTPPort = 5318
	res, err := Run(req)
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"# mine\n",
		"export OTEL_EXPORTER_OTLP_HEADERS=x-team=blue\n",
		"export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:5318\n",
		"export OTEL_EXPORTER_OTLP_PROTOCOL=http/json\n",
		// The user's own capture choice is kept, not reset.
		"export OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=SPAN_ONLY\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "collector:4318") || strings.Count(got, "CAPTURE_CONTENT") != 1 {
		t.Fatalf("not merged\n%s", got)
	}
	// Order is kept: the endpoint line stays where it was.
	if strings.Index(got, "OTLP_HEADERS") > strings.Index(got, "OTLP_ENDPOINT") || strings.Index(got, "OTLP_ENDPOINT") > strings.Index(got, "CAPTURE_CONTENT") {
		t.Fatalf("order changed\n%s", got)
	}
	for _, want := range []string{
		"warning: replacing OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318 with http://127.0.0.1:5318",
		"warning: content capture is already on in this file (OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=SPAN_ONLY)",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("missing %q\n%s", want, res.Text)
		}
	}
	backup, err := os.ReadFile(path + ".sdf-bak")
	if err != nil || string(backup) != old {
		t.Fatalf("backup %v %q", err, backup)
	}
	again, err := Run(req)
	if err != nil || again.Wrote || !strings.Contains(again.Text, "already has these values") {
		t.Fatal(err, again.Text)
	}
}

func TestCopilotWriteFreshFileDisablesContent(t *testing.T) {
	clearCopilotEnv(t)
	home := t.TempDir()
	if _, err := Run(copilotRequest(home, true)); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(home, ".config", "sdf", "copilot.env"))
	if err != nil {
		t.Fatal(err)
	}
	want := "# source this file before starting copilot\n" +
		"export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318\n" +
		"export OTEL_EXPORTER_OTLP_PROTOCOL=http/json\n" +
		"export OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=NO_CONTENT\n"
	if string(body) != want {
		t.Fatalf("got\n%s", body)
	}
}

func TestCopilotConfiguredReadsProcessEnv(t *testing.T) {
	clearCopilotEnv(t)
	got, err := Configured(copilotRequest(t.TempDir(), false))
	if err != nil || !strings.Contains(got, "http://127.0.0.1:4318: false") {
		t.Fatal(err, got)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318/")
	got, err = Configured(copilotRequest(t.TempDir(), false))
	if err != nil || !strings.Contains(got, "http://127.0.0.1:4318: true") {
		t.Fatal(err, got)
	}
}

func TestCopilotDryRunPrintsMergedFile(t *testing.T) {
	clearCopilotEnv(t)
	home := t.TempDir()
	path := copilotEnvPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := "export OTEL_EXPORTER_OTLP_PROTOCOL=grpc\nexport OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://other:4318/v1/traces\n"
	if err := os.WriteFile(path, []byte(old), 0o640); err != nil {
		t.Fatal(err)
	}
	res, err := Run(copilotRequest(home, false))
	if err != nil || res.Wrote {
		t.Fatal(err, res.Text)
	}
	for _, want := range []string{
		path + ":\nexport OTEL_EXPORTER_OTLP_PROTOCOL=http/json\nexport OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://other:4318/v1/traces\nexport OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318\nexport OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=NO_CONTENT\n",
		"warning: replacing OTEL_EXPORTER_OTLP_PROTOCOL=grpc with http/json",
		"warning: OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://other:4318/v1/traces in this file overrides",
		"not written",
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("missing %q\n%s", want, res.Text)
		}
	}
	if body, _ := os.ReadFile(path); string(body) != old {
		t.Fatal("dry run changed the file")
	}
}

func TestCopilotWriteKeepsModeFollowsSymlinkAndKeepsOldBackups(t *testing.T) {
	clearCopilotEnv(t)
	home := t.TempDir()
	real := filepath.Join(home, "dotfiles", "copilot.env")
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("# mine\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real+".sdf-bak", []byte("an older backup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := copilotEnvPath(home)
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	res, err := Run(copilotRequest(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a file", err)
	}
	body, _ := os.ReadFile(real)
	if !strings.HasPrefix(string(body), "# mine\nexport OTEL_EXPORTER_OTLP_ENDPOINT=") {
		t.Fatalf("target not updated\n%s", body)
	}
	if info, _ := os.Stat(real); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	if old, _ := os.ReadFile(real + ".sdf-bak"); string(old) != "an older backup\n" {
		t.Fatalf("existing backup clobbered: %q", old)
	}
	if b1, _ := os.ReadFile(real + ".sdf-bak.1"); string(b1) != "# mine\n" {
		t.Fatalf("new backup %q", b1)
	}
	if !strings.Contains(res.Text, "backup "+real+".sdf-bak.1") || !strings.Contains(res.Text, "(symlink "+link+")") {
		t.Fatal(res.Text)
	}
	entries, _ := os.ReadDir(filepath.Dir(real))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".sdf-tmp-") {
			t.Fatal("temp file left", e.Name())
		}
	}
}

func TestCopilotFailedWriteRemovesBackupAndKeepsFile(t *testing.T) {
	clearCopilotEnv(t)
	home := t.TempDir()
	path := copilotEnvPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	copilotRename = func(string, string) error { return os.ErrPermission }
	t.Cleanup(func() { copilotRename = os.Rename })
	if _, err := Run(copilotRequest(home, true)); err == nil {
		t.Fatal("expected the write to fail")
	}
	if body, _ := os.ReadFile(path); string(body) != "# mine\n" {
		t.Fatalf("file changed %q", body)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("left behind %v", entries)
	}
}

func TestCopilotDanglingSymlinkIsNotReplaced(t *testing.T) {
	clearCopilotEnv(t)
	home := t.TempDir()
	link := copilotEnvPath(home)
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "missing", "copilot.env"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(copilotRequest(home, true)); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatal(err)
	}
}

func TestCopilotEnvOverridesAreReported(t *testing.T) {
	clearCopilotEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://elsewhere:4318/v1/metrics")
	t.Setenv("COPILOT_OTEL_FILE_EXPORTER_PATH", "/tmp/copilot-otel.jsonl")
	t.Setenv(copilotCaptureKey, "SPAN_AND_EVENT")
	t.Setenv(copilotProtocolKey, "grpc")
	res, err := Run(copilotRequest(t.TempDir(), false))
	if err != nil {
		t.Fatal(err)
	}
	check, err := Configured(copilotRequest(t.TempDir(), false))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://elsewhere:4318/v1/metrics is set in this environment and overrides",
		"COPILOT_OTEL_FILE_EXPORTER_PATH=/tmp/copilot-otel.jsonl is set",
		"content capture is on in this environment (OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=SPAN_AND_EVENT)",
		"OTEL_EXPORTER_OTLP_PROTOCOL=grpc is set; Copilot CLI supports only",
	} {
		if !strings.Contains(res.Text, want) || !strings.Contains(check, want) {
			t.Fatalf("missing %q\n%s\n---\n%s", want, res.Text, check)
		}
	}
	if !strings.Contains(check, "managed settings") {
		t.Fatal(check)
	}
}

func TestCopilotConfiguredReadsSnippet(t *testing.T) {
	clearCopilotEnv(t)
	home := t.TempDir()
	if _, err := Run(copilotRequest(home, true)); err != nil {
		t.Fatal(err)
	}
	got, err := Configured(copilotRequest(home, false))
	if err != nil || !strings.Contains(got, "copilot snippet "+copilotEnvPath(home)+" OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318: true") {
		t.Fatal(err, got)
	}
}
