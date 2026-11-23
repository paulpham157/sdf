package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Review tests for PR #299: every class of terminal-unsafe input reaches
// stderr quoted, the quoted form round-trips to the original argument
// (except a value longer than the display cap), and exit codes stay 2.
func TestUnrecognizedArgumentsQuoteEveryUnsafeClass(t *testing.T) {
	cases := map[string]string{
		"csi":        "--x\x1b[31m",
		"osc":        "--x\x1b]0;title\x07",
		"bel":        "--x\x07",
		"cr":         "--x\rforged",
		"lf":         "--x\nforged: line",
		"tab":        "--x\ty",
		"backspace":  "--x\b",
		"del":        "--x\x7f",
		"c1 csi":     "--x\u009b31m",
		"raw c1":     "--x\x9b31m",
		"bad utf8":   "--x\xff\xfe",
		"rlo":        "--x\u202eabc",
		"lri":        "--x\u2066abc",
		"zwsp":       "--x\u200b",
		"zwj":        "--x\u200d",
		"bom":        "--x\ufeff",
		"nbsp":       "--x\u00a0",
		"line sep":   "--x\u2028",
		"nul":        "--x\x00",
		"long + esc": "--" + strings.Repeat("a", 8192) + "\x1b[2J",
	}
	for name, arg := range cases {
		t.Run(name, func(t *testing.T) {
			got := run(t, "decision", "list", arg)
			if got.code != 2 {
				t.Fatalf("code %d, want 2; stderr %q", got.code, got.stderr)
			}
			const prefix = "unrecognized arguments: "
			if !strings.HasPrefix(got.stderr, prefix) || !strings.HasSuffix(got.stderr, "\n") || strings.Count(got.stderr, "\n") != 1 {
				t.Fatalf("want one %q line, got %q", prefix, got.stderr)
			}
			quoted := strings.TrimSuffix(strings.TrimPrefix(got.stderr, prefix), "\n")
			for _, r := range quoted {
				if r < 0x20 || r >= 0x7f {
					t.Fatalf("non-printable-ASCII rune %U on stderr: %q", r, got.stderr)
				}
			}
			if got.stdout != "" {
				t.Fatalf("stdout %q", got.stdout)
			}
			if name == "long + esc" {
				// After quoting, display is capped at 256 bytes plus "...".
				if !strings.HasSuffix(quoted, "...") || len(quoted) > 256+len("...") {
					t.Fatalf("want truncated display, got len %d %q", len(quoted), quoted)
				}
				if strings.Contains(got.stderr, "\x1b") {
					t.Fatalf("raw ESC on truncated stderr: %q", got.stderr)
				}
				return
			}
			back, err := strconv.Unquote(quoted)
			if err != nil || back != arg {
				t.Fatalf("quoted form %s does not round-trip: %q, %v", quoted, back, err)
			}
		})
	}
}

// Printable non-ASCII text is not quoted, so existing messages are
// unchanged for normal typos.
func TestUnrecognizedArgumentsLeavePrintableUnicodeUnquoted(t *testing.T) {
	for _, arg := range []string{"--café", "--x😀", `--"x"`, "extra"} {
		got := run(t, "decision", "list", arg)
		if got.code != 2 || got.stderr != "unrecognized arguments: "+arg+"\n" {
			t.Fatalf("%q: %d %q", arg, got.code, got.stderr)
		}
	}
}

// Every "unrecognized arguments" message in the cli package goes through
// unrecognizedArgs, so a new subcommand cannot reintroduce a raw echo.
func TestNoRawUnrecognizedArgumentsMessages(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`"unrecognized arguments: `)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "unrecognized.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if re.MatchString(line) {
				t.Errorf("%s:%d prints unrecognized arguments without unrecognizedArgs: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

func TestDecisionGraphQuotesUnrecognizedArgument(t *testing.T) {
	got := run(t, "decision", "graph", "--bogus\x1b[31m")
	if got.code != 2 || strings.Contains(got.stderr, "\x1b") || !strings.Contains(got.stderr, `"--bogus\x1b[31m"`) {
		t.Fatalf("%d %q", got.code, got.stderr)
	}
}

// Flag values that name a file are echoed through ledger.Safe in error
// messages. Not "unrecognized arguments", but the same terminal-escape
// class the CHANGELOG entry describes.
func TestFlagValuePathsAreQuoted(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "x\x1b[31m")
	_ = os.Remove(bad)
	for _, args := range [][]string{
		{"summary", "--budget=" + bad},
		{"summary", "--prices=" + bad},
		{"summary", "--db=" + bad},
		{"check", "--cases=" + bad},
		{"redact", "--db=" + bad},
		{"serve", "--otlp-token-file=" + bad},
		{"task", "declare", "--repo=" + bad, "--id", "t", "--title", "t", "--check", "c=true"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			got := run(t, args...)
			if strings.Contains(got.stderr, "\x1b") || strings.Contains(got.stdout, "\x1b") {
				t.Fatalf("raw ESC: %d %q", got.code, got.stderr)
			}
		})
	}
}

// A mistyped secret-bearing flag is not echoed verbatim to stderr (or CI logs).
func TestUnrecognizedTokenFlagValueIsNotEchoed(t *testing.T) {
	const secret = "review-fake-token-value"
	got := run(t, "summary", "--otlp-token="+secret)
	if got.code != 2 || strings.Contains(got.stderr, secret) {
		t.Fatalf("%d %q", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "--otlp-token=[redacted]") {
		t.Fatalf("want redacted flag, got %q", got.stderr)
	}
}
