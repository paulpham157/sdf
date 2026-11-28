package live

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decision/recall"
)

const (
	statusMeasured    = "measured"
	statusUnsupported = "unsupported"
	statusUnavailable = "unavailable"
)

// Report is the measurement. JSON uses schema sdf.decision.recall.live/v1.
// Recall and precision are pooled over scored runs. A failed run is not a
// miss and is not in those pools. Wilson95 is the 95% Wilson interval of
// that pooled recall.
type Report struct {
	Schema        string        `json:"schema"`
	Model         string        `json:"model"`
	K             int           `json:"k"`
	Skipped       bool          `json:"skipped"`
	OpenAIBase    string        `json:"openai_base_url"`
	AnthropicBase string        `json:"anthropic_base_url"`
	Agents        []AgentReport `json:"agents,omitempty"`
	Overall       *RatioPair    `json:"overall,omitempty"`
}

// AgentReport is one CLI. Status is measured, unsupported, or unavailable.
type AgentReport struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Protocol  string           `json:"protocol,omitempty"`
	Status    string           `json:"status"`
	Reason    string           `json:"reason,omitempty"`
	Scenarios []ScenarioReport `json:"scenarios,omitempty"`
	Overall   *RatioPair       `json:"overall,omitempty"`
}

// ScenarioReport is one fixture for one agent.
type ScenarioReport struct {
	Name      string   `json:"name"`
	Runs      int      `json:"runs"`
	Scored    int      `json:"scored"`
	Failed    int      `json:"failed"`
	Capped    int      `json:"capped"`
	Decisions Ratio    `json:"decisions"`
	Links     Ratio    `json:"links"`
	Errors    []string `json:"errors,omitempty"`
}

// RatioPair is decisions and links for a scenario rollup or an agent.
type RatioPair struct {
	Decisions Ratio `json:"decisions"`
	Links     Ratio `json:"links"`
	Runs      int   `json:"runs"`
	Scored    int   `json:"scored"`
	Failed    int   `json:"failed"`
	Capped    int   `json:"capped"`
}

// Ratio is a pooled recall and precision. Recall and precision are null
// when the denominator is zero. Wilson95 is null when recall is null.
type Ratio struct {
	Labeled   int                      `json:"labeled"`
	Recorded  int                      `json:"recorded"`
	Hits      int                      `json:"hits"`
	Recall    *float64                 `json:"recall"`
	Precision *float64                 `json:"precision"`
	Wilson95  *decision.WilsonInterval `json:"wilson95"`
}

type counts struct {
	runs, scored, failed, capped int
	decL, decR, decH             int
	linkL, linkR, linkH          int
}

func (c *counts) add(sc recall.ScenarioResult, capped bool) {
	c.runs++
	c.scored++
	if capped {
		c.capped++
	}
	c.decL += sc.Decisions.Labeled
	c.decR += sc.Decisions.Recorded
	c.decH += sc.Decisions.Hits
	c.linkL += sc.Links.Labeled
	c.linkR += sc.Links.Recorded
	c.linkH += sc.Links.Hits
}

func (c *counts) fail() {
	c.runs++
	c.failed++
}

func (c *counts) merge(o counts) {
	c.runs += o.runs
	c.scored += o.scored
	c.failed += o.failed
	c.capped += o.capped
	c.decL += o.decL
	c.decR += o.decR
	c.decH += o.decH
	c.linkL += o.linkL
	c.linkR += o.linkR
	c.linkH += o.linkH
}

func (c counts) pair() RatioPair {
	return RatioPair{
		Decisions: newRatio(c.decL, c.decR, c.decH),
		Links:     newRatio(c.linkL, c.linkR, c.linkH),
		Runs:      c.runs,
		Scored:    c.scored,
		Failed:    c.failed,
		Capped:    c.capped,
	}
}

func (c counts) scenario(name string, errors []string) ScenarioReport {
	return ScenarioReport{
		Name:      name,
		Runs:      c.runs,
		Scored:    c.scored,
		Failed:    c.failed,
		Capped:    c.capped,
		Decisions: newRatio(c.decL, c.decR, c.decH),
		Links:     newRatio(c.linkL, c.linkR, c.linkH),
		Errors:    errors,
	}
}

