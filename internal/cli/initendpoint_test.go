package cli_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// --check validates --host and the ports before it probes anything: a host
// sdf init would refuse to write is not dialled or fetched either.
func TestInitCheckValidatesEndpointBeforeProbe(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "--check", "--host", "a b"},
		{"init", "--check", "--host", "127.0.0.1/../x"},
		{"init", "--check", "--host", host + ":" + port, "--port", port},
		{"init", "--check", "--host", host, "--port", port, "--grpc-port", "0"},
		{"init", "--check", "--host", host, "--port", "0"},
	} {
		out := run(t, args...)
		if out.code != 2 || out.stdout != "" || !strings.Contains(out.stderr, "sdf init: --") {
			t.Fatalf("%v: %d %q %q", args, out.code, out.stdout, out.stderr)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("probed %d times", n)
	}
}

func TestInitRejectsPortZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, args := range [][]string{
		{"init", "--port", "0", "claude-code"},
		{"init", "--port=0", "claude-code", "--write"},
		{"init", "--grpc-port", "0", "codex"},
	} {
		out := run(t, args...)
		if out.code != 2 || !strings.Contains(out.stderr, "port 0 is not in 1-65535") || out.stdout != "" {
			t.Fatalf("%v: %d %q %q", args, out.code, out.stdout, out.stderr)
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("wrote %v", entries)
	}
}

func TestInitHostAsHostPortHasAClearError(t *testing.T) {
	for _, host := range []string{"127.0.0.1:4318", "localhost:4318", "[::1]:4318"} {
		out := run(t, "init", "--host", host, "claude-code")
		if out.code != 2 || !strings.Contains(out.stderr, "looks like host:port") || !strings.Contains(out.stderr, "--port 4318") {
			t.Fatalf("%q: %d %q", host, out.code, out.stderr)
		}
	}
}

// --git-hooks accepts the bracketed IPv6 loopback, as every agent does.
func TestGitHooksAcceptBracketedIPv6Loopback(t *testing.T) {
	isolateAgentGit(t)
	dir := hookRepo(t)
	chdir(t, dir)
	out := run(t, "init", "--git-hooks", "--host", "[::1]")
	if out.code != 0 || !strings.Contains(out.stdout, "installed sdf git hooks") {
		t.Fatalf("%d %s %s", out.code, out.stdout, out.stderr)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".git", "hooks", "post-commit"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "http://[::1]:4318/v1/logs") {
		t.Fatalf("hook does not post to [::1]:\n%s", body)
	}
}
