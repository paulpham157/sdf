package cli_test

import (
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func busyPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func TestServeNamesTheBusyPortAndItsFlag(t *testing.T) {
	busy := strconv.Itoa(busyPort(t))
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"http", []string{"--port", busy, "--grpc-port", "0", "--no-ui"}, []string{"OTLP/HTTP port " + busy, "already in use", "--port PORT"}},
		{"grpc", []string{"--port", "0", "--grpc-port", busy, "--no-ui"}, []string{"OTLP/gRPC port " + busy, "already in use", "--grpc-port PORT"}},
		{"ui", []string{"--port", "0", "--grpc-port", "0", "--ui-addr", "127.0.0.1:" + busy}, []string{"UI port " + busy, "already in use", "--ui-addr", "--no-ui"}},
	}
	for _, c := range cases {
		args := append([]string{"serve", "--db", filepath.Join(t.TempDir(), "t.sqlite")}, c.args...)
		out := run(t, args...)
		if out.code != 1 {
			t.Fatalf("%s: %d %q", c.name, out.code, out.stderr)
		}
		for _, w := range c.want {
			if !strings.Contains(out.stderr, w) {
				t.Fatalf("%s: lacks %q: %q", c.name, w, out.stderr)
			}
		}
	}
}
