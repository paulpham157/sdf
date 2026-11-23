package cli_test

import (
	"strings"
	"testing"
)

// TestEveryCommandAcceptsHelp checks that every command and subcommand
// prints usage on stdout and exits 0 for --help and -h. sdf prices refresh
// used to reject both as unrecognized arguments (exit 2) even though
// sdf prices --help worked, and the README says every command accepts --help.
// The expected lines are the usage text those commands print, including
// sdf decision audit and sdf help (the command index).
func TestEveryCommandAcceptsHelp(t *testing.T) {
	// A regression that falls through into refresh must not write the real cache.
	t.Setenv("SDF_PRICE_CACHE", t.TempDir())

	cases := []struct {
		args []string
		want string
	}{
		{nil, "usage: sdf <serve|summary|check|init|prices|decision|audit|task|verify|redact|version>"},
		{[]string{"help"}, "usage: sdf <serve|summary|check|init|prices|decision|audit|task|verify|redact|version>"},
		{[]string{"serve"}, "usage: sdf serve "},
		{[]string{"summary"}, "usage: sdf summary "},
		{[]string{"check"}, "usage: sdf check "},
		{[]string{"init"}, "usage: sdf init "},
		{[]string{"prices"}, "usage: sdf prices refresh [--url URL] [--out PATH]"},
		{[]string{"prices", "refresh"}, "usage: sdf prices refresh [--url URL] [--out PATH]"},
		{[]string{"decision"}, "usage: sdf decision <add|link|list|retract|audit> [flags]"},
		{[]string{"decision", "add"}, "usage: sdf decision add "},
		{[]string{"decision", "link"}, "usage: sdf decision link "},
		{[]string{"decision", "list"}, "usage: sdf decision list "},
		{[]string{"decision", "retract"}, "usage: sdf decision retract "},
		{[]string{"decision", "audit"}, "usage: sdf decision audit "},
		{[]string{"audit"}, "usage: sdf audit "},
		{[]string{"audit", "verify"}, "usage: sdf audit verify "},
		{[]string{"audit", "head"}, "usage: sdf audit head "},
		{[]string{"task"}, "usage: sdf task <declare|show>"},
		{[]string{"task", "declare"}, "usage: sdf task declare "},
		{[]string{"task", "show"}, "usage: sdf task show "},
		{[]string{"verify"}, "usage: sdf verify "},
		{[]string{"redact"}, "usage: sdf redact "},
		{[]string{"version"}, "usage: sdf version"},
	}
	for _, tc := range cases {
		for _, flag := range []string{"--help", "-h"} {
			args := append(append([]string{}, tc.args...), flag)
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				out := run(t, args...)
				if out.code != 0 || out.stderr != "" || !strings.HasPrefix(out.stdout, "usage:") || !strings.Contains(out.stdout, tc.want) {
					t.Fatalf("code %d\nstdout: %s\nstderr: %s", out.code, out.stdout, out.stderr)
				}
			})
		}
	}

	// Bare "help" prints the command index. sdf help <command> is the same
	// text as sdf <command> --help; TestHelpCommandMatchesDashDashHelp checks that.
	t.Run("help", func(t *testing.T) {
		out := run(t, "help")
		want := "usage: sdf <serve|summary|check|init|prices|decision|audit|task|verify|redact|version>"
		if out.code != 0 || out.stderr != "" || !strings.Contains(out.stdout, want) {
			t.Fatalf("code %d\nstdout: %s\nstderr: %s", out.code, out.stdout, out.stderr)
		}
	})
}

var topCommands = []string{"serve", "summary", "check", "init", "prices", "decision", "audit", "task", "verify", "redact", "version"}

// Every command in the usage line has a help line, so a new command (as
// redact was) cannot be added to the dispatch without one.
func TestUsageLineCommandsAreAllListed(t *testing.T) {
	out := run(t, "--help")
	first := strings.SplitN(out.stdout, "\n", 2)[0]
	open, end := strings.Index(first, "<"), strings.Index(first, ">")
	if open < 0 || end < open {
		t.Fatalf("usage line %q", first)
	}
	names := strings.Split(first[open+1:end], "|")
	if strings.Join(names, " ") != strings.Join(topCommands, " ") {
		t.Fatalf("usage line has %v, help test lists %v", names, topCommands)
	}
}

func TestTopHelpListsEveryCommandWithADescription(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		out := run(t, args...)
		if out.code != 0 || out.stderr != "" {
			t.Fatalf("%v: %d %q", args, out.code, out.stderr)
		}
		if strings.Contains(out.stdout, "demo") || !strings.Contains(out.stdout, "Start here: run sdf serve") {
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
