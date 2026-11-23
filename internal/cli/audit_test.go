package cli_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(out.stdout, "ledger broken") || !strings.Contains(out.stdout, `first failure: seq 2 ("sdf.task/v1" "task-2"): modified`) {
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

func TestAuditVerifyRequireNonEmpty(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none", "ledger.db")
	out := run(t, "audit", "verify", "--ledger", missing, "--require-nonempty")
	if out.code != 1 || !strings.Contains(out.stdout, "has no entries, and --require-nonempty was given") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("verify created the ledger directory")
	}
	js := run(t, "audit", "verify", "--ledger", missing, "--require-nonempty", "--json")
	var doc struct {
		OK              bool `json:"ok"`
		ChainOK         bool `json:"chain_ok"`
		Empty           bool `json:"empty"`
		RequireNonEmpty bool `json:"require_nonempty"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &doc); err != nil || js.code != 1 {
		t.Fatalf("%d %q %v", js.code, js.stdout, err)
	}
	if doc.OK || !doc.ChainOK || !doc.Empty || !doc.RequireNonEmpty {
		t.Fatalf("%+v", doc)
	}

	path, _ := seedLedger(t)
	if out := run(t, "audit", "verify", "--ledger", path, "--require-nonempty"); out.code != 0 {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}

func TestAuditVerifyExpectHeadMatches(t *testing.T) {
	path, head := seedLedger(t)
	out := run(t, "audit", "verify", "--ledger", path, "--expect-head", head.EntryHash)
	if out.code != 0 || !strings.Contains(out.stdout, "head matches expected "+head.EntryHash) {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	// Each form `sdf audit head` prints can be pinned in a file.
	dir := t.TempDir()
	for i, args := range [][]string{
		{"audit", "head", "--ledger", path},
		{"audit", "head", "--ledger", path, "--json"},
	} {
		pinned := run(t, args...)
		f := filepath.Join(dir, "head"+string(rune('a'+i)))
		if err := os.WriteFile(f, []byte(pinned.stdout), 0o600); err != nil {
			t.Fatal(err)
		}
		if out := run(t, "audit", "verify", "--ledger", path, "--expect-head-file="+f); out.code != 0 {
			t.Fatalf("%v: %d %q %q", args, out.code, out.stdout, out.stderr)
		}
	}
	bare := filepath.Join(dir, "bare")
	if err := os.WriteFile(bare, []byte(head.EntryHash+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := run(t, "audit", "verify", "--ledger", path, "--expect-head-file", bare); out.code != 0 {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}

// Deleting the newest entry leaves a valid chain. Only a pinned head catches it.
func TestAuditVerifyExpectHeadDetectsTruncation(t *testing.T) {
	path, head := seedLedger(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM ledger_entries WHERE seq = 3`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if out := run(t, "audit", "verify", "--ledger", path); out.code != 0 {
		t.Fatalf("plain verify should pass a truncated chain: %d %q", out.code, out.stdout)
	}
	out := run(t, "audit", "verify", "--ledger", path, "--expect-head", head.EntryHash)
	if out.code != 1 || !strings.Contains(out.stdout, "head mismatch: expected "+head.EntryHash+", actual sha256:") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	js := run(t, "audit", "verify", "--ledger", path, "--expect-head", head.EntryHash, "--json")
	var doc struct {
		OK           bool   `json:"ok"`
		ChainOK      bool   `json:"chain_ok"`
		ExpectedHead string `json:"expected_head"`
		HeadMatch    *bool  `json:"head_match"`
		Head         struct {
			Seq       int64  `json:"seq"`
			EntryHash string `json:"entry_hash"`
		} `json:"head"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &doc); err != nil || js.code != 1 {
		t.Fatalf("%d %q %v", js.code, js.stdout, err)
	}
	if doc.OK || !doc.ChainOK || doc.ExpectedHead != head.EntryHash || doc.HeadMatch == nil || *doc.HeadMatch || doc.Head.Seq != 2 {
		t.Fatalf("%+v", doc)
	}
}

func TestAuditVerifyExpectHeadOnAnEmptyLedger(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "ledger.db")
	want := "sha256:" + strings.Repeat("a", 64)
	out := run(t, "audit", "verify", "--ledger", missing, "--expect-head", want)
	if out.code != 1 || !strings.Contains(out.stdout, "actual none") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}

func TestAuditVerifyExpectHeadUsage(t *testing.T) {
	path, head := seedLedger(t)
	f := filepath.Join(t.TempDir(), "head")
	if err := os.WriteFile(f, []byte(head.EntryHash), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(bad, []byte("not a hash"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--expect-head", head.EntryHash, "--expect-head-file", f},
		{"--expect-head", "sha256:XYZ"},
		{"--expect-head", strings.TrimPrefix(head.EntryHash, "sha256:")},
		{"--expect-head"},
		{"--expect-head-file", bad},
	} {
		full := append([]string{"audit", "verify", "--ledger", path}, args...)
		if out := run(t, full...); out.code != 2 {
			t.Fatalf("%v: exit %d %q %q", args, out.code, out.stdout, out.stderr)
		}
	}
}

// A file that is not a readable SQLite database (garbage, or truncated) is
// a broken ledger: exit 1, like a broken chain.
func TestAuditExitsOneForAFileThatIsNotALedgerDatabase(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(garbage, []byte(strings.Repeat("not a sqlite file ", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _ := seedLedger(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(dir, "truncated.db")
	if err := os.WriteFile(truncated, data[:len(data)/2+100], 0o600); err != nil {
		t.Fatal(err)
	}
	pin := "3:sha256:" + strings.Repeat("a", 64)
	pinFile := filepath.Join(dir, "pin")
	if err := os.WriteFile(pinFile, []byte(pin), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"audit", "verify", "--ledger", garbage},
		{"audit", "verify", "--ledger", garbage, "--json"},
		{"audit", "head", "--ledger", garbage},
		{"audit", "verify", "--ledger", truncated},
		{"audit", "verify", "--ledger", garbage, "--expect-prefix", pin},
		{"audit", "verify", "--ledger", garbage, "--expect-prefix-file", pinFile},
		{"audit", "verify", "--ledger", garbage, "--expect-prefix", pin, "--json"},
	} {
		out := run(t, args...)
		if out.code != 1 || out.stdout != "" || !strings.Contains(out.stderr, "ledger broken") {
			t.Fatalf("%v: %d %q %q", args, out.code, out.stdout, out.stderr)
		}
	}
}

// Failing to open or read for any other reason is I/O: exit 3.
func TestAuditExitsThreeWhenItCannotRead(t *testing.T) {
	dir := t.TempDir()
	wellFormed := "1:sha256:" + strings.Repeat("a", 64)
	cases := [][]string{
		{"audit", "verify", "--ledger", dir},
		{"audit", "head", "--ledger", dir},
		{"audit", "verify", "--ledger", dir, "--expect-prefix", wellFormed},
	}
	if os.Geteuid() != 0 {
		path, _ := seedLedger(t)
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		cases = append(cases, []string{"audit", "verify", "--ledger", path})
	}
	for _, args := range cases {
		out := run(t, args...)
		if out.code != 3 || out.stdout != "" || out.stderr == "" {
			t.Fatalf("%v: %d %q %q", args, out.code, out.stdout, out.stderr)
		}
	}
	path, _ := seedLedger(t)
	out := run(t, "audit", "verify", "--ledger", path, "--expect-head-file", filepath.Join(dir, "absent"))
	if out.code != 3 || !strings.Contains(out.stderr, "--expect-head-file") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	out = run(t, "audit", "verify", "--ledger", path, "--expect-prefix-file", filepath.Join(dir, "absent"))
	if out.code != 3 || !strings.Contains(out.stderr, "--expect-prefix-file") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}

// When the chain is broken, the stored head proves nothing, even if it
// equals the pinned one. The message says so instead of "expected X, actual X".
func TestAuditVerifyExpectHeadOnABrokenChain(t *testing.T) {
	path, head := seedLedger(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ledger_entries SET payload = 'x' WHERE seq = 1`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	out := run(t, "audit", "verify", "--ledger", path, "--expect-head", head.EntryHash)
	if out.code != 1 || !strings.Contains(out.stdout, "head not accepted: expected "+head.EntryHash+", but the chain is broken") {
		t.Fatalf("%d %q", out.code, out.stdout)
	}
	if strings.Contains(out.stdout, "head mismatch") {
		t.Fatalf("%q", out.stdout)
	}
}

// A rechained row whose kind holds a terminal escape ("\x1b[2K\r" + "ledger
// intact") must not reach the terminal raw: it would erase the failure line.
func TestAuditVerifyNeverPrintsARawEscapeSequence(t *testing.T) {
	path, _ := seedLedger(t)
	l, err := ledger.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	head, err := l.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	forged := head
	forged.Kind = "sdf.task/v1\x1b[2K\rledger intact"
	payload := []byte("{}")
	forged.Hash = ledger.PayloadHash(forged.Kind, payload)
	forged.EntryHash = ledger.EntryHash(forged)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ledger_entries SET kind = ?, payload = ?, hash = ?, entry_hash = ? WHERE seq = ?`,
		string(forged.Kind), payload, forged.Hash, forged.EntryHash, head.Seq); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	pin := fmt.Sprintf("%d:%s", head.Seq, head.EntryHash)
	for _, args := range [][]string{
		{"audit", "verify", "--ledger", path},
		{"audit", "verify", "--ledger", path, "--json"},
		{"audit", "verify", "--ledger", path, "--expect-head", head.EntryHash},
		{"audit", "verify", "--ledger", path, "--expect-prefix", pin},
		{"audit", "head", "--ledger", path},
	} {
		out := run(t, args...)
		if out.code != 1 {
			t.Fatalf("%v: exit %d", args, out.code)
		}
		if strings.ContainsAny(out.stdout+out.stderr, "\x1b\r") {
			t.Fatalf("%v: raw control bytes in %q %q", args, out.stdout, out.stderr)
		}
		if !strings.Contains(out.stdout+out.stderr, "broken") && !strings.Contains(out.stdout, `"ok": false`) {
			t.Fatalf("%v: does not say broken: %q %q", args, out.stdout, out.stderr)
		}
	}
	out := run(t, "audit", "verify", "--ledger", path)
	if !strings.Contains(out.stdout, `first failure: seq 3 ("sdf.task/v1\x1b[2K\rledger intact" "task-3"): modified`) {
		t.Fatalf("%q", out.stdout)
	}
}

func TestAuditVerifyExpectPrefixAllowsLaterAppends(t *testing.T) {
	path, head := seedLedger(t)
	pin := fmt.Sprintf("%d:%s", head.Seq, head.EntryHash)
	l, err := ledger.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := []byte(`{"objective":"task-4"}`)
	if _, err := l.Append(context.Background(), "sdf.task/v1", "task-4", p, ledger.PayloadHash("sdf.task/v1", p)); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()

	out := run(t, "audit", "verify", "--ledger", path, "--expect-prefix", pin, "--require-nonempty")
	if out.code != 0 || !strings.Contains(out.stdout, "prefix matches "+pin) {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}

	dir := t.TempDir()
	fText := filepath.Join(dir, "text")
	if err := os.WriteFile(fText, []byte(fmt.Sprintf("seq %d entry_hash %s\n", head.Seq, head.EntryHash)), 0o600); err != nil {
		t.Fatal(err)
	}
	jsHead, err := json.Marshal(map[string]any{"head": map[string]any{"seq": head.Seq, "entry_hash": head.EntryHash}})
	if err != nil {
		t.Fatal(err)
	}
	fJSON := filepath.Join(dir, "json")
	if err := os.WriteFile(fJSON, jsHead, 0o600); err != nil {
		t.Fatal(err)
	}
	fBare := filepath.Join(dir, "bare")
	if err := os.WriteFile(fBare, []byte(pin+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cur := run(t, "audit", "head", "--ledger", path)
	if cur.code != 0 {
		t.Fatalf("head: %d %q %q", cur.code, cur.stdout, cur.stderr)
	}
	fHeadText := filepath.Join(dir, "head-text")
	if err := os.WriteFile(fHeadText, []byte(cur.stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	curJSON := run(t, "audit", "head", "--ledger", path, "--json")
	if curJSON.code != 0 {
		t.Fatalf("head --json: %d %q %q", curJSON.code, curJSON.stdout, curJSON.stderr)
	}
	fHeadJSON := filepath.Join(dir, "head-json")
	if err := os.WriteFile(fHeadJSON, []byte(curJSON.stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err = ledger.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p = []byte(`{"objective":"task-5"}`)
	if _, err := l.Append(context.Background(), "sdf.task/v1", "task-5", p, ledger.PayloadHash("sdf.task/v1", p)); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	for _, f := range []string{fText, fJSON, fBare, fHeadText, fHeadJSON} {
		if out := run(t, "audit", "verify", "--ledger", path, "--expect-prefix-file="+f); out.code != 0 {
			t.Fatalf("%s: %d %q %q", f, out.code, out.stdout, out.stderr)
		}
	}
}

func TestAuditVerifyExpectPrefixDetectsTruncation(t *testing.T) {
	path, head := seedLedger(t)
	pin := fmt.Sprintf("%d:%s", head.Seq, head.EntryHash)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM ledger_entries WHERE seq >= 3`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if out := run(t, "audit", "verify", "--ledger", path); out.code != 0 {
		t.Fatalf("plain verify should pass a truncated chain: %d %q", out.code, out.stdout)
	}
	out := run(t, "audit", "verify", "--ledger", path, "--expect-prefix", pin)
	if out.code != 1 || !strings.Contains(out.stdout, "prefix mismatch: expected "+pin+", actual none") || !strings.Contains(out.stdout, "truncated below that seq") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	js := run(t, "audit", "verify", "--ledger", path, "--expect-prefix", pin, "--json")
	var doc struct {
		OK             bool   `json:"ok"`
		ChainOK        bool   `json:"chain_ok"`
		ExpectedPrefix string `json:"expected_prefix"`
		PrefixMatch    *bool  `json:"prefix_match"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &doc); err != nil || js.code != 1 {
		t.Fatalf("%d %q %v", js.code, js.stdout, err)
	}
	if doc.OK || !doc.ChainOK || doc.ExpectedPrefix != pin || doc.PrefixMatch == nil || *doc.PrefixMatch {
		t.Fatalf("%+v", doc)
	}
}

func TestAuditVerifyExpectPrefixDetectsRewrite(t *testing.T) {
	path, _ := seedLedger(t)
	l, err := ledger.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, e2, err := l.GetBySeq(context.Background(), 2)
	_ = l.Close()
	if err != nil {
		t.Fatal(err)
	}
	pin := fmt.Sprintf("%d:%s", e2.Seq, e2.EntryHash)
	rewriteAndRehash(t, path, 2, `{"objective":"forged-task-2"}`)

	if out := run(t, "audit", "verify", "--ledger", path); out.code != 0 {
		t.Fatalf("plain verify should pass a fully rehashed chain: %d %q %q", out.code, out.stdout, out.stderr)
	}
	out := run(t, "audit", "verify", "--ledger", path, "--expect-prefix", pin)
	if out.code != 1 || !strings.Contains(out.stdout, "prefix mismatch: expected "+pin+", actual 2:sha256:") || !strings.Contains(out.stdout, "rewritten at or through that point") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	js := run(t, "audit", "verify", "--ledger", path, "--expect-prefix", pin, "--json")
	var doc struct {
		OK             bool   `json:"ok"`
		ChainOK        bool   `json:"chain_ok"`
		ExpectedPrefix string `json:"expected_prefix"`
		PrefixMatch    *bool  `json:"prefix_match"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &doc); err != nil || js.code != 1 {
		t.Fatalf("%d %q %v", js.code, js.stdout, err)
	}
	if doc.OK || !doc.ChainOK || doc.ExpectedPrefix != pin || doc.PrefixMatch == nil || *doc.PrefixMatch {
		t.Fatalf("%+v", doc)
	}
}

func TestAuditVerifyExpectPrefixOnAnEmptyLedger(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "ledger.db")
	hash := "sha256:" + strings.Repeat("a", 64)
	pin := "3:" + hash
	out := run(t, "audit", "verify", "--ledger", missing, "--expect-prefix", pin)
	if out.code != 1 || !strings.Contains(out.stdout, "prefix mismatch: expected "+pin+", actual none (the ledger is empty)") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}

func TestAuditVerifyExpectPrefixOnABrokenChain(t *testing.T) {
	path, head := seedLedger(t)
	pin := fmt.Sprintf("%d:%s", head.Seq, head.EntryHash)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ledger_entries SET payload = 'x' WHERE seq = 1`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	out := run(t, "audit", "verify", "--ledger", path, "--expect-prefix", pin)
	if out.code != 1 || !strings.Contains(out.stdout, "prefix not accepted: expected "+pin+", but the chain is broken") {
		t.Fatalf("%d %q", out.code, out.stdout)
	}
	if strings.Contains(out.stdout, "prefix mismatch") {
		t.Fatalf("%q", out.stdout)
	}
}

func TestAuditVerifyExpectPrefixUsage(t *testing.T) {
	path, head := seedLedger(t)
	pin := fmt.Sprintf("%d:%s", head.Seq, head.EntryHash)
	f := filepath.Join(t.TempDir(), "prefix")
	if err := os.WriteFile(f, []byte(pin), 0o600); err != nil {
		t.Fatal(err)
	}
	headFile := filepath.Join(t.TempDir(), "head")
	if err := os.WriteFile(headFile, []byte(head.EntryHash), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(bad, []byte("not a prefix"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--expect-prefix", pin, "--expect-prefix-file", f},
		{"--expect-prefix", pin, "--expect-head", head.EntryHash},
		{"--expect-prefix-file", f, "--expect-head-file", headFile},
		{"--expect-prefix", "sha256:" + strings.Repeat("a", 64)},
		{"--expect-prefix", "3:sha256:XYZ"},
		{"--expect-prefix", strings.TrimPrefix(head.EntryHash, "sha256:")},
		{"--expect-prefix", "-1:" + head.EntryHash},
		{"--expect-prefix"},
		{"--expect-prefix-file", bad},
	} {
		full := append([]string{"audit", "verify", "--ledger", path}, args...)
		if out := run(t, full...); out.code != 2 {
			t.Fatalf("%v: exit %d %q %q", args, out.code, out.stdout, out.stderr)
		}
	}
}

func TestAuditVerifyExpectPrefixFileUnreadable(t *testing.T) {
	path, head := seedLedger(t)
	pin := fmt.Sprintf("%d:%s", head.Seq, head.EntryHash)
	dir := t.TempDir()
	out := run(t, "audit", "verify", "--ledger", path, "--expect-prefix-file", dir)
	if out.code != 3 || !strings.Contains(out.stderr, "--expect-prefix-file:") {
		t.Fatalf("dir: %d %q %q", out.code, out.stdout, out.stderr)
	}
	unreadable := filepath.Join(t.TempDir(), "pin")
	if err := os.WriteFile(unreadable, []byte(pin), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
	out = run(t, "audit", "verify", "--ledger", path, "--expect-prefix-file", unreadable)
	if os.Geteuid() == 0 {
		if out.code != 0 && out.code != 3 {
			t.Fatalf("root read of mode 0000: %d %q %q", out.code, out.stdout, out.stderr)
		}
		return
	}
	if out.code != 3 || !strings.Contains(out.stderr, "--expect-prefix-file:") {
		t.Fatalf("mode 0000: %d %q %q", out.code, out.stdout, out.stderr)
	}
}

// rewriteAndRehash changes the payload at seq and recomputes every hash from
// that point on, so the chain remains internally valid.
func rewriteAndRehash(t *testing.T, path string, seq int64, payload string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT seq, kind, id, payload, hash, prev_hash, entry_hash, chained_at FROM ledger_entries ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	type chainRow struct {
		seq       int64
		kind      string
		id        string
		payload   []byte
		hash      string
		prevHash  string
		entryHash string
		chainedAt string
	}
	var all []chainRow
	for rows.Next() {
		var r chainRow
		if err := rows.Scan(&r.seq, &r.kind, &r.id, &r.payload, &r.hash, &r.prevHash, &r.entryHash, &r.chainedAt); err != nil {
			t.Fatal(err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()

	var prevHash string
	found := false
	for i := range all {
		r := &all[i]
		if r.seq == seq {
			r.payload = []byte(payload)
			r.hash = ledger.PayloadHash(ledger.Kind(r.kind), r.payload)
			found = true
		}
		if found {
			if r.seq != seq {
				r.prevHash = prevHash
			}
			at, err := time.Parse(ledger.TimeLayout, r.chainedAt)
			if err != nil {
				t.Fatal(err)
			}
			r.entryHash = ledger.EntryHash(ledger.Entry{
				Seq:       r.seq,
				Kind:      ledger.Kind(r.kind),
				ID:        r.id,
				Hash:      r.hash,
				PrevHash:  r.prevHash,
				ChainedAt: at,
			})
			if _, err := db.Exec(`UPDATE ledger_entries SET payload = ?, hash = ?, prev_hash = ?, entry_hash = ? WHERE seq = ?`,
				r.payload, r.hash, r.prevHash, r.entryHash, r.seq); err != nil {
				t.Fatal(err)
			}
		}
		prevHash = r.entryHash
	}
	if !found {
		t.Fatalf("seq %d not in ledger", seq)
	}
}
