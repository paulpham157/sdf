package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/store"
)

const (
	autoLinkSHA1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	autoLinkSHA2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	autoLinkSHA3 = "cccccccccccccccccccccccccccccccccccccccc"
	autoLinkSHA4 = "dddddddddddddddddddddddddddddddddddddddd"
	autoLinkSHA5 = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	autoLinkSess = "grok-autolink-7f3c"
	autoLinkTwo  = "grok-autolink-multi"
)

func TestGitCommitAutoLinksWhenTheSessionAlreadyImplementsOneDecision(t *testing.T) {
	base, db, logs := startAutoLink(t)
	seedSession(t, db, autoLinkSess)
	addDecision(t, db, "use-sqlite", "Keep the collector store in SQLite.")
	mustLink(t, db, decision.RelImplements, decision.Endpoint{Kind: decision.KindSession, Ref: autoLinkSess},
		decision.Endpoint{Kind: decision.KindDecision, Ref: "use-sqlite"})

	resp := postBytes(t, base+"/v1/logs", "application/json", gitCommitPayload(autoLinkSess, autoLinkSHA1, nil))
	if resp.status != 200 {
		t.Fatalf("status %d %s", resp.status, resp.body)
	}
	assertImplements(t, db, autoLinkSHA1, "use-sqlite")
	assertDecisionAttrNotStored(t, db, autoLinkSHA1)
	if strings.Contains(logs.String(), "subject-secret") || strings.Contains(logs.String(), "commit message") {
		t.Fatalf("logs mentioned a commit message: %s", logs.String())
	}

	// A repeated event for the same SHA is idempotent: no duplicate edge, still 200.
	resp = postBytes(t, base+"/v1/logs", "application/json", gitCommitPayload(autoLinkSess, autoLinkSHA1, nil))
	if resp.status != 200 {
		t.Fatalf("repeat status %d %s", resp.status, resp.body)
	}
	assertImplementsCount(t, db, autoLinkSHA1, "use-sqlite", 1)
}

func TestGitCommitSkipsOptionAWhenTheSessionImplementsMoreThanOneDecision(t *testing.T) {
	base, db, logs := startAutoLink(t)
	seedSession(t, db, autoLinkTwo)
	addDecision(t, db, "use-sqlite", "Keep the collector store in SQLite.")
	addDecision(t, db, "pure-go", "The driver stays pure Go.")
	mustLink(t, db, decision.RelImplements, decision.Endpoint{Kind: decision.KindSession, Ref: autoLinkTwo},
		decision.Endpoint{Kind: decision.KindDecision, Ref: "use-sqlite"})
	mustLink(t, db, decision.RelImplements, decision.Endpoint{Kind: decision.KindSession, Ref: autoLinkTwo},
		decision.Endpoint{Kind: decision.KindDecision, Ref: "pure-go"})

	resp := postBytes(t, base+"/v1/logs", "application/json", gitCommitPayload(autoLinkTwo, autoLinkSHA2, nil))
	if resp.status != 200 {
		t.Fatalf("status %d %s", resp.status, resp.body)
	}
	assertNoCommitImplements(t, db, autoLinkSHA2)
	got := logs.String()
	if !strings.Contains(got, autoLinkSHA2) || !strings.Contains(got, autoLinkTwo) {
		t.Fatalf("skip was not logged with sha and session: %s", got)
	}
	if strings.Contains(got, "commit message") {
		t.Fatalf("logs mentioned a commit message: %s", got)
	}
}

func TestGitCommitLinksEachSDFDecisionTrailerAndRejectsUnknownOrRetracted(t *testing.T) {
	base, db, logs := startAutoLink(t)
	addDecision(t, db, "use-sqlite", "Keep the collector store in SQLite.")
	addDecision(t, db, "drop-cgo", "CGO can be dropped from release builds.")
	if _, err := decision.Retract(db, decision.RetractInput{ID: "drop-cgo", Reason: "Windows still needs CGO", RetractedBy: decision.DecidedByUser}); err != nil {
		t.Fatal(err)
	}

	resp := postBytes(t, base+"/v1/logs", "application/json", gitCommitPayload("", autoLinkSHA3, []string{"use-sqlite", "no-such-decision", "drop-cgo", `bad"quote`}))
	if resp.status != 200 {
		t.Fatalf("status %d %s", resp.status, resp.body)
	}
	assertImplements(t, db, autoLinkSHA3, "use-sqlite")
	assertNoImplements(t, db, autoLinkSHA3, "no-such-decision")
	assertNoImplements(t, db, autoLinkSHA3, "drop-cgo")
	assertDecisionAttrNotStored(t, db, autoLinkSHA3)
	got := logs.String()
	if !strings.Contains(got, autoLinkSHA3) || !strings.Contains(got, "no-such-decision") || !strings.Contains(got, "drop-cgo") {
		t.Fatalf("rejections were not logged with sha and id: %s", got)
	}
	if strings.Contains(got, `bad"quote`) && strings.Count(got, "\n"+`bad"`) > 0 {
		t.Fatalf("logs looked like a raw parse dump: %s", got)
	}
	// Unknown or retracted ids must not appear as ghost edges; the valid one stays.
	assertImplementsCount(t, db, autoLinkSHA3, "use-sqlite", 1)
}

