package cli_test

import (
	"strings"
	"testing"
)

// TestEveryCommandAcceptsHelp checks that every command and subcommand
// prints usage on stdout and exits 0 for --help and -h. sdf prices refresh
// used to reject both as unrecognized arguments (exit 2) even though
// sdf prices --help worked, and the README says every command accepts --help.
func TestEveryCommandAcceptsHelp(t *testing.T) {
	// A regression that falls through into refresh must not write the real cache.
	t.Setenv("SDF_PRICE_CACHE", t.TempDir())

	cases := []struct {
		args []string
		want string
	}{
		{nil, "usage: sdf <serve|summary|check|init|prices|decision|audit|task|verify|redact|version>"},
		{[]string{"serve"}, "usage: sdf serve "},
		{[]string{"summary"}, "usage: sdf summary "},
		{[]string{"check"}, "usage: sdf check "},
		{[]string{"init"}, "usage: sdf init "},
		{[]string{"prices"}, "usage: sdf prices refresh [--url URL] [--out PATH]"},
		{[]string{"prices", "refresh"}, "usage: sdf prices refresh [--url URL] [--out PATH]"},
		{[]string{"decision"}, "usage: sdf decision <add|link|list|retract>"},
		{[]string{"decision", "add"}, "usage: sdf decision add "},
		{[]string{"decision", "link"}, "usage: sdf decision link "},
		{[]string{"decision", "list"}, "usage: sdf decision list "},
		{[]string{"decision", "retract"}, "usage: sdf decision retract "},
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

	// Top-level "help" is the third spelling of --help. Subcommands do not
	// take a bare "help" word; they take --help and -h.
	t.Run("help", func(t *testing.T) {
		out := run(t, "help")
		want := "usage: sdf <serve|summary|check|init|prices|decision|audit|task|verify|redact|version>"
		if out.code != 0 || out.stderr != "" || !strings.Contains(out.stdout, want) {
			t.Fatalf("code %d\nstdout: %s\nstderr: %s", out.code, out.stdout, out.stderr)
		}
	})
}
