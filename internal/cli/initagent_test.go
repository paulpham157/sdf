package cli_test

import (
	"strings"
	"testing"
)

func TestInitUnknownAgentSuggestsAndLists(t *testing.T) {
	cases := []struct{ arg, want string }{
		{"claude", "Did you mean claude-code?"},
		{"claudecode", "Did you mean claude-code?"},
		{"Claude-Code", "Did you mean claude-code?"},
		{"gemni", "Did you mean gemini?"},
		{"copliot", "Did you mean copilot?"},
		{"opencode-v2", "Did you mean opencode?"},
	}
	for _, c := range cases {
		out := run(t, "init", c.arg)
		if out.code != 2 || !strings.Contains(out.stderr, `unknown agent "`+c.arg+`"`) || !strings.Contains(out.stderr, c.want) {
			t.Fatalf("%s: %d %q", c.arg, out.code, out.stderr)
		}
		if out.stdout != "" {
			t.Fatalf("%s: wrote %q", c.arg, out.stdout)
		}
	}
	far := run(t, "init", "cursor")
	if far.code != 2 || strings.Contains(far.stderr, "Did you mean") {
		t.Fatalf("%d %q", far.code, far.stderr)
	}
	for _, agent := range []string{"claude-code", "codex", "opencode", "pi", "qwen", "grok", "gemini", "copilot"} {
		if !strings.Contains(far.stderr, agent) {
			t.Fatalf("list lacks %s: %q", agent, far.stderr)
		}
	}
	flag := run(t, "init", "--nope")
	if flag.code != 2 || !strings.Contains(flag.stderr, "unrecognized arguments: --nope") {
		t.Fatalf("%d %q", flag.code, flag.stderr)
	}
}
