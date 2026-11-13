// Package check runs a fixed set of local cases against the collector and
// reports how each one came out. It does not score them, and it does not
// decide whether a change may land.
//
// A case names one field of the report the collector produces and the value
// that field should have. The input is something the collector can already
// read: an OTLP export, or a summary report. A case whose input cannot be
// priced or cannot be linked is not_checked. Unknown is not zero, and it is
// not a pass.
package check

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/summary"
)

const (
	// Pass means the field matched the expected value.
	Pass = "pass"
	// Fail means the field was produced and did not match.
	Fail = "fail"
	// NotChecked means the input cannot be priced or cannot be linked.
	// It is reported and it is not a failure.
	NotChecked = "not_checked"
)

// Case is one reviewed fixture. ID is the file name without .json when the
// file omits it. Field is one path into the report, such as
// sessions.0.computed_cost_usd.usd. Expect is the value that path should have.
//
// Input is exactly one of OTLP or Summary. OTLP is an export the collector
// decodes. Summary is a report the collector already produced, checked
// without running it again.
type Case struct {
	ID      string          `json:"id"`
	Field   string          `json:"field"`
	Expect  json.RawMessage `json:"expect"`
	OTLP    *OTLPInput      `json:"otlp,omitempty"`
	Summary json.RawMessage `json:"summary,omitempty"`
}

// OTLPInput is one export body. Signal is traces, metrics, or logs.
type OTLPInput struct {
	Signal string          `json:"signal"`
	Body   json.RawMessage `json:"body"`
}

// Result is one case after it has been run.
type Result struct {
	ID     string
	Status string
	Got    string
	Expect string
	Note   string
}

// Report is every case, in the order they were given.
type Report struct {
	Results []Result
	Passed  int
	Failed  int
	Skipped int
}

// DefaultDir is the reviewed case set committed with this package.
func DefaultDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return filepath.Join("internal", "check", "testdata", "cases")
	}
	return filepath.Join(filepath.Dir(file), "testdata", "cases")
}

// Load reads every *.json case in dir. Names sort byte-wise, which is the
// order the cases run in. A name that is not a case file is an error.
func Load(dir string) ([]Case, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	cases := make([]Case, 0, len(names))
	for _, name := range names {
		one, err := loadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		cases = append(cases, one)
	}
	return cases, nil
}

