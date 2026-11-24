package decision

import (
	"math/big"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/store"
)

// OverviewChartsSchema names the shape of GET /api/overview-charts.
// A change that removes or renames a field gets a new name.
const OverviewChartsSchema = "sdf.overview.charts/v1"

// OverviewCharts is the read-only document the overview page plots.
// Totals and findings match sdf decision audit on the same store, plus
// commits_with_implements and a day series. SDF-Decision trailers are not
// in the store, so recall counts only an implements edge from commit:SHA
// to a stored decision (see issue #281). Days are UTC calendar dates.
type OverviewCharts struct {
	Schema        string        `json:"schema"`
	Totals        ChartTotals   `json:"totals"`
	RecallByDay   []RecallDay   `json:"recall_by_day"`
	UnlinkedByDay []UnlinkedDay `json:"unlinked_by_day"`
}

// ChartTotals are store-wide figures, using every stored edge whatever its
// time, the same way Audit does.
type ChartTotals struct {
	CommitsTotal                  int    `json:"commits_total"`
	CommitsWithImplements         int    `json:"commits_with_implements"`
	CommitsLinked                 int    `json:"commits_linked"`
	UnlinkedCommits               int    `json:"unlinked_commits"`
	UnlinkedDecisions             int    `json:"unlinked_decisions"`
	DecisionsActive               int    `json:"decisions_active"`
	DecisionsActiveWithImplements int    `json:"decisions_active_with_implements"`
	RecallStatus                  string `json:"recall_status"`
	Recall                        string `json:"recall,omitempty"`
	RecallReason                  string `json:"recall_reason,omitempty"`
}

// RecallDay is one UTC day of commit-link recall. Daily recall is omitted
// when that day has no commits (it is not zero). Cumulative recall uses
// every commit through that day.
type RecallDay struct {
	Day                    string `json:"day"`
	CommitsTotal           int    `json:"commits_total"`
	CommitsWithImplements  int    `json:"commits_with_implements"`
	CommitsLinked          int    `json:"commits_linked"`
	RecallStatus           string `json:"recall_status"`
	Recall                 string `json:"recall,omitempty"`
	CumulativeTotal        int    `json:"cumulative_total"`
	CumulativeImplements   int    `json:"cumulative_implements"`
	CumulativeRecallStatus string `json:"cumulative_recall_status"`
	CumulativeRecall       string `json:"cumulative_recall,omitempty"`
}

// UnlinkedDay is the audit gaps whose commit time or decision recorded_at
// falls on that UTC day.
type UnlinkedDay struct {
	Day               string `json:"day"`
	UnlinkedCommits   int    `json:"unlinked_commits"`
	UnlinkedDecisions int    `json:"unlinked_decisions"`
}

// Charts builds the overview chart document from the same stored
// structure Audit reads: decisions, edges, retractions, and commit events.
// It reads no log body or commit message and writes nothing.
func Charts(s *store.Store, opt AuditOptions) (OverviewCharts, error) {
	g, err := Load(s)
	if err != nil {
		return OverviewCharts{}, err
	}
	events, err := s.LoadCommitEvents()
	if err != nil {
		return OverviewCharts{}, err
	}
	return projectCharts(g, events, opt), nil
}

