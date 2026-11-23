package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codexTOMLReq(home string) (Request, string) {
	codexHome := filepath.Join(home, ".codex")
	return Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}},
		filepath.Join(codexHome, "config.toml")
}

func writeCodexConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Every TOML form that sets the top-level otel key counts as an existing
// table. The old line scan saw only "[otel]" and "[otel.", so --write
// appended a second [otel] to the other forms and broke the file
// (duplicate key).
func TestCodexExistingOtelInAnyTOMLFormIsLeftAlone(t *testing.T) {
	forms := map[string]string{
		"table":         "model = \"keep\"\n\n[otel]\nexporter = \"none\"\n",
		"spaced header": "[ otel ]\nexporter = \"none\"\n",
		"tabbed header": "[\totel\t]\nexporter = \"none\"\n",
		"quoted header": "[\"otel\"]\nexporter = \"none\"\n",
		"literal quote": "['otel']\nexporter = \"none\"\n",
		"sub table":     "[otel.exporter]\nx = 1\n",
		"spaced sub":    "[ otel . exporter ]\nx = 1\n",
		"inline":        "model = \"keep\"\notel = { exporter = \"none\" }\n",
		"dotted":        "model = \"keep\"\notel.exporter = \"none\"\n",
		"spaced dotted": "otel . exporter = \"none\"\n",
		"quoted dotted": "\"otel\".exporter = \"none\"\n",
	}
	for name, body := range forms {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			req, config := codexTOMLReq(home)
			writeCodexConfig(t, config, body)
			res, err := Run(req)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(res.Text, "already has an [otel] table") {
				t.Fatalf("not reported as an existing table:\n%s", res.Text)
			}
			if got := mustReadString(t, config); got != body {
				t.Fatalf("config.toml changed:\n%s", got)
			}
			if _, err := os.Stat(config + ".sdf-bak"); !os.IsNotExist(err) {
				t.Fatalf("left a backup (err=%v)", err)
			}
		})
	}
}

// Text that only mentions otel (a comment, a string, another key, a nested
// table) is not the top-level key, so the table is appended and the result
// still parses.
func TestCodexOtelLookalikesStillGetTheTable(t *testing.T) {
	forms := map[string]string{
		"comment":      "# [otel]\nmodel = \"keep\"\n",
		"string":       "note = \"[otel]\"\n",
		"multiline":    "note = '''\n[otel]\n'''\n",
		"prefix key":   "otel_extra = 1\n",
		"nested table": "[profiles.dev.otel]\nx = 1\n",
		"nested key":   "[profiles.dev]\notel = { exporter = \"none\" }\n",
	}
	for name, body := range forms {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			req, config := codexTOMLReq(home)
			writeCodexConfig(t, config, body)
			res, err := Run(req)
			if err != nil || !res.Wrote {
				t.Fatal(err, res.Text)
			}
			got := mustReadString(t, config)
			if !strings.HasPrefix(got, body) || !strings.Contains(got, "\n[otel]\nenvironment = \"dev\"\n") {
				t.Fatalf("table not appended:\n%s", got)
			}
			if has, err := tomlHasKey([]byte(got), "otel"); err != nil || !has {
				t.Fatalf("written file: has=%t err=%v\n%s", has, err, got)
			}
		})
	}
}

// A config.toml that is not valid TOML is refused before anything is
// written: Codex cannot load it, and appending to it cannot be checked.
func TestCodexInvalidTOMLIsRefused(t *testing.T) {
	home := t.TempDir()
	req, config := codexTOMLReq(home)
	body := "model = [\n"
	writeCodexConfig(t, config, body)
	_, err := Run(req)
	if err == nil || !strings.Contains(err.Error(), "is not valid TOML") || !strings.Contains(err.Error(), "it was not changed") {
		t.Fatalf("err = %v", err)
	}
	if got := mustReadString(t, config); got != body {
		t.Fatalf("config.toml changed:\n%s", got)
	}
	for _, p := range []string{config + ".sdf-bak", filepath.Join(filepath.Dir(config), "hooks.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s exists (err=%v)", p, err)
		}
	}
}
