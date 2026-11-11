package ui

import (
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/summary"
)

// Input and output field names the summary actually emits. A session uses
// one of them. Two at once is not a total this page will add together.
var (
	inputKeys  = []string{"input", "input_tokens", "input_token_count"}
	outputKeys = []string{"output", "output_tokens", "output_token_count"}
)

// SnapshotMeta is server configuration the page can name. It is not a cost.
type SnapshotMeta struct {
	GitHub     bool
	PriceTable string
}

// Snapshot is the summary report arranged for the page.
// Sessions are newest first. Cost figures are the summary's figures.
type Snapshot struct {
	Sessions        []SessionView           `json:"sessions"`
	Unattributed    []summary.Unattributed  `json:"unattributed"`
	CostPerAccepted summary.CostPerAccepted `json:"cost_per_accepted_outcome"`
	Totals          Totals                  `json:"totals"`
	Agents          []AgentRollup           `json:"agents"`
	GitHub          bool                    `json:"github"`
	PriceTable      string                  `json:"price_table,omitempty"`
	PriceSource     string                  `json:"price_source,omitempty"`
	PriceDate       string                  `json:"price_date,omitempty"`
}

// Totals are sums of summary fields. A missing piece makes the amount
// unknown and leaves Value empty. Counts are counts of sessions, so zero
// means none, which is known.
type Totals struct {
	Sessions             int    `json:"sessions"`
	ComputedCostUSD      Amount `json:"computed_cost_usd"`
	AgentReportedCostUSD Amount `json:"agent_reported_cost_usd"`
	InputTokens          Amount `json:"input_tokens"`
	OutputTokens         Amount `json:"output_tokens"`
	Linked               int    `json:"linked"`
	LinkedExact          int    `json:"linked_exact"`
	LinkedHook           int    `json:"linked_hook"`
	LinkedTrailer        int    `json:"linked_trailer"`
	LinkedHeuristic      int    `json:"linked_heuristic"`
	Unlinked             int    `json:"unlinked"`
	Merged               int    `json:"merged"`
	NotMerged            int    `json:"not_merged"`
	AcceptedUnknown      int    `json:"accepted_unknown"`
}

// AgentRollup is one tool. CostPerAccepted is outcome.CostPerAccepted on
// that tool's sessions only.
type AgentRollup struct {
	Tool                 string                  `json:"tool"`
	Sessions             int                     `json:"sessions"`
	CostPerAccepted      summary.CostPerAccepted `json:"cost_per_accepted_outcome"`
	ComputedCostUSD      Amount                  `json:"computed_cost_usd"`
	AgentReportedCostUSD Amount                  `json:"agent_reported_cost_usd"`
	InputTokens          Amount                  `json:"input_tokens"`
	OutputTokens         Amount                  `json:"output_tokens"`
	Linked               int                     `json:"linked"`
	LinkedExact          int                     `json:"linked_exact"`
	LinkedHook           int                     `json:"linked_hook"`
	LinkedTrailer        int                     `json:"linked_trailer"`
	LinkedHeuristic      int                     `json:"linked_heuristic"`
	Unlinked             int                     `json:"unlinked"`
	Merged               int                     `json:"merged"`
	NotMerged            int                     `json:"not_merged"`
	AcceptedUnknown      int                     `json:"accepted_unknown"`
}

// SessionView is one summary session plus display fields that stay unknown
// when the summary has no value. Summary is the session object from the report.
type SessionView struct {
	Summary          summary.SessionSummary `json:"summary"`
	LinkMethod       string                 `json:"link_method"`
	LinkReason       string                 `json:"link_reason,omitempty"`
	InputTokens      Amount                 `json:"input_tokens"`
	OutputTokens     Amount                 `json:"output_tokens"`
	ComputedCostUSD  Amount                 `json:"computed_cost_usd"`
	AgentReportedUSD Amount                 `json:"agent_reported_cost_usd"`
	ModelBreakdown   ModelBreakdown         `json:"model_breakdown"`
	ToolCallsStatus  string                 `json:"tool_calls_status"`
	ToolCounts       []ToolCount            `json:"tool_counts"`
	Started          Amount                 `json:"started"`
	StartedUnixNano  string                 `json:"started_unix_nano,omitempty"`
	Duration         Amount                 `json:"duration"`
}

