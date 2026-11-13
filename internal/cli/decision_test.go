package cli_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/ui"
)

const decisionSHA = "0123456789abcdef0123456789abcdef01234567"

func TestDecisionAddAppendsOneRecordAndPrintsNothing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	seedTelemetry(t, dbPath)
	db := openStore(t, dbPath)
	srv := serveStore(t, db)
	before := httpGet(t, srv.URL()+"api/summary")

	out := run(t, "decision", "add", "--db", dbPath,
		"--id", "use-sqlite",
		"--statement", "Keep the collector store in SQLite.",
		"--status", "decision",
		"--source", "issue 168")
	if out.code != 0 || out.stdout != "" || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if httpGet(t, srv.URL()+"api/summary") != before {
		t.Fatal("summary changed after a decision was recorded")
	}
	doc := decisionsDoc(t, httpGet(t, srv.URL()+"api/decisions"))
	if len(doc.Decisions) != 1 || len(doc.Edges) != 0 {
		t.Fatalf("%d decisions, %d edges", len(doc.Decisions), len(doc.Edges))
	}
	got := doc.Decisions[0]
	if got.ID != "use-sqlite" || got.Statement != "Keep the collector store in SQLite." || got.Status != "decision" || got.Source != "issue 168" {
		t.Fatalf("%+v", got)
	}
	if got.RecordedAt == "" {
		t.Fatal("recorded_at empty")
	}

	again := run(t, "decision", "add", "--db", dbPath,
		"--id", "use-sqlite",
		"--statement", "A second statement.",
		"--status", "hypothesis")
	if again.code == 0 {
		t.Fatal("repeated id accepted")
	}
	if again.stdout != "" {
		t.Fatalf("stdout %q", again.stdout)
	}
	doc = decisionsDoc(t, httpGet(t, srv.URL()+"api/decisions"))
	if len(doc.Decisions) != 1 || doc.Decisions[0].Statement != "Keep the collector store in SQLite." {
		t.Fatalf("repeated id wrote: %+v", doc.Decisions)
	}
}

