package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
)

// seedDecisionGraph records two decisions and three edges through the CLI.
func seedDecisionGraph(t *testing.T, dbPath string) {
	t.Helper()
	seedTelemetry(t, dbPath)
	adds := [][]string{
		{"--id", "use-sqlite", "--statement", "Keep the collector store in SQLite.", "--status", "decision", "--source", "issue 168"},
		{"--id", "pure-go", "--statement", "The driver stays pure Go.", "--status", "hypothesis"},
	}
	for _, args := range adds {
		out := run(t, append([]string{"decision", "add", "--db", dbPath}, args...)...)
		if out.code != 0 {
			t.Fatalf("%v: %d %s", args, out.code, out.stderr)
		}
	}
	links := [][3]string{
		{"implements", "decision:pure-go", "decision:use-sqlite"},
		{"validates", "session:sess-1", "decision:use-sqlite"},
		{"validates", "commit:" + decisionSHA, "decision:use-sqlite"},
	}
	for _, l := range links {
		out := run(t, "decision", "link", "--db", dbPath, "--relation", l[0], "--from", l[1], "--to", l[2])
		if out.code != 0 {
			t.Fatalf("%v: %d %s", l, out.code, out.stderr)
		}
	}
}

func TestDecisionListPrintsDecisionsAndEdgesAsTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	seedDecisionGraph(t, dbPath)

	out := run(t, "decision", "list", "--db", dbPath)
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	graph, err := decision.Load(openStore(t, dbPath))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.stdout, "\n"), "\n")
	want := [][]string{
		{"Decisions: 2"},
		{"ID", "STATUS", "RECORDED AT", "SOURCE", "STATEMENT"},
		{"use-sqlite", "decision", graph.Decisions[0].RecordedAt, "issue 168", "Keep the collector store in SQLite."},
		{"pure-go", "hypothesis", graph.Decisions[1].RecordedAt, "-", "The driver stays pure Go."},
		{""},
		{"Edges: 3"},
		{"RELATION", "FROM", "TO", "RECORDED AT"},
		{"implements", "decision:pure-go", "decision:use-sqlite", graph.Edges[0].RecordedAt},
		{"validates", "session:sess-1", "decision:use-sqlite", graph.Edges[1].RecordedAt},
		{"validates", "commit:" + decisionSHA, "decision:use-sqlite", graph.Edges[2].RecordedAt},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), out.stdout)
	}
	for i, cells := range want {
		if got := strings.Fields(lines[i]); strings.Join(got, " ") != strings.Join(strings.Fields(strings.Join(cells, " ")), " ") {
			t.Fatalf("line %d = %q, want cells %q\n%s", i, lines[i], cells, out.stdout)
		}
	}
	// Every row of a table starts each column at the same offset.
	for _, block := range [][2]int{{1, 4}, {6, 10}} {
		col := strings.Index(lines[block[0]], "RECORDED AT")
		if block[0] == 6 {
			col = strings.Index(lines[block[0]], "TO")
		}
		for i := block[0] + 1; i < block[1]; i++ {
			row := lines[i]
			if col >= len(row) || row[col-1] != ' ' || row[col] == ' ' {
				t.Fatalf("column at %d not aligned in %q\n%s", col, row, out.stdout)
			}
		}
	}
}

func TestDecisionListJSONIsTheAPIResponse(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	seedDecisionGraph(t, dbPath)

	out := run(t, "decision", "list", "--db", dbPath, "--json")
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	srv := serveStore(t, openStore(t, dbPath))
	api := httpGet(t, srv.URL()+"api/decisions")
	if out.stdout != api {
		t.Fatalf("sdf decision list --json differs from GET /api/decisions\ncli:\n%s\napi:\n%s", out.stdout, api)
	}
	doc := decisionsDoc(t, out.stdout)
	if len(doc.Decisions) != 2 || len(doc.Edges) != 3 {
		t.Fatalf("%d decisions, %d edges", len(doc.Decisions), len(doc.Edges))
	}
}

func TestDecisionListOnAnEmptyOrMissingStore(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.sqlite")
	openStore(t, empty)
	missing := filepath.Join(dir, "missing.sqlite")

	for name, dbPath := range map[string]string{"empty": empty, "missing": missing} {
		out := run(t, "decision", "list", "--db", dbPath)
		if out.code != 0 || out.stdout != "No decisions in the store.\n" || out.stderr != "" {
			t.Fatalf("%s: %d %q %q", name, out.code, out.stdout, out.stderr)
		}
		asJSON := run(t, "decision", "list", "--db="+dbPath, "--json")
		if asJSON.code != 0 || asJSON.stderr != "" {
			t.Fatalf("%s json: %d %q", name, asJSON.code, asJSON.stderr)
		}
		srv := serveStore(t, openStore(t, dbPath))
		if api := httpGet(t, srv.URL()+"api/decisions"); asJSON.stdout != api {
			t.Fatalf("%s json %q, api %q", name, asJSON.stdout, api)
		}
		if asJSON.stdout != "{\n  \"decisions\": [],\n  \"edges\": []\n}\n" {
			t.Fatalf("%s json %q", name, asJSON.stdout)
		}
	}

	// sdf summary opens (and so creates) a missing store; list does the same.
	if _, err := os.Stat(missing); err != nil {
		t.Fatalf("missing store not opened like sdf summary: %v", err)
	}
	summary := run(t, "summary", "--db", filepath.Join(dir, "nope", "x.sqlite"))
	list := run(t, "decision", "list", "--db", filepath.Join(dir, "nope", "x.sqlite"))
	if summary.code != 1 || list.code != summary.code || list.stdout != "" || !strings.HasPrefix(list.stderr, "sdf decision list: ") {
		t.Fatalf("unopenable store: summary %d, list %d %q %q", summary.code, list.code, list.stdout, list.stderr)
	}
}

func TestDecisionListUsage(t *testing.T) {
	help := run(t, "decision", "list", "--help")
	if help.code != 0 || help.stdout != "usage: sdf decision list [--db PATH] [--json]\n" {
		t.Fatalf("%d %q", help.code, help.stdout)
	}
	top := run(t, "decision", "--help")
	if top.code != 0 || !strings.Contains(top.stdout, "<add|link|list>") {
		t.Fatalf("%d %q", top.code, top.stdout)
	}
	bad := run(t, "decision", "list", "--db")
	if bad.code != 2 || bad.stderr != "sdf decision list: --db requires a value\n" {
		t.Fatalf("%d %q", bad.code, bad.stderr)
	}
	unknown := run(t, "decision", "list", "--session", "x")
	if unknown.code != 2 || !strings.Contains(unknown.stderr, "unrecognized arguments: --session") {
		t.Fatalf("%d %q", unknown.code, unknown.stderr)
	}
}

func TestDecisionListDefaultsToTheServeStore(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	seedDecisionGraph(t, "sdf-telemetry.sqlite")
	out := run(t, "decision", "list", "--json")
	if out.code != 0 || len(decisionsDoc(t, out.stdout).Decisions) != 2 {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}
