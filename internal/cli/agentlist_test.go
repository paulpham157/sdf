package cli_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var allAgents = []string{"claude-code", "codex", "opencode", "pi", "qwen", "grok", "gemini", "copilot"}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// The places a new user meets first (the README Quickstart, sdf --help, and
// the empty UI) name every agent sdf init accepts, and the steps to a real
// session.
func TestFirstRunTextNamesEveryAgent(t *testing.T) {
	readme := repoFile(t, "README.md")
	start := strings.Index(readme, "## Quickstart")
	if start < 0 {
		t.Fatal("README has no Quickstart section")
	}
	end := strings.Index(readme[start+1:], "\n## ")
	section := readme[start:]
	if end >= 0 {
		section = readme[start : start+1+end]
	}
	for _, want := range []string{"sdf serve", "--write", "Restart the agent", "http://127.0.0.1:4320/", "sdf summary"} {
		if !strings.Contains(section, want) {
			t.Fatalf("README Quickstart lacks %q", want)
		}
	}
	help := run(t, "--help")
	if help.code != 0 {
		t.Fatalf("%d %s", help.code, help.stderr)
	}
	app := repoFile(t, "internal/ui/assets/app.js")
	empty := app[strings.Index(app, "function emptyState"):]
	empty = empty[:strings.Index(empty, "function isEmpty")]
	for _, agent := range allAgents {
		if !strings.Contains(section, agent) {
			t.Fatalf("README Quickstart lacks %s", agent)
		}
		if !strings.Contains(help.stdout, agent) {
			t.Fatalf("sdf --help lacks %s", agent)
		}
		if !strings.Contains(empty, agent) {
			t.Fatalf("UI empty state lacks %s", agent)
		}
	}
	if !strings.Contains(help.stdout, "--write") || !strings.Contains(empty, "--write") {
		t.Fatal("sdf --help or UI empty state does not say --write")
	}
}