func TestDecisionAddOmitsAnEmptySource(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	out := run(t, "decision", "add", "--db", dbPath,
		"--id", "pure-go",
		"--statement", "The driver stays pure Go.",
		"--status", "hypothesis")
	if out.code != 0 || out.stdout != "" || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	db := openStore(t, dbPath)
	srv := serveStore(t, db)
	body := httpGet(t, srv.URL()+"api/decisions")
	var raw struct {
		Decisions []map[string]any `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Decisions) != 1 || raw.Decisions[0]["status"] != "hypothesis" {
		t.Fatalf("%#v", raw.Decisions)
	}
	if _, ok := raw.Decisions[0]["source"]; ok {
		t.Fatal("empty source was written")
	}
}

func TestDecisionAddRejectsABadStatusAndWritesNothing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	out := run(t, "decision", "add", "--db", dbPath,
		"--id", "use-sqlite",
		"--statement", "Keep the collector store in SQLite.",
		"--status", "assumption")
	if out.code == 0 || out.stdout != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	db := openStore(t, dbPath)
	got, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Decisions) != 0 || len(got.Edges) != 0 {
		t.Fatalf("rejected status was stored: %+v", got)
	}
}

func TestDecisionLinkAppendsOneEdgeAndPrintsNothing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	seedTelemetry(t, dbPath)
	for _, rec := range []struct{ id, status string }{
		{"use-sqlite", "decision"},
		{"pure-go", "hypothesis"},
	} {
		out := run(t, "decision", "add", "--db", dbPath,
			"--id", rec.id, "--statement", rec.id, "--status", rec.status)
		if out.code != 0 || out.stdout != "" {
			t.Fatalf("%s: %d %q %q", rec.id, out.code, out.stdout, out.stderr)
		}
	}

	cases := []struct {
		relation string
		from, to string
	}{
		{"implements", "decision:pure-go", "decision:use-sqlite"},
		{"validates", "session:sess-1", "decision:use-sqlite"},
		{"validates", "commit:" + strings.ToUpper(decisionSHA), "decision:use-sqlite"},
	}
	for _, item := range cases {
		out := run(t, "decision", "link", "--db", dbPath,
			"--relation", item.relation, "--from", item.from, "--to", item.to)
		if out.code != 0 || out.stdout != "" || out.stderr != "" {
			t.Fatalf("%s: %d %q %q", item.relation, out.code, out.stdout, out.stderr)
		}
	}
	db := openStore(t, dbPath)
	srv := serveStore(t, db)
	doc := decisionsDoc(t, httpGet(t, srv.URL()+"api/decisions"))
	if len(doc.Decisions) != 2 || len(doc.Edges) != 3 {
		t.Fatalf("%d decisions, %d edges", len(doc.Decisions), len(doc.Edges))
	}
	want := []struct{ relation, fromKind, fromRef, toRef string }{
		{"implements", "decision", "pure-go", "use-sqlite"},
		{"validates", "session", "sess-1", "use-sqlite"},
		{"validates", "commit", decisionSHA, "use-sqlite"},
	}
	for i, edge := range want {
		got := doc.Edges[i]
		if got.Relation != edge.relation || got.From.Kind != edge.fromKind || got.From.Ref != edge.fromRef || got.To.Kind != "decision" || got.To.Ref != edge.toRef {
			t.Fatalf("edge %d %+v", i, got)
		}
	}
}

func TestDecisionLinkRejectsABadRelationAndWritesNothing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	out := run(t, "decision", "add", "--db", dbPath,
		"--id", "use-sqlite", "--statement", "Recorded.", "--status", "decision")
	if out.code != 0 {
		t.Fatal(out.stderr)
	}
	linked := run(t, "decision", "link", "--db", dbPath,
		"--relation", "causes",
		"--from", "decision:use-sqlite",
		"--to", "decision:use-sqlite")
	if linked.code == 0 || linked.stdout != "" {
		t.Fatalf("%d %q %q", linked.code, linked.stdout, linked.stderr)
	}
	db := openStore(t, dbPath)
	got, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Edges) != 0 {
		t.Fatalf("rejected relation was stored: %+v", got.Edges)
	}
}

func TestDecisionLinkRejectsAnUnknownEndpointAndWritesNothing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	out := run(t, "decision", "add", "--db", dbPath,
		"--id", "use-sqlite", "--statement", "Recorded.", "--status", "decision")
	if out.code != 0 {
		t.Fatal(out.stderr)
	}
	cases := [][2]string{
		{"session:missing-session", "decision:use-sqlite"},
		{"commit:" + decisionSHA, "decision:use-sqlite"},
		{"decision:use-sqlite", "decision:never-recorded"},
	}
	for _, item := range cases {
		linked := run(t, "decision", "link", "--db", dbPath,
			"--relation", "validates", "--from", item[0], "--to", item[1])
		if linked.code == 0 || linked.stdout != "" {
			t.Fatalf("%s -> %s: %d %q %q", item[0], item[1], linked.code, linked.stdout, linked.stderr)
		}
	}
	db := openStore(t, dbPath)
	got, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Edges) != 0 {
		t.Fatalf("rejected endpoints were stored: %+v", got.Edges)
	}
}

type decisionJSON struct {
	ID         string `json:"id"`
	Statement  string `json:"statement"`
	Status     string `json:"status"`
	RecordedAt string `json:"recorded_at"`
	Source     string `json:"source"`
}

type endpointJSON struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type edgeJSON struct {
	Relation string       `json:"relation"`
	From     endpointJSON `json:"from"`
	To       endpointJSON `json:"to"`
}

type graphJSON struct {
	Decisions []decisionJSON `json:"decisions"`
	Edges     []edgeJSON     `json:"edges"`
}

func decisionsDoc(t *testing.T, body string) graphJSON {
	t.Helper()
	var doc graphJSON
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func seedTelemetry(t *testing.T, dbPath string) {
	t.Helper()
	db := openStore(t, dbPath)
	if _, err := db.Insert([]record.Record{{
		Signal:     "logs",
		SessionID:  record.Str("sess-1"),
		Name:       record.Str("sdf.git.commit"),
		Attributes: map[string]any{"vcs.ref.head.revision": decisionSHA},
		Resource:   map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
}

func openStore(t *testing.T, dbPath string) *store.Store {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func serveStore(t *testing.T, db *store.Store) *ui.Server {
	t.Helper()
	srv, err := ui.Start(ui.Config{Addr: "127.0.0.1:0", Store: db})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv
}
