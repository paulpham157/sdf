// Package summary turns collected telemetry into one block per session.
//
// Claude Code, Codex, OpenCode, and Pi do not emit the same signals. Every
// number names the signal it came from. A signal that did not arrive is
// listed as missing, with the reason when the telemetry itself explains it.
package summary

import (
	"encoding/json"

	"github.com/paulpham157/sdf/internal/pricing"
)

// ToolCall is one tool invocation joined from a span and, when present, a log.
type ToolCall struct {
	Name            *string  `json:"name"`
	CallID          *string  `json:"call_id"`
	Success         *string  `json:"success"`
	DurationMS      *float64 `json:"duration_ms"`
	Count           *float64 `json:"count"`
	Source          string   `json:"source"`
	WaitMS          *float64 `json:"wait_ms"`
	WaitSource      *string  `json:"wait_source"`
	ExecutionMS     *float64 `json:"execution_ms"`
	ExecutionSource *string  `json:"execution_source"`
}

// SpanNode is one node of the printed span tree.
type SpanNode struct {
	Name            string     `json:"name"`
	DurationMS      *float64   `json:"duration_ms"`
	CallID          *string    `json:"call_id"`
	ToolName        *string    `json:"tool_name"`
	Success         *string    `json:"success"`
	SuccessSource   *string    `json:"success_source"`
	WaitMS          *float64   `json:"wait_ms"`
	WaitSource      *string    `json:"wait_source"`
	ExecutionMS     *float64   `json:"execution_ms"`
	ExecutionSource *string    `json:"execution_source"`
	StatusCode      string     `json:"status_code"`
	StatusMessage   *string    `json:"status_message"`
	Children        []SpanNode `json:"children"`
}

// LogLine is a collapsed log event.
type LogLine struct {
	Name   string  `json:"name"`
	Count  int     `json:"count"`
	Detail *string `json:"detail"`
}

// MetricLine is one reduced metric point.
type MetricLine struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Unit   *string `json:"unit"`
	Detail *string `json:"detail"`
	Source string  `json:"source"`
}

// Missing explains a signal that did not arrive.
type Missing struct {
	Signal string `json:"signal"`
	Reason string `json:"reason"`
}

// SessionSummary is one real session.
type SessionSummary struct {
	SessionID               string        `json:"session_id"`
	Tool                    string        `json:"tool"`
	ServiceNames            []string      `json:"service_names"`
	Models                  []string      `json:"models"`
	Traces                  int           `json:"traces"`
	Logs                    int           `json:"logs"`
	Metrics                 int           `json:"metrics"`
	DurationMS              *float64      `json:"duration_ms"`
	DurationSource          *string       `json:"duration_source"`
	Tokens                  OrderedFloats `json:"tokens"`
	TokensSource            *string       `json:"tokens_source"`
	TokensMethod            *string       `json:"tokens_method"`
	CostUSD                 *float64      `json:"cost_usd"`
	CostSource              *string       `json:"cost_source"`
	CostNote                *string       `json:"cost_note"`
	AgentReportedCostUSD    *float64      `json:"agent_reported_cost_usd"`
	AgentReportedCostSource *string       `json:"agent_reported_cost_source"`
	AgentReportedCostNote   *string       `json:"agent_reported_cost_note"`
	// ComputedCost is SDF's price for the summarized token counts.
	// A nil USD inside it means the price is unknown, not zero.
	ComputedCost *pricing.Cost   `json:"computed_cost_usd"`
	Spans        []SpanNode      `json:"spans"`
	SpansHidden  int             `json:"spans_hidden"`
	LogEvents    []LogLine       `json:"log_events"`
	MetricLines  []MetricLine    `json:"metric_lines"`
	Missing      []Missing       `json:"missing"`
	ToolCalls    []ToolCall      `json:"tool_calls"`
	Repository   RepositoryFacts `json:"repository"`
	Outcome      *SessionOutcome `json:"outcome,omitempty"`
}

// RepositoryFacts is what the agent exported about where the session ran.
// Paths and commit ids come only from those attributes.
type RepositoryFacts struct {
	WorkDirs            []string `json:"work_dirs"`
	CommitSHAs          []string `json:"commit_shas"`
	RepositoryURL       string   `json:"repository_url,omitempty"`
	Owner               string   `json:"owner,omitempty"`
	RepositoryName      string   `json:"repository_name,omitempty"`
	Provider            string   `json:"provider,omitempty"`
	AmbiguousWorkDirs   bool     `json:"ambiguous_work_dirs,omitempty"`
	AmbiguousRepository bool     `json:"ambiguous_repository,omitempty"`
	StartUnixNano       *int64   `json:"start_unix_nano,omitempty"`
	EndUnixNano         *int64   `json:"end_unix_nano,omitempty"`
}

