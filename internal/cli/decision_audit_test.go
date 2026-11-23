package cli_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

// Commit SHAs and agent session ids shaped like real ones: a Codex thread id
// (CODEX_THREAD_ID), a Grok CLI session id (GROK_SESSION_ID), and a Claude
// Code session.id.
const (
	auditCodexSession  = "019a8f3e-7c2b-7d41-9a55-3be1c0d2e4f6"
	auditGrokSession   = "grok-7f3c2a9e-51d0-4b8e-a6c1-0d9e8f7a6b5c"
	auditClaudeSession = "5d1c9b7e-2a4f-4e3b-8c6d-9f0a1b2c3d4e"
	auditSHACodex      = "3f2a9c1e5b7d4a6c8e0f1a2b3c4d5e6f7a8b9c0d"
	auditSHAGrok       = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	auditSHANoSession  = "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a291807"
	auditSHAClaude     = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
	auditSHACodex2     = "5b6c7d8e9f0a1b2c3d4e5f60718293a4b5c6d7e8"
)

// hookPayload is the OTLP/JSON body the post-commit hook from
// `sdf init --git-hooks` posts (internal/setup/githook_script.go): an
// sdf.git.commit log with session.id (only when one agent's session variable
// is set), sdf.git.agent, vcs.ref.head.revision, sdf.git.repo and
// sdf.git.branch.
func hookPayload(service, agent, session, sha, repo, branch string, at time.Time) []byte {
	sess := ""
	if session != "" {
		sess = fmt.Sprintf(`{"key":"session.id","value":{"stringValue":%q}},`, session)
	}
	return []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"` + service + `"}},{"key":"sdf.git.hook","value":{"stringValue":"1"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"` +
		fmt.Sprint(at.UnixNano()) + `","body":{"stringValue":"sdf.git.commit"},"attributes":[{"key":"event.name","value":{"stringValue":"sdf.git.commit"}},` + sess +
		`{"key":"sdf.git.agent","value":{"stringValue":"` + agent + `"}},{"key":"vcs.ref.head.revision","value":{"stringValue":"` + sha +
		`"}},{"key":"sdf.git.repo","value":{"stringValue":"` + repo + `"}},{"key":"sdf.git.branch","value":{"stringValue":"` + branch +
		`"}},{"key":"sdf.git.hook","value":{"stringValue":"1"}}]}]}]}]}`)
}

// ingest decodes OTLP/JSON log bodies the way the receiver does and stores
// them, then closes the store so the file holds every row.
func ingest(t *testing.T, dbPath string, bodies [][]byte, extra ...record.Record) {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var recs []record.Record
	for _, b := range bodies {
		got, err := otlp.Decode("logs", b, "application/json")
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, got...)
	}
	recs = append(recs, extra...)
	if _, err := db.Insert(recs); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, args ...string) {
	t.Helper()
	if out := run(t, args...); out.code != 0 {
		t.Fatalf("%v: %d %s", args, out.code, out.stderr)
	}
}

var auditBase = time.Date(2026, 11, 20, 14, 30, 0, 0, time.UTC)

