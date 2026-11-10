// Package summary turns collected telemetry into one block per session.
//
// Claude Code, Codex, OpenCode, and Pi do not emit the same signals. Every
// number names the signal it came from. A signal that did not arrive is
// listed as missing, with the reason when the telemetry itself explains it.
package summary

import "encoding/json"

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
	SessionID      string        `json:"session_id"`
	Tool           string        `json:"tool"`
	ServiceNames   []string      `json:"service_names"`
	Models         []string      `json:"models"`
	Traces         int           `json:"traces"`
	Logs           int           `json:"logs"`
	Metrics        int           `json:"metrics"`
	DurationMS     *float64      `json:"duration_ms"`
	DurationSource *string       `json:"duration_source"`
	Tokens         OrderedFloats `json:"tokens"`
	TokensSource   *string       `json:"tokens_source"`
	TokensMethod   *string       `json:"tokens_method"`
	CostUSD        *float64      `json:"cost_usd"`
	CostSource     *string       `json:"cost_source"`
	CostNote       *string       `json:"cost_note"`
	Spans          []SpanNode    `json:"spans"`
	SpansHidden    int           `json:"spans_hidden"`
	LogEvents      []LogLine     `json:"log_events"`
	MetricLines    []MetricLine  `json:"metric_lines"`
	Missing        []Missing     `json:"missing"`
	ToolCalls      []ToolCall    `json:"tool_calls"`
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
	Sessions     []SessionSummary `json:"sessions"`
	Unattributed []Unattributed   `json:"unattributed"`
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
