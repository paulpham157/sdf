package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecisionRecallProxyAndFlags(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	const (
		sess    = "recall-proxy-1"
		other   = "recall-proxy-other"
		linked  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		miss    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		outside = "cccccccccccccccccccccccccccccccccccccccc"
	)
	// Far enough in the past that --since 24h excludes it on any clock after
	// 2020-01-16.
	at := time.Date(2020, 1, 15, 14, 0, 0, 0, time.UTC)
	ingest(t, dbPath, [][]byte{
		hookPayload("recall-fixture", "recall", sess, linked, "/tmp/recall-fixture", "main", at),
		hookPayload("recall-fixture", "recall", sess, miss, "/tmp/recall-fixture", "main", at.Add(time.Hour)),
		hookPayload("recall-fixture", "recall", other, outside, "/tmp/recall-fixture", "main", at.Add(2*time.Hour)),
	})
	mustRun(t, "decision", "add", "--db", dbPath, "--id", "use-wal",
		"--statement", "Use WAL mode for the store.", "--status", "decision", "--decided-by", "user")
	// A session-only implements edge makes SessionHasDecision true, but
	// does not count miss as linked. Only the commit implements edge on
	// linked does.
	mustRun(t, "decision", "link", "--db", dbPath, "--relation", "implements",
		"--from", "session:"+sess, "--to", "decision:use-wal")
	mustRun(t, "decision", "link", "--db", dbPath, "--relation", "implements",
		"--from", "commit:"+linked, "--to", "decision:use-wal")

	dir := filepath.Dir(dbPath)
	before := snapshotDir(t, dir)
	out := run(t, "decision", "recall", "--db", dbPath, "--json")
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	assertStoreUnchanged(t, before, snapshotDir(t, dir))
	doc := decodeProxy(t, out.stdout)
	if doc.Schema != "sdf.decision.recall/v1" || doc.Kind != "link_recall_by_proxy" {
		t.Fatalf("%+v", doc)
	}
	if doc.CommitsInDecisionSessions != 2 || doc.CommitsLinked != 1 || doc.Misses != 1 {
		t.Fatalf("%+v", doc)
	}
	if doc.LinkRecallByProxy == nil || *doc.LinkRecallByProxy != 0.5 {
		t.Fatalf("ratio %v", doc.LinkRecallByProxy)
	}
	if doc.Wilson95 == nil || doc.Wilson95.Low < 0 || doc.Wilson95.Low >= 0.5 || doc.Wilson95.High <= 0.5 || doc.Wilson95.High > 1 {
		t.Fatalf("wilson %+v", doc.Wilson95)
	}
	if doc.SessionsWithDecisionAndCommit != 1 || doc.SessionsWithUnlinkedCommit != 1 {
		t.Fatalf("sessions %+v", doc)
	}
	if len(doc.MissedCommits) != 1 || doc.MissedCommits[0].SHA != miss || doc.MissedCommits[0].SessionID != sess ||
		doc.MissedCommits[0].Timestamp != "2020-01-15T15:00:00.000000+00:00" {
		t.Fatalf("misses %+v", doc.MissedCommits)
	}

	text := run(t, "decision", "recall", "--db", dbPath)
	if text.code != 0 || text.stderr != "" || !strings.Contains(text.stdout, "1/2") || !strings.Contains(text.stdout, miss) || strings.Contains(text.stdout, outside) {
		t.Fatalf("%d %q %q", text.code, text.stdout, text.stderr)
	}

	help := run(t, "decision", "recall", "--help")
	if help.code != 0 || help.stdout != "usage: sdf decision recall [--db PATH] [--json] [--since DURATION|RFC3339] [--until RFC3339]\n" {
		t.Fatalf("%d %q", help.code, help.stdout)
	}
	top := run(t, "decision", "--help")
	if top.code != 0 || !strings.Contains(top.stdout, "recall") {
		t.Fatalf("%d %q", top.code, top.stdout)
	}

	window := run(t, "decision", "recall", "--db", dbPath, "--json", "--since", "2020-01-15T14:30:00Z")
	if window.code != 0 {
		t.Fatal(window.stderr)
	}
	late := decodeProxy(t, window.stdout)
	if late.CommitsInDecisionSessions != 1 || late.CommitsLinked != 0 || late.Misses != 1 || late.LinkRecallByProxy == nil || *late.LinkRecallByProxy != 0 {
		t.Fatalf("since window %+v", late)
	}
	early := run(t, "decision", "recall", "--db", dbPath, "--json", "--until", "2020-01-15T14:30:00Z")
	if early.code != 0 {
		t.Fatal(early.stderr)
	}
	first := decodeProxy(t, early.stdout)
	if first.CommitsInDecisionSessions != 1 || first.CommitsLinked != 1 || first.Misses != 0 || first.LinkRecallByProxy == nil || *first.LinkRecallByProxy != 1 {
		t.Fatalf("until window %+v", first)
	}

	day := run(t, "decision", "recall", "--db", dbPath, "--json", "--since=24h")
	if day.code != 0 {
		t.Fatal(day.stderr)
	}
	none := decodeProxy(t, day.stdout)
	if none.CommitsInDecisionSessions != 0 || none.Misses != 0 || none.LinkRecallByProxy != nil || none.Wilson95 != nil || len(none.MissedCommits) != 0 || none.Window.Since == "" {
		t.Fatalf("24h %+v", none)
	}

	for _, args := range [][]string{
		{"decision", "recall", "--since", "tomorrow"},
		{"decision", "recall", "--until", "tomorrow"},
		{"decision", "recall", "--since", "2026-11-21T00:00:00Z", "--until", "2026-11-20T00:00:00Z"},
		{"decision", "recall", "--db"},
		{"decision", "recall", "--nope"},
	} {
		bad := run(t, args...)
		if bad.code != 2 || bad.stdout != "" {
			t.Fatalf("%v: %d %q %q", args, bad.code, bad.stdout, bad.stderr)
		}
	}

	missingDir := run(t, "decision", "recall", "--db", filepath.Join(t.TempDir(), "missing-dir", "telemetry.sqlite"))
	if missingDir.code != 3 || !strings.Contains(missingDir.stderr, "sdf decision recall:") {
		t.Fatalf("%d %q", missingDir.code, missingDir.stderr)
	}
	absent := filepath.Join(t.TempDir(), "absent.sqlite")
	empty := run(t, "decision", "recall", "--db", absent, "--json")
	if empty.code != 0 {
		t.Fatal(empty.stderr)
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatal("recall created a missing store")
	}
	emptyDoc := decodeProxy(t, empty.stdout)
	if emptyDoc.LinkRecallByProxy != nil || emptyDoc.Misses != 0 || len(emptyDoc.MissedCommits) != 0 {
		t.Fatalf("empty %+v", emptyDoc)
	}
}

type proxyDoc struct {
	Schema                        string      `json:"schema"`
	Kind                          string      `json:"kind"`
	Window                        proxyWin    `json:"window"`
	CommitsInDecisionSessions     int         `json:"commits_in_decision_sessions"`
	CommitsLinked                 int         `json:"commits_linked"`
	Misses                        int         `json:"misses"`
	LinkRecallByProxy             *float64    `json:"link_recall_by_proxy"`
	Wilson95                      *wilson     `json:"wilson95"`
	SessionsWithDecisionAndCommit int         `json:"sessions_with_decision_and_commit"`
	SessionsWithUnlinkedCommit    int         `json:"sessions_with_unlinked_commit"`
	MissedCommits                 []proxyMiss `json:"missed_commits"`
}

type proxyWin struct {
	Since string `json:"since"`
	Until string `json:"until"`
}

type wilson struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

type proxyMiss struct {
	SHA       string `json:"sha"`
	SessionID string `json:"session_id"`
	Timestamp string `json:"timestamp"`
}

func decodeProxy(t *testing.T, stdout string) proxyDoc {
	t.Helper()
	var doc proxyDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	return doc
}
