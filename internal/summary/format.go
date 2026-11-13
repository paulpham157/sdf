package summary

import (
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/paulpham157/sdf/internal/pricing"
)

// Format renders the text summary. It always ends with a newline.
func Format(report Report) string {
	if len(report.Sessions) == 0 && len(report.Unattributed) == 0 {
		return "No telemetry sessions in the store.\n"
	}
	var lines []string
	for _, session := range report.Sessions {
		lines = append(lines, "session "+session.SessionID)
		lines = append(lines, "  tool: "+session.Tool)
		lines = append(lines, "  service: "+join(session.ServiceNames))
		lines = append(lines, "  model: "+join(session.Models))
		lines = append(lines,
			"  signals: traces="+itoa(session.Traces)+" logs="+itoa(session.Logs)+" metrics="+itoa(session.Metrics),
		)
		lines = append(lines, "  duration_ms: "+field(session.DurationMS, session.DurationSource))
		lines = append(lines, "  tokens: "+formatTokens(session))
		lines = append(lines, "  cost_usd: "+formatCost(session))
		if session.ComputedCost != nil {
			lines = append(lines, "  agent_reported_cost_usd: "+formatCost(session))
			lines = append(lines, "  computed_cost_usd: "+formatComputed(session.ComputedCost))
		}
		if len(session.Spans) > 0 {
			lines = append(lines, "  spans:")
		} else {
			lines = append(lines, "  spans: (none in the default view)")
		}
		for _, node := range session.Spans {
			formatSpan(&lines, node, 2)
		}
		if session.SpansHidden > 0 {
			noun := "spans"
			if session.SpansHidden == 1 {
				noun = "span"
			}
			lines = append(lines, "    ("+itoa(session.SpansHidden)+" internal "+noun+" hidden; rerun with --all-spans)")
		}
		lines = append(lines, "  logs:")
		if len(session.LogEvents) > 0 {
			for _, event := range session.LogEvents {
				detail := ""
				if event.Detail != nil {
					detail = " " + *event.Detail
				}
				lines = append(lines, "    - "+event.Name+" x"+itoa(event.Count)+detail)
			}
		} else {
			lines = append(lines, "    - (none)")
		}
		lines = append(lines, "  metrics:")
		if len(session.MetricLines) > 0 {
			for _, metric := range session.MetricLines {
				unit := ""
				if metric.Unit != nil && *metric.Unit != "" {
					unit = " " + *metric.Unit
				}
				detail := ""
				if metric.Detail != nil && *metric.Detail != "" {
					detail = " " + *metric.Detail
				}
				lines = append(lines, "    - "+metric.Name+detail+" "+fmtNum(metric.Value)+unit+" ("+metric.Source+")")
			}
		} else {
			lines = append(lines, "    - (none)")
		}
		lines = append(lines, "  tool_calls:")
		if len(session.ToolCalls) > 0 {
			for _, call := range session.ToolCalls {
				lines = append(lines, "    - "+formatCall(call))
			}
		} else {
			lines = append(lines, "    - (none reported)")
		}
		if len(session.Missing) > 0 {
			lines = append(lines, "  missing:")
			for _, item := range session.Missing {
				lines = append(lines, "    - "+item.Signal+": "+item.Reason)
			}
		} else {
			lines = append(lines, "  missing: (none)")
		}
		if session.Outcome != nil {
			lines = append(lines, "  outcome: "+formatOutcome(session.Outcome))
		}
	}
	if len(report.Unattributed) > 0 {
		lines = append(lines, "unattributed:")
		for _, group := range report.Unattributed {
			lines = append(lines,
				"  "+group.Tool+" signals traces="+itoa(group.Traces)+" logs="+itoa(group.Logs)+" metrics="+itoa(group.Metrics),
			)
			lines = append(lines, "    reason: "+group.Reason)
			for _, name := range group.Names {
				lines = append(lines, "    - "+name)
			}
		}
	}
	if report.CostPerAccepted != nil {
		lines = append(lines, "cost_per_accepted_outcome_usd: "+formatPerAccepted(report.CostPerAccepted))
	}
	if report.Budget != nil {
		lines = append(lines, "budget: "+formatBudget(report.Budget))
	}
	return strings.Join(lines, "\n") + "\n"
}

