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

func TestCheckHostEdgeCases(t *testing.T) {
	for host, want := range map[string]string{
		"example.com.":   "example.com.",
		"otel-1.corp.x.": "otel-1.corp.x.",
		"[::1]":          "::1",
	} {
		got, err := checkHost(host)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", host, got, err)
		}
	}
	for host, msg := range map[string]string{
		".":                  "is not a host name",
		"example.com..":      "is not a host name",
		"127.0.0.1.":         "all numbers",
		"127.0.0.1:4318":     "looks like host:port",
		"[::1]:4318":         "looks like host:port",
		"collector.lan:4318": "looks like host:port",
		"::127.0.0.1":        "IPv4 address written as IPv6; pass --host 127.0.0.1",
		"[::127.0.0.1]":      "IPv4 address written as IPv6",
		"::ffff:127.0.0.1":   "IPv4 address written as IPv6; pass --host 127.0.0.1",
		"::ffff:c000:0280":   "pass --host 192.0.2.128",
		"fe80::1%eth0":       "not a valid IPv6 address",
	} {
		if _, err := checkHost(host); err == nil || !strings.Contains(err.Error(), msg) {
			t.Fatalf("%q: want %q, got %v", host, msg, err)
		}
	}
}

func TestValidateEndpointRefusesPortZero(t *testing.T) {
	for _, ep := range []Endpoint{{Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 4317}, {Host: "127.0.0.1", HTTPPort: 4318, GRPCPort: 0}} {
		if err := ValidateEndpoint(&ep); err == nil || !strings.Contains(err.Error(), "port 0 is not in 1-65535") {
			t.Fatalf("%+v: %v", ep, err)
		}
	}
	ep := Endpoint{Host: "[::1]", HTTPPort: 4318, GRPCPort: 4317}
	if err := ValidateEndpoint(&ep); err != nil || ep.Host != "::1" {
		t.Fatal(ep, err)
	}
}

func TestProbeDoesNotDialAnInvalidHost(t *testing.T) {
	got := Probe(Endpoint{Host: "a b", HTTPPort: 4318, GRPCPort: 4317})
	if got.Reachable || !strings.HasPrefix(got.Text, "not probed: --host") {
		t.Fatalf("%+v", got)
	}
}

func TestGitHookURLAcceptsBracketedLoopback(t *testing.T) {
	for host, want := range map[string]string{
		"[::1]":      "http://[::1]:4318/v1/logs",
		"::1":        "http://[::1]:4318/v1/logs",
		"localhost.": "http://127.0.0.1:4318/v1/logs",
		"LocalHost":  "http://127.0.0.1:4318/v1/logs",
	} {
		got, err := hookLogsURL(Endpoint{Host: host, HTTPPort: 4318})
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", host, got, err)
		}
	}
	for _, host := range []string{"10.0.0.1", "::127.0.0.1", "a b"} {
		if _, err := hookLogsURL(Endpoint{Host: host, HTTPPort: 4318}); err == nil {
			t.Fatalf("%q accepted", host)
		}
	}
}

// localhost. is written as localhost by every writer (agent configs, the
// git hook, the token check), not kept with the dot that sends it to DNS.
func TestLocalhostTrailingDotIsNormalized(t *testing.T) {
	for _, host := range []string{"localhost.", "LOCALHOST.", "Localhost"} {
		got, err := checkHost(host)
		if err != nil || got != "localhost" {
			t.Fatalf("%q: %q %v", host, got, err)
		}
	}
	home := t.TempDir()
	res, err := Run(Request{Agent: "claude-code", Home: home, ProjectDir: home, Endpoint: Endpoint{Host: "localhost."}})
	if err != nil || strings.Contains(res.Text, "localhost.") || !strings.Contains(res.Text, "http://localhost:4318/v1/logs") {
		t.Fatal(err, res.Text)
	}
	if !strings.Contains(res.Text, hookTokenNote) {
		t.Fatal("localhost. should get the loopback token note")
	}
}

func TestNonDecimalIPv4FormsAreRefused(t *testing.T) {
	for _, host := range []string{"0x7f.1", "0x7f.0.0.1", "0x7f000001", "0X7F000001", "0177.0.0.1", "127.000.0.1", "2130706433", "127.1", "0x7f.1.", "foo.0x1", "a.123"} {
		if _, err := checkHost(host); err == nil || !strings.Contains(err.Error(), "dotted-decimal") {
			t.Fatalf("%q: %v", host, err)
		}
	}
	for _, host := range []string{"127.0.0.1", "10.1.2.3", "1password.com", "0xdeadbeef.example.com", "x0x7f"} {
		if _, err := checkHost(host); err != nil {
			t.Fatalf("%q: %v", host, err)
		}
	}
}

func TestHostPortHints(t *testing.T) {
	for host, want := range map[string]string{
		"::1:4318":       "pass --host '[::1]' --port 4318",
		"fe80::1:4318":   "pass --host '[fe80::1]' --port 4318",
		"127.0.0.1:":     "looks like host:port; pass the host alone (--host 127.0.0.1) and the port as a number with --port",
		"127.0.0.1:http": "looks like host:port; pass the host alone (--host 127.0.0.1) and the port as a number with --port",
		"[::1]:4318":     "pass the host alone (--host '[::1]') and the port with --port 4318",
	} {
		_, err := checkHost(host)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: want %q, got %v", host, want, err)
		}
		if strings.Contains(err.Error(), "0.1.67.24") || strings.Contains(err.Error(), "IPv6 address\"") {
			t.Fatalf("%q: wrong hint %v", host, err)
		}
	}
	// The bracketed form is the address it spells, and short last groups
	// are not read as ports.
	for host, want := range map[string]string{"[fe80::1:4318]": "fe80::1:4318", "fd00::2:3": "fd00::2:3", "2001:db8::1": "2001:db8::1"} {
		if got, err := checkHost(host); err != nil || got != want {
			t.Fatalf("%q: %q %v", host, got, err)
		}
	}
}

func TestUnspecifiedHostIsRefused(t *testing.T) {
	for _, host := range []string{"::", "[::]", "0.0.0.0", "0:0:0:0:0:0:0:0"} {
		if _, err := checkHost(host); err == nil || !strings.Contains(err.Error(), "unspecified address") {
			t.Fatalf("%q: %v", host, err)
		}
	}
}