// Amount is a number the summary established, or unknown with a reason.
// Value is omitted when Status is not "known", including when a cost would
// otherwise be mistaken for zero.
type Amount struct {
	Status string `json:"status"`
	Value  string `json:"value,omitempty"`
	Detail string `json:"detail,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ModelBreakdown says whether the session token total belongs to one model.
// Several models stay unknown: the summary does not split that total.
type ModelBreakdown struct {
	Status string                 `json:"status"`
	Reason string                 `json:"reason,omitempty"`
	Note   string                 `json:"note,omitempty"`
	Models []string               `json:"models"`
	Tokens *summary.OrderedFloats `json:"tokens,omitempty"`
}

// ToolCount is one tool name. Status "known" carries Count. An empty tool
// list is reported on the session as none_reported, not as a zero count.
type ToolCount struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Count  string `json:"count,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// BuildSnapshot projects a summary report. It does not price tokens again.
// The report's cost-per-accepted value is copied. Each agent calls
// outcome.CostPerAccepted on its own sessions.
func BuildSnapshot(report summary.Report, meta SnapshotMeta) Snapshot {
	sessions := append([]summary.SessionSummary(nil), report.Sessions...)
	slices.SortStableFunc(sessions, newer)
	views := make([]SessionView, 0, len(sessions))
	for _, session := range sessions {
		views = append(views, sessionView(session))
	}
	cost := report.CostPerAccepted
	if cost == nil {
		rolled := outcome.CostPerAccepted(report.Sessions)
		cost = &rolled
	}
	unattributed := report.Unattributed
	if unattributed == nil {
		unattributed = []summary.Unattributed{}
	}
	agents := rollups(report.Sessions)
	snap := Snapshot{
		Sessions:        views,
		Unattributed:    unattributed,
		CostPerAccepted: *cost,
		Totals:          totals(report.Sessions),
		Agents:          agents,
		GitHub:          meta.GitHub,
		PriceTable:      meta.PriceTable,
	}
	for _, session := range report.Sessions {
		if session.ComputedCost == nil {
			continue
		}
		if snap.PriceSource == "" {
			snap.PriceSource = session.ComputedCost.PriceSource
		}
		if snap.PriceDate == "" {
			snap.PriceDate = session.ComputedCost.PriceDate
		}
		if snap.PriceSource != "" && snap.PriceDate != "" {
			break
		}
	}
	return snap
}

func sessionView(session summary.SessionSummary) SessionView {
	method, reason := linkMethod(session.Outcome)
	view := SessionView{
		Summary:          session,
		LinkMethod:       method,
		LinkReason:       reason,
		InputTokens:      tokenAmount(session.Tokens, inputKeys, "no input tokens in the summary"),
		OutputTokens:     tokenAmount(session.Tokens, outputKeys, "no output tokens in the summary"),
		ComputedCostUSD:  computedAmount(session.ComputedCost),
		AgentReportedUSD: agentAmount(session),
		ModelBreakdown:   modelBreakdown(session),
		Started:          startedAmount(session),
		Duration:         durationAmount(session),
	}
	if session.Repository.StartUnixNano != nil {
		view.StartedUnixNano = strconv.FormatInt(*session.Repository.StartUnixNano, 10)
	}
	view.ToolCallsStatus, view.ToolCounts = toolCounts(session.ToolCalls)
	return view
}

func newer(a, b summary.SessionSummary) int {
	an, bn := a.Repository.StartUnixNano, b.Repository.StartUnixNano
	switch {
	case an != nil && bn != nil && *an != *bn:
		if *an > *bn {
			return -1
		}
		return 1
	case an != nil && bn == nil:
		return -1
	case an == nil && bn != nil:
		return 1
	}
	if a.Tool != b.Tool {
		if a.Tool < b.Tool {
			return -1
		}
		return 1
	}
	if a.SessionID < b.SessionID {
		return -1
	}
	if a.SessionID > b.SessionID {
		return 1
	}
	return 0
}

func linkMethod(o *summary.SessionOutcome) (string, string) {
	if o == nil {
		return "none", "outcome was not attached"
	}
	if o.Status == "linked" && knownLink(o.Match) {
		return o.Match, o.Reason
	}
	if o.Reason != "" {
		return "none", o.Reason
	}
	return "none", "unlinked"
}

// knownLink is the link order summary uses: exact, hook, trailer, heuristic.
func knownLink(method string) bool {
	switch method {
	case "exact", "hook", "trailer", "heuristic":
		return true
	default:
		return false
	}
}

func tokenAmount(tokens summary.OrderedFloats, keys []string, missing string) Amount {
	value, key, n := pickToken(tokens, keys)
	switch n {
	case 0:
		return Amount{Status: "unknown", Reason: missing}
	case 1:
		return Amount{Status: "known", Value: summary.FormatNumber(value), Detail: key}
	default:
		return Amount{Status: "unknown", Reason: "more than one token field matches (" + strings.Join(keys, ", ") + ")"}
	}
}

func pickToken(tokens summary.OrderedFloats, keys []string) (float64, string, int) {
	n := 0
	var value float64
	var key string
	for _, candidate := range keys {
		got, ok := tokens.Get(candidate)
		if !ok {
			continue
		}
		n++
		value = got
		key = candidate
	}
	return value, key, n
}

func computedAmount(cost *pricing.Cost) Amount {
	if cost == nil || cost.Status != "known" || cost.USD == "" {
		reason := "not computed"
		if cost != nil && cost.Reason != "" {
			reason = cost.Reason
		}
		return Amount{Status: "unknown", Reason: reason}
	}
	return Amount{Status: "known", Value: cost.USD, Detail: cost.Model}
}

func agentAmount(session summary.SessionSummary) Amount {
	if session.AgentReportedCostUSD == nil {
		return Amount{Status: "unknown", Reason: "agent did not report a cost"}
	}
	detail := ""
	if session.AgentReportedCostSource != nil {
		detail = *session.AgentReportedCostSource
	}
	amount := Amount{Status: "known", Value: summary.FormatNumber(*session.AgentReportedCostUSD), Detail: detail}
	if session.AgentReportedCostNote != nil && *session.AgentReportedCostNote != "" {
		if amount.Detail != "" {
			amount.Detail += "; " + *session.AgentReportedCostNote
		} else {
			amount.Detail = *session.AgentReportedCostNote
		}
	}
	return amount
}

func startedAmount(session summary.SessionSummary) Amount {
	if session.Repository.StartUnixNano == nil {
		return Amount{Status: "unknown", Reason: "no timestamp on the session"}
	}
	when := time.Unix(0, *session.Repository.StartUnixNano).UTC().Format(time.RFC3339Nano)
	return Amount{Status: "known", Value: when}
}

func durationAmount(session summary.SessionSummary) Amount {
	if session.DurationMS == nil {
		return Amount{Status: "unknown", Reason: "not reported"}
	}
	detail := ""
	if session.DurationSource != nil {
		detail = *session.DurationSource
	}
	return Amount{Status: "known", Value: summary.FormatNumber(*session.DurationMS), Detail: detail}
}

func modelBreakdown(session summary.SessionSummary) ModelBreakdown {
	models := session.Models
	if models == nil {
		models = []string{}
	}
	switch len(models) {
	case 0:
		return ModelBreakdown{Status: "unknown", Reason: "no model id on the session", Models: models}
	case 1:
		if session.Tokens.Len() == 0 {
			return ModelBreakdown{Status: "unknown", Reason: "no token counts", Models: models}
		}
		tokens := session.Tokens
		return ModelBreakdown{
			Status: "known",
			Note:   "session totals for " + models[0] + "; sdf summary does not split these further by request",
			Models: models,
			Tokens: &tokens,
		}
	default:
		return ModelBreakdown{
			Status: "unknown",
			Reason: "the summary keeps one token total for the session and does not split it across " + strings.Join(models, ", "),
			Models: models,
		}
	}
}

func toolCounts(calls []summary.ToolCall) (string, []ToolCount) {
	if len(calls) == 0 {
		return "none_reported", []ToolCount{}
	}
	type acc struct {
		sum big.Rat
		bad bool
	}
	order := []string{}
	by := map[string]*acc{}
	for _, call := range calls {
		name := "(unnamed)"
		if call.Name != nil && strings.TrimSpace(*call.Name) != "" {
			name = *call.Name
		}
		item := by[name]
		if item == nil {
			item = &acc{}
			by[name] = item
			order = append(order, name)
		}
		if item.bad {
			continue
		}
		var part *big.Rat
		if call.Count == nil {
			part = big.NewRat(1, 1)
		} else {
			parsed, ok := new(big.Rat).SetString(strconv.FormatFloat(*call.Count, 'f', -1, 64))
			if !ok || parsed.Sign() < 0 {
				item.bad = true
				continue
			}
			part = parsed
		}
		item.sum.Add(&item.sum, part)
	}
	slices.Sort(order)
	counts := make([]ToolCount, 0, len(order))
	for _, name := range order {
		item := by[name]
		if item.bad {
			counts = append(counts, ToolCount{Name: name, Status: "unknown", Reason: "tool call count is not a non-negative number"})
			continue
		}
		text, ok := formatRat(&item.sum)
		if !ok {
			counts = append(counts, ToolCount{Name: name, Status: "unknown", Reason: "tool call count is not a terminating decimal"})
			continue
		}
		counts = append(counts, ToolCount{Name: name, Status: "known", Count: text})
	}
	return "reported", counts
}

func totals(sessions []summary.SessionSummary) Totals {
	out := Totals{
		Sessions:             len(sessions),
		ComputedCostUSD:      sumComputed(sessions),
		AgentReportedCostUSD: sumAgent(sessions),
		InputTokens:          sumTokens(sessions, inputKeys, "input"),
		OutputTokens:         sumTokens(sessions, outputKeys, "output"),
	}
	counted := classify(sessions)
	out.Linked = counted.linked
	out.LinkedExact = counted.exact
	out.LinkedHook = counted.hook
	out.LinkedTrailer = counted.trailer
	out.LinkedHeuristic = counted.heuristic
	out.Unlinked = counted.unlinked
	out.Merged = counted.merged
	out.NotMerged = counted.notMerged
	out.AcceptedUnknown = counted.acceptedUnknown
	return out
}

func rollups(sessions []summary.SessionSummary) []AgentRollup {
	var tools []string
	grouped := map[string][]summary.SessionSummary{}
	for _, session := range sessions {
		if _, ok := grouped[session.Tool]; !ok {
			tools = append(tools, session.Tool)
		}
		grouped[session.Tool] = append(grouped[session.Tool], session)
	}
	slices.Sort(tools)
	out := make([]AgentRollup, 0, len(tools))
	for _, tool := range tools {
		group := grouped[tool]
		counted := classify(group)
		out = append(out, AgentRollup{
			Tool:                 tool,
			Sessions:             len(group),
			CostPerAccepted:      outcome.CostPerAccepted(group),
			ComputedCostUSD:      sumComputed(group),
			AgentReportedCostUSD: sumAgent(group),
			InputTokens:          sumTokens(group, inputKeys, "input"),
			OutputTokens:         sumTokens(group, outputKeys, "output"),
			Linked:               counted.linked,
			LinkedExact:          counted.exact,
			LinkedHook:           counted.hook,
			LinkedTrailer:        counted.trailer,
			LinkedHeuristic:      counted.heuristic,
			Unlinked:             counted.unlinked,
			Merged:               counted.merged,
			NotMerged:            counted.notMerged,
			AcceptedUnknown:      counted.acceptedUnknown,
		})
	}
	return out
}

type linkCounts struct {
	linked, exact, hook, trailer, heuristic, unlinked int
	merged, notMerged, acceptedUnknown                int
}

func classify(sessions []summary.SessionSummary) linkCounts {
	var counted linkCounts
	for _, session := range sessions {
		o := session.Outcome
		switch {
		case o != nil && o.Status == "linked" && o.Match == "exact":
			counted.linked++
			counted.exact++
		case o != nil && o.Status == "linked" && o.Match == "hook":
			counted.linked++
			counted.hook++
		case o != nil && o.Status == "linked" && o.Match == "trailer":
			counted.linked++
			counted.trailer++
		case o != nil && o.Status == "linked" && o.Match == "heuristic":
			counted.linked++
			counted.heuristic++
		default:
			counted.unlinked++
		}
		switch {
		case o == nil || o.Accepted == nil:
			counted.acceptedUnknown++
		case *o.Accepted:
			counted.merged++
		default:
			counted.notMerged++
		}
	}
	return counted
}

func sumComputed(sessions []summary.SessionSummary) Amount {
	if len(sessions) == 0 {
		return Amount{Status: "unknown", Reason: "no sessions"}
	}
	var total big.Rat
	unknown := 0
	for _, session := range sessions {
		if session.ComputedCost == nil || session.ComputedCost.Status != "known" || session.ComputedCost.USD == "" {
			unknown++
			continue
		}
		part, ok := new(big.Rat).SetString(session.ComputedCost.USD)
		if !ok {
			return Amount{Status: "unknown", Reason: "computed cost for " + session.SessionID + " is not a decimal"}
		}
		total.Add(&total, part)
	}
	if unknown > 0 {
		return Amount{Status: "unknown", Reason: countPhrase(unknown, "has unknown computed cost", "have unknown computed cost")}
	}
	text, ok := formatRat(&total)
	if !ok {
		return Amount{Status: "unknown", Reason: "computed cost total is not a terminating decimal"}
	}
	return Amount{Status: "known", Value: text}
}

func sumAgent(sessions []summary.SessionSummary) Amount {
	if len(sessions) == 0 {
		return Amount{Status: "unknown", Reason: "no sessions"}
	}
	var total big.Rat
	missing := 0
	for _, session := range sessions {
		if session.AgentReportedCostUSD == nil {
			missing++
			continue
		}
		part, ok := new(big.Rat).SetString(strconv.FormatFloat(*session.AgentReportedCostUSD, 'f', -1, 64))
		if !ok {
			return Amount{Status: "unknown", Reason: "agent-reported cost for " + session.SessionID + " is not a decimal"}
		}
		total.Add(&total, part)
	}
	if missing > 0 {
		return Amount{Status: "unknown", Reason: countPhrase(missing, "did not report a cost", "did not report a cost")}
	}
	text, ok := formatRat(&total)
	if !ok {
		return Amount{Status: "unknown", Reason: "agent-reported cost total is not a terminating decimal"}
	}
	return Amount{Status: "known", Value: text}
}

func sumTokens(sessions []summary.SessionSummary, keys []string, label string) Amount {
	if len(sessions) == 0 {
		return Amount{Status: "unknown", Reason: "no sessions"}
	}
	var total big.Rat
	missing := 0
	for _, session := range sessions {
		value, _, n := pickToken(session.Tokens, keys)
		if n != 1 {
			missing++
			continue
		}
		part, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'f', -1, 64))
		if !ok {
			return Amount{Status: "unknown", Reason: label + " total for " + session.SessionID + " is not a decimal"}
		}
		total.Add(&total, part)
	}
	if missing > 0 {
		return Amount{Status: "unknown", Reason: countPhrase(missing, "has no single "+label+" total", "have no single "+label+" total")}
	}
	text, ok := formatRat(&total)
	if !ok {
		return Amount{Status: "unknown", Reason: label + " total is not a terminating decimal"}
	}
	return Amount{Status: "known", Value: text}
}

func countPhrase(n int, one, many string) string {
	if n == 1 {
		return "1 session " + one
	}
	return fmt.Sprintf("%d sessions %s", n, many)
}

func formatRat(r *big.Rat) (string, bool) {
	if r == nil {
		return "", false
	}
	if r.Sign() == 0 {
		return "0", true
	}
	den := new(big.Int).Set(r.Denom())
	two := big.NewInt(2)
	five := big.NewInt(5)
	zero := big.NewInt(0)
	tmp := new(big.Int)
	for tmp.Mod(den, two).Cmp(zero) == 0 {
		den.Quo(den, two)
	}
	for tmp.Mod(den, five).Cmp(zero) == 0 {
		den.Quo(den, five)
	}
	if den.Cmp(big.NewInt(1)) != 0 {
		return "", false
	}
	text := strings.TrimRight(r.FloatString(24), "0")
	text = strings.TrimRight(text, ".")
	if text == "" || text == "-" {
		return "", false
	}
	back, ok := new(big.Rat).SetString(text)
	if !ok || back.Cmp(r) != 0 {
		return "", false
	}
	return text, true
}
