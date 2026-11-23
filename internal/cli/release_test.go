package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

func TestCheckWithoutCasesRunsTheEmbeddedSet(t *testing.T) {
	// No source tree in the working directory: the default case set must
	// come from the binary, not from a path next to the source.
	t.Chdir(t.TempDir())
	out := run(t, "check")
	if out.code != 0 {
		t.Fatalf("code %d\n%s%s", out.code, out.stdout, out.stderr)
	}
	for _, want := range []string{"priced-session pass\n", "log-count pass\n", "unknown-price not_checked", "0 failed"} {
		if !strings.Contains(out.stdout, want) {
			t.Fatalf("missing %q\n%s", want, out.stdout)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		out := run(t, arg)
		if out.code != 0 {
			t.Fatalf("%s: code %d\n%s", arg, out.code, out.stderr)
		}
		for _, want := range []string{"sdf ", "commit: ", "built: ", "go: go1.", "platform: "} {
			if !strings.Contains(out.stdout, want) {
				t.Fatalf("%s: missing %q\n%s", arg, want, out.stdout)
			}
		}
	}
	if out := run(t, "version", "--bogus"); out.code != 2 {
		t.Fatalf("code %d", out.code)
	}
}

func TestDemoCommand(t *testing.T) {
	t.Chdir(t.TempDir())
	out := run(t, "demo")
	if out.code != 0 {
		t.Fatalf("code %d\n%s%s", out.code, out.stdout, out.stderr)
	}
	for _, want := range []string{
		"sdf.fixture=demo",
		"metrics over OTLP/gRPC -> ok",
		"tool: claude-code",
		"tokens: input=4200 output=480 cacheRead=12000",
		"computed_cost_usd: 0.0156 ",
		"Bash call_id=toolu_demo_bash success=true",
		"missing: (none)",
		"temporary store was removed",
	} {
		if !strings.Contains(out.stdout, want) {
			t.Fatalf("missing %q\n%s", want, out.stdout)
		}
	}
	if entries, _ := os.ReadDir("."); len(entries) != 0 {
		t.Fatalf("demo wrote into the working directory: %v", entries)
	}

	db := filepath.Join(t.TempDir(), "demo.sqlite")
	kept := run(t, "demo", "--db", db, "--json")
	if kept.code != 0 {
		t.Fatalf("code %d\n%s", kept.code, kept.stderr)
	}
	var report struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(kept.stdout), &report); err != nil {
		t.Fatalf("%v\n%s", err, kept.stdout)
	}
	if len(report.Sessions) != 1 || !strings.HasPrefix(report.Sessions[0].SessionID, "sdf-demo-") {
		t.Fatalf("%s", kept.stdout)
	}
	again := run(t, "summary", "--db", db)
	if again.code != 0 || !strings.Contains(again.stdout, report.Sessions[0].SessionID) {
		t.Fatalf("code %d\n%s%s", again.code, again.stdout, again.stderr)
	}
}

// TestTrimpathReleaseBinary builds sdf the way `make build` and GoReleaser
// do (-trimpath, stripped, version ldflags) and runs the 5-minute path from
// a directory that has no source tree.
func TestTrimpathReleaseBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	root := moduleRoot(t)
	bin := filepath.Join(t.TempDir(), "sdf")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command(goBin, "build", "-trimpath",
		"-ldflags", "-s -w -X github.com/paulpham157/sdf/internal/version.Version=v0.1.0-test -X github.com/paulpham157/sdf/internal/version.Commit=abc1234 -X github.com/paulpham157/sdf/internal/version.Date=2026-11-23T00:00:00Z",
		"-o", bin, "./cmd/sdf")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	work := t.TempDir()
	exe := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sdf %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	if out := exe("version"); !strings.Contains(out, "sdf v0.1.0-test\n") || !strings.Contains(out, "commit: abc1234\n") || !strings.Contains(out, "built: 2026-11-23T00:00:00Z\n") {
		t.Fatal(out)
	}
	if out := exe("check"); !strings.Contains(out, "priced-session pass\n") || !strings.Contains(out, " 0 failed") {
		t.Fatal(out)
	}
	if out := exe("demo"); !strings.Contains(out, "computed_cost_usd: 0.0156 ") {
		t.Fatal(out)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// sdf demo --db refuses a store that already holds real telemetry or
// decisions, so synthetic rows never mix into real summaries. A new or empty
// file, or a store with only earlier demo sessions, is accepted.
func TestDemoRefusesAStoreWithRealTelemetry(t *testing.T) {
	dir := t.TempDir()

	demoOnly := filepath.Join(dir, "demo.sqlite")
	for i := 0; i < 2; i++ {
		if out := run(t, "demo", "--db", demoOnly, "--json"); out.code != 0 {
			t.Fatalf("demo run %d into a demo-only store: code %d\n%s", i, out.code, out.stderr)
		}
	}

	empty := filepath.Join(dir, "empty.sqlite")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := run(t, "demo", "--db", empty, "--json"); out.code != 0 {
		t.Fatalf("empty file: code %d\n%s", out.code, out.stderr)
	}

	real := filepath.Join(dir, "real.sqlite")
	st, err := store.Open(real)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Insert([]record.Record{{
		Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("live-1"),
		Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(1),
		Attributes: map[string]any{}, Resource: map[string]any{"service.name": "claude-code"},
	}}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	decided := filepath.Join(dir, "decided.sqlite")
	st, err = store.Open(decided)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertDecision(store.Decision{ID: "d1", Statement: "s", Status: "hypothesis", RecordedAt: "2026-11-23T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	for _, db := range []string{real, decided} {
		out := run(t, "demo", "--db", db)
		if out.code != 1 || !strings.Contains(out.stderr, "not from sdf demo; refusing") {
			t.Fatalf("%s: code %d\n%s%s", db, out.code, out.stdout, out.stderr)
		}
		sum := run(t, "summary", "--db", db, "--json")
		if strings.Contains(sum.stdout, "sdf-demo-") {
			t.Fatalf("%s: demo rows were written\n%s", db, sum.stdout)
		}
	}
}
