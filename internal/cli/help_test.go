package cli_test

import (
	"strings"
	"testing"
)

var topCommands = []string{"serve", "summary", "demo", "check", "init", "prices", "decision", "audit", "task", "verify", "version"}

func TestTopHelpListsEveryCommandWithADescription(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		out := run(t, args...)
		if out.code != 0 || out.stderr != "" {
			t.Fatalf("%v: %d %q", args, out.code, out.stderr)
		}
		if !strings.Contains(out.stdout, "sdf demo") {
			t.Fatalf("%v: no starting point\n%s", args, out.stdout)
		}
		for _, name := range topCommands {
			found := false
			for _, line := range strings.Split(out.stdout, "\n") {
				fields := strings.Fields(line)
				if len(fields) > 2 && fields[0] == name {
					found = true
				}
			}
			if !found {
				t.Fatalf("%v: %s has no description line\n%s", args, name, out.stdout)
			}
		}
		for _, agent := range []string{"claude-code", "codex", "opencode", "pi", "qwen", "grok", "gemini", "copilot"} {
			if !strings.Contains(out.stdout, agent) {
				t.Fatalf("%v: agent %s missing\n%s", args, agent, out.stdout)
			}
		}
	}
}

func TestHelpCommandMatchesDashDashHelp(t *testing.T) {
	for _, name := range topCommands {
		viaHelp := run(t, "help", name)
		direct := run(t, name, "--help")
		if viaHelp.code != 0 || direct.code != 0 {
			t.Fatalf("%s: help=%d %q --help=%d %q", name, viaHelp.code, viaHelp.stderr, direct.code, direct.stderr)
		}
		if viaHelp.stdout != direct.stdout {
			t.Fatalf("%s: sdf help %s differs from sdf %s --help", name, name, name)
		}
		if !strings.HasPrefix(direct.stdout, "usage: sdf "+name) {
			t.Fatalf("%s: %q", name, direct.stdout)
		}
		// Every command explains itself, not just its usage line.
		if len(strings.Split(strings.TrimSpace(direct.stdout), "\n")) < 2 {
			t.Fatalf("%s --help is only a usage line: %q", name, direct.stdout)
		}
	}
}

func TestHelpForAnUnknownCommand(t *testing.T) {
	out := run(t, "help", "sevre")
	if out.code != 2 || !strings.Contains(out.stderr, `unrecognized command "sevre"`) || !strings.Contains(out.stderr, "sdf --help") {
		t.Fatalf("%d %q", out.code, out.stderr)
	}
}

func TestPricesRefreshHelp(t *testing.T) {
	out := run(t, "prices", "refresh", "--help")
	if out.code != 0 || !strings.Contains(out.stdout, "--out PATH") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}

func TestNoArgumentsPointsAtHelp(t *testing.T) {
	out := run(t)
	if out.code != 2 || !strings.Contains(out.stderr, "sdf --help") {
		t.Fatalf("%d %q", out.code, out.stderr)
	}
}