// seedAuditStore builds one store with every finding kind:
//   - commits: two Codex commits whose session implements use-sqlite, one
//     of them with its own implements edge (linked) and one without,
//     a Grok CLI commit whose session has no edge (finding b), a Claude Code
//     commit stored with no session (counted, never a finding), and a Claude Code
//     tool_result commit with an implements edge of its own (linked);
//   - decisions: use-sqlite and pure-go are implemented; store-columns is
//     active with no implements edge (finding a); store-json is superseded
//     by store-columns; drop-cgo is retracted;
//   - edges: validates session -> store-json and validates commit ->
//     drop-cgo point at a superseded and a retracted decision (finding c).
func seedAuditStore(t *testing.T, dbPath string) {
	t.Helper()
	claude := record.Record{
		Signal:       "logs",
		ServiceName:  record.Str("claude-code"),
		SessionID:    record.Str(auditClaudeSession),
		Name:         record.Str("claude_code.tool_result"),
		TimeUnixNano: int64Ptr(auditBase.Add(40 * time.Minute).UnixNano()),
		Attributes: map[string]any{
			"event.name": "tool_result", "session.id": auditClaudeSession, "tool_name": "Bash",
			"success": "true", "duration_ms": "412", "sdf.git_commit_id": auditSHAClaude,
		},
		Resource: map[string]any{"service.name": "claude-code"},
	}
	// The git hook posts only when an agent session is set, so a commit with
	// no session comes from a Claude Code tool_result log that carried no
	// session.id.
	noSession := record.Record{
		Signal:       "logs",
		ServiceName:  record.Str("claude-code"),
		Name:         record.Str("claude_code.tool_result"),
		TimeUnixNano: int64Ptr(auditBase.Add(20 * time.Minute).UnixNano()),
		Attributes: map[string]any{
			"event.name": "tool_result", "tool_name": "Bash", "success": "true", "duration_ms": "388",
			"sdf.git_commit_id": auditSHANoSession,
		},
		Resource: map[string]any{"service.name": "claude-code"},
	}
	ingest(t, dbPath, [][]byte{
		hookPayload("codex_exec", "codex", auditCodexSession, auditSHACodex, "/home/dev/sdf", "feat/store", auditBase),
		hookPayload("grok-cli", "grok-cli", auditGrokSession, auditSHAGrok, "/home/dev/sdf", "fix/wal", auditBase.Add(10*time.Minute)),
		hookPayload("codex_exec", "codex", auditCodexSession, auditSHACodex2, "/home/dev/sdf", "feat/store", auditBase.Add(30*time.Minute)),
	}, claude, noSession)
	add := func(id, status, statement string) {
		mustRun(t, "decision", "add", "--db", dbPath, "--id", id, "--status", status, "--statement", statement)
	}
	link := func(rel, from, to string) {
		mustRun(t, "decision", "link", "--db", dbPath, "--relation", rel, "--from", from, "--to", to)
	}
	add("use-sqlite", "decision", "Keep the collector store in SQLite.")
	add("pure-go", "decision", "The SQLite driver stays pure Go.")
	add("store-json", "decision", "Store attributes as one JSON column.")
	add("store-columns", "decision", "Store hot attributes in their own columns.")
	add("drop-cgo", "hypothesis", "CGO can be dropped from release builds.")
	link("implements", "session:"+auditCodexSession, "decision:use-sqlite")
	link("implements", "commit:"+auditSHAClaude, "decision:pure-go")
	link("validates", "session:"+auditCodexSession, "decision:store-json")
	link("validates", "commit:"+auditSHACodex, "decision:drop-cgo")
	link("implements", "commit:"+auditSHACodex2, "decision:use-sqlite")
	link("supersedes", "decision:store-columns", "decision:store-json")
	mustRun(t, "decision", "retract", "--db", dbPath, "--id", "drop-cgo", "--reason", "release builds still need CGO on Windows", "--retracted-by", "user")
}

// snapshotDir returns every file in dir with its bytes.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

// assertStoreUnchanged checks that every file that existed keeps its
// bytes and that no file appeared, except what a WAL reader leaves: SQLite
// opens a WAL database in mode=ro by creating -shm (shared memory, not
// data) and an empty -wal beside it when the directory is writable, as
// sdf decision list and sdf summary do. An empty -wal holds no write.
func assertStoreUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	for name, data := range before {
		if after[name] != data {
			t.Fatalf("%s changed: %d bytes, was %d", name, len(after[name]), len(data))
		}
	}
	for name, data := range after {
		if _, ok := before[name]; ok {
			continue
		}
		switch {
		case strings.HasSuffix(name, ".sqlite-shm"):
		case strings.HasSuffix(name, ".sqlite-wal") && data == "":
		default:
			t.Fatalf("sdf decision audit created %s (%d bytes)", name, len(data))
		}
	}
}

type auditDoc struct {
	Schema       string            `json:"schema"`
	Window       map[string]string `json:"window"`
	Totals       map[string]int    `json:"totals"`
	Commits      []map[string]any  `json:"commits"`
	FindingCount int               `json:"finding_count"`
	Findings     struct {
		ActiveWithoutImplements []map[string]any `json:"active_without_implements"`
		UnlinkedSessionCommits  []map[string]any `json:"unlinked_session_commits"`
		StaleEdges              []map[string]any `json:"stale_edges"`
	} `json:"findings"`
}

