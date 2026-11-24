package recall_test

import (
	"math"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decision/recall"
	"github.com/paulpham157/sdf/internal/store"
)

// Skill-only baseline measured on these fixtures. SDF does not infer a
// decision from telemetry, and Measure's default recorder writes nothing,
// so every labeled decision and commit link is a miss and precision is
// undefined (nothing was recorded).
//
// Issue #284's target is link recall >= 0.95 and a precision drop of at
// most 0.05 against a stronger pipeline. CI gates on this snapshot, not
// on that target. Raise the floors when a recorder is plugged into the
// default and the new run has been measured. docs/recall.md records the
// same numbers.
const (
	baselineDecisionRecall = 0.0
	baselineLinkRecall     = 0.0
	precisionDropAllowance = 0.05
)

const (
	shaDecide = "1111111111111111111111111111111111111111"
	shaLong   = "3333333333333333333333333333333333333333"
	shaM1     = "4444444444444444444444444444444444444444"
	shaM2     = "5555555555555555555555555555555555555555"
	shaM3     = "6666666666666666666666666666666666666666"
)

type wantScenario struct {
	decLabeled, decHits int
	linkLabeled         int
	linkHits            int
	missedDecisions     []string
	missedLinks         []string
}

func TestSkillOnlyBaseline(t *testing.T) {
	rep, err := recall.Measure(recall.SkillOnly{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pipeline != "skill-only" {
		t.Fatalf("pipeline %q", rep.Pipeline)
	}
	want := map[string]wantScenario{
		"approve":          {decLabeled: 1, missedDecisions: []string{"approve-wal"}},
		"decide-commit":    {decLabeled: 1, linkLabeled: 1, missedDecisions: []string{"pure-go"}, missedLinks: []string{"commit:" + shaDecide + " decision:pure-go"}},
		"long-session":     {decLabeled: 1, linkLabeled: 1, missedDecisions: []string{"use-wal"}, missedLinks: []string{"commit:" + shaLong + " decision:use-wal"}},
		"many-to-many":     {decLabeled: 2, linkLabeled: 4, missedDecisions: []string{"use-wal", "pure-go"}, missedLinks: []string{"commit:" + shaM1 + " decision:use-wal", "commit:" + shaM2 + " decision:use-wal", "commit:" + shaM2 + " decision:pure-go", "commit:" + shaM3 + " decision:pure-go"}},
		"plain-chat":       {},
		"reject":           {decLabeled: 1, missedDecisions: []string{"stay-on-sqlite"}},
		"unrelated-commit": {decLabeled: 1, missedDecisions: []string{"use-sqlite"}},
	}
	if len(rep.Scenarios) != len(want) {
		t.Fatalf("scenarios %d", len(rep.Scenarios))
	}
	var prev string
	for _, sc := range rep.Scenarios {
		if sc.Name < prev {
			t.Fatalf("scenarios not sorted: %s then %s", prev, sc.Name)
		}
		prev = sc.Name
		w, ok := want[sc.Name]
		if !ok {
			t.Fatalf("unexpected scenario %s", sc.Name)
		}
		assertScenario(t, sc, w)
	}
	assertMetric(t, "decisions", rep.Decisions, 7, 0, 0, baselineDecisionRecall, false)
	assertMetric(t, "links", rep.Links, 6, 0, 0, baselineLinkRecall, false)
	// 0/n Wilson interval sits on 0 and stays below 1.
	for _, name := range []string{"decisions", "links"} {
		m := rep.Decisions
		if name == "links" {
			m = rep.Links
		}
		if m.Wilson95 == nil || m.Wilson95.Low != 0 || m.Wilson95.High <= 0 || m.Wilson95.High >= 1 {
			t.Fatalf("%s wilson %+v", name, m.Wilson95)
		}
	}
}

func TestFixtureLinkMustNameAnIngestedCommit(t *testing.T) {
	const body = `{
	  "name": "bad-link",
	  "notes": "The labeled commit is not in the log export.",
	  "logs": [{
	    "resourceLogs": [{
	      "resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "recall-fixture"}}]},
	      "scopeLogs": [{"logRecords": [{
	        "timeUnixNano": "1579100000000000000",
	        "body": {"stringValue": "Use WAL mode for the store."},
	        "attributes": [
	          {"key": "event.name", "value": {"stringValue": "recall.turn"}},
	          {"key": "session.id", "value": {"stringValue": "recall-bad"}}
	        ]
	      }]}]
	    }]
	  }],
	  "labels": {
	    "decisions": [{"id": "use-wal", "statement": "Use WAL mode for the store.", "status": "decision", "decided_by": "user"}],
	    "links": [{"commit": "1111111111111111111111111111111111111111", "decision": "use-wal"}]
	  }
	}`
	fsys := fstest.MapFS{"bad-link.json": &fstest.MapFile{Data: []byte(body)}}
	_, err := recall.MeasureFS(fsys, recall.SkillOnly{})
	if err == nil || !strings.Contains(err.Error(), "1111111111111111111111111111111111111111") {
		t.Fatal(err)
	}
}

func TestMeasureNilRecorderIsSkillOnly(t *testing.T) {
	rep, err := recall.Measure(nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pipeline != "skill-only" || rep.Decisions.Hits != 0 || rep.Links.Hits != 0 {
		t.Fatalf("%+v", rep)
	}
}

// labelRecorder writes each fixture's own labels. It is a stand-in for a
// future auto-linker (issue #281): Measure must score whatever AfterIngest
// stored, not a hardcoded empty graph.
type labelRecorder struct{}

func (labelRecorder) Name() string { return "labels" }

func (labelRecorder) AfterIngest(s *store.Store, fx recall.Fixture) error {
	for _, d := range fx.Labels.Decisions {
		if _, err := decision.Add(s, decision.RecordInput{
			ID: d.ID, Statement: d.Statement, Status: d.Status, DecidedBy: d.DecidedBy,
		}); err != nil {
			return err
		}
	}
	for _, l := range fx.Labels.Links {
		if _, err := decision.Link(s, decision.EdgeInput{
			Relation: decision.RelImplements,
			From:     decision.Endpoint{Kind: decision.KindCommit, Ref: l.Commit},
			To:       decision.Endpoint{Kind: decision.KindDecision, Ref: l.Decision},
		}); err != nil {
			return err
		}
	}
	return nil
}

func TestLabelRecorderScoresPerfectRecall(t *testing.T) {
	rep, err := recall.Measure(labelRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pipeline != "labels" {
		t.Fatalf("pipeline %q", rep.Pipeline)
	}
	assertMetric(t, "decisions", rep.Decisions, 7, 7, 7, 1, true)
	assertMetric(t, "links", rep.Links, 6, 6, 6, 1, true)
	if rep.Decisions.Wilson95 == nil || rep.Decisions.Wilson95.High != 1 || rep.Links.Wilson95.High != 1 {
		t.Fatalf("wilson decisions %+v links %+v", rep.Decisions.Wilson95, rep.Links.Wilson95)
	}
	for _, sc := range rep.Scenarios {
		if len(sc.MissedDecisions) != 0 || len(sc.MissedLinks) != 0 || len(sc.FalseDecisions) != 0 || len(sc.FalseLinks) != 0 {
			t.Fatalf("%s %+v", sc.Name, sc)
		}
	}
}

// statementRecorder stores the approve choice under a different id. A hit
// is the statement, not only the label id.
type statementRecorder struct{}

func (statementRecorder) Name() string { return "statement" }

func (statementRecorder) AfterIngest(s *store.Store, fx recall.Fixture) error {
	if fx.Name != "approve" {
		return nil
	}
	_, err := decision.Add(s, decision.RecordInput{
		ID:        "wal-approved",
		Statement: "  use   WAL mode for the store. ",
		Status:    decision.StatusDecision,
		DecidedBy: decision.DecidedByUser,
	})
	return err
}

func TestStatementMatchCountsAsAHit(t *testing.T) {
	rep, err := recall.Measure(statementRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	sc := scenario(t, rep, "approve")
	if sc.Decisions.Hits != 1 || sc.Decisions.Recorded != 1 || !sc.Decisions.PrecisionDefined || sc.Decisions.Precision != 1 {
		t.Fatalf("%+v", sc.Decisions)
	}
	if len(sc.MissedDecisions) != 0 || len(sc.FalseDecisions) != 0 {
		t.Fatalf("missed %v false %v", sc.MissedDecisions, sc.FalseDecisions)
	}
	if rep.Decisions.Hits != 1 || rep.Decisions.Recorded != 1 {
		t.Fatalf("aggregate %+v", rep.Decisions)
	}
}

// extraLinkRecorder records the unrelated-commit decision and an implements
// edge the label does not list. That edge is a false link.
type extraLinkRecorder struct{}

func (extraLinkRecorder) Name() string { return "extra-link" }

func (extraLinkRecorder) AfterIngest(s *store.Store, fx recall.Fixture) error {
	if fx.Name != "unrelated-commit" {
		return nil
	}
	if _, err := decision.Add(s, decision.RecordInput{
		ID: "use-sqlite", Statement: "Keep the collector store in SQLite.",
		Status: decision.StatusDecision, DecidedBy: decision.DecidedByUser,
	}); err != nil {
		return err
	}
	_, err := decision.Link(s, decision.EdgeInput{
		Relation: decision.RelImplements,
		From:     decision.Endpoint{Kind: decision.KindCommit, Ref: "2222222222222222222222222222222222222222"},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: "use-sqlite"},
	})
	return err
}

func TestUnlabeledLinkIsAPrecisionMiss(t *testing.T) {
	rep, err := recall.Measure(extraLinkRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	sc := scenario(t, rep, "unrelated-commit")
	if sc.Decisions.Hits != 1 || sc.Links.Labeled != 0 || sc.Links.Recorded != 1 || sc.Links.Hits != 0 {
		t.Fatalf("decisions %+v links %+v", sc.Decisions, sc.Links)
	}
	if !sc.Links.PrecisionDefined || sc.Links.Precision != 0 {
		t.Fatalf("precision %+v", sc.Links)
	}
	if len(sc.FalseLinks) != 1 || sc.FalseLinks[0] != "commit:2222222222222222222222222222222222222222 decision:use-sqlite" {
		t.Fatalf("false links %v", sc.FalseLinks)
	}
	// Aggregate link precision is defined and below a perfect baseline by
	// more than the allowed drop. The skill-only gate does not apply that
	// rule, because its precision is undefined.
	if !rep.Links.PrecisionDefined || rep.Links.Precision != 0 {
		t.Fatalf("aggregate %+v", rep.Links)
	}
	if rep.Links.Precision+precisionDropAllowance >= 1 {
		t.Fatal("precision drop allowance would hide a total miss")
	}
}

func assertScenario(t *testing.T, sc recall.ScenarioResult, w wantScenario) {
	t.Helper()
	assertMetric(t, sc.Name+" decisions", sc.Decisions, w.decLabeled, 0, w.decHits, ratio(w.decHits, w.decLabeled), false)
	assertMetric(t, sc.Name+" links", sc.Links, w.linkLabeled, 0, w.linkHits, ratio(w.linkHits, w.linkLabeled), false)
	if stringsJoin(sc.MissedDecisions) != stringsJoin(w.missedDecisions) {
		t.Fatalf("%s missed decisions %v", sc.Name, sc.MissedDecisions)
	}
	if stringsJoin(sc.MissedLinks) != stringsJoin(w.missedLinks) {
		t.Fatalf("%s missed links %v", sc.Name, sc.MissedLinks)
	}
	if len(sc.FalseDecisions) != 0 || len(sc.FalseLinks) != 0 {
		t.Fatalf("%s false decisions %v links %v", sc.Name, sc.FalseDecisions, sc.FalseLinks)
	}
}

func assertMetric(t *testing.T, name string, m recall.Metric, labeled, recorded, hits int, recallFloor float64, precisionDefined bool) {
	t.Helper()
	if m.Labeled != labeled || m.Recorded != recorded || m.Hits != hits {
		t.Fatalf("%s counts labeled %d recorded %d hits %d", name, m.Labeled, m.Recorded, m.Hits)
	}
	if labeled == 0 {
		if m.RecallDefined || m.Recall != 0 || m.Wilson95 != nil {
			t.Fatalf("%s recall defined on an empty label set: %+v", name, m)
		}
	} else {
		if !m.RecallDefined || m.Recall+1e-12 < recallFloor {
			t.Fatalf("%s recall %v defined %v, floor %v", name, m.Recall, m.RecallDefined, recallFloor)
		}
		if math.Abs(m.Recall-float64(hits)/float64(labeled)) > 1e-12 {
			t.Fatalf("%s recall %v", name, m.Recall)
		}
	}
	if precisionDefined {
		if !m.PrecisionDefined || recorded == 0 || math.Abs(m.Precision-float64(hits)/float64(recorded)) > 1e-12 {
			t.Fatalf("%s precision %+v", name, m)
		}
		return
	}
	if m.PrecisionDefined || m.Precision != 0 {
		t.Fatalf("%s precision became defined (%v); remeasure the baseline in docs/recall.md", name, m.Precision)
	}
}

func ratio(hits, labeled int) float64 {
	if labeled == 0 {
		return 0
	}
	return float64(hits) / float64(labeled)
}

func scenario(t *testing.T, rep recall.Report, name string) recall.ScenarioResult {
	t.Helper()
	for _, sc := range rep.Scenarios {
		if sc.Name == name {
			return sc
		}
	}
	t.Fatalf("missing scenario %s", name)
	return recall.ScenarioResult{}
}

func stringsJoin(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += "\n"
		}
		out += v
	}
	return out
}
