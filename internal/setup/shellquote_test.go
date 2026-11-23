package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var injections = []string{
	"127.0.0.1;touch PWNED",
	"127.0.0.1 && touch PWNED",
	"$(touch PWNED)",
	"`touch PWNED`",
	"127.0.0.1'; touch PWNED; '",
	`127.0.0.1"; touch PWNED; "`,
	"127.0.0.1\ntouch PWNED",
	"a b",
	"127.0.0.1/../x",
	"-oProxyCommand=x",
	"exa_mple.com",
	"[127.0.0.1]",
	"[::1",
	"fe80::1%eth0",
}

func TestCheckHost(t *testing.T) {
	for host, want := range map[string]string{
		"127.0.0.1":     "127.0.0.1",
		"localhost":     "localhost",
		"collector.lan": "collector.lan",
		"otel-1.corp.x": "otel-1.corp.x",
		"::1":           "::1",
		"[::1]":         "::1",
		"[fd00::2:3]":   "fd00::2:3",
	} {
		got, err := checkHost(host)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", host, got, err)
		}
	}
	for _, host := range append(injections, "", "999.1.1.1", "127.1", "1.2.3.4.5", "256.0.0.1") {
		if _, err := checkHost(host); err == nil {
			t.Fatalf("%q accepted", host)
		}
	}
}

func TestInitRefusesUnsafeHostOrPortForEveryAgent(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex", "opencode", "pi"} {
		for _, host := range injections {
			home := t.TempDir()
			req := Request{Agent: agent, Home: home, ProjectDir: home, CodexHome: filepath.Join(home, ".codex"), Write: true, Endpoint: Endpoint{Host: host, HTTPPort: 4318}}
			if _, err := Run(req); err == nil || !strings.Contains(err.Error(), "--host") {
				t.Fatalf("%s %q: %v", agent, host, err)
			}
			if _, err := Configured(req); err == nil {
				t.Fatalf("--check %s %q accepted", agent, host)
			}
			entries, _ := os.ReadDir(home)
			if len(entries) != 0 {
				t.Fatalf("%s %q wrote %v", agent, host, entries)
			}
		}
		for _, ep := range []Endpoint{{Host: "127.0.0.1", HTTPPort: -1}, {Host: "127.0.0.1", HTTPPort: 65536}, {Host: "127.0.0.1", HTTPPort: 4318, GRPCPort: 70000}} {
			home := t.TempDir()
			if _, err := Run(Request{Agent: agent, Home: home, ProjectDir: home, CodexHome: filepath.Join(home, ".codex"), Write: true, Endpoint: ep}); err == nil || !strings.Contains(err.Error(), "port") {
				t.Fatalf("%s %+v: %v", agent, ep, err)
			}
		}
	}
}

func TestShellQuoteRoundTripsInSh(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	for _, value := range append(injections, `it's "x" \n $HOME`, "") {
		value = strings.ReplaceAll(value, "PWNED", filepath.Join(dir, "PWNED"))
		file := filepath.Join(dir, "env")
		if err := os.WriteFile(file, []byte("export SDF_TEST_VALUE="+shQuote(value)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(sh, "-c", `. "$1" && printf %s "$SDF_TEST_VALUE"`, "sh", file).Output()
		if err != nil || string(out) != value {
			t.Fatalf("%q: got %q %v", value, out, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "PWNED")); !os.IsNotExist(err) {
		t.Fatal("sourcing ran a command")
	}
}

func TestOpenCodeSnippetSourcesForValidHosts(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for host, want := range map[string]string{
		"127.0.0.1":     "http://127.0.0.1:5318",
		"collector.lan": "http://collector.lan:5318",
		"[::1]":         "http://[::1]:5318",
	} {
		home := t.TempDir()
		if _, err := Run(Request{Agent: "opencode", Home: home, Write: true, Endpoint: Endpoint{Host: host, HTTPPort: 5318}}); err != nil {
			t.Fatal(host, err)
		}
		out, err := exec.Command(sh, "-c", `. "$1" && printf %s "$OTEL_EXPORTER_OTLP_ENDPOINT"`, "sh", filepath.Join(home, ".config", "sdf", "opencode.env")).Output()
		if err != nil || string(out) != want {
			t.Fatalf("%s: %q %v", host, out, err)
		}
	}
}

// The session hook command quotes its URL too, so even a value that skipped
// checkEndpoint reaches curl as one argument and runs nothing.
func TestSessionHookCommandQuotesTheURL(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "PWNED")
	args := filepath.Join(dir, "args")
	stub := filepath.Join(dir, "curl")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nfor a; do printf '%s\\n' \"$a\"; done > \""+args+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	url := "http://127.0.0.1:4318/v1/sessions; touch " + marker + " $(touch " + marker + ")"
	cmd := exec.Command(sh, "-c", sessionHookCommand("claude-code", "open", url))
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	cmd.Stdin = strings.NewReader(`{"session_id":"s1","source":"startup"}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("hook command ran an injected command")
	}
	got, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if lines[len(lines)-1] != url {
		t.Fatalf("curl got %q", lines)
	}
}