func newRatio(labeled, recorded, hits int) Ratio {
	r := Ratio{Labeled: labeled, Recorded: recorded, Hits: hits}
	if labeled > 0 {
		v := float64(hits) / float64(labeled)
		r.Recall = &v
		if w, ok := decision.Wilson95(hits, labeled); ok {
			r.Wilson95 = &w
		}
	}
	if recorded > 0 {
		v := float64(hits) / float64(recorded)
		r.Precision = &v
	}
	return r
}

// JSON is the indented report. It does not contain the API key or a transcript.
func (r Report) JSON() (string, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// Markdown is the human-readable table. Failures are counted in their own
// column and are not inside the recall denominator.
func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# Live decision recall\n\n")
	if r.Skipped {
		b.WriteString("Skipped. No API key is set. Set SDF_RECALL_API_KEY or TOKENROUTER_API_KEY. Nothing was sent to a model.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Model `%s`. k=%d. OpenAI base `%s`. Anthropic base `%s`.\n\n", r.Model, r.K, r.OpenAIBase, r.AnthropicBase)
	b.WriteString("Recall and precision are pooled over scored runs. A failed run is not a miss. Wilson 95% is the interval of that pooled recall.\n\n")
	measured := false
	for _, a := range r.Agents {
		if a.Status == statusMeasured {
			measured = true
			break
		}
	}
	if !measured {
		b.WriteString("No agent was measured.\n\n")
	} else {
		b.WriteString("| Agent | Protocol | Scenario | Scored | Failed | Capped | Decision recall | Wilson 95% | Decision precision | Link recall | Wilson 95% | Link precision |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, a := range r.Agents {
			if a.Status != statusMeasured {
				continue
			}
			for _, sc := range a.Scenarios {
				writeMetricRow(&b, a.ID, a.Protocol, sc.Name, sc.Scored, sc.Failed, sc.Capped, sc.Decisions, sc.Links)
			}
			if a.Overall != nil {
				writeMetricRow(&b, a.ID, a.Protocol, "overall", a.Overall.Scored, a.Overall.Failed, a.Overall.Capped, a.Overall.Decisions, a.Overall.Links)
			}
		}
		if r.Overall != nil {
			writeMetricRow(&b, "all", "", "overall", r.Overall.Scored, r.Overall.Failed, r.Overall.Capped, r.Overall.Decisions, r.Overall.Links)
		}
		b.WriteString("\n")
	}
	var notes []AgentReport
	for _, a := range r.Agents {
		if a.Status != statusMeasured {
			notes = append(notes, a)
		}
	}
	if len(notes) > 0 {
		b.WriteString("## Not measured\n\n")
		b.WriteString("| Agent | Status | Reason |\n")
		b.WriteString("| --- | --- | --- |\n")
		for _, a := range notes {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", a.ID, a.Status, escapeCell(a.Reason))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func writeMetricRow(b *strings.Builder, agent, proto, scenario string, scored, failed, capped int, dec, links Ratio) {
	dRec, dWil, dPr := fmtRatio(dec)
	lRec, lWil, lPr := fmtRatio(links)
	fmt.Fprintf(b, "| %s | %s | %s | %d | %d | %d | %s | %s | %s | %s | %s | %s |\n",
		agent, proto, scenario, scored, failed, capped, dRec, dWil, dPr, lRec, lWil, lPr)
}

func fmtRatio(r Ratio) (recall, wilson, precision string) {
	recall, precision = "undefined", "undefined"
	if r.Recall != nil {
		recall = fmt.Sprintf("%.6f", *r.Recall)
	}
	if r.Precision != nil {
		precision = fmt.Sprintf("%.6f", *r.Precision)
	}
	if r.Wilson95 != nil {
		wilson = fmt.Sprintf("[%.6f, %.6f]", r.Wilson95.Low, r.Wilson95.High)
	}
	return recall, wilson, precision
}

func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "/")
	return s
}

func countsFromScenario(sc ScenarioReport) counts {
	return counts{
		runs:   sc.Runs,
		scored: sc.Scored,
		failed: sc.Failed,
		capped: sc.Capped,
		decL:   sc.Decisions.Labeled,
		decR:   sc.Decisions.Recorded,
		decH:   sc.Decisions.Hits,
		linkL:  sc.Links.Labeled,
		linkR:  sc.Links.Recorded,
		linkH:  sc.Links.Hits,
	}
}

// mergeReports pools scored runs from earlier reports. Recall, precision,
// and the Wilson interval are recomputed from the summed counts. A failed
// run stays a failure.
func mergeReports(parts []Report) (Report, error) {
	if len(parts) == 0 {
		return Report{}, errors.New("no reports to merge")
	}
	out := Report{Schema: schemaLive}
	agents := map[string]*AgentReport{}
	var order []string
	var model string
	mixed := false
	saw := false
	for _, p := range parts {
		if p.Schema != "" && p.Schema != schemaLive {
			return Report{}, fmt.Errorf("report schema %q", p.Schema)
		}
		if p.Skipped {
			continue
		}
		saw = true
		if p.Model != "" {
			if model == "" {
				model = p.Model
			} else if model != p.Model {
				mixed = true
			}
		}
		if out.OpenAIBase == "" {
			out.OpenAIBase = p.OpenAIBase
		}
		if out.AnthropicBase == "" {
			out.AnthropicBase = p.AnthropicBase
		}
		for _, a := range p.Agents {
			dst, ok := agents[a.ID]
			if !ok {
				cp := a
				cp.Scenarios = append([]ScenarioReport(nil), a.Scenarios...)
				cp.Overall = nil
				agents[a.ID] = &cp
				order = append(order, a.ID)
				continue
			}
			merged := mergeAgent(*dst, a)
			*dst = merged
		}
	}
	if !saw {
		out.Skipped = true
		return out, nil
	}
	if mixed {
		model = "mixed"
	}
	out.Model = model
	var grand counts
	measured := 0
	maxRuns := 0
	for _, id := range order {
		a := agents[id]
		if a.Status == statusMeasured {
			sortScenarios(a.Scenarios)
			var c counts
			for _, sc := range a.Scenarios {
				c.merge(countsFromScenario(sc))
				if sc.Runs > maxRuns {
					maxRuns = sc.Runs
				}
			}
			pair := c.pair()
			a.Overall = &pair
			grand.merge(c)
			measured++
		}
		out.Agents = append(out.Agents, *a)
	}
	if measured > 0 {
		pair := grand.pair()
		out.Overall = &pair
	}
	out.K = maxRuns
	return out, nil
}

func mergeAgent(a, b AgentReport) AgentReport {
	if a.Status != statusMeasured {
		if b.Status == statusMeasured {
			b.Overall = nil
			b.Scenarios = append([]ScenarioReport(nil), b.Scenarios...)
			return b
		}
		return a
	}
	if b.Status != statusMeasured {
		return a
	}
	if a.Protocol == "" {
		a.Protocol = b.Protocol
	}
	by := map[string]int{}
	for i, sc := range a.Scenarios {
		by[sc.Name] = i
	}
	for _, sc := range b.Scenarios {
		i, ok := by[sc.Name]
		if !ok {
			a.Scenarios = append(a.Scenarios, sc)
			continue
		}
		a.Scenarios[i] = mergeScenario(a.Scenarios[i], sc)
	}
	return a
}

func mergeScenario(a, b ScenarioReport) ScenarioReport {
	errs := append(append([]string{}, a.Errors...), b.Errors...)
	if len(errs) > 8 {
		errs = errs[:8]
	}
	c := countsFromScenario(a)
	c.merge(countsFromScenario(b))
	return c.scenario(a.Name, errs)
}

func sortScenarios(sc []ScenarioReport) {
	for i := 1; i < len(sc); i++ {
		j := i
		for j > 0 && sc[j].Name < sc[j-1].Name {
			sc[j], sc[j-1] = sc[j-1], sc[j]
			j--
		}
	}
}
