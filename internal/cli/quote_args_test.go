package cli_test

import (
	"strings"
	"testing"
)

// TestInitQuotesUnrecognizedArgument ports the unique test from
// closed PR #229: a typo with a terminal escape is quoted on stderr,
// never written raw.
func TestInitQuotesUnrecognizedArgument(t *testing.T) {
	got := run(t, "init", "--bogus\x1b[31m")
	if got.code != 2 || strings.Contains(got.stderr, "\x1b") || !strings.Contains(got.stderr, `\x1b`) {
		t.Fatalf("%d %q", got.code, got.stderr)
	}
}

func TestUnrecognizedArgumentsQuoteControlCharacters(t *testing.T) {
	const typo = "--bogus\x1b[31m"
	// Commands and subcommands that treat an unknown flag or leftover
	// token as "unrecognized arguments".
	cases := [][]string{
		{"version", typo},
		{"serve", typo},
		{"summary", typo},
		{"check", typo},
		{"prices", "refresh", typo},
		{"decision", typo},
		{"decision", "add", typo},
		{"decision", "link", typo},
		{"decision", "list", typo},
		{"decision", "graph", typo},
		{"decision", "brief", typo},
		{"decision", "retract", typo},
		{"decision", "audit", typo},
		{"decision", "pre-push", typo},
		{"task", typo},
		{"task", "declare", typo},
		{"task", "show", typo},
		{"verify", typo},
		{"audit", typo},
		{"audit", "verify", typo},
		{"audit", "head", typo},
		{"redact", typo},
		{"init", typo},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got := run(t, args...)
			if got.code != 2 {
				t.Fatalf("code %d, want 2; stderr %q", got.code, got.stderr)
			}
			if strings.Contains(got.stderr, "\x1b") {
				t.Fatalf("raw ESC on stderr: %q", got.stderr)
			}
			if !strings.Contains(got.stderr, `\x1b`) {
				t.Fatalf("quoted form missing: %q", got.stderr)
			}
			if !strings.Contains(got.stderr, "unrecognized arguments:") {
				t.Fatalf("want unrecognized arguments, got %q", got.stderr)
			}
		})
	}
}

func TestUnrecognizedCommandQuotesControlCharacters(t *testing.T) {
	const typo = "nope\x1b[31m"
	for _, args := range [][]string{
		{typo},
		{"help", typo},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got := run(t, args...)
			if got.code != 2 || strings.Contains(got.stderr, "\x1b") || !strings.Contains(got.stderr, `\x1b`) {
				t.Fatalf("%d %q", got.code, got.stderr)
			}
			if !strings.Contains(got.stderr, `unrecognized command`) {
				t.Fatalf("want unrecognized command, got %q", got.stderr)
			}
		})
	}
}

func TestUnknownAgentQuotesControlCharacters(t *testing.T) {
	got := run(t, "init", "not-an-agent\x1b[31m")
	if got.code != 2 || strings.Contains(got.stderr, "\x1b") || !strings.Contains(got.stderr, `\x1b`) {
		t.Fatalf("%d %q", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "unknown agent") {
		t.Fatalf("want unknown agent, got %q", got.stderr)
	}
}

// Printable typos stay unquoted, matching the existing messages and
// ledger.Safe (which only quotes when a control character is present).
func TestUnrecognizedArgumentsLeavePrintableTextUnquoted(t *testing.T) {
	got := run(t, "init", "--nope")
	if got.code != 2 || !strings.Contains(got.stderr, "unrecognized arguments: --nope") {
		t.Fatalf("%d %q", got.code, got.stderr)
	}
	if strings.Contains(got.stderr, `"--nope"`) {
		t.Fatalf("printable typo was quoted: %q", got.stderr)
	}
}
