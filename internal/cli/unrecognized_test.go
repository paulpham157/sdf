package cli

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactSecretFlag(t *testing.T) {
	const secret = "s3cret-value"
	cases := map[string]string{
		"--otlp-token=" + secret:      "--otlp-token=[redacted]",
		"--OTLP-TOKEN=" + secret:      "--OTLP-TOKEN=[redacted]",
		"--api-secret=" + secret:      "--api-secret=[redacted]",
		"--password=" + secret:        "--password=[redacted]",
		"--passwd=" + secret:          "--passwd=[redacted]",
		"--api-key=" + secret:         "--api-key=[redacted]",
		"--key=" + secret:             "--key=[redacted]",
		"--otlp-token-file=" + secret: "--otlp-token-file=[redacted]",
		"-token=" + secret:            "-token=[redacted]",
		"--otlp-token=":               "--otlp-token=[redacted]",
		"--db=" + secret:              "--db=" + secret,
		"--tokenizer=" + secret:       "--tokenizer=" + secret,
		"--secretive=" + secret:       "--secretive=" + secret,
		"--otlp-token":                "--otlp-token",
		"plain":                       "plain",
	}
	for in, want := range cases {
		if got := redactSecretFlag(in); got != want {
			t.Errorf("redactSecretFlag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateDisplayDoesNotSplitEscapeOrRune(t *testing.T) {
	quoted := strconv.QuoteToASCII("\x1b[31m")
	// Cut inside \x1b, which starts after the opening quote.
	got := truncateDisplay(quoted, 4)
	if strings.Contains(got, `\x`) && !strings.Contains(got, `\x1b`) {
		t.Fatalf("split escape: %q from %q", got, quoted)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("want ellipsis, got %q", got)
	}
	// Unquoted emoji: do not split the rune.
	emoji := "café😀end"
	cut := 6 // mid-emoji (café is 5 bytes, emoji starts at 5)
	if cut >= len(emoji) || utf8.RuneStart(emoji[cut]) {
		// find a mid-rune index
		for i := 1; i < len(emoji); i++ {
			if !utf8.RuneStart(emoji[i]) {
				cut = i
				break
			}
		}
	}
	got = truncateDisplay(emoji, cut)
	prefix := strings.TrimSuffix(got, "...")
	if prefix != "" && !utf8.ValidString(prefix) {
		t.Fatalf("split rune: %q", got)
	}
	if len(prefix) > cut {
		t.Fatalf("prefix longer than cap: %d > %d, %q", len(prefix), cut, got)
	}
	// A short string is unchanged.
	if got := truncateDisplay("ok", 256); got != "ok" {
		t.Fatalf("%q", got)
	}
}

func TestQuoteArgQuotesNBSPAndLeavesEmoji(t *testing.T) {
	nbsp := "--x\u00a0"
	got := quoteArg(nbsp)
	back, err := strconv.Unquote(got)
	if err != nil || back != nbsp {
		t.Fatalf("nbsp: %q %v", got, err)
	}
	if got := quoteArg("--x😀"); got != "--x😀" {
		t.Fatalf("emoji was quoted: %q", got)
	}
}

func TestDisplayArgRedactsBeforeQuoteAndTruncate(t *testing.T) {
	secret := strings.Repeat("S", 512) + "\x1b[31m"
	got := displayArg("--otlp-token=" + secret)
	if strings.Contains(got, "S") || strings.Contains(got, "\x1b") {
		t.Fatalf("secret or ESC leaked: %q", got)
	}
	if !strings.Contains(got, "--otlp-token=[redacted]") {
		t.Fatalf("want redacted, got %q", got)
	}
}
