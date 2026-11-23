package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveServeOTLPTokenFile(t *testing.T) {
	token := "test-otlp-token"
	env := func(m map[string]string) func(string) string {
		return func(key string) string { return m[key] }
	}

	t.Run("file trims surrounding whitespace", func(t *testing.T) {
		path := writeTokenFile(t, " \t"+token+"\n", 0o600)
		var stderr bytes.Buffer
		got, code := resolveServeOTLPToken("", false, path, true, env(nil), &stderr)
		if code != 0 || got != token {
			t.Fatalf("code %d token %q stderr %q", code, got, stderr.String())
		}
		if strings.Contains(stderr.String(), token) {
			t.Fatalf("token in stderr: %q", stderr.String())
		}
	})

	t.Run("env file", func(t *testing.T) {
		path := writeTokenFile(t, token+" \t\r\n", 0o600)
		var stderr bytes.Buffer
		got, code := resolveServeOTLPToken("", false, "", false, env(map[string]string{"SDF_OTLP_TOKEN_FILE": path}), &stderr)
		if code != 0 || got != token {
			t.Fatalf("code %d token %q stderr %q", code, got, stderr.String())
		}
	})

	t.Run("empty file is an error", func(t *testing.T) {
		path := writeTokenFile(t, "\n\t  \n", 0o600)
		var stderr bytes.Buffer
		got, code := resolveServeOTLPToken("", false, path, true, env(nil), &stderr)
		if code != 1 || got != "" || !strings.Contains(stderr.String(), "empty") {
			t.Fatalf("code %d token %q stderr %q", code, got, stderr.String())
		}
		if strings.Contains(stderr.String(), "\n\t") {
			t.Fatalf("file contents in stderr: %q", stderr.String())
		}
	})

	t.Run("unreadable file is an error", func(t *testing.T) {
		path := writeTokenFile(t, token, 0o000)
		var stderr bytes.Buffer
		got, code := resolveServeOTLPToken("", false, path, true, env(nil), &stderr)
		if code != 1 || got != "" || !strings.Contains(stderr.String(), "could not read") || !strings.Contains(stderr.String(), path) {
			t.Fatalf("code %d token %q stderr %q", code, got, stderr.String())
		}
		if strings.Contains(stderr.String(), token) {
			t.Fatalf("token in stderr: %q", stderr.String())
		}
	})

	t.Run("missing file is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing-token")
		var stderr bytes.Buffer
		got, code := resolveServeOTLPToken("", false, path, true, env(nil), &stderr)
		if code != 1 || got != "" || !strings.Contains(stderr.String(), "could not read") || !strings.Contains(stderr.String(), path) {
			t.Fatalf("code %d token %q stderr %q", code, got, stderr.String())
		}
	})

	t.Run("both flags are mutually exclusive", func(t *testing.T) {
		var stderr bytes.Buffer
		got, code := resolveServeOTLPToken(token, true, "token.txt", true, env(nil), &stderr)
		if code != 2 || got != "" || !strings.Contains(stderr.String(), "mutually exclusive") {
			t.Fatalf("code %d token %q stderr %q", code, got, stderr.String())
		}
		if strings.Contains(stderr.String(), token) {
			t.Fatalf("token in stderr: %q", stderr.String())
		}
	})

	t.Run("both env vars are mutually exclusive", func(t *testing.T) {
		path := writeTokenFile(t, token, 0o600)
		var stderr bytes.Buffer
		got, code := resolveServeOTLPToken("", false, "", false, env(map[string]string{
			"SDF_OTLP_TOKEN":      "env-otlp-token",
			"SDF_OTLP_TOKEN_FILE": path,
		}), &stderr)
		if code != 2 || got != "" || !strings.Contains(stderr.String(), "mutually exclusive") {
			t.Fatalf("code %d token %q stderr %q", code, got, stderr.String())
		}
		if strings.Contains(stderr.String(), token) || strings.Contains(stderr.String(), "env-otlp-token") {
			t.Fatalf("token in stderr: %q", stderr.String())
		}
	})

	t.Run("token flag beats env file", func(t *testing.T) {
		path := writeTokenFile(t, "file-otlp-token", 0o600)
		var stderr bytes.Buffer
		got, code := resolveServeOTLPToken(token, true, "", false, env(map[string]string{"SDF_OTLP_TOKEN_FILE": path}), &stderr)
		if code != 0 || got != token {
			t.Fatalf("code %d token %q stderr %q", code, got, stderr.String())
		}
	})

	t.Run("file flag beats env token", func(t *testing.T) {
		path := writeTokenFile(t, token, 0o600)
		var stderr bytes.Buffer
		got, code := resolveServeOTLPToken("", false, path, true, env(map[string]string{"SDF_OTLP_TOKEN": "env-otlp-token"}), &stderr)
		if code != 0 || got != token {
			t.Fatalf("code %d token %q stderr %q", code, got, stderr.String())
		}
	})

	t.Run("empty file flag path is a usage error", func(t *testing.T) {
		var stderr bytes.Buffer
		got, code := resolveServeOTLPToken("", false, "  ", true, env(nil), &stderr)
		if code != 2 || got != "" || !strings.Contains(stderr.String(), "--otlp-token-file") {
			t.Fatalf("code %d token %q stderr %q", code, got, stderr.String())
		}
	})
}

func TestTokenFileModeWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode warning is Unix-only")
	}
	open := writeTokenFile(t, "test-otlp-token", 0o644)
	if !strings.Contains(tokenFileModeWarning(open), "group- or world-readable") {
		t.Fatalf("expected warning for 0644: %q", tokenFileModeWarning(open))
	}
	if strings.Contains(tokenFileModeWarning(open), "test-otlp-token") {
		t.Fatal("token in warning")
	}
	closed := writeTokenFile(t, "test-otlp-token", 0o600)
	if tokenFileModeWarning(closed) != "" {
		t.Fatalf("unexpected warning for 0600: %q", tokenFileModeWarning(closed))
	}
}

func writeTokenFile(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "otlp-token")
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
