package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCopilotPrintsAndWritesEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OTEL_EXPORTER_OTLP_CLIENT_KEY", "")
	t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", "")
	t.Setenv("OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE", "")
	path := filepath.Join(home, ".config", "sdf", "copilot.env")
	printed := run(t, "init", "copilot")
	if printed.code != 0 || !strings.Contains(printed.stdout, "OTEL_EXPORTER_OTLP_ENDPOINT='http://127.0.0.1:4318'") || !strings.Contains(printed.stdout, "CAPTURE_CONTENT='NO_CONTENT'") || !strings.Contains(printed.stdout, "not written") {
		t.Fatalf("%d %s %s", printed.code, printed.stdout, printed.stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("print wrote copilot.env")
	}
	wrote := run(t, "init", "copilot-cli", "--write", "--port", "5318")
	if wrote.code != 0 || !strings.Contains(wrote.stdout, "wrote "+path) {
		t.Fatalf("%d %s %s", wrote.code, wrote.stdout, wrote.stderr)
	}
	if body := mustRead(t, path); !strings.Contains(body, "export OTEL_EXPORTER_OTLP_ENDPOINT='http://127.0.0.1:5318'\n") {
		t.Fatal(body)
	}
	global := run(t, "init", "copilot", "--global")
	if global.code != 2 || !strings.Contains(global.stderr, "--global applies to pi") {
		t.Fatalf("%d %s", global.code, global.stderr)
	}
}

func TestInitCheckCopilotReadsSnippetUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	if w := run(t, "init", "copilot", "--write"); w.code != 0 {
		t.Fatal(w.stderr)
	}
	out := run(t, "init", "--check", "copilot", "--port", "1", "--grpc-port", "1")
	if !strings.Contains(out.stdout, "copilot snippet "+filepath.Join(home, ".config", "sdf", "copilot.env")) || !strings.Contains(out.stdout, "managed settings") {
		t.Fatalf("%d %s %s", out.code, out.stdout, out.stderr)
	}
}
