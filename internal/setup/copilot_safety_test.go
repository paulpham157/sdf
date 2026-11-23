package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopilotRejectsUnsafeHostAndPort(t *testing.T) {
	clearCopilotEnv(t)
	for _, host := range []string{
		"127.0.0.1;touch /tmp/sdf-pwned",
		"$(touch /tmp/sdf-pwned)",
		"`id`",
		"a b",
		"h'x",
		`h"x`,
		"host\nexport X=1",
		"-bad.example",
		"[127.0.0.1]",
		"[::1",
		"exa_mple.com",
		"999.1.1.1",
		"127.1",
		"1.2.3.4.5",
		"256.0.0.1",
		"",
	} {
		home := t.TempDir()
		req := copilotRequest(home, true)
		req.Endpoint.Host = host
		if host == "" {
			// normalize fills an empty host; call the validator directly.
			if _, err := copilotEndpoint(Endpoint{Host: "", HTTPPort: 4318}); err == nil {
				t.Fatal("empty host accepted")
			}
			continue
		}
		if _, err := Run(req); err == nil || !strings.Contains(err.Error(), "--host") {
			t.Fatalf("host %q: %v", host, err)
		}
		if _, err := os.Stat(copilotEnvPath(home)); !os.IsNotExist(err) {
			t.Fatalf("host %q wrote the snippet", host)
		}
	}
	for _, port := range []int{-1, 65536, 100000} {
		req := copilotRequest(t.TempDir(), true)
		req.Endpoint.HTTPPort = port
		if _, err := Run(req); err == nil || !strings.Contains(err.Error(), "--port") {
			t.Fatalf("port %d: %v", port, err)
		}
	}
}

func TestCopilotAcceptsHostNamesAndIPs(t *testing.T) {
	clearCopilotEnv(t)
	for host, want := range map[string]string{
		"localhost":        "http://localhost:4318",
		"collector.local":  "http://collector.local:4318",
		"10.0.0.7":         "http://10.0.0.7:4318",
		"[::1]":            "http://[::1]:4318",
		"::1":              "http://[::1]:4318",
		"[fd00::1:2]":      "http://[fd00::1:2]:4318",
		"otel-1.corp.test": "http://otel-1.corp.test:4318",
	} {
		req := copilotRequest(t.TempDir(), false)
		req.Endpoint.Host = host
		res, err := Run(req)
		if err != nil || !strings.Contains(res.Text, "OTEL_EXPORTER_OTLP_ENDPOINT='"+want+"'") {
			t.Fatalf("host %q: %v\n%s", host, err, res.Text)
		}
	}
}

func TestShellQuoteSurvivesASourcingShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	value := `it's $(touch ` + marker + `); ` + "`touch " + marker + "`" + ` "x" \n`
	file := filepath.Join(dir, "env")
	if err := os.WriteFile(file, []byte(copilotExport("SDF_TEST_VALUE", value)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(sh, "-c", `. "$1" && printf %s "$SDF_TEST_VALUE"`, "sh", file).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != value {
		t.Fatalf("got %q, want %q", out, value)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("sourcing the snippet ran a command")
	}
}

func TestCopilotWrittenSnippetSourcesCleanly(t *testing.T) {
	clearCopilotEnv(t)
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	home := t.TempDir()
	if _, err := Run(copilotRequest(home, true)); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(sh, "-c", `. "$1" && printf '%s|%s|%s' "$OTEL_EXPORTER_OTLP_ENDPOINT" "$OTEL_EXPORTER_OTLP_PROTOCOL" "$OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT"`, "sh", copilotEnvPath(home)).Output()
	if err != nil || string(out) != "http://127.0.0.1:4318|http/json|NO_CONTENT" {
		t.Fatalf("%v %q", err, out)
	}
}

// Mode 0400 is refused by its mode bits, which also holds under root (root
// may open any file for writing). The second test needs a non-root user.
func TestCopilotReadOnlySnippetIsRefused(t *testing.T) {
	clearCopilotEnv(t)
	home := t.TempDir()
	path := copilotEnvPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := "# mine\n"
	if err := os.WriteFile(path, []byte(old), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(copilotRequest(home, true)); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(path); string(body) != old {
		t.Fatal("read-only file changed")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o400 {
		t.Fatal(info.Mode())
	}
	if backups, _ := filepath.Glob(path + ".sdf-bak*"); len(backups) != 0 {
		t.Fatal(backups)
	}
}

func TestCopilotUnwritableSnippetIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open any file for writing")
	}
	clearCopilotEnv(t)
	home := t.TempDir()
	path := copilotEnvPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# mine\n"), 0o466); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o466); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(copilotRequest(home, true)); err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatal(err)
	}
}

