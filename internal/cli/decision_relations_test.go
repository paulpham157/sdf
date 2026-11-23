package cli_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// seedRelationGraph records a small graph that uses every new relation and
// returns the store path.
func seedRelationGraph(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	seedTelemetry(t, dbPath)
	adds := [][]string{
		{"--id", "which-db", "--statement", "Pick the store: SQLite or Postgres.", "--status", "hypothesis", "--decided-by", "agent"},
		{"--id", "design-schema", "--statement", "Design the rejections schema.", "--status", "decision", "--decided-by", "agent"},
		{"--id", "use-sqlite", "--statement", "Keep the store in SQLite.", "--status", "decision", "--decided-by", "user"},
		{"--id", "use-postgres", "--statement", "Move the store to Postgres.", "--status", "decision", "--decided-by", "agent"},
		{"--id", "dont-push", "--statement", "Do not push without an OK.", "--status", "decision", "--decided-by", "user"},
		{"--id", "push-after-ok", "--statement", "Push feature branches once the user says OK.", "--status", "decision", "--decided-by", "user"},
	}
	for _, args := range adds {
		if out := run(t, append([]string{"decision", "add", "--db", dbPath}, args...)...); out.code != 0 {
			t.Fatalf("%v: %d %s", args, out.code, out.stderr)
		}
	}
	links := [][3]string{
		{"depends_on", "decision:design-schema", "decision:which-db"},
		{"conflicts_with", "decision:use-sqlite", "decision:use-postgres"},
		{"refines", "decision:push-after-ok", "decision:dont-push"},
		{"contradicts", "session:sess-1", "decision:use-postgres"},
	}
	for _, l := range links {
		if out := run(t, "decision", "link", "--db", dbPath, "--relation", l[0], "--from", l[1], "--to", l[2]); out.code != 0 || out.stdout != "" || out.stderr != "" {
			t.Fatalf("%v: %d %q %q", l, out.code, out.stdout, out.stderr)
		}
	}
	return dbPath
}

func TestDecisionListShowsBlockedConflictedAndRefined(t *testing.T) {
	dbPath := seedRelationGraph(t)
	out := run(t, "decision", "list", "--db", dbPath)
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q", out.code, out.stderr)
	}
	rows := map[string][]string{}
	var extras []string
	for _, line := range strings.Split(out.stdout, "\n") {
		f := strings.Fields(line)
		if len(f) > 2 && (f[1] == "decision" || f[1] == "hypothesis") {
			rows[f[0]] = f
		}
		if strings.HasPrefix(strings.TrimSpace(line), "blocked by:") || strings.HasPrefix(strings.TrimSpace(line), "conflicts with:") ||
			strings.HasPrefix(strings.TrimSpace(line), "refined by:") {
			extras = append(extras, strings.TrimSpace(line))
		}
	}
	wantEffective := map[string]string{
		"which-db":      "active",
		"design-schema": "active, blocked",
		"use-sqlite":    "active, conflicted",
		"use-postgres":  "active, conflicted, challenged",
		"dont-push":     "active",
		"push-after-ok": "active",
	}
	for id, want := range wantEffective {
		if !strings.Contains(out.stdout, id+"  ") || !strings.Contains(strings.Join(rows[id], " "), want) {
			t.Fatalf("%s: row %q, want EFFECTIVE %q\n%s", id, rows[id], want, out.stdout)
		}
	}
	wantExtras := []string{
		"blocked by: which-db (open: which-db)",
		"conflicts with: use-postgres",
		"conflicts with: use-sqlite",
		"refined by: push-after-ok",
	}
	if !reflect.DeepEqual(extras, wantExtras) {
		t.Fatalf("extra lines %q, want %q\n%s", extras, wantExtras, out.stdout)
	}
	// The alias and the symmetric relation are stored canonically.
	for _, want := range []string{
		"blocks          decision:which-db       decision:design-schema",
		"conflicts_with  decision:use-postgres   decision:use-sqlite",
	} {
		if !strings.Contains(out.stdout, want) {
			t.Fatalf("missing edge %q\n%s", want, out.stdout)
		}
	}
}

