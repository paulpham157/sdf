package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitGrokPrintsAndWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"GROK_HOME", "GROK_EXTERNAL_OTEL", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_LOG_USER_PROMPTS", "OTEL_LOG_ASSISTANT_RESPONSES", "OTEL_LOG_TOOL_CONTENT"} {
		t.Setenv(key, "")
	}
	config := filepath.Join(home, ".grok", "config.toml")
	hook := filepath.Join(home, ".grok", "hooks", "sdf-session.json")
	printed := run(t, "init", "grok")
	if printed.code != 0 || !strings.Contains(printed.stdout, "export GROK_EXTERNAL_OTEL=1") || !strings.Contains(printed.stdout, "not written") {
		t.Fatalf("%d %s %s", printed.code, printed.stdout, printed.stderr)
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatal("print wrote config.toml")
	}
	wrote := run(t, "init", "grok-cli", "--write", "--port", "5318")
	if wrote.code != 0 || !strings.Contains(wrote.stdout, "wrote "+config) || !strings.Contains(wrote.stdout, "wrote "+hook) {
		t.Fatalf("%d %s %s", wrote.code, wrote.stdout, wrote.stderr)
	}
	if body := mustRead(t, config); !strings.Contains(body, `otel_endpoint = "http://127.0.0.1:5318"`) {
		t.Fatal(body)
	}
	if body := mustRead(t, hook); !strings.Contains(body, "http://127.0.0.1:5318/v1/sessions") {
		t.Fatal(body)
	}
	check := run(t, "init", "--check", "grok", "--port", "5318", "--grpc-port", "1")
	if !strings.Contains(check.stdout, "has [telemetry] pointing at http://127.0.0.1:5318: true") {
		t.Fatalf("%d %s %s", check.code, check.stdout, check.stderr)
	}
	if err := os.WriteFile(config, []byte("[telemetry]\notel_enabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept := run(t, "init", "grok", "--write")
	if kept.code != 0 || !strings.Contains(kept.stdout, "already has a [telemetry] table") {
		t.Fatalf("%d %s %s", kept.code, kept.stdout, kept.stderr)
	}
	if body := mustRead(t, config); body != "[telemetry]\notel_enabled = false\n" {
		t.Fatalf("existing table changed: %q", body)
	}
}
