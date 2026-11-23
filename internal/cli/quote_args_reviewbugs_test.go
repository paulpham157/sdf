//go:build reviewbugs

package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sdf decision graph (added on main by #279) still echoes an unknown flag
// raw after #299 merges. On the PR head the subcommand does not exist yet,
// so this also fails there ("unrecognized arguments: graph").
func TestDecisionGraphQuotesUnrecognizedArgument(t *testing.T) {
	got := run(t, "decision", "graph", "--bogus\x1b[31m")
	if got.code != 2 || strings.Contains(got.stderr, "\x1b") || !strings.Contains(got.stderr, `"--bogus\x1b[31m"`) {
		t.Fatalf("%d %q", got.code, got.stderr)
	}
}

// Flag values that name a file are echoed raw in error messages. Not
// "unrecognized arguments", but the same terminal-escape class the
// CHANGELOG entry describes.
func TestFlagValuePathsAreQuoted(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "x\x1b[31m")
	_ = os.Remove(bad)
	for _, args := range [][]string{
		{"summary", "--budget=" + bad},
		{"summary", "--prices=" + bad},
		{"check", "--cases=" + bad},
		{"redact", "--db=" + bad},
		{"serve", "--otlp-token-file=" + bad},
	} {
		t.Run(args[0]+" "+strings.SplitN(args[1], "=", 2)[0], func(t *testing.T) {
			got := run(t, args...)
			if strings.Contains(got.stderr, "\x1b") || strings.Contains(got.stdout, "\x1b") {
				t.Fatalf("raw ESC: %d %q", got.code, got.stderr)
			}
		})
	}
}

// A mistyped secret-bearing flag is echoed verbatim to stderr (and so to
// CI logs).
func TestUnrecognizedTokenFlagValueIsNotEchoed(t *testing.T) {
	const secret = "review-fake-token-value"
	got := run(t, "summary", "--otlp-token="+secret)
	if got.code != 2 || strings.Contains(got.stderr, secret) {
		t.Fatalf("%d %q", got.code, got.stderr)
	}
}