func auditJSON(t *testing.T, args ...string) (auditDoc, result) {
	t.Helper()
	out := run(t, append([]string{"decision", "audit", "--json"}, args...)...)
	var doc auditDoc
	if err := json.Unmarshal([]byte(out.stdout), &doc); err != nil {
		t.Fatalf("not JSON (%v): %d %q %q", err, out.code, out.stdout, out.stderr)
	}
	return doc, out
}

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestDecisionAuditReportsEveryFindingKind(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "telemetry.sqlite")
	seedAuditStore(t, dbPath)

	doc, out := auditJSON(t, "--db", dbPath)
	if out.code != 1 || out.stderr != "" {
		t.Fatalf("findings must exit 1: %d %q", out.code, out.stderr)
	}
	if doc.Schema != "sdf.decision.audit/v1" {
		t.Fatalf("schema %q", doc.Schema)
	}
	var top map[string]any
	if err := json.Unmarshal([]byte(out.stdout), &top); err != nil {
		t.Fatal(err)
	}
	if got := keysOf(top); !reflect.DeepEqual(got, []string{"commits", "finding_count", "findings", "schema", "totals", "window"}) {
		t.Fatalf("top-level keys %v", got)
	}
	if got := keysOf(top["findings"].(map[string]any)); !reflect.DeepEqual(got, []string{"active_without_implements", "stale_edges", "unlinked_session_commits"}) {
		t.Fatalf("findings keys %v", got)
	}
	wantTotals := map[string]int{
		"commits_total": 5, "commits_with_session": 4, "commits_in_decision_sessions": 2, "commits_linked": 1,
		"decisions_active": 3, "decisions_active_with_implements": 2, "edges_checked": 5,
	}
	if !reflect.DeepEqual(doc.Totals, wantTotals) {
		t.Fatalf("totals = %v, want %v", doc.Totals, wantTotals)
	}
	if doc.FindingCount != 4 {
		t.Fatalf("finding_count = %d\n%s", doc.FindingCount, out.stdout)
	}
	ts := func(m time.Duration) string { return auditBase.Add(m).Format("2006-01-02T15:04:05.000000-07:00") }
	wantCommits := []map[string]any{
		{"sha": auditSHACodex, "session_id": auditCodexSession, "source": "event", "timestamp": ts(0), "session_has_decision": true, "linked": false},
		{"sha": auditSHAGrok, "session_id": auditGrokSession, "source": "event", "timestamp": ts(10 * time.Minute), "session_has_decision": false, "linked": false},
		{"sha": auditSHACodex2, "session_id": auditCodexSession, "source": "event", "timestamp": ts(30 * time.Minute), "session_has_decision": true, "linked": true},
		{"sha": auditSHAClaude, "session_id": auditClaudeSession, "source": "event", "timestamp": ts(40 * time.Minute), "session_has_decision": false, "linked": false},
	}
	if !reflect.DeepEqual(doc.Commits, wantCommits) {
		t.Fatalf("commits = %v\nwant %v", doc.Commits, wantCommits)
	}

	a := doc.Findings.ActiveWithoutImplements
	if len(a) != 1 || a[0]["id"] != "store-columns" || a[0]["status"] != "decision" ||
		a[0]["statement"] != "Store hot attributes in their own columns." || a[0]["recorded_at"] == "" {
		t.Fatalf("active_without_implements = %v", a)
	}
	if got := keysOf(a[0]); !reflect.DeepEqual(got, []string{"id", "recorded_at", "statement", "status"}) {
		t.Fatalf("decision finding keys %v", got)
	}

	b := doc.Findings.UnlinkedSessionCommits
	want := map[string]any{
		"sha": auditSHAGrok, "session_id": auditGrokSession, "timestamp": "2026-11-20T14:40:00.000000+00:00",
		"source": "event", "event": "sdf.git.commit", "repo": "/home/dev/sdf", "branch": "fix/wal",
	}
	if len(b) != 1 || !reflect.DeepEqual(b[0], want) {
		t.Fatalf("unlinked_session_commits = %v, want [%v]", b, want)
	}

	c := doc.Findings.StaleEdges
	if len(c) != 2 {
		t.Fatalf("stale_edges = %v", c)
	}
	type stale struct{ rel, from, to, end, id, status string }
	var got []stale
	for _, e := range c {
		from, to := e["from"].(map[string]any), e["to"].(map[string]any)
		ends := e["stale"].([]any)
		if len(ends) != 1 {
			t.Fatalf("stale ends %v", ends)
		}
		end := ends[0].(map[string]any)
		got = append(got, stale{e["relation"].(string), from["kind"].(string) + ":" + from["ref"].(string),
			to["kind"].(string) + ":" + to["ref"].(string), end["end"].(string), end["id"].(string), end["effective_status"].(string)})
		if got := keysOf(e); !reflect.DeepEqual(got, []string{"from", "recorded_at", "relation", "stale", "to"}) {
			t.Fatalf("edge finding keys %v", got)
		}
	}
	wantStale := []stale{
		{"validates", "session:" + auditCodexSession, "decision:store-json", "to", "store-json", "superseded"},
		{"validates", "commit:" + auditSHACodex, "decision:drop-cgo", "to", "drop-cgo", "retracted"},
	}
	if !reflect.DeepEqual(got, wantStale) {
		t.Fatalf("stale edges = %v, want %v", got, wantStale)
	}
	if by := c[0]["stale"].([]any)[0].(map[string]any)["superseded_by"]; !reflect.DeepEqual(by, []any{"store-columns"}) {
		t.Fatalf("superseded_by %v", by)
	}

	text := run(t, "decision", "audit", "--db", dbPath)
	if text.code != 1 || text.stderr != "" {
		t.Fatalf("text: %d %q", text.code, text.stderr)
	}
	for _, line := range []string{
		"Findings: 4",
		"Active decisions with no implements edge: 1 of 3",
		"  decision:store-columns  decision  ",
		"Commits whose session is not linked to a decision: 1 of 4 with a session",
		"  commit:" + auditSHAGrok + "  session:" + auditGrokSession + "  2026-11-20T14:40:00.000000+00:00  (event sdf.git.commit)",
		"Edges at a retracted or superseded decision: 2 of 5 checked",
		"(to store-json superseded by store-columns)",
		"(to drop-cgo retracted)",
	} {
		if !strings.Contains(text.stdout, line) {
			t.Fatalf("text output lacks %q:\n%s", line, text.stdout)
		}
	}
}