func loadFile(path string) (Case, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Case{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var one Case
	if err := dec.Decode(&one); err != nil {
		return Case{}, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Case{}, fmt.Errorf("trailing data")
	}
	if one.ID == "" {
		one.ID = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	if err := validate(one); err != nil {
		return Case{}, err
	}
	return one, nil
}

func validate(one Case) error {
	if strings.TrimSpace(one.ID) == "" {
		return fmt.Errorf("case has no id")
	}
	if one.Field == "" {
		return fmt.Errorf("case %s has no field", one.ID)
	}
	if len(one.Expect) == 0 {
		return fmt.Errorf("case %s has no expect", one.ID)
	}
	if !json.Valid(one.Expect) {
		return fmt.Errorf("case %s expect is not json", one.ID)
	}
	hasOTLP := one.OTLP != nil
	hasSummary := len(one.Summary) > 0 && string(one.Summary) != "null"
	if hasOTLP == hasSummary {
		return fmt.Errorf("case %s needs exactly one of otlp or summary", one.ID)
	}
	if hasOTLP {
		if one.OTLP.Signal != "traces" && one.OTLP.Signal != "metrics" && one.OTLP.Signal != "logs" {
			return fmt.Errorf("case %s otlp signal must be traces, metrics, or logs", one.ID)
		}
		if !json.Valid(one.OTLP.Body) {
			return fmt.Errorf("case %s otlp body is not json", one.ID)
		}
	}
	return nil
}

// Run runs cases in order against the bundled price catalog. It links
// outcomes offline, so a session with no commit stays unlinked.
func Run(cases []Case) Report {
	return RunWith(cases, pricing.Bundled())
}

// RunWith is Run with an explicit price catalog. A nil catalog leaves every
// computed cost unknown, and a case that depends on that cost is not_checked.
func RunWith(cases []Case, prices *pricing.Catalog) Report {
	report := Report{Results: make([]Result, 0, len(cases))}
	for _, one := range cases {
		result := runOne(one, prices)
		switch result.Status {
		case Pass:
			report.Passed++
		case Fail:
			report.Failed++
		default:
			report.Skipped++
		}
		report.Results = append(report.Results, result)
	}
	return report
}

func runOne(one Case, prices *pricing.Catalog) Result {
	out := Result{ID: one.ID, Expect: canonical(one.Expect)}
	produced, err := produce(one, prices)
	if err != nil {
		out.Status = Fail
		out.Note = err.Error()
		return out
	}
	if note := blocked(one.Field, produced); note != "" {
		out.Status = NotChecked
		out.Note = note
		return out
	}
	got, ok, err := lookup(produced, one.Field)
	if err != nil {
		out.Status = Fail
		out.Note = err.Error()
		return out
	}
	if !ok {
		out.Status = Fail
		out.Note = "field " + one.Field + " is absent"
		return out
	}
	out.Got = canonical(got)
	if out.Got == out.Expect {
		out.Status = Pass
		return out
	}
	out.Status = Fail
	out.Note = "got " + out.Got + ", expected " + out.Expect
	return out
}

func produce(one Case, prices *pricing.Catalog) (summary.Report, error) {
	if one.OTLP != nil {
		records, err := otlp.Decode(one.OTLP.Signal, one.OTLP.Body, "application/json")
		if err != nil {
			return summary.Report{}, fmt.Errorf("decode otlp: %w", err)
		}
		report := summary.SummarizeWith(records, false, prices)
		outcome.Attach(&report, outcome.Options{})
		return report, nil
	}
	var report summary.Report
	if err := json.Unmarshal(one.Summary, &report); err != nil {
		return summary.Report{}, fmt.Errorf("decode summary: %w", err)
	}
	return report, nil
}

// blocked reports why a field cannot be judged. A field that does not depend
// on a price or a link is never blocked, so the other cases still run.
func blocked(field string, report summary.Report) string {
	if field == "cost_per_accepted_outcome" || strings.HasPrefix(field, "cost_per_accepted_outcome.") {
		if report.CostPerAccepted == nil || report.CostPerAccepted.Status != "known" {
			return "cost per accepted outcome is unknown"
		}
		return ""
	}
	index, rest, ok := sessionField(field)
	if !ok || index < 0 || index >= len(report.Sessions) {
		return ""
	}
	session := report.Sessions[index]
	switch {
	case costField(rest):
		if session.ComputedCost == nil || session.ComputedCost.Status != "known" {
			reason := "computed cost is unknown"
			if session.ComputedCost != nil && session.ComputedCost.Reason != "" {
				reason = session.ComputedCost.Reason
			}
			return reason
		}
	case outcomeField(rest):
		if session.Outcome == nil || session.Outcome.Status != "linked" {
			reason := "outcome is not linked"
			if session.Outcome != nil && session.Outcome.Reason != "" {
				reason = session.Outcome.Reason
			}
			return reason
		}
	}
	return ""
}

func sessionField(field string) (int, string, bool) {
	parts := strings.Split(field, ".")
	if len(parts) < 3 || parts[0] != "sessions" {
		return 0, "", false
	}
	index, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, "", false
	}
	return index, strings.Join(parts[2:], "."), true
}

func costField(rest string) bool {
	return rest == "computed_cost_usd" || strings.HasPrefix(rest, "computed_cost_usd.")
}

func outcomeField(rest string) bool {
	return rest == "outcome" || strings.HasPrefix(rest, "outcome.")
}

func lookup(report summary.Report, field string) (json.RawMessage, bool, error) {
	body, err := json.Marshal(report)
	if err != nil {
		return nil, false, err
	}
	var node any
	if err := json.Unmarshal(body, &node); err != nil {
		return nil, false, err
	}
	current := node
	for _, part := range strings.Split(field, ".") {
		switch box := current.(type) {
		case map[string]any:
			next, ok := box[part]
			if !ok {
				return nil, false, nil
			}
			current = next
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(box) {
				return nil, false, nil
			}
			current = box[index]
		default:
			return nil, false, nil
		}
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return nil, false, err
	}
	return encoded, true, nil
}

func canonical(raw json.RawMessage) string {
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return string(raw)
	}
	encoded, err := json.Marshal(node)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

// Format writes one line per case and a count of each outcome. The counts
// are not a score.
func Format(report Report) string {
	var lines []string
	for _, result := range report.Results {
		line := result.ID + " " + result.Status
		if result.Note != "" && result.Status != Pass {
			line += " " + result.Note
		}
		lines = append(lines, line)
	}
	lines = append(lines, fmt.Sprintf("%d passed, %d failed, %d not_checked", report.Passed, report.Failed, report.Skipped))
	return strings.Join(lines, "\n") + "\n"
}
