package cli_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/cli"
	"github.com/paulpham157/sdf/internal/store"
)

func TestDecisionAddCategoryAndLogin(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	restore := cli.SetIdentityRunner(func(name string, args ...string) ([]byte, error) {
		if name == "gh" {
			return []byte(`{"login":"paulpham157"}`), nil
		}
		t.Errorf("unexpected %s %v", name, args)
		return nil, errIdentity
	})
	t.Cleanup(restore)

	omitted := run(t, "decision", "add", "--db", dbPath,
		"--id", "use-sqlite", "--statement", "Keep the collector store in SQLite.", "--status", "decision")
	if omitted.code != 0 || omitted.stderr != "" {
		t.Fatalf("omit: %d %q", omitted.code, omitted.stderr)
	}
	explicitEmpty := run(t, "decision", "add", "--db", dbPath,
		"--id", "no-one", "--statement", "Leave decided-by empty.", "--status", "decision", "--decided-by=")
	if explicitEmpty.code != 0 || explicitEmpty.stderr != "" {
		t.Fatalf("empty flag: %d %q", explicitEmpty.code, explicitEmpty.stderr)
	}
	login := run(t, "decision", "add", "--db", dbPath,
		"--id", "dont-push", "--statement", "Do not push until the user says so.",
		"--status", "decision", "--decided-by", " octocat ", "--category", "policy")
	if login.code != 0 || login.stderr != "" {
		t.Fatalf("login: %d %q", login.code, login.stderr)
	}
	badCat := run(t, "decision", "add", "--db", dbPath,
		"--id", "bad-cat", "--statement", "Nope.", "--status", "decision", "--category", "none")
	if badCat.code != 1 || badCat.stdout != "" || !strings.Contains(badCat.stderr, "category") {
		t.Fatalf("bad category: %d %q %q", badCat.code, badCat.stdout, badCat.stderr)
	}
	missing := run(t, "decision", "add", "--db", dbPath,
		"--id", "missing", "--statement", "Nope.", "--status", "decision", "--category")
	if missing.code != 2 {
		t.Fatalf("missing category value: %d %q", missing.code, missing.stderr)
	}
	policyHyp := run(t, "decision", "add", "--db", dbPath,
		"--id", "wait", "--statement", "Waiting for the user's OK to push.",
		"--status", "hypothesis", "--category", "policy")
	if policyHyp.code != 1 || !strings.Contains(policyHyp.stderr, `category "policy" requires status "decision"`) {
		t.Fatalf("policy hypothesis: %d %q", policyHyp.code, policyHyp.stderr)
	}
	badLogin := run(t, "decision", "add", "--db", dbPath,
		"--id", "bad-login", "--statement", "Nope.", "--status", "decision", "--decided-by", "a--b")
	if badLogin.code != 1 || badLogin.stdout != "" {
		t.Fatalf("bad login: %d %q %q", badLogin.code, badLogin.stdout, badLogin.stderr)
	}

	db := openStore(t, dbPath)
	rows, err := db.LoadDecisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rejected adds were stored: %+v", rows)
	}
	if rows[0].DecidedBy != "paulpham157" || rows[0].Category != "" {
		t.Fatalf("resolved %+v", rows[0])
	}
	if rows[1].DecidedBy != "" || rows[1].Category != "" {
		t.Fatalf("explicit empty %+v", rows[1])
	}
	if rows[2].DecidedBy != "octocat" || rows[2].Category != "policy" {
		t.Fatalf("explicit %+v", rows[2])
	}

	srv := serveStore(t, db)
	body := httpGet(t, srv.URL()+"api/decisions")
	list := run(t, "decision", "list", "--db", dbPath, "--json")
	if list.code != 0 || list.stdout != body {
		t.Fatalf("json differs\ncli:\n%s\napi:\n%s", list.stdout, body)
	}
	var doc struct {
		Decisions []map[string]any `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Decisions[0]["category"]; ok {
		t.Fatalf("unset category was present: %#v", doc.Decisions[0])
	}
	if doc.Decisions[0]["decided_by"] != "paulpham157" {
		t.Fatalf("%#v", doc.Decisions[0])
	}
	if _, ok := doc.Decisions[0]["decided_by_legacy"]; ok {
		t.Fatalf("login marked legacy: %#v", doc.Decisions[0])
	}
	if doc.Decisions[2]["category"] != "policy" || doc.Decisions[2]["decided_by"] != "octocat" {
		t.Fatalf("%#v", doc.Decisions[2])
	}

	text := run(t, "decision", "list", "--db", dbPath)
	if text.code != 0 || !strings.Contains(text.stdout, "CATEGORY") || !strings.Contains(text.stdout, "implementation") || !strings.Contains(text.stdout, "paulpham157") || strings.Contains(text.stdout, "paulpham157 (legacy)") {
		t.Fatalf("%s", text.stdout)
	}
	if !strings.Contains(text.stdout, "policy") {
		t.Fatalf("stored policy not shown:\n%s", text.stdout)
	}
}

func TestDecisionListCategoryFilter(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	seedTelemetry(t, dbPath)
	adds := [][]string{
		{"--id", "arch", "--statement", "Store telemetry in SQLite.", "--status", "decision", "--category", "architecture", "--decided-by", "user"},
		{"--id", "impl", "--statement", "The driver stays pure Go.", "--status", "decision", "--category", "implementation"},
		{"--id", "unset", "--statement", "Leave the category empty.", "--status", "hypothesis"},
		{"--id", "ops", "--statement", "Do not deploy on Friday.", "--status", "decision", "--category=operations"},
	}
	for _, args := range adds {
		out := run(t, append([]string{"decision", "add", "--db", dbPath}, args...)...)
		if out.code != 0 {
			t.Fatalf("%v: %s", args, out.stderr)
		}
	}
	db := openStore(t, dbPath)
	if err := db.InsertDecision(store.Decision{
		ID: "bogus", Statement: "A stored string outside the five.", Status: "decision",
		RecordedAt: "2026-11-27T00:00:00.000000+00:00", Category: "security",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	out := run(t, "decision", "link", "--db", dbPath, "--relation", "implements", "--from", "session:sess-1", "--to", "decision:unset")
	if out.code != 0 {
		t.Fatal(out.stderr)
	}
	out = run(t, "decision", "link", "--db", dbPath, "--relation", "refines", "--from", "decision:impl", "--to", "decision:arch")
	if out.code != 0 {
		t.Fatal(out.stderr)
	}

	impl := run(t, "decision", "list", "--db", dbPath, "--category", "implementation", "--json")
	if impl.code != 0 {
		t.Fatal(impl.stderr)
	}
	ids := decisionIDs(t, impl.stdout)
	if ids != "impl unset" {
		t.Fatalf("implementation %s\n%s", ids, impl.stdout)
	}
	none := run(t, "decision", "list", "--db", dbPath, "--category", "none", "--json")
	if none.code != 0 || decisionIDs(t, none.stdout) != "bogus" {
		t.Fatalf("none %s %s", decisionIDs(t, none.stdout), none.stderr)
	}
	both := run(t, "decision", "list", "--db", dbPath, "--session", "sess-1", "--category", "implementation", "--json")
	if both.code != 0 || decisionIDs(t, both.stdout) != "unset" {
		t.Fatalf("session+category %s\n%s", decisionIDs(t, both.stdout), both.stdout)
	}
	bad := run(t, "decision", "list", "--db", dbPath, "--category", "nope")
	if bad.code != 2 {
		t.Fatalf("bad filter %d %q", bad.code, bad.stderr)
	}
	missing := run(t, "decision", "list", "--db", dbPath, "--category")
	if missing.code != 2 {
		t.Fatalf("missing filter %d", missing.code)
	}
	text := run(t, "decision", "list", "--db", dbPath, "--category", "architecture")
	if text.code != 0 || !strings.Contains(text.stdout, "user (legacy)") || !strings.Contains(text.stdout, "architecture") {
		t.Fatalf("%s", text.stdout)
	}
}

func decisionIDs(t *testing.T, body string) string {
	t.Helper()
	var doc struct {
		Decisions []struct {
			ID string `json:"id"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(doc.Decisions))
	for i, d := range doc.Decisions {
		ids[i] = d.ID
	}
	return strings.Join(ids, " ")
}

var errIdentity = errString("identity")

type errString string

func (e errString) Error() string { return string(e) }
