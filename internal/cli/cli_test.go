package cli_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/cli"
	"github.com/paulpham157/sdf/internal/pricing"
)

func TestSummaryOfANewDatabase(t *testing.T) {
	out := run(t, "summary", "--db", filepath.Join(t.TempDir(), "empty.sqlite"))
	if out.code != 0 || out.stdout != "No telemetry sessions in the store.\n" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}

func TestPricesRefreshWritesACatalogWithoutCallingOpenRouter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("authorization header")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"vendor/priced","pricing":{"prompt":"0.000001","completion":"0.000002"}}]}`))
	}))
	defer server.Close()
	outPath := filepath.Join(t.TempDir(), "prices.json")
	out := run(t, "prices", "refresh", "--url", server.URL, "--out", outPath)
	if out.code != 0 {
		t.Fatal(out.stderr)
	}
	if !strings.Contains(out.stdout, "wrote 1 models") || !strings.Contains(out.stdout, pricing.SourceURL) {
		t.Fatal(out.stdout)
	}
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "0.000001") || !strings.Contains(string(body), pricing.SourceURL) {
		t.Fatal(string(body))
	}
	missing := run(t, "summary", "--db", filepath.Join(t.TempDir(), "empty.sqlite"), "--prices", filepath.Join(t.TempDir(), "nope.json"))
	if missing.code != 1 || !strings.Contains(missing.stderr, "price catalog") {
		t.Fatalf("%d %s", missing.code, missing.stderr)
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
