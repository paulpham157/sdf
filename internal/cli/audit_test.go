package cli_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger"
)

func seedLedger(t *testing.T) (string, ledger.Entry) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	l, err := ledger.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var last ledger.Entry
	for _, id := range []string{"task-1", "task-2", "task-3"} {
		p := []byte(`{"objective":"` + id + `"}`)
		last, err = l.Append(context.Background(), "sdf.task/v1", id, p, ledger.PayloadHash("sdf.task/v1", p))
		if err != nil {
			t.Fatal(err)
		}
	}
	return path, last
}

func TestAuditVerifyIntactPrintsTheHead(t *testing.T) {
	path, head := seedLedger(t)
	out := run(t, "audit", "verify", "--ledger", path)
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if !strings.Contains(out.stdout, "ledger intact") || !strings.Contains(out.stdout, "head seq 3 entry_hash "+head.EntryHash) {
		t.Fatalf("%q", out.stdout)
	}

	head2 := run(t, "audit", "head", "--ledger="+path)
	if head2.code != 0 || head2.stdout != "seq 3 entry_hash "+head.EntryHash+"\n" {
		t.Fatalf("%d %q %q", head2.code, head2.stdout, head2.stderr)
	}

	js := run(t, "audit", "head", "--ledger", path, "--json")
	var doc struct {
		Head struct {
			Seq       int64  `json:"seq"`
			EntryHash string `json:"entry_hash"`
		} `json:"head"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &doc); err != nil || js.code != 0 {
		t.Fatalf("%d %q %v", js.code, js.stdout, err)
	}
	if doc.Head.Seq != 3 || doc.Head.EntryHash != head.EntryHash {
		t.Fatalf("%+v", doc)
	}
}

// The acceptance test: hand-edit one SQLite row and verify reports it.
func TestAuditVerifyReportsAHandEditedRow(t *testing.T) {
	path, _ := seedLedger(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ledger_entries SET payload = '{"objective":"forged"}' WHERE id = 'task-2'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	out := run(t, "audit", "verify", "--ledger", path)
	if out.code != 1 {
		t.Fatalf("exit %d %q %q", out.code, out.stdout, out.stderr)
	}
	if !strings.Contains(out.stdout, "ledger broken") || !strings.Contains(out.stdout, `first failure: seq 2 (sdf.task/v1 "task-2"): modified`) {
		t.Fatalf("%q", out.stdout)
	}

	js := run(t, "audit", "verify", "--ledger", path, "--json")
	var doc struct {
		OK      bool `json:"ok"`
		Failure struct {
			Seq     int64  `json:"seq"`
			ID      string `json:"id"`
			Problem string `json:"problem"`
		} `json:"failure"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &doc); err != nil || js.code != 1 {
		t.Fatalf("%d %q %v", js.code, js.stdout, err)
	}
	if doc.OK || doc.Failure.Seq != 2 || doc.Failure.ID != "task-2" || doc.Failure.Problem != "modified" {
		t.Fatalf("%+v", doc)
	}

	head := run(t, "audit", "head", "--ledger", path)
	if head.code != 1 || head.stdout != "" || !strings.Contains(head.stderr, "ledger broken") {
		t.Fatalf("head on a broken ledger: %d %q %q", head.code, head.stdout, head.stderr)
	}
}

func TestAuditVerifyReportsADeletedRow(t *testing.T) {
	path, _ := seedLedger(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM ledger_entries WHERE seq = 1`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	out := run(t, "audit", "verify", "--ledger", path)
	if out.code != 1 || !strings.Contains(out.stdout, "first failure: seq 1: missing") {
		t.Fatalf("%d %q", out.code, out.stdout)
	}
}

func TestAuditOnAMissingLedgerCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "none")
	t.Setenv("SDF_LEDGER", filepath.Join(dir, "ledger.db"))
	out := run(t, "audit", "verify")
	if out.code != 0 || !strings.Contains(out.stdout, "ledger empty") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	head := run(t, "audit", "head")
	if head.code != 1 || !strings.Contains(head.stderr, "ledger empty") {
		t.Fatalf("%d %q %q", head.code, head.stdout, head.stderr)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audit created %s", dir)
	}
}

func TestAuditUsage(t *testing.T) {
	for _, args := range [][]string{
		{"audit"},
		{"audit", "nope"},
		{"audit", "verify", "--ledger"},
		{"audit", "verify", "--bogus"},
		{"audit", "head", "--ledger="},
	} {
		if out := run(t, args...); out.code != 2 {
			t.Fatalf("%v: exit %d %q", args, out.code, out.stderr)
		}
	}
	if out := run(t, "audit", "--help"); out.code != 0 || !strings.Contains(out.stdout, "sdf audit") {
		t.Fatalf("%d %q", out.code, out.stdout)
	}
}
