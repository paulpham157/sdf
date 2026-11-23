package cli_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/store"
)

// startReceiver runs a receiver on free ports and returns its port flags
// for sdf init --check, and the store so a test can see nothing was written.
func startReceiver(t *testing.T, token string) ([]string, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "r.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := &server.Server{Host: "127.0.0.1", Store: db, Token: token}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	_, httpPort, _ := net.SplitHostPort(srv.HTTPAddress())
	_, grpcPort, _ := net.SplitHostPort(srv.GRPCAddress())
	return []string{"--port", httpPort, "--grpc-port", grpcPort}, db
}

func initCheck(t *testing.T, ports []string) result {
	t.Helper()
	return run(t, append([]string{"init", "--check"}, ports...)...)
}

func TestInitCheckReportsAReceiverThatNeedsAToken(t *testing.T) {
	t.Setenv("SDF_OTLP_TOKEN", "")
	t.Setenv("SDF_OTLP_TOKEN_FILE", "")
	ports, db := startReceiver(t, "s3cret")

	out := initCheck(t, ports)
	if out.code != 1 || !strings.Contains(out.stdout, "token required (HTTP 401)") || !strings.Contains(out.stdout, "SDF_OTLP_TOKEN") {
		t.Fatalf("no token: %d %q %q", out.code, out.stdout, out.stderr)
	}

	t.Setenv("SDF_OTLP_TOKEN", "wrong")
	out = initCheck(t, ports)
	if out.code != 1 || !strings.Contains(out.stdout, "token refused (HTTP 401)") || strings.Contains(out.stdout, "wrong") {
		t.Fatalf("wrong token: %d %q", out.code, out.stdout)
	}

	t.Setenv("SDF_OTLP_TOKEN", " s3cret \n")
	out = initCheck(t, ports)
	if out.code != 0 || !strings.Contains(out.stdout, "writes accepted with the token from SDF_OTLP_TOKEN") || strings.Contains(out.stdout, "s3cret") {
		t.Fatalf("right token: %d %q", out.code, out.stdout)
	}

	file := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(file, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDF_OTLP_TOKEN", "")
	t.Setenv("SDF_OTLP_TOKEN_FILE", file)
	out = initCheck(t, ports)
	if out.code != 0 || !strings.Contains(out.stdout, "token from SDF_OTLP_TOKEN_FILE") {
		t.Fatalf("token file: %d %q", out.code, out.stdout)
	}

	t.Setenv("SDF_OTLP_TOKEN", "s3cret")
	out = initCheck(t, ports)
	if out.code != 1 || !strings.Contains(out.stdout, "both set") || !strings.Contains(out.stdout, "token required") {
		t.Fatalf("both set: %d %q", out.code, out.stdout)
	}

	if rows, err := db.Load(); err != nil || len(rows) != 0 {
		t.Fatalf("the probe stored %d rows (%v)", len(rows), err)
	}
}

func TestInitCheckOnAnOpenReceiver(t *testing.T) {
	t.Setenv("SDF_OTLP_TOKEN", "")
	t.Setenv("SDF_OTLP_TOKEN_FILE", "")
	ports, db := startReceiver(t, "")
	out := initCheck(t, ports)
	if out.code != 0 || !strings.Contains(out.stdout, "writes accepted without a token") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if rows, err := db.Load(); err != nil || len(rows) != 0 {
		t.Fatalf("the probe stored %d rows (%v)", len(rows), err)
	}
}