func projectCharts(g Graph, events []store.CommitEvent, opt AuditOptions) OverviewCharts {
	report := audit(g, events, opt)
	out := OverviewCharts{
		Schema:        OverviewChartsSchema,
		RecallByDay:   []RecallDay{},
		UnlinkedByDay: []UnlinkedDay{},
	}
	out.Totals.CommitsTotal = report.Totals.CommitsTotal
	out.Totals.CommitsLinked = report.Totals.CommitsLinked
	out.Totals.UnlinkedCommits = len(report.Findings.UnlinkedSessionCommits)
	out.Totals.UnlinkedDecisions = len(report.Findings.ActiveWithoutImplements)
	out.Totals.DecisionsActive = report.Totals.DecisionsActive
	out.Totals.DecisionsActiveWithImplements = report.Totals.DecisionsActiveWithImplements

	byID := make(map[string]Record, len(g.Decisions))
	for _, d := range g.Decisions {
		byID[d.ID] = d
	}
	isDecision := func(e Endpoint) bool {
		if e.Kind != KindDecision {
			return false
		}
		_, ok := byID[e.Ref]
		return ok
	}
	implementsFrom := map[string]bool{}
	for _, e := range g.Edges {
		if e.Relation == RelImplements && e.From.Kind == KindCommit && isDecision(e.To) {
			implementsFrom[e.From.Ref] = true
		}
	}

	type commit struct {
		first   store.CommitEvent
		hasSess bool
	}
	var order []string
	commits := map[string]*commit{}
	for _, ev := range events {
		c := commits[ev.SHA]
		if c == nil {
			c = &commit{first: ev}
			commits[ev.SHA] = c
			order = append(order, ev.SHA)
		}
		if ev.SessionID != "" {
			c.hasSess = true
		}
	}
	linkedBySHA := map[string]bool{}
	for _, c := range report.Commits {
		linkedBySHA[c.SHA] = c.Linked
	}

	type recDay struct {
		total, implements, linked int
	}
	recallDays := map[string]*recDay{}
	var firstDay, lastDay string
	noteDay := func(day string) {
		if firstDay == "" || day < firstDay {
			firstDay = day
		}
		if lastDay == "" || day > lastDay {
			lastDay = day
		}
	}
	for _, sha := range order {
		c := commits[sha]
		if !opt.within(c.first.Time) {
			continue
		}
		if implementsFrom[sha] {
			out.Totals.CommitsWithImplements++
		}
		day, ok := utcDay(c.first.Time)
		if !ok {
			continue
		}
		noteDay(day)
		d := recallDays[day]
		if d == nil {
			d = &recDay{}
			recallDays[day] = d
		}
		d.total++
		if implementsFrom[sha] {
			d.implements++
		}
		if linkedBySHA[sha] {
			d.linked++
		}
	}
	out.Totals.RecallStatus, out.Totals.Recall, out.Totals.RecallReason = ratio(out.Totals.CommitsWithImplements, out.Totals.CommitsTotal)

	unlinkedDays := map[string]*UnlinkedDay{}
	for _, c := range report.Findings.UnlinkedSessionCommits {
		day, ok := utcDay(c.Timestamp)
		if !ok {
			continue
		}
		noteDay(day)
		d := unlinkedDays[day]
		if d == nil {
			d = &UnlinkedDay{Day: day}
			unlinkedDays[day] = d
		}
		d.UnlinkedCommits++
	}
	for _, d := range report.Findings.ActiveWithoutImplements {
		day, ok := utcDay(d.RecordedAt)
		if !ok {
			continue
		}
		noteDay(day)
		u := unlinkedDays[day]
		if u == nil {
			u = &UnlinkedDay{Day: day}
			unlinkedDays[day] = u
		}
		u.UnlinkedDecisions++
	}

	if firstDay == "" {
		return out
	}
	var cumTotal, cumImpl int
	for _, day := range daysInclusive(firstDay, lastDay) {
		r := recallDays[day]
		if r == nil {
			r = &recDay{}
		}
		cumTotal += r.total
		cumImpl += r.implements
		item := RecallDay{
			Day:                   day,
			CommitsTotal:          r.total,
			CommitsWithImplements: r.implements,
			CommitsLinked:         r.linked,
			CumulativeTotal:       cumTotal,
			CumulativeImplements:  cumImpl,
		}
		item.RecallStatus, item.Recall, _ = ratio(r.implements, r.total)
		item.CumulativeRecallStatus, item.CumulativeRecall, _ = ratio(cumImpl, cumTotal)
		out.RecallByDay = append(out.RecallByDay, item)
		u := unlinkedDays[day]
		if u == nil {
			u = &UnlinkedDay{Day: day}
		}
		out.UnlinkedByDay = append(out.UnlinkedByDay, *u)
	}
	return out
}

func utcDay(stamp string) (string, bool) {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return "", false
	}
	return t.UTC().Format("2006-01-02"), true
}

func daysInclusive(from, to string) []string {
	start, err1 := time.Parse("2006-01-02", from)
	end, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil || start.After(end) {
		return nil
	}
	out := make([]string, 0, int(end.Sub(start).Hours()/24)+1)
	for !start.After(end) {
		out = append(out, start.Format("2006-01-02"))
		start = start.AddDate(0, 0, 1)
	}
	return out
}

// ratio is n/d as a decimal string. A zero denominator is unknown, not
// zero. A terminating decimal (2/5 = 0.4) is written without trailing
// zeros; a repeating one is trimmed to 12 fractional digits.
func ratio(n, d int) (status, value, reason string) {
	if d == 0 {
		return "unknown", "", "no commits"
	}
	r := new(big.Rat).SetFrac64(int64(n), int64(d))
	if r.Sign() == 0 {
		return "known", "0", ""
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
	if den.Cmp(big.NewInt(1)) == 0 {
		text := strings.TrimRight(r.FloatString(24), "0")
		text = strings.TrimRight(text, ".")
		if text == "" || text == "-" {
			return "known", "0", ""
		}
		return "known", text, ""
	}
	text := strings.TrimRight(strings.TrimRight(r.FloatString(12), "0"), ".")
	if text == "" || text == "-" {
		return "known", "0", ""
	}
	return "known", text, ""
}