func TestGitCommitTrailerOverridesSessionAutoLink(t *testing.T) {
	base, db, _ := startAutoLink(t)
	seedSession(t, db, autoLinkSess)
	addDecision(t, db, "use-sqlite", "Keep the collector store in SQLite.")
	addDecision(t, db, "store-columns", "Store hot attributes in their own columns.")
	mustLink(t, db, decision.RelImplements, decision.Endpoint{Kind: decision.KindSession, Ref: autoLinkSess},
		decision.Endpoint{Kind: decision.KindDecision, Ref: "use-sqlite"})

	resp := postBytes(t, base+"/v1/logs", "application/json", gitCommitPayload(autoLinkSess, autoLinkSHA4, []string{"store-columns"}))
	if resp.status != 200 {
		t.Fatalf("status %d %s", resp.status, resp.body)
	}
	assertImplements(t, db, autoLinkSHA4, "store-columns")
	assertNoImplements(t, db, autoLinkSHA4, "use-sqlite")
}

func TestGitCommitDoesNotAutoLinkFromValidatesOrContradicts(t *testing.T) {
	base, db, _ := startAutoLink(t)
	seedSession(t, db, autoLinkSess)
	addDecision(t, db, "use-sqlite", "Keep the collector store in SQLite.")
	mustLink(t, db, decision.RelValidates, decision.Endpoint{Kind: decision.KindSession, Ref: autoLinkSess},
		decision.Endpoint{Kind: decision.KindDecision, Ref: "use-sqlite"})

	resp := postBytes(t, base+"/v1/logs", "application/json", gitCommitPayload(autoLinkSess, autoLinkSHA5, nil))
	if resp.status != 200 {
		t.Fatalf("status %d %s", resp.status, resp.body)
	}
	assertNoCommitImplements(t, db, autoLinkSHA5)
}

func startAutoLink(t *testing.T) (string, *store.Store, *bytes.Buffer) {
	t.Helper()
	logs := new(bytes.Buffer)
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true, Log: logs}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Shutdown()
		_ = db.Close()
	})
	base := "http://" + srv.HTTPAddress()
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(base + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return base, db, logs
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not become ready", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func seedSession(t *testing.T, db *store.Store, sessionID string) {
	t.Helper()
	if _, err := db.Insert([]record.Record{{
		Signal:     "logs",
		SessionID:  record.Str(sessionID),
		Name:       record.Str("session.start"),
		Attributes: map[string]any{"session.id": sessionID},
		Resource:   map[string]any{"service.name": "grok-cli"},
	}}); err != nil {
		t.Fatal(err)
	}
}

func addDecision(t *testing.T, db *store.Store, id, statement string) {
	t.Helper()
	if _, err := decision.Add(db, decision.RecordInput{ID: id, Statement: statement, Status: decision.StatusDecision}); err != nil {
		t.Fatal(err)
	}
}

func mustLink(t *testing.T, db *store.Store, rel string, from, to decision.Endpoint) {
	t.Helper()
	if _, err := decision.Link(db, decision.EdgeInput{Relation: rel, From: from, To: to}); err != nil {
		t.Fatal(err)
	}
}

func gitCommitPayload(session, sha string, decisionIDs []string) []byte {
	sess := ""
	if session != "" {
		sess = fmt.Sprintf(`{"key":"session.id","value":{"stringValue":%q}},{"key":"sdf.git.agent","value":{"stringValue":"grok-cli"}},`, session)
	}
	dec := ""
	if len(decisionIDs) > 0 {
		var items []string
		for _, id := range decisionIDs {
			b, _ := json.Marshal(id)
			items = append(items, `{"stringValue":`+string(b)+`}`)
		}
		dec = `{"key":"sdf.git.decision","value":{"arrayValue":{"values":[` + strings.Join(items, ",") + `]}}},`
	}
	return []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"grok-cli"}},{"key":"sdf.git.hook","value":{"stringValue":"1"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"1","body":{"stringValue":"sdf.git.commit"},"attributes":[{"key":"event.name","value":{"stringValue":"sdf.git.commit"}},` + sess + dec + `{"key":"vcs.ref.head.revision","value":{"stringValue":"` + sha + `"}},{"key":"sdf.git.repo","value":{"stringValue":"/tmp/repo"}},{"key":"sdf.git.branch","value":{"stringValue":"main"}},{"key":"sdf.git.hook","value":{"stringValue":"1"}}]}]}]}]}`)
}

func assertImplements(t *testing.T, db *store.Store, sha, id string) {
	t.Helper()
	assertImplementsCount(t, db, sha, id, 1)
}

func assertImplementsCount(t *testing.T, db *store.Store, sha, id string, want int) {
	t.Helper()
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range g.Edges {
		if e.Relation == decision.RelImplements && e.From == (decision.Endpoint{Kind: decision.KindCommit, Ref: sha}) && e.To == (decision.Endpoint{Kind: decision.KindDecision, Ref: id}) {
			n++
		}
	}
	if n != want {
		t.Fatalf("implements commit:%s -> decision:%s count %d, want %d (edges %+v)", sha, id, n, want, g.Edges)
	}
}

func assertNoImplements(t *testing.T, db *store.Store, sha, id string) {
	t.Helper()
	assertImplementsCount(t, db, sha, id, 0)
}

func assertNoCommitImplements(t *testing.T, db *store.Store, sha string) {
	t.Helper()
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range g.Edges {
		if e.Relation == decision.RelImplements && e.From == (decision.Endpoint{Kind: decision.KindCommit, Ref: sha}) {
			t.Fatalf("unexpected implements edge %+v", e)
		}
	}
}

func assertDecisionAttrNotStored(t *testing.T, db *store.Store, sha string) {
	t.Helper()
	recs, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range recs {
		if rec.Name == nil || *rec.Name != "sdf.git.commit" {
			continue
		}
		if rec.Attributes["vcs.ref.head.revision"] != sha {
			continue
		}
		found = true
		if _, ok := rec.Attributes["sdf.git.decision"]; ok {
			t.Fatalf("stored hook event kept sdf.git.decision: %v", rec.Attributes)
		}
	}
	if !found {
		t.Fatalf("no stored sdf.git.commit for %s", sha)
	}
}