func TestDecisionListJSONAndAPICarryTheDerivedRelations(t *testing.T) {
	dbPath := seedRelationGraph(t)
	listed := run(t, "decision", "list", "--db", dbPath, "--json")
	if listed.code != 0 {
		t.Fatal(listed.stderr)
	}
	srv := serveStore(t, openStore(t, dbPath))
	api := httpGet(t, srv.URL()+"api/decisions")
	if api != listed.stdout {
		t.Fatalf("API and --json differ:\n%s\n---\n%s", api, listed.stdout)
	}
	var doc struct {
		Decisions []map[string]any `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(api), &doc); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, d := range doc.Decisions {
		byID[d["id"].(string)] = d
	}
	check := func(id, key string, want any) {
		t.Helper()
		got, ok := byID[id][key]
		if want == nil {
			if ok {
				t.Fatalf("%s.%s = %v, want absent", id, key, got)
			}
			return
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s.%s = %#v, want %#v", id, key, got, want)
		}
	}
	check("design-schema", "blocked", true)
	check("design-schema", "blocked_by", []any{"which-db"})
	check("design-schema", "open_blockers", []any{"which-db"})
	check("design-schema", "effective_status", "active")
	check("use-sqlite", "conflicted", true)
	check("use-sqlite", "conflicts_with", []any{"use-postgres"})
	check("use-postgres", "challenged", true)
	check("dont-push", "refined_by", []any{"push-after-ok"})
	check("which-db", "blocked", nil)
	check("which-db", "conflicted", nil)

	// Settling the hypothesis clears the block in the API.
	if out := run(t, "decision", "add", "--db", dbPath, "--id", "settled-sqlite", "--statement", "SQLite it is.", "--status", "decision", "--decided-by", "user"); out.code != 0 {
		t.Fatal(out.stderr)
	}
	if out := run(t, "decision", "link", "--db", dbPath, "--relation", "supersedes", "--from", "decision:settled-sqlite", "--to", "decision:which-db"); out.code != 0 {
		t.Fatal(out.stderr)
	}
	doc.Decisions = nil
	if err := json.Unmarshal([]byte(httpGet(t, srv.URL()+"api/decisions")), &doc); err != nil {
		t.Fatal(err)
	}
	for _, d := range doc.Decisions {
		if d["id"] == "design-schema" {
			if _, ok := d["blocked"]; ok {
				t.Fatalf("still blocked after settling: %v", d)
			}
			if _, ok := d["open_blockers"]; ok {
				t.Fatalf("open_blockers after settling: %v", d)
			}
		}
	}
}

func TestDecisionLinkRelationRulesFromTheCLI(t *testing.T) {
	dbPath := seedRelationGraph(t)
	link := func(rel, from, to string) result {
		return run(t, "decision", "link", "--db", dbPath, "--relation", rel, "--from", from, "--to", to)
	}
	rejected := []struct{ rel, from, to, stderr string }{
		{"blocks", "decision:design-schema", "decision:which-db",
			"sdf decision link: blocks cycle: decision:which-db already reaches decision:design-schema through blocks (which-db -> design-schema), so decision:design-schema cannot block decision:which-db\n"},
		{"blocked_by", "decision:design-schema", "decision:which-db",
			"sdf decision link: edge already recorded: decision:which-db blocks decision:design-schema\n"},
		{"conflicts_with", "decision:use-postgres", "decision:use-sqlite",
			"sdf decision link: edge already recorded: decision:use-postgres conflicts_with decision:use-sqlite\n"},
		{"refines", "decision:dont-push", "decision:push-after-ok",
			"sdf decision link: refines cycle: decision:push-after-ok already reaches decision:dont-push through refines (push-after-ok -> dont-push), so decision:dont-push cannot refine decision:push-after-ok\n"},
		{"blocks", "session:sess-1", "decision:use-sqlite",
			"sdf decision link: blocks links a decision to a decision: from is session:sess-1\n"},
		{"implements", "decision:use-sqlite", "decision:dont-push",
			"sdf decision link: implements links a session or commit to a decision: from is decision:use-sqlite\n"},
		{"validates", "decision:use-sqlite", "session:sess-1",
			"sdf decision link: validates points to a decision: to is session:sess-1\n"},
		{"constrains", "session:sess-1", "decision:use-sqlite",
			"sdf decision link: constrains starts from a decision: from is session:sess-1\n"},
	}
	for _, c := range rejected {
		out := link(c.rel, c.from, c.to)
		if out.code != 1 || out.stdout != "" || out.stderr != c.stderr {
			t.Fatalf("%s %s %s: %d %q\nwant stderr %q", c.from, c.rel, c.to, out.code, out.stderr, c.stderr)
		}
	}
	warned := link("blocks", "decision:use-sqlite", "decision:use-postgres")
	if warned.code != 0 || warned.stderr != "sdf decision link: warning: decision:use-sqlite and decision:use-postgres also have a conflicts_with edge\n" {
		t.Fatalf("%d %q", warned.code, warned.stderr)
	}
}
