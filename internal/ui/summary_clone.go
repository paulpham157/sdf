package ui

import (
	"slices"

	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/summary"
)

// cloneReport is a deep copy. The cached summary is one value shared by
// later requests, so a caller that writes into a slice or a map must not
// be writing into that cached value. Scalar pointers are copied too: they
// are the same kind of shared mutable memory.
func cloneReport(r summary.Report) summary.Report {
	out := r
	out.Sessions = cloneSlice(r.Sessions, cloneSession)
	out.Unattributed = cloneSlice(r.Unattributed, cloneUnattributed)
	out.CostPerAccepted = cloneVal(r.CostPerAccepted)
	out.Budget = cloneVal(r.Budget)
	return out
}

func cloneSession(s summary.SessionSummary) summary.SessionSummary {
	s.ServiceNames = slices.Clone(s.ServiceNames)
	s.Models = slices.Clone(s.Models)
	s.DurationMS = cloneVal(s.DurationMS)
	s.DurationSource = cloneVal(s.DurationSource)
	s.Tokens = cloneOrdered(s.Tokens)
	s.TokensSource = cloneVal(s.TokensSource)
	s.TokensMethod = cloneVal(s.TokensMethod)
	s.CostUSD = cloneVal(s.CostUSD)
	s.CostSource = cloneVal(s.CostSource)
	s.CostNote = cloneVal(s.CostNote)
	s.AgentReportedCostUSD = cloneVal(s.AgentReportedCostUSD)
	s.AgentReportedCostSource = cloneVal(s.AgentReportedCostSource)
	s.AgentReportedCostNote = cloneVal(s.AgentReportedCostNote)
	s.ComputedCost = cloneCost(s.ComputedCost)
	s.Spans = cloneSlice(s.Spans, cloneSpan)
	s.LogEvents = cloneSlice(s.LogEvents, cloneLogLine)
	s.MetricLines = cloneSlice(s.MetricLines, cloneMetricLine)
	s.Missing = slices.Clone(s.Missing)
	s.ToolCalls = cloneSlice(s.ToolCalls, cloneToolCall)
	s.Repository = cloneRepository(s.Repository)
	s.Outcome = cloneOutcome(s.Outcome)
	return s
}

func cloneOrdered(o summary.OrderedFloats) summary.OrderedFloats {
	o.Keys = slices.Clone(o.Keys)
	if o.Vals != nil {
		vals := make(map[string]float64, len(o.Vals))
		for k, v := range o.Vals {
			vals[k] = v
		}
		o.Vals = vals
	}
	return o
}

func cloneSpan(n summary.SpanNode) summary.SpanNode {
	n.DurationMS = cloneVal(n.DurationMS)
	n.CallID = cloneVal(n.CallID)
	n.ToolName = cloneVal(n.ToolName)
	n.Success = cloneVal(n.Success)
	n.SuccessSource = cloneVal(n.SuccessSource)
	n.WaitMS = cloneVal(n.WaitMS)
	n.WaitSource = cloneVal(n.WaitSource)
	n.ExecutionMS = cloneVal(n.ExecutionMS)
	n.ExecutionSource = cloneVal(n.ExecutionSource)
	n.StatusMessage = cloneVal(n.StatusMessage)
	n.ErrorType = cloneVal(n.ErrorType)
	n.Children = cloneSlice(n.Children, cloneSpan)
	return n
}

func cloneLogLine(l summary.LogLine) summary.LogLine {
	l.Detail = cloneVal(l.Detail)
	return l
}

func cloneMetricLine(m summary.MetricLine) summary.MetricLine {
	m.Unit = cloneVal(m.Unit)
	m.Detail = cloneVal(m.Detail)
	return m
}

func cloneToolCall(c summary.ToolCall) summary.ToolCall {
	c.Name = cloneVal(c.Name)
	c.CallID = cloneVal(c.CallID)
	c.Success = cloneVal(c.Success)
	c.DurationMS = cloneVal(c.DurationMS)
	c.Count = cloneVal(c.Count)
	c.WaitMS = cloneVal(c.WaitMS)
	c.WaitSource = cloneVal(c.WaitSource)
	c.ExecutionMS = cloneVal(c.ExecutionMS)
	c.ExecutionSource = cloneVal(c.ExecutionSource)
	return c
}

func cloneRepository(r summary.RepositoryFacts) summary.RepositoryFacts {
	r.WorkDirs = slices.Clone(r.WorkDirs)
	r.CommitSHAs = slices.Clone(r.CommitSHAs)
	r.StartUnixNano = cloneVal(r.StartUnixNano)
	r.EndUnixNano = cloneVal(r.EndUnixNano)
	r.HookCommits = slices.Clone(r.HookCommits)
	return r
}

func cloneOutcome(o *summary.SessionOutcome) *summary.SessionOutcome {
	if o == nil {
		return nil
	}
	v := *o
	v.Commits = slices.Clone(o.Commits)
	v.Accepted = cloneVal(o.Accepted)
	if o.PullRequest != nil {
		pr := *o.PullRequest
		v.PullRequest = &pr
	}
	return &v
}

func cloneUnattributed(u summary.Unattributed) summary.Unattributed {
	u.Names = slices.Clone(u.Names)
	return u
}

func cloneCost(c *pricing.Cost) *pricing.Cost {
	if c == nil {
		return nil
	}
	v := *c
	v.Components = slices.Clone(c.Components)
	return &v
}

func cloneSlice[T any](in []T, clone func(T) T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	for i := range in {
		out[i] = clone(in[i])
	}
	return out
}

func cloneVal[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
