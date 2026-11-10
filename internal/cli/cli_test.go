package cli_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/cli"
)

func TestSummaryOfANewDatabase(t *testing.T) {
	out := run(t, "summary", "--db", filepath.Join(t.TempDir(), "empty.sqlite"))
	if out.code != 0 || out.stdout != "No telemetry sessions in the store.\n" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}

func TestUnknownCommand(t *testing.T) {
	out := run(t, "nope")
	if out.code != 2 || !strings.Contains(out.stderr, "unrecognized command") {
		t.Fatalf("%d %q", out.code, out.stderr)
	}
}

type result struct {
	code           int
	stdout, stderr string
}

func run(t *testing.T, args ...string) result {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	code := cli.Main(args)
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	var stdout, stderr bytes.Buffer
	_, _ = io.Copy(&stdout, outR)
	_, _ = io.Copy(&stderr, errR)
	return result{code, stdout.String(), stderr.String()}
}
