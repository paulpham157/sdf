package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
)

func TestDecisionBriefPrintsRankedText(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	seedBrief(t, dbPath)

	out := run(t, "decision", "brief", "--db", dbPath)
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(out.stdout, "\n"), "\n")
	want := []string{
		"constraint\tdont-push\tuser\tDo not push until the user says so.",
		"constraint\tkeep-local\tagent\tKeep the store on this machine.",
		"blocked\tship-it\tagent\tPush the branch now.",
		"conflicting\tuse-postgres\tagent\tStore telemetry in Postgres.",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %q", out.stdout)
	}
	for i, line := range want {
		if lines[i] != line {
			t.Fatalf("line %d = %q, want %q\n%s", i, lines[i], line, out.stdout)
		}
	}
}

func TestDecisionBriefJSONAndCaps(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	seedBrief(t, dbPath)

	out := run(t, "decision", "brief", "--db", dbPath, "--format", "json", "--max-items", "1")
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	var doc struct {
		Items []struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Statement string `json:"statement"`
			Status    string `json:"status"`
			DecidedBy string `json:"decided_by"`
			Locked    bool   `json:"locked"`
		} `json:"items"`
		Omitted int `json:"omitted"`
	}
	if err := json.Unmarshal([]byte(out.stdout), &doc); err != nil {
		t.Fatalf("%v %q", err, out.stdout)
	}
	if doc.Omitted != 3 || len(doc.Items) != 1 || doc.Items[0].ID != "dont-push" || doc.Items[0].Kind != "constraint" || !doc.Items[0].Locked || doc.Items[0].DecidedBy != "user" || doc.Items[0].Status != "decision" {
		t.Fatalf("%+v", doc)
	}

	capped := run(t, "decision", "brief", "--db", dbPath, "--max-items=2")
	if capped.code != 0 || !strings.HasSuffix(capped.stdout, "2 more: run sdf decision list\n") || strings.Contains(capped.stdout, "ship-it") {
		t.Fatalf("%d %q %q", capped.code, capped.stdout, capped.stderr)
	}

	// One kept line plus the omitted note fits; two kept lines do not.
	one := run(t, "decision", "brief", "--db", dbPath, "--max-items", "1")
	two := run(t, "decision", "brief", "--db", dbPath, "--max-items", "2")
	if len(one.stdout) >= len(two.stdout) {
		t.Fatalf("one %d two %d", len(one.stdout), len(two.stdout))
	}
	bytes := run(t, "decision", "brief", "--db", dbPath, "--max-bytes", strconv.Itoa(len(one.stdout)))
	if bytes.code != 0 || bytes.stdout != one.stdout {
		t.Fatalf("byte cap %d\n got %q\nwant %q", len(one.stdout), bytes.stdout, one.stdout)
	}
}

func TestDecisionBriefMaxItemsZeroStillReportsAMatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	seedBrief(t, dbPath)

	text := run(t, "decision", "brief", "--db", dbPath, "--max-items", "0")
	if text.code != 0 || text.stderr != "" || !strings.Contains(text.stdout, "\tdont-push\t") || !strings.HasSuffix(text.stdout, "3 more: run sdf decision list\n") || strings.Contains(text.stdout, "ship-it") {
		t.Fatalf("%d %q %q", text.code, text.stdout, text.stderr)
	}
	js := run(t, "decision", "brief", "--db", dbPath, "--max-items=0", "--format", "json")
	if js.code != 0 || js.stderr != "" || !strings.Contains(js.stdout, `"id": "dont-push"`) || !strings.Contains(js.stdout, `"omitted": 3`) || !strings.Contains(js.stdout, `"omitted_locked": 0`) {
		t.Fatalf("%d %q %q", js.code, js.stdout, js.stderr)
	}
}

