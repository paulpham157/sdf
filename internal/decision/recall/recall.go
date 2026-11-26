// Package recall measures decision recall and commit-link recall on
// labeled fixtures.
//
// Measure decodes each fixture's OTLP/JSON logs and stores them with
// store.Insert, the same write the receiver uses after otlp.Decode.
// It then calls a Recorder. SkillOnly, the default, writes no decision
// and no edge: SDF does not infer a decision from telemetry, and this
// package does not run the agent skill. decision.AutoLinkGitCommits
// (pull request #323) can be wrapped in a Recorder and passed to
// Measure. This package does not call it, so the CI gate stays
// skill-only.
//
// Recorders used as a measurement must not read Fixture.Labels. Tests
// may, to check the scorer.
package recall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

var (
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	shaPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// Recorder writes decisions after a fixture's telemetry is in the store.
type Recorder interface {
	Name() string
	AfterIngest(s *store.Store, fx Fixture) error
}

// SkillOnly is the current pipeline. It records nothing.
type SkillOnly struct{}

func (SkillOnly) Name() string { return "skill-only" }

func (SkillOnly) AfterIngest(*store.Store, Fixture) error { return nil }

// Fixture is one labeled scenario. Logs are OTLP/JSON log export
// bodies. Labels are the ground truth, not telemetry.
type Fixture struct {
	Name   string            `json:"name"`
	Notes  string            `json:"notes"`
	Logs   []json.RawMessage `json:"logs"`
	Labels Labels            `json:"labels"`
}

// Labels is the ground truth for one fixture.
type Labels struct {
	Decisions []LabeledDecision `json:"decisions"`
	Links     []LabeledLink     `json:"links"`
}

// LabeledDecision is a choice the transcript contains. Status is
// hypothesis or decision. DecidedBy is user, agent, or empty.
type LabeledDecision struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
	Status    string `json:"status"`
	DecidedBy string `json:"decided_by,omitempty"`
}

// LabeledLink is an implements edge from a commit to a labeled
// decision. Commit is a lowercase 40-character SHA.
type LabeledLink struct {
	Commit   string `json:"commit"`
	Decision string `json:"decision"`
}

// Report is the scored run, scenarios in name order, then the sums.
type Report struct {
	Pipeline  string
	Scenarios []ScenarioResult
	Decisions Metric
	Links     Metric
}

// ScenarioResult is one fixture. Missed lists are label order. False
// lists are store order. A missed link is "commit:SHA decision:ID"
// with the label id. A false link uses the stored decision id.
type ScenarioResult struct {
	Name            string
	Decisions       Metric
	Links           Metric
	MissedDecisions []string
	MissedLinks     []string
	FalseDecisions  []string
	FalseLinks      []string
}

// Metric is a recall and precision count. Recall is hits/labeled and
// is undefined when nothing was labeled. Precision is hits/recorded
// and is undefined when nothing was recorded. Wilson95 is the 95%
// Wilson interval of recall, nil when recall is undefined.
type Metric struct {
	Labeled          int
	Recorded         int
	Hits             int
	Recall           float64
	RecallDefined    bool
	Precision        float64
	PrecisionDefined bool
	Wilson95         *decision.WilsonInterval
}

// Measure runs the embedded fixtures.
func Measure(rec Recorder) (Report, error) {
	sub, err := fs.Sub(fixtureFS, "fixtures")
	if err != nil {
		return Report{}, err
	}
	return MeasureFS(sub, rec)
}

// MeasureFS loads every *.json file in fsys, ingests each into its own
// store, runs rec, and scores the graph against the labels.
func MeasureFS(fsys fs.FS, rec Recorder) (Report, error) {
	if rec == nil {
		rec = SkillOnly{}
	}
	fixtures, err := Load(fsys)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Pipeline: rec.Name(), Scenarios: []ScenarioResult{}}
	var decL, decR, decH, linkL, linkR, linkH int
	for _, fx := range fixtures {
		sc, err := runOne(fx, rec)
		if err != nil {
			return Report{}, err
		}
		rep.Scenarios = append(rep.Scenarios, sc)
		decL += sc.Decisions.Labeled
		decR += sc.Decisions.Recorded
		decH += sc.Decisions.Hits
		linkL += sc.Links.Labeled
		linkR += sc.Links.Recorded
		linkH += sc.Links.Hits
	}
	sort.Slice(rep.Scenarios, func(i, j int) bool { return rep.Scenarios[i].Name < rep.Scenarios[j].Name })
	rep.Decisions = metric(decL, decR, decH)
	rep.Links = metric(linkL, linkR, linkH)
	return rep, nil
}