func TestCopilotHardLinkIsReported(t *testing.T) {
	clearCopilotEnv(t)
	home := t.TempDir()
	path := copilotEnvPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, "linked.env")
	if err := os.Link(path, other); err != nil {
		t.Skip(err)
	}
	res, err := Run(copilotRequest(home, true))
	if err != nil || !strings.Contains(res.Text, "had 2 hard links") {
		t.Fatal(err, res.Text)
	}
	if body, _ := os.ReadFile(other); string(body) != "# mine\n" {
		t.Fatal("other link changed")
	}
}

func TestCopilotCaptureWarnings(t *testing.T) {
	cases := []struct {
		name, file string
		warn       bool
		schemaNote bool
	}{
		{"legacy only is overridden by the NO_CONTENT sdf adds", "export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true\n", false, true},
		{"event only keeps spans clean", "export OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=EVENT_ONLY\n", false, false},
		{"span only", "export OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=SPAN_ONLY\n", true, false},
		{"span and event", "export OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=SPAN_AND_EVENT\n", true, false},
		{"unrecognized disables", "export OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=everything\n", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, warnings := mergeCopilotEnv(tc.file, Endpoint{Host: "127.0.0.1", HTTPPort: 4318})
			joined := strings.Join(warnings, "\n")
			if got := strings.Contains(joined, "content capture is on"); got != tc.warn {
				t.Fatalf("capture warning %t, want %t: %s", got, tc.warn, joined)
			}
			if got := strings.Contains(joined, "tool schemas"); got != tc.schemaNote {
				t.Fatalf("schema note %t, want %t: %s", got, tc.schemaNote, joined)
			}
		})
	}
	clearCopilotEnv(t)
	t.Setenv(copilotLegacyKey, "true")
	if w := strings.Join(copilotEnvWarnings(Endpoint{Host: "127.0.0.1", HTTPPort: 4318}), "\n"); !strings.Contains(w, "content capture is on in this environment") {
		t.Fatal(w)
	}
	t.Setenv(copilotCaptureKey, "EVENT_ONLY")
	if w := strings.Join(copilotEnvWarnings(Endpoint{Host: "127.0.0.1", HTTPPort: 4318}), "\n"); strings.Contains(w, "content capture") {
		t.Fatal(w)
	}
}

func TestCopilotNumericHostsMustBeIPv4(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "10.0.0.255", "localhost", "1password.example", "a1.b2"} {
		if _, err := copilotEndpoint(Endpoint{Host: host, HTTPPort: 4318}); err != nil {
			t.Fatalf("%q refused: %v", host, err)
		}
	}
}

// The printed run line must work when pasted into any shell, so it runs sh
// explicitly instead of a POSIX subshell, which fish does not have.
func TestCopilotRunHintScopesTheEnvironment(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	clearCopilotEnv(t)
	home := t.TempDir()
	res, err := Run(copilotRequest(home, true))
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(res.Text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "sh -c ") {
			line = strings.TrimSpace(l)
		}
	}
	if line == "" {
		t.Fatalf("no run line\n%s", res.Text)
	}
	bin := t.TempDir()
	fake := "#!/bin/sh\nprintf '%s' \"$OTEL_EXPORTER_OTLP_ENDPOINT\"\n"
	if err := os.WriteFile(filepath.Join(bin, "copilot"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	// Run the line as a user's interactive shell would: here sh stands in
	// for bash, zsh, or fish, which all pass the quoted words to sh as is.
	cmd := exec.Command(sh, "-c", line)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.Output()
	if err != nil || string(out) != "http://127.0.0.1:4318" {
		t.Fatalf("%s: %q %v", line, out, err)
	}
	if fish, err := exec.LookPath("fish"); err == nil {
		cmd := exec.Command(fish, "-c", line)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		if out, err := cmd.Output(); err != nil || string(out) != "http://127.0.0.1:4318" {
			t.Fatalf("fish: %q %v", out, err)
		}
	}
}
