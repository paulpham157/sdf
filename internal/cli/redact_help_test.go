package cli_test

import (
	"strings"
	"testing"
)

// sdf --help and sdf help redact must list redact. #216 replaces the usage
// line with its own command table (internal/cli/help.go commands); merged
// with this PR, that table needs {"redact", ...} or this test fails.
func TestHelpListsRedact(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"help", "redact"}} {
		out := run(t, args...)
		if out.code != 0 || !strings.Contains(out.stdout, "redact") {
			t.Fatalf("%v: %d %s%s", args, out.code, out.stdout, out.stderr)
		}
	}
	out := run(t, "redact", "--help")
	if out.code != 0 || !strings.Contains(out.stdout, "--dry-run") || !strings.Contains(out.stdout, "twice the file's size") {
		t.Fatalf("%d %s", out.code, out.stdout)
	}
}
