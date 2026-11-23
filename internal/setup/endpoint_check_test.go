package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckEndpointRefusesPortZero(t *testing.T) {
	for _, ep := range []Endpoint{
		{Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 4317},
		{Host: "127.0.0.1", HTTPPort: 4318, GRPCPort: 0},
	} {
		if _, err := CheckEndpoint(ep); err == nil || !strings.Contains(err.Error(), "is not in 1-65535") {
			t.Fatalf("%+v: %v", ep, err)
		}
	}
	if ep, err := CheckEndpoint(Endpoint{Host: "[::1]", HTTPPort: 4318, GRPCPort: 4317}); err != nil || ep.Host != "::1" {
		t.Fatalf("%+v %v", ep, err)
	}
}

func TestCheckHostFollowups(t *testing.T) {
	for host, want := range map[string]string{
		"collector.lan.": "collector.lan.",
		"localhost.":     "localhost.",
		"::":             "::",
		"::1":            "::1",
		"10.0.0.1":       "10.0.0.1",
		"1password.lan":  "1password.lan",
	} {
		if got, err := checkHost(host); err != nil || got != want {
			t.Fatalf("%q: %q %v", host, got, err)
		}
	}
	for host, msg := range map[string]string{
		"127.0.0.1:4318":   "must not include a port",
		"localhost:4318":   "must not include a port",
		"[::1]:4318":       "must not include a port",
		"::127.0.0.1":      "IPv4-compatible",
		"[::10.1.2.3]":     "IPv4-compatible",
		"::ffff:127.0.0.1": "not a valid IPv6 address",
		"999.1.1.1":        "all numbers",
		"127.1":            "all numbers",
		"1.2.3.4.5":        "all numbers",
		"127.0.0.1.":       "all numbers",
		"a..b":             "not a host name",
		".":                "not a host name",
	} {
		if _, err := checkHost(host); err == nil || !strings.Contains(err.Error(), msg) {
			t.Fatalf("%q: %v, want %q", host, err, msg)
		}
	}
}

func TestProbeDoesNotEchoInvalidHostRaw(t *testing.T) {
	for _, host := range []string{"\n", "evil\x1b[2J", "a\rb"} {
		got := Probe(Endpoint{Host: host, HTTPPort: 4318, GRPCPort: 4317})
		if got.Reachable || !strings.HasPrefix(got.Text, "not probed: ") {
			t.Fatalf("%q: %+v", host, got)
		}
		body := strings.TrimSuffix(got.Text, "\n")
		if strings.ContainsAny(body, "\n\r\x1b") {
			t.Fatalf("%q echoed raw: %q", host, got.Text)
		}
	}
}

func TestClaudeAndPiExportLinesAreQuoted(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for agent, wants := range map[string]map[string]string{
		"claude-code": {"CLAUDE_CODE_ENABLE_TELEMETRY": "1", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://[::1]:4318/v1/traces", "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf"},
		"pi":          {"OTEL_EXPORTER_OTLP_ENDPOINT": "http://[::1]:4318", "OTEL_EXPORTER_OTLP_PROTOCOL": "http/json", "PI_OTEL_SPAN_NAMING": "genai"},
	} {
		home := t.TempDir()
		res, err := Run(Request{Agent: agent, Home: home, ProjectDir: home, Endpoint: Endpoint{Host: "[::1]", HTTPPort: 4318, GRPCPort: 4317}})
		if err != nil {
			t.Fatal(agent, err)
		}
		var exports []string
		for _, line := range strings.Split(res.Text, "\n") {
			if strings.HasPrefix(line, "export ") {
				if !strings.Contains(line, "='") || !strings.HasSuffix(line, "'") {
					t.Fatalf("%s: unquoted %q", agent, line)
				}
				exports = append(exports, line)
			}
		}
		script := strings.Join(exports, "\n") + "\nenv\n"
		out, err := exec.Command(sh, "-c", script).Output()
		if err != nil {
			t.Fatal(agent, err)
		}
		for key, want := range wants {
			if !strings.Contains(string(out), key+"="+want+"\n") {
				t.Fatalf("%s: %s is not %q after sourcing\n%s", agent, key, want, script)
			}
		}
	}
}

func TestGitHookAcceptsBracketedLoopback(t *testing.T) {
	for host, want := range map[string]string{
		"[::1]":      "http://[::1]:4318/v1/logs",
		"::1":        "http://[::1]:4318/v1/logs",
		"localhost.": "http://127.0.0.1:4318/v1/logs",
		"":           "http://127.0.0.1:4318/v1/logs",
	} {
		got, err := hookLogsURL(Endpoint{Host: host, HTTPPort: 4318})
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", host, got, err)
		}
	}
	for _, host := range []string{"collector.lan", "10.0.0.1", "[127.0.0.1]", "evil\n"} {
		if _, err := hookLogsURL(Endpoint{Host: host, HTTPPort: 4318}); err == nil {
			t.Fatalf("%q accepted", host)
		}
	}
	repo := t.TempDir()
	if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
		t.Skip("no git")
	}
	if _, err := InstallGitHooks(repo, Endpoint{Host: "[::1]", HTTPPort: 4318, GRPCPort: 4317}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(repo, ".git", "hooks", "post-commit"))
	if err != nil || !strings.Contains(string(body), "http://[::1]:4318/v1/logs") {
		t.Fatal(err, string(body))
	}
}
