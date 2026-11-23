package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitGeminiPrintsAndWritesSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".gemini", "settings.json")
	printed := run(t, "init", "gemini")
	if printed.code != 0 || !strings.Contains(printed.stdout, "GEMINI_TELEMETRY_OTLP_ENDPOINT=http://127.0.0.1:4318") || !strings.Contains(printed.stdout, "not written") {
		t.Fatalf("%d %s %s", printed.code, printed.stdout, printed.stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("print wrote settings.json")
	}
	wrote := run(t, "init", "gemini", "--write", "--port", "5318")
	if wrote.code != 0 || !strings.Contains(wrote.stdout, "wrote "+path) {
		t.Fatalf("%d %s %s", wrote.code, wrote.stdout, wrote.stderr)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, path)), &root); err != nil {
		t.Fatal(err)
	}
	tel, _ := root["telemetry"].(map[string]any)
	if tel["enabled"] != true || tel["otlpEndpoint"] != "http://127.0.0.1:5318" || tel["otlpProtocol"] != "http" || tel["target"] != "local" || tel["logPrompts"] != false {
		t.Fatalf("telemetry %#v", tel)
	}
	alias := run(t, "init", "gemini-cli")
	if alias.code != 0 {
		t.Fatalf("%d %s", alias.code, alias.stderr)
	}
	global := run(t, "init", "gemini", "--global")
	if global.code != 2 || !strings.Contains(global.stderr, "--global applies to pi") {
		t.Fatalf("%d %s", global.code, global.stderr)
	}
}