// Load reads and checks every JSON fixture in fsys.
func Load(fsys fs.FS) ([]Fixture, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []Fixture
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		fx, err := parseFixture(body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if seen[fx.Name] {
			return nil, fmt.Errorf("%s: duplicate fixture name %s", name, fx.Name)
		}
		seen[fx.Name] = true
		out = append(out, fx)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no fixtures")
	}
	return out, nil
}

func parseFixture(body []byte) (Fixture, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var fx Fixture
	if err := dec.Decode(&fx); err != nil {
		return Fixture{}, err
	}
	if err := validate(fx); err != nil {
		return Fixture{}, err
	}
	return fx, nil
}

func validate(fx Fixture) error {
	if !namePattern.MatchString(fx.Name) {
		return fmt.Errorf("name must be lowercase words separated by hyphens")
	}
	if strings.TrimSpace(fx.Notes) == "" {
		return fmt.Errorf("notes are empty")
	}
	if len(fx.Logs) == 0 {
		return fmt.Errorf("logs are empty")
	}
	recs, err := decodeLogs(fx)
	if err != nil {
		return err
	}
	var bodies strings.Builder
	shas := map[string]bool{}
	for _, rec := range recs {
		if rec.Body != nil {
			bodies.WriteString(*rec.Body)
			bodies.WriteByte('\n')
		}
		if rec.Name != nil && *rec.Name == "sdf.git.commit" {
			sha, _ := rec.Attributes["vcs.ref.head.revision"].(string)
			sha = strings.ToLower(sha)
			if shaPattern.MatchString(sha) {
				shas[sha] = true
			}
		}
	}
	text := bodies.String()
	ids := map[string]bool{}
	stmts := map[string]bool{}
	if fx.Labels.Decisions == nil || fx.Labels.Links == nil {
		return fmt.Errorf("labels.decisions and labels.links must be arrays")
	}
	for _, d := range fx.Labels.Decisions {
		if !idPattern.MatchString(d.ID) {
			return fmt.Errorf("decision id %q is not a record id", d.ID)
		}
		if ids[d.ID] {
			return fmt.Errorf("duplicate decision id %s", d.ID)
		}
		ids[d.ID] = true
		if d.Statement == "" || strings.ContainsAny(d.Statement, "\r\n") || len(d.Statement) > 500 {
			return fmt.Errorf("decision %s statement must be one non-empty line", d.ID)
		}
		norm := normStatement(d.Statement)
		if stmts[norm] {
			return fmt.Errorf("duplicate decision statement %q", d.Statement)
		}
		stmts[norm] = true
		if d.Status != decision.StatusHypothesis && d.Status != decision.StatusDecision {
			return fmt.Errorf("decision %s status must be hypothesis or decision", d.ID)
		}
		if d.DecidedBy != "" && d.DecidedBy != decision.DecidedByUser && d.DecidedBy != decision.DecidedByAgent {
			return fmt.Errorf("decision %s decided_by must be user or agent", d.ID)
		}
		if !strings.Contains(text, d.Statement) {
			return fmt.Errorf("decision %s statement is not in a log body", d.ID)
		}
	}
	seenLink := map[string]bool{}
	for _, l := range fx.Labels.Links {
		if !shaPattern.MatchString(l.Commit) {
			return fmt.Errorf("link commit %q must be a lowercase 40-character SHA", l.Commit)
		}
		if !ids[l.Decision] {
			return fmt.Errorf("link decision %s is not labeled", l.Decision)
		}
		key := l.Commit + " " + l.Decision
		if seenLink[key] {
			return fmt.Errorf("duplicate link %s", key)
		}
		seenLink[key] = true
		if !shas[l.Commit] {
			return fmt.Errorf("link commit %s is not in the telemetry", l.Commit)
		}
	}
	return nil
}

func decodeLogs(fx Fixture) ([]record.Record, error) {
	var recs []record.Record
	for i, body := range fx.Logs {
		got, err := otlp.Decode("logs", body, "application/json")
		if err != nil {
			return nil, fmt.Errorf("logs[%d]: %w", i, err)
		}
		recs = append(recs, got...)
	}
	return recs, nil
}

