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
		{"claud", "Did you mean claude-code?"},
		{"codx", "Did you mean codex?"},
		{"grk", "Did you mean grok?"},
		{"pi_otel", "Did you mean pi?"},
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
	// Unrelated names get the list and no suggestion; in particular the
	// two-letter pi is not suggested for anything that merely starts with
	// "pi" or is two edits away from it.
	for _, unrelated := range []string{"cursor", "vim", "pip", "ping", "pinecone", "gpt", "aider", "kiro", "p"} {
		out := run(t, "init", unrelated)
		if out.code != 2 || strings.Contains(out.stderr, "Did you mean") || !strings.Contains(out.stderr, "Agents: claude-code") {
			t.Fatalf("%s: %d %q", unrelated, out.code, out.stderr)
		}
	}
	far := run(t, "init", "cursor")
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
