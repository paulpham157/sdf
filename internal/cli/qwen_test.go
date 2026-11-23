package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitQwenPrintsAndWritesSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QWEN_HOME", "")
	t.Setenv("QWEN_CODE_SYSTEM_SETTINGS_PATH", filepath.Join(home, "none.json"))
	path := filepath.Join(home, ".qwen", "settings.json")
	printed := run(t, "init", "qwen")
	if printed.code != 0 || !strings.Contains(printed.stdout, `"otlpEndpoint": "http://127.0.0.1:4318"`) || !strings.Contains(printed.stdout, `"logPrompts": false`) || !strings.Contains(printed.stdout, "not written") {
		t.Fatalf("%d %s %s", printed.code, printed.stdout, printed.stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("print wrote settings.json")
	}
	wrote := run(t, "init", "qwen-code", "--write", "--port", "5318")
	if wrote.code != 0 || !strings.Contains(wrote.stdout, "wrote "+path) {
		t.Fatalf("%d %s %s", wrote.code, wrote.stdout, wrote.stderr)
	}
	if body := mustRead(t, path); !strings.Contains(body, `"otlpEndpoint": "http://127.0.0.1:5318"`) {
		t.Fatal(body)
	}
	check := run(t, "init", "--check", "qwen", "--port", "5318", "--grpc-port", "1")
	if !strings.Contains(check.stdout, "qwen points at http://127.0.0.1:5318 over http: true") {
		t.Fatalf("%d %s %s", check.code, check.stdout, check.stderr)
	}
	global := run(t, "init", "qwen", "--global")
	if global.code != 2 || !strings.Contains(global.stderr, "--global applies to pi") {
		t.Fatalf("%d %s", global.code, global.stderr)
	}
}

func TestInitQwenWriteFailsOnFileItCannotMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QWEN_HOME", "")
	t.Setenv("QWEN_CODE_SYSTEM_SETTINGS_PATH", filepath.Join(home, "none.json"))
	path := filepath.Join(home, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"{\n  // mine\n}\n", "{ broken", "[]"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		wrote := run(t, "init", "qwen", "--write")
		if wrote.code == 0 || !strings.Contains(wrote.stderr, "was not changed") {
			t.Fatalf("%q: %d %s %s", body, wrote.code, wrote.stdout, wrote.stderr)
		}
		if got := mustRead(t, path); got != body {
			t.Fatalf("%q changed to %q", body, got)
		}
		if dry := run(t, "init", "qwen"); dry.code != 0 {
			t.Fatalf("dry run %q: %d %s", body, dry.code, dry.stderr)
		}
	}
}