func runOne(fx Fixture, rec Recorder) (ScenarioResult, error) {
	dir, err := os.MkdirTemp("", "sdf-recall-")
	if err != nil {
		return ScenarioResult{}, err
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(filepath.Join(dir, "sdf-telemetry.sqlite"))
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("%s: %w", fx.Name, err)
	}
	defer st.Close()
	recs, err := decodeLogs(fx)
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("%s: %w", fx.Name, err)
	}
	if _, err := st.Insert(recs); err != nil {
		return ScenarioResult{}, fmt.Errorf("%s: %w", fx.Name, err)
	}
	if err := rec.AfterIngest(st, fx); err != nil {
		return ScenarioResult{}, fmt.Errorf("%s: %w", fx.Name, err)
	}
	g, err := decision.Load(st)
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("%s: %w", fx.Name, err)
	}
	return score(fx, g), nil
}

func score(fx Fixture, g decision.Graph) ScenarioResult {
	used := map[int]bool{}
	match := map[string]int{}
	missedDec := []string{}
	for _, lab := range fx.Labels.Decisions {
		found := -1
		for i, rec := range g.Decisions {
			if used[i] {
				continue
			}
			if rec.ID == lab.ID {
				found = i
				break
			}
		}
		if found >= 0 {
			used[found] = true
			match[lab.ID] = found
		}
	}
	for _, lab := range fx.Labels.Decisions {
		if _, ok := match[lab.ID]; ok {
			continue
		}
		want := normStatement(lab.Statement)
		found := -1
		for i, rec := range g.Decisions {
			if used[i] {
				continue
			}
			if normStatement(rec.Statement) == want {
				found = i
				break
			}
		}
		if found < 0 {
			missedDec = append(missedDec, lab.ID)
			continue
		}
		used[found] = true
		match[lab.ID] = found
	}
	falseDec := []string{}
	for i, rec := range g.Decisions {
		if !used[i] {
			falseDec = append(falseDec, rec.ID)
		}
	}

	type gotLink struct {
		sha, decisionID string
	}
	var links []gotLink
	for _, e := range g.Edges {
		if e.Relation != decision.RelImplements || e.From.Kind != decision.KindCommit || e.To.Kind != decision.KindDecision {
			continue
		}
		links = append(links, gotLink{sha: strings.ToLower(e.From.Ref), decisionID: e.To.Ref})
	}
	usedLink := map[int]bool{}
	missedLinks := []string{}
	for _, lab := range fx.Labels.Links {
		text := "commit:" + lab.Commit + " decision:" + lab.Decision
		recIdx, ok := match[lab.Decision]
		if !ok {
			missedLinks = append(missedLinks, text)
			continue
		}
		wantID := g.Decisions[recIdx].ID
		found := -1
		for i, l := range links {
			if usedLink[i] {
				continue
			}
			if l.sha == lab.Commit && l.decisionID == wantID {
				found = i
				break
			}
		}
		if found < 0 {
			missedLinks = append(missedLinks, text)
			continue
		}
		usedLink[found] = true
	}
	falseLinks := []string{}
	for i, l := range links {
		if !usedLink[i] {
			falseLinks = append(falseLinks, "commit:"+l.sha+" decision:"+l.decisionID)
		}
	}
	return ScenarioResult{
		Name:            fx.Name,
		Decisions:       metric(len(fx.Labels.Decisions), len(g.Decisions), len(match)),
		Links:           metric(len(fx.Labels.Links), len(links), len(fx.Labels.Links)-len(missedLinks)),
		MissedDecisions: missedDec,
		MissedLinks:     missedLinks,
		FalseDecisions:  falseDec,
		FalseLinks:      falseLinks,
	}
}

func metric(labeled, recorded, hits int) Metric {
	m := Metric{Labeled: labeled, Recorded: recorded, Hits: hits}
	if labeled > 0 {
		m.RecallDefined = true
		m.Recall = float64(hits) / float64(labeled)
		if w, ok := decision.Wilson95(hits, labeled); ok {
			m.Wilson95 = &w
		}
	}
	if recorded > 0 {
		m.PrecisionDefined = true
		m.Precision = float64(hits) / float64(recorded)
	}
	return m
}

func normStatement(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
