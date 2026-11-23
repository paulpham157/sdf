package cli_test

import (
	"strings"
	"testing"
)

func TestInitValidatesEndpointBeforeProbing(t *testing.T) {
	for _, args := range [][]string{
		{"init", "--check", "--host", "\n"},
		{"init", "--check", "--host", "evil\x1b[2J\x1b]0;pwned\x07"},
		{"init", "--check", "--host=a\rb", "claude-code"},
		{"init", "--host", "x\n", "--git-hooks"},
	} {
		got := run(t, args...)
		if got.code != 2 || got.stdout != "" {
			t.Fatalf("%q: code %d stdout %q", args, got.code, got.stdout)
		}
		msg := strings.TrimSuffix(got.stderr, "\n")
		if !strings.Contains(msg, "--host") || strings.ContainsAny(msg, "\n\r\x1b\x07") {
			t.Fatalf("%q: stderr not escaped: %q", args, got.stderr)
		}
	}
}

func TestInitRefusesPortZeroAndHostWithPort(t *testing.T) {
	for args, want := range map[string]string{
		"--port 0":              "--port 0 is not in 1-65535",
		"--grpc-port 0":         "--grpc-port 0 is not in 1-65535",
		"--host 127.0.0.1:4318": "must not include a port",
	} {
		full := append([]string{"init"}, strings.Fields(args)...)
		full = append(full, "claude-code")
		got := run(t, full...)
		if got.code != 2 || got.stdout != "" || !strings.Contains(got.stderr, want) {
			t.Fatalf("%s: %d %q %q", args, got.code, got.stdout, got.stderr)
		}
	}
}

func TestInitQuotesUnrecognizedArgument(t *testing.T) {
	got := run(t, "init", "--bogus\x1b[31m")
	if got.code != 2 || strings.Contains(got.stderr, "\x1b") || !strings.Contains(got.stderr, `\x1b`) {
		t.Fatalf("%d %q", got.code, got.stderr)
	}
}
