//go:build e2e

package e2e

import (
	"database/sql"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// A database written by an older sdf (content stored, no policy stamp):
// serve warns at start and does not change it; sdf redact scrubs it.
func TestSdfRedactScrubsALegacyDatabase(t *testing.T) {
	srv := startServeArgs(t, "--store-content")
	postSamples(t, srv.httpBase, leakSamples)
	srv.stop(t)
	db := srv.db
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	srv = startServeArgs(t, "--db", db)
	srv.stop(t)
	if !strings.Contains(srv.stderr.String(), "run: sdf redact --db "+db) {
		t.Fatalf("no legacy warning: %s", srv.stderr.String())
	}
	if !dbHas(t, db, "SDF-LEAK-01-") {
		t.Fatal("serve changed stored rows")
	}

	run := func(args ...string) (string, int) {
		out, err := exec.Command(binPath(t), args...).CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	out, code := run("redact", "--db", db, "--dry-run")
	if code != 0 || !strings.Contains(out, "rows would change; nothing written") || !dbHas(t, db, "SDF-LEAK-01-") {
		t.Fatalf("dry run %d: %s", code, out)
	}
	out, code = run("redact", "--db", db)
	if code != 0 || !strings.Contains(out, "rows scrubbed; WAL checkpointed and file vacuumed") {
		t.Fatalf("redact %d: %s", code, out)
	}
	if dbHas(t, db, "SDF-LEAK-") {
		t.Fatal("content left after sdf redact")
	}
	if sum := summarize(t, db, "--json"); !strings.Contains(sum, `"0123456789abcdef0123456789abcdef01234567"`) || !strings.Contains(sum, `"sample-leak-1"`) {
		t.Fatalf("summary lost the session or commit: %s", sum)
	}
	srv = startServeArgs(t, "--db", db)
	srv.stop(t)
	if strings.Contains(srv.stderr.String(), "sdf redact") {
		t.Fatalf("warning after scrub: %s", srv.stderr.String())
	}
	if out, code := run("redact", "--db", filepath.Join(t.TempDir(), "missing.sqlite")); code != 1 || !strings.Contains(out, "does not exist") {
		t.Fatalf("missing db %d: %s", code, out)
	}
	if out, code := run("redact"); code != 2 || !strings.Contains(out, "--db PATH is required") {
		t.Fatalf("no --db %d: %s", code, out)
	}
}