func TestDecisionAuditCleanStoreExitsZero(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	ingest(t, dbPath, [][]byte{
		hookPayload("codex_exec", "codex", auditCodexSession, auditSHACodex, "/home/dev/sdf", "feat/store", auditBase),
	})
	mustRun(t, "decision", "add", "--db", dbPath, "--id", "use-sqlite", "--status", "decision", "--statement", "Keep the collector store in SQLite.")
	mustRun(t, "decision", "link", "--db", dbPath, "--relation", "implements", "--from", "session:"+auditCodexSession, "--to", "decision:use-sqlite")

	// The session's link alone clears the finding; the commit is not yet linked.
	doc, out := auditJSON(t, "--db", dbPath)
	if out.code != 0 || doc.Totals["commits_in_decision_sessions"] != 1 || doc.Totals["commits_linked"] != 0 ||
		len(doc.Commits) != 1 || doc.Commits[0]["session_has_decision"] != true || doc.Commits[0]["linked"] != false {
		t.Fatalf("session linked only: %d %s", out.code, out.stdout)
	}
	mustRun(t, "decision", "link", "--db", dbPath, "--relation", "implements", "--from", "commit:"+auditSHACodex, "--to", "decision:use-sqlite")

	doc, out = auditJSON(t, "--db", dbPath)
	if out.code != 0 || out.stderr != "" || doc.FindingCount != 0 {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if doc.Findings.ActiveWithoutImplements == nil || doc.Findings.UnlinkedSessionCommits == nil || doc.Findings.StaleEdges == nil ||
		!strings.Contains(out.stdout, `"active_without_implements": []`) || !strings.Contains(out.stdout, `"unlinked_session_commits": []`) ||
		!strings.Contains(out.stdout, `"stale_edges": []`) {
		t.Fatalf("empty finding lists must be [] not null:\n%s", out.stdout)
	}
	if !strings.Contains(out.stdout, `"window": {}`) {
		t.Fatalf("unbounded window must be {}:\n%s", out.stdout)
	}
	wantTotals := map[string]int{
		"commits_total": 1, "commits_with_session": 1, "commits_in_decision_sessions": 1, "commits_linked": 1,
		"decisions_active": 1, "decisions_active_with_implements": 1, "edges_checked": 2,
	}
	if !reflect.DeepEqual(doc.Totals, wantTotals) {
		t.Fatalf("totals %v", doc.Totals)
	}
	if len(doc.Commits) != 1 || doc.Commits[0]["linked"] != true || doc.Commits[0]["sha"] != auditSHACodex {
		t.Fatalf("commits %v", doc.Commits)
	}
	text := run(t, "decision", "audit", "--db", dbPath)
	if text.code != 0 || text.stdout != "No findings: 1 active decisions, 1 commits (1 with a session, 1 in a decision session, 1 linked), 2 edges checked.\n" {
		t.Fatalf("%d %q", text.code, text.stdout)
	}
}

func TestDecisionAuditIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "telemetry.sqlite")
	seedAuditStore(t, dbPath)
	before := snapshotDir(t, dir)
	for _, args := range [][]string{{"--db", dbPath}, {"--db", dbPath, "--json"}, {"--db", dbPath, "--since", "2026-11-20T14:35:00Z"}} {
		if out := run(t, append([]string{"decision", "audit"}, args...)...); out.code != 1 {
			t.Fatalf("%v: %d %q", args, out.code, out.stderr)
		}
	}
	assertStoreUnchanged(t, before, snapshotDir(t, dir))

	// A file older than the decision tables is read without being migrated.
	old := filepath.Join(dir, "old.sqlite")
	ingest(t, old, [][]byte{hookPayload("grok-cli", "grok-cli", auditGrokSession, auditSHAGrok, "/home/dev/sdf", "fix/wal", auditBase)})
	raw, err := sql.Open("sqlite", old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE decision_retractions; DROP TABLE decision_edges; DROP TABLE decisions`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	oldBefore := snapshotDir(t, dir)
	doc, out := auditJSON(t, "--db", old)
	if out.code != 1 || len(doc.Findings.UnlinkedSessionCommits) != 1 {
		t.Fatalf("old store: %d %q", out.code, out.stdout)
	}
	assertStoreUnchanged(t, oldBefore, snapshotDir(t, dir))
}

func TestDecisionAuditMissingStoreAndExitCodes(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.sqlite")
	doc, out := auditJSON(t, "--db="+missing)
	if out.code != 0 || out.stderr != "" || doc.FindingCount != 0 || doc.Totals["commits_total"] != 0 {
		t.Fatalf("missing file: %d %q %q", out.code, out.stdout, out.stderr)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("sdf decision audit created the missing store: %v", err)
	}
	// Errors exit 3, distinct from findings (1) and usage (2).
	if out := run(t, "decision", "audit", "--db", filepath.Join(dir, "no-such-dir", "x.sqlite")); out.code != 3 || out.stdout != "" {
		t.Fatalf("missing directory: %d %q %q", out.code, out.stdout, out.stderr)
	}
	notDB := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notDB, []byte("not a database, just some notes about the store\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := run(t, "decision", "audit", "--db", notDB); out.code != 3 {
		t.Fatalf("not a database: %d %q", out.code, out.stderr)
	}
	for _, args := range [][]string{
		{"--bogus"}, {"--db"}, {"--since", "yesterday"}, {"--until=2026-11-20"}, {"--json=yes"},
		{"--since", "2026-11-21T00:00:00Z", "--until", "2026-11-20T00:00:00Z"},
	} {
		if out := run(t, append([]string{"decision", "audit", "--db", missing}, args...)...); out.code != 2 {
			t.Fatalf("%v: %d %q", args, out.code, out.stderr)
		}
	}
	if out := run(t, "decision", "audit", "--help"); out.code != 0 || !strings.Contains(out.stdout, "sdf decision audit") {
		t.Fatalf("help: %d %q", out.code, out.stdout)
	}
	if out := run(t, "decision", "--help"); !strings.Contains(out.stdout, "audit") {
		t.Fatalf("decision usage lacks audit: %q", out.stdout)
	}
}

// TestDecisionAuditWindow checks --since (inclusive) and --until
// (exclusive) against decision recorded_at, commit event time, and edge
// recorded_at, while links are read from every edge.
func TestDecisionAuditWindow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	before := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	after := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	ingest(t, dbPath, [][]byte{
		hookPayload("grok-cli", "grok-cli", auditGrokSession, auditSHAGrok, "/home/dev/sdf", "fix/wal", before),
		hookPayload("codex_exec", "codex", auditCodexSession, auditSHACodex, "/home/dev/sdf", "feat/store", after),
	})
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	stamp := func(t time.Time) string { return t.Format("2006-01-02T15:04:05.000000-07:00") }
	for _, d := range []store.Decision{
		{ID: "old-choice", Statement: "Poll the store every second.", Status: "decision", RecordedAt: stamp(before)},
		{ID: "new-choice", Statement: "Push store changes to the UI.", Status: "decision", RecordedAt: stamp(after)},
	} {
		if err := db.InsertDecision(d); err != nil {
			t.Fatal(err)
		}
	}
	// The Codex session's link is recorded after the window it is audited in.
	edge := store.DecisionEdge{Relation: "implements", FromKind: "session", FromRef: auditCodexSession, ToKind: "decision", ToRef: "new-choice",
		RecordedAt: stamp(after.Add(48 * time.Hour))}
	if err := db.AppendDecisionEdge(edge, func(store.DecisionGraph) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	split := "2026-11-21T00:00:00Z"

	early, out := auditJSON(t, "--db", dbPath, "--until", split)
	if out.code != 1 || early.Window["until"] != split || early.Window["since"] != "" {
		t.Fatalf("until: %d %q", out.code, out.stdout)
	}
	if early.Totals["decisions_active"] != 1 || early.Totals["commits_total"] != 1 || early.Totals["commits_in_decision_sessions"] != 0 || early.Totals["edges_checked"] != 0 ||
		len(early.Commits) != 1 || early.Commits[0]["sha"] != auditSHAGrok ||
		len(early.Findings.ActiveWithoutImplements) != 1 || early.Findings.ActiveWithoutImplements[0]["id"] != "old-choice" ||
		len(early.Findings.UnlinkedSessionCommits) != 1 || early.Findings.UnlinkedSessionCommits[0]["sha"] != auditSHAGrok {
		t.Fatalf("until window: %s", out.stdout)
	}

	late, out := auditJSON(t, "--db", dbPath, "--since", split)
	if out.code != 0 || late.Window["since"] != split {
		t.Fatalf("since: %d %q", out.code, out.stdout)
	}
	wantLate := map[string]int{
		"commits_total": 1, "commits_with_session": 1, "commits_in_decision_sessions": 1, "commits_linked": 0,
		"decisions_active": 1, "decisions_active_with_implements": 1, "edges_checked": 1,
	}
	if !reflect.DeepEqual(late.Totals, wantLate) {
		t.Fatalf("since totals %v", late.Totals)
	}
	if len(late.Commits) != 1 || late.Commits[0]["sha"] != auditSHACodex || late.Commits[0]["session_has_decision"] != true {
		t.Fatalf("since commits %v", late.Commits)
	}
	// since is inclusive: a bound equal to the commit time keeps it, and an
	// until equal to it drops it.
	at := after.Format(time.RFC3339)
	if doc, _ := auditJSON(t, "--db", dbPath, "--since", at); doc.Totals["commits_total"] != 1 {
		t.Fatalf("since == commit time: %v", doc.Totals)
	}
	if doc, _ := auditJSON(t, "--db", dbPath, "--since", split, "--until", at); doc.Totals["commits_total"] != 0 || len(doc.Commits) != 0 {
		t.Fatalf("until == commit time: %v", doc.Totals)
	}
}

func int64Ptr(v int64) *int64 { return &v }