// SessionOutcome is the git result attached to one session.
// Match is "exact" or "heuristic" when Status is "linked".
// Accepted is nil when merge status was not established.
type SessionOutcome struct {
	Status         string         `json:"status"`
	Match          string         `json:"match,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	Repo           string         `json:"repo,omitempty"`
	Commits        []LinkedCommit `json:"commits,omitempty"`
	PullRequest    *PullRequest   `json:"pull_request,omitempty"`
	CI             string         `json:"ci,omitempty"`
	Accepted       *bool          `json:"accepted"`
	AcceptedReason string         `json:"accepted_reason,omitempty"`
}

// LinkedCommit is one commit the linker attached to a session.
type LinkedCommit struct {
	SHA           string `json:"sha"`
	Subject       string `json:"subject,omitempty"`
	CommitterUnix int64  `json:"committer_unix,omitempty"`
}

// PullRequest is a GitHub pull request that contains the linked commits.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url,omitempty"`
	State  string `json:"state,omitempty"`
	Merged bool   `json:"merged"`
}

// CostPerAccepted is computed cost divided by the number of merged outcomes.
// Status "unknown" leaves USD empty.
type CostPerAccepted struct {
	Status           string `json:"status"`
	USD              string `json:"usd,omitempty"`
	Reason           string `json:"reason,omitempty"`
	Note             string `json:"note,omitempty"`
	AcceptedOutcomes int    `json:"accepted_outcomes,omitempty"`
	AcceptedCostUSD  string `json:"accepted_cost_usd,omitempty"`
	Sessions         int    `json:"sessions"`
	UnknownCost      int    `json:"unknown_cost"`
	UnknownOutcome   int    `json:"unknown_outcome"`
}

// Unattributed is records that could not be joined to exactly one session.
type Unattributed struct {
	Tool    string   `json:"tool"`
	Traces  int      `json:"traces"`
	Logs    int      `json:"logs"`
	Metrics int      `json:"metrics"`
	Names   []string `json:"names"`
	Reason  string   `json:"reason"`
}

// Report is the full summary.
type Report struct {
	Sessions        []SessionSummary `json:"sessions"`
	Unattributed    []Unattributed   `json:"unattributed"`
	CostPerAccepted *CostPerAccepted `json:"cost_per_accepted_outcome,omitempty"`
}

// OrderedFloats is a JSON object that keeps insertion order. Python dicts
// do; encoding/json sorts map keys.
type OrderedFloats struct {
	Keys []string
	Vals map[string]float64
}

// Add sums v into key, appending key the first time it is seen.
func (o *OrderedFloats) Add(key string, v float64) {
	o.ensure()
	if _, ok := o.Vals[key]; !ok {
		o.Keys = append(o.Keys, key)
	}
	o.Vals[key] += v
}

// Set stores v at key, appending key the first time it is seen.
func (o *OrderedFloats) Set(key string, v float64) {
	o.ensure()
	if _, ok := o.Vals[key]; !ok {
		o.Keys = append(o.Keys, key)
	}
	o.Vals[key] = v
}

// Get returns the value stored at key.
func (o OrderedFloats) Get(key string) (float64, bool) {
	if o.Vals == nil {
		return 0, false
	}
	v, ok := o.Vals[key]
	return v, ok
}

// Len is the number of keys.
func (o OrderedFloats) Len() int { return len(o.Keys) }

func (o *OrderedFloats) ensure() {
	if o.Vals == nil {
		o.Vals = map[string]float64{}
	}
}

// MarshalJSON writes an object in insertion order. An empty value is {}.
func (o OrderedFloats) MarshalJSON() ([]byte, error) {
	if len(o.Keys) == 0 {
		return []byte("{}"), nil
	}
	buf := []byte{'{'}
	for i, key := range o.Keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(o.Vals[key])
		if err != nil {
			return nil, err
		}
		buf = append(buf, kb...)
		buf = append(buf, ':')
		buf = append(buf, vb...)
	}
	buf = append(buf, '}')
	return buf, nil
}