// WriteJSON writes the summary as indented JSON, with a trailing newline.
// HTML characters are not escaped, matching Python's json.dump.
func WriteJSON(w io.Writer, report Report) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func join(values []string) string {
	if len(values) == 0 {
		return "(not reported)"
	}
	return strings.Join(values, ", ")
}

func field(value *float64, source *string) string {
	if value == nil {
		return "(not reported)"
	}
	rendered := fmtNum(*value)
	if source != nil && *source != "" {
		return rendered + " (" + *source + ")"
	}
	return rendered
}

func formatTokens(session SessionSummary) string {
	if session.Tokens.Len() == 0 {
		return "(not reported)"
	}
	parts := make([]string, 0, session.Tokens.Len())
	for _, key := range session.Tokens.Keys {
		parts = append(parts, key+"="+fmtNum(session.Tokens.Vals[key]))
	}
	note := ""
	if session.TokensSource != nil {
		note = *session.TokensSource
	}
	if session.TokensMethod != nil && *session.TokensMethod != "" {
		if note != "" {
			note = note + "; " + *session.TokensMethod
		} else {
			note = *session.TokensMethod
		}
	}
	rendered := strings.Join(parts, " ")
	if note != "" {
		return rendered + " (" + note + ")"
	}
	return rendered
}

func formatCost(session SessionSummary) string {
	if session.CostUSD == nil {
		return "(not reported)"
	}
	rendered := field(session.CostUSD, session.CostSource)
	if session.CostNote != nil && *session.CostNote != "" {
		return rendered + "; " + *session.CostNote
	}
	return rendered
}

func formatComputed(cost *pricing.Cost) string {
	if cost == nil || cost.Status == "" {
		return "(not computed)"
	}
	meta := cost.PriceSource
	if cost.PriceDate != "" {
		if meta != "" {
			meta += " retrieved " + cost.PriceDate
		} else {
			meta = "retrieved " + cost.PriceDate
		}
	}
	if cost.PriceTable != "" {
		if meta != "" {
			meta += ", table " + cost.PriceTable
		} else {
			meta = "table " + cost.PriceTable
		}
	}
	if cost.Status != "known" {
		reason := cost.Reason
		if reason == "" {
			reason = "unknown"
		}
		if meta != "" {
			return "unknown (" + reason + "; " + meta + ")"
		}
		return "unknown (" + reason + ")"
	}
	detail := meta
	if cost.CatalogID != "" {
		piece := "model " + cost.CatalogID
		if cost.MatchedBy != "" && cost.MatchedBy != "exact" {
			piece += " matched by " + cost.MatchedBy
		}
		if detail != "" {
			detail = piece + "; " + detail
		} else {
			detail = piece
		}
	}
	if cost.Note != "" {
		if detail != "" {
			detail += "; " + cost.Note
		} else {
			detail = cost.Note
		}
	}
	if detail == "" {
		return cost.USD
	}
	return cost.USD + " (" + detail + ")"
}

func formatOutcome(outcome *SessionOutcome) string {
	if outcome.Status != "linked" {
		reason := outcome.Reason
		if reason == "" {
			reason = "unlinked"
		}
		return "unlinked (" + reason + ")"
	}
	parts := []string{outcome.Match}
	if outcome.Repo != "" {
		parts = append(parts, "repo="+outcome.Repo)
	}
	for _, commit := range outcome.Commits {
		bit := commit.SHA
		if commit.Subject != "" {
			bit += " " + commit.Subject
		}
		parts = append(parts, bit)
	}
	text := strings.Join(parts, " ")
	if outcome.PullRequest != nil {
		state := "not merged"
		switch {
		case outcome.PullRequest.Merged:
			state = "merged"
		case strings.EqualFold(outcome.PullRequest.State, "closed"):
			state = "closed without merging"
		}
		text += "; pr #" + itoa(outcome.PullRequest.Number) + " " + state
	}
	if outcome.CI != "" {
		text += "; ci=" + outcome.CI
	}
	switch {
	case outcome.Accepted == nil:
		text += "; accepted=unknown"
		if outcome.AcceptedReason != "" {
			text += " (" + outcome.AcceptedReason + ")"
		}
	case *outcome.Accepted:
		text += "; accepted=true"
	default:
		text += "; accepted=false"
		if outcome.AcceptedReason != "" {
			text += " (" + outcome.AcceptedReason + ")"
		}
	}
	return text
}