func TestDecisionBriefMissingDBIsSilent(t *testing.T) {
	dir := t.TempDir()
	cases := [][]string{
		{"decision", "brief", "--db", filepath.Join(dir, "missing.sqlite")},
		{"decision", "brief", "--db", filepath.Join(dir, "no-such-dir", "t.sqlite"), "--format", "json"},
	}
	for _, args := range cases {
		out := run(t, args...)
		if out.code != 0 || out.stdout != "" || out.stderr != "" {
			t.Fatalf("%v: %d %q %q", args, out.code, out.stdout, out.stderr)
		}
	}
	bad := filepath.Join(dir, "bad.sqlite")
	if err := os.WriteFile(bad, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := run(t, "decision", "brief", "--db", bad, "--format", "json")
	if out.code != 0 || out.stderr != "" || out.stdout != "{\"unavailable\":true}\n" {
		t.Fatalf("corrupt: %d %q %q", out.code, out.stdout, out.stderr)
	}
	text := run(t, "decision", "brief", "--db", bad)
	if text.code != 0 || text.stderr != "" || text.stdout != "sdf decision brief unavailable: run sdf decision list\n" {
		t.Fatalf("corrupt text: %d %q %q", text.code, text.stdout, text.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.sqlite")); !os.IsNotExist(err) {
		t.Fatal("brief created a missing database")
	}
}

func TestDecisionBriefNonRegularFileIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	asDir := run(t, "decision", "brief", "--db", dir)
	if asDir.code != 0 || asDir.stderr != "" || asDir.stdout != "sdf decision brief unavailable: run sdf decision list\n" {
		t.Fatalf("dir: %d %q %q", asDir.code, asDir.stdout, asDir.stderr)
	}

	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan result, 1)
	go func() {
		done <- run(t, "decision", "brief", "--db", fifo, "--format", "json")
	}()
	select {
	case out := <-done:
		if out.code != 0 || out.stderr != "" || out.stdout != "{\"unavailable\":true}\n" {
			t.Fatalf("fifo: %d %q %q", out.code, out.stdout, out.stderr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fifo brief did not return")
	}
}

func TestDecisionBriefEmptyStoreJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	out := run(t, "decision", "brief", "--db", dbPath, "--format=json")
	if out.code != 0 || out.stderr != "" || out.stdout != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	text := run(t, "decision", "brief", "--db", dbPath)
	if text.code != 0 || text.stdout != "" || text.stderr != "" {
		t.Fatalf("text %d %q %q", text.code, text.stdout, text.stderr)
	}
}

func TestDecisionBriefUsage(t *testing.T) {
	help := run(t, "decision", "brief", "--help")
	if help.code != 0 || !strings.Contains(help.stdout, "usage: sdf decision brief [--db PATH] [--max-items N] [--max-bytes N] [--format text|json]") || !strings.Contains(help.stdout, "when any decision matched") || !strings.Contains(help.stdout, `N more (K locked): run sdf decision list`) || !strings.Contains(help.stdout, "omitted_locked") || !strings.Contains(help.stdout, "scripts/brief") {
		t.Fatalf("%d %q", help.code, help.stdout)
	}
	bad := run(t, "decision", "brief", "--format", "yaml")
	if bad.code != 2 || bad.stdout != "" || !strings.Contains(bad.stderr, "--format must be text or json") {
		t.Fatalf("%d %q %q", bad.code, bad.stdout, bad.stderr)
	}
	neg := run(t, "decision", "brief", "--max-bytes", "-1")
	if neg.code != 2 || !strings.Contains(neg.stderr, "--max-bytes must be a non-negative integer") {
		t.Fatalf("%d %q", neg.code, neg.stderr)
	}
	alias := run(t, "decision", "brief", "--json")
	if alias.code != 2 || alias.stdout != "" || !strings.Contains(alias.stderr, "unrecognized arguments") {
		t.Fatalf("--json: %d %q %q", alias.code, alias.stdout, alias.stderr)
	}
}

func TestDecisionBriefDefaultPathIsCwdFile(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	missing := run(t, "decision", "brief")
	if missing.code != 0 || missing.stdout != "" || missing.stderr != "" {
		t.Fatalf("missing: %d %q %q", missing.code, missing.stdout, missing.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "sdf-telemetry.sqlite")); !os.IsNotExist(err) {
		t.Fatal("default brief created sdf-telemetry.sqlite")
	}

	dbPath := filepath.Join(dir, "sdf-telemetry.sqlite")
	seedBrief(t, dbPath)
	start := time.Now()
	out := run(t, "decision", "brief")
	elapsed := time.Since(start)
	if out.code != 0 || out.stderr != "" || !strings.Contains(out.stdout, "dont-push") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("brief took %s", elapsed)
	}
}

func seedBrief(t *testing.T, dbPath string) {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	add := func(id, statement, status, by string) {
		t.Helper()
		if _, err := decision.Add(db, decision.RecordInput{ID: id, Statement: statement, Status: status, DecidedBy: by}); err != nil {
			t.Fatal(err)
		}
	}
	add("keep-local", "Keep the store on this machine.", decision.StatusDecision, decision.DecidedByAgent)
	add("ship-it", "Push the branch now.", decision.StatusDecision, decision.DecidedByAgent)
	add("use-postgres", "Store telemetry in Postgres.", decision.StatusDecision, decision.DecidedByAgent)
	add("dont-push", "Do not push until the user says so.", decision.StatusDecision, decision.DecidedByUser)
	add("which-db", "Pick the store.", decision.StatusHypothesis, decision.DecidedByAgent)
	add("old-ban", "Do not push, ever.", decision.StatusDecision, decision.DecidedByUser)
	add("withdrawn", "Do not merge.", decision.StatusDecision, decision.DecidedByUser)
	add("maybe", "Maybe ban deploys.", decision.StatusHypothesis, decision.DecidedByUser)
	link := func(relation, from, to string) {
		t.Helper()
		if _, err := decision.Link(db, decision.EdgeInput{
			Relation: relation,
			From:     decision.Endpoint{Kind: decision.KindDecision, Ref: from},
			To:       decision.Endpoint{Kind: decision.KindDecision, Ref: to},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// keep-local constrains the locked rule. The target stays a constraint,
	// so this edge only marks keep-local itself as a constraint.
	link(decision.RelConstrains, "keep-local", "dont-push")
	// which-db is a hypothesis, so it is an open blocker and ship-it is blocked.
	link(decision.RelBlocks, "which-db", "ship-it")
	link(decision.RelSupersedes, "dont-push", "old-ban")
	link(decision.RelConflictsWith, "use-postgres", "ship-it")
	if _, err := decision.Retract(db, decision.RetractInput{ID: "withdrawn", RetractedBy: decision.DecidedByUser}); err != nil {
		t.Fatal(err)
	}
}