func formatBudget(budget *BudgetStatus) string {
	var head string
	if budget.Status == "unknown" {
		reason := budget.Reason
		if reason == "" {
			reason = "unknown"
		}
		head = "unknown (" + reason + ")"
	} else {
		head = budget.Status + " (total_usd=" + budget.TotalUSD + ")"
	}
	text := head + " ceiling_usd=" + budget.CeilingUSD + " window=" + budget.Window
	if budget.Day != "" {
		text += " day=" + budget.Day
	}
	return text
}

func formatPerAccepted(cost *CostPerAccepted) string {
	if cost.Status != "known" {
		reason := cost.Reason
		if reason == "" {
			reason = "unknown"
		}
		return "unknown (" + reason + ")"
	}
	text := cost.USD + " (accepted_outcomes=" + itoa(cost.AcceptedOutcomes) + ", accepted_cost_usd=" + cost.AcceptedCostUSD + ")"
	if cost.Note != "" {
		text += "; " + cost.Note
	}
	return text
}

func formatCall(call ToolCall) string {
	name := "(unnamed)"
	if call.Name != nil && *call.Name != "" {
		name = *call.Name
	}
	parts := []string{name}
	if call.CallID != nil && *call.CallID != "" {
		parts = append(parts, "call_id="+*call.CallID)
	}
	if call.Success != nil && *call.Success != "" {
		parts = append(parts, "success="+*call.Success)
	}
	if call.DurationMS != nil {
		parts = append(parts, "duration_ms="+fmtNum(*call.DurationMS))
	}
	if call.WaitMS != nil {
		parts = append(parts, "wait_ms="+fmtNum(*call.WaitMS)+" ("+deref(call.WaitSource)+")")
	}
	if call.ExecutionMS != nil {
		parts = append(parts, "execution_ms="+fmtNum(*call.ExecutionMS)+" ("+deref(call.ExecutionSource)+")")
	}
	parts = append(parts, "source="+call.Source)
	return strings.Join(parts, " ")
}

func formatSpan(lines *[]string, node SpanNode, indent int) {
	pad := strings.Repeat("  ", indent)
	bits := []string{node.Name}
	if node.ToolName != nil && *node.ToolName != "" && *node.ToolName != node.Name && !strings.HasSuffix(node.Name, " "+*node.ToolName) {
		bits = append(bits, *node.ToolName)
	}
	if node.CallID != nil && *node.CallID != "" {
		bits = append(bits, "call_id="+*node.CallID)
	}
	if node.DurationMS != nil {
		bits = append(bits, fmtNum(*node.DurationMS)+"ms")
	}
	if node.StatusCode == "ERROR" {
		if node.StatusMessage != nil && *node.StatusMessage != "" {
			bits = append(bits, "status=ERROR ("+*node.StatusMessage+")")
		} else {
			bits = append(bits, "status=ERROR")
		}
	}
	if node.Success != nil && *node.Success != "" {
		if node.SuccessSource != nil && *node.SuccessSource != "" {
			bits = append(bits, "success="+*node.Success+" ("+*node.SuccessSource+")")
		} else {
			bits = append(bits, "success="+*node.Success)
		}
	}
	if node.WaitMS != nil {
		bits = append(bits, "wait="+fmtNum(*node.WaitMS)+"ms ("+deref(node.WaitSource)+")")
	}
	if node.ExecutionMS != nil {
		bits = append(bits, "execution="+fmtNum(*node.ExecutionMS)+"ms ("+deref(node.ExecutionSource)+")")
	}
	*lines = append(*lines, pad+"- "+strings.Join(bits, " "))
	for _, child := range node.Children {
		formatSpan(lines, child, indent+1)
	}
}

// FormatNumber renders a summary quantity the way the text report does.
func FormatNumber(value float64) string {
	return fmtNum(value)
}

// fmtNum matches Python's _fmt: an integer when the value is within 1e-9 of
// a whole number, otherwise six significant digits.
func fmtNum(value float64) string {
	rounded := math.Round(value)
	if math.Abs(value-rounded) < 1e-9 {
		return strconv.FormatInt(int64(rounded), 10)
	}
	return strconv.FormatFloat(value, 'g', 6, 64)
}
