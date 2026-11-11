// Package outcome attaches a summarized session to a git commit, and optionally
// to a GitHub pull request. It does not call the network unless Options.Lookup
// is set. A session stays unlinked when the only candidate would be a guess.
// An sdf git hook event or an SDF-Session trailer names the session. The
// time-window heuristic runs only when neither one does.
package outcome

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/summary"
)

const defaultSlack = 2 * time.Second

// Options controls linking. Lookup nil means pull requests are not queried.
type Options struct {
	// Repo is a checkout the user named with --repo. It is not inferred.
	Repo string
	// Slack widens the session window for a heuristic committer-time match.
	// Zero selects two seconds. A negative value selects no slack.
	Slack time.Duration
	// Lookup queries GitHub. Leave it nil to stay offline.
	Lookup Lookup
	// GitHubNote is recorded when the user asked for GitHub and Lookup is nil.
	GitHubNote string
}

// Attach fills Outcome on each session and CostPerAccepted on the report.
// It reads the local git directory only. Lookup is the only network path.
func Attach(report *summary.Report, opt Options) {
	if report == nil {
		return
	}
	slack := defaultSlack
	if opt.Slack < 0 {
		slack = 0
	} else if opt.Slack > 0 {
		slack = opt.Slack
	}
	linked := make([]*summary.SessionOutcome, len(report.Sessions))
	scan := &trailerScan{}
	for i := range report.Sessions {
		linked[i] = linkSession(report.Sessions[i], opt, slack, scan)
	}
	dropHeuristicsClaimedByStronger(report.Sessions, linked)
	dropSharedHeuristics(report.Sessions, linked)
	for i := range report.Sessions {
		if opt.Lookup != nil && linked[i].Status == "linked" {
			enrich(linked[i], report.Sessions[i].Repository, opt.Lookup)
		} else if linked[i].Status == "linked" && linked[i].Accepted == nil && linked[i].AcceptedReason == "" {
			linked[i].AcceptedReason = offlineReason(opt)
		}
		report.Sessions[i].Outcome = linked[i]
	}
	rolled := CostPerAccepted(report.Sessions)
	report.CostPerAccepted = &rolled
}

func offlineReason(opt Options) string {
	if opt.GitHubNote != "" {
		return opt.GitHubNote
	}
	return "merge status was not requested; pass --github to query pull requests"
}

func dropHeuristicsClaimedByStronger(sessions []summary.SessionSummary, linked []*summary.SessionOutcome) {
	claimed := map[string]string{}
	for i, item := range linked {
		if item == nil || item.Status != "linked" || item.Match == "heuristic" {
			continue
		}
		for _, commit := range item.Commits {
			claimed[item.Repo+"\x00"+commit.SHA] = sessions[i].SessionID
		}
	}
	for i, item := range linked {
		if item == nil || item.Status != "linked" || item.Match != "heuristic" || len(item.Commits) != 1 {
			continue
		}
		owner, ok := claimed[item.Repo+"\x00"+item.Commits[0].SHA]
		if !ok || owner == sessions[i].SessionID {
			continue
		}
		linked[i] = &summary.SessionOutcome{
			Status:         "unlinked",
			AcceptedReason: "commit is attributed to another session",
			Reason:         "commit " + item.Commits[0].SHA + " is attributed to " + owner,
		}
	}
}

func dropSharedHeuristics(sessions []summary.SessionSummary, linked []*summary.SessionOutcome) {
	owners := map[string][]int{}
	for i, item := range linked {
		if item.Status != "linked" || item.Match != "heuristic" || len(item.Commits) != 1 {
			continue
		}
		key := item.Repo + "\x00" + item.Commits[0].SHA
		owners[key] = append(owners[key], i)
	}
	for _, idxs := range owners {
		if len(idxs) < 2 {
			continue
		}
		names := make([]string, len(idxs))
		for n, idx := range idxs {
			names[n] = sessions[idx].SessionID
		}
		for _, idx := range idxs {
			linked[idx] = &summary.SessionOutcome{
				Status:         "unlinked",
				AcceptedReason: "commit also falls in another session",
				Reason:         "commit " + linked[idx].Commits[0].SHA + " also falls in " + strings.Join(otherIDs(names, sessions[idx].SessionID), ", "),
			}
		}
	}
}

func otherIDs(ids []string, self string) []string {
	var out []string
	for _, id := range ids {
		if id != self {
			out = append(out, id)
		}
	}
	return out
}

// CostPerAccepted divides the computed cost of merged sessions by how many
// there are. A missing outcome or a missing cost on an accepted session
// makes the ratio unknown. Zero merged sessions is unknown, not zero.
func CostPerAccepted(sessions []summary.SessionSummary) summary.CostPerAccepted {
	out := summary.CostPerAccepted{Status: "unknown", Sessions: len(sessions)}
	if len(sessions) == 0 {
		out.Reason = "no sessions"
		return out
	}
	var (
		acceptedCost big.Rat
		accepted     int
		shas         = map[string]int{}
	)
	for _, session := range sessions {
		if session.ComputedCost == nil || session.ComputedCost.Status != "known" || session.ComputedCost.USD == "" {
			out.UnknownCost++
		}
		if session.Outcome == nil || session.Outcome.Accepted == nil {
			out.UnknownOutcome++
			continue
		}
		if !*session.Outcome.Accepted {
			continue
		}
		accepted++
		if session.ComputedCost == nil || session.ComputedCost.Status != "known" || session.ComputedCost.USD == "" {
			continue
		}
		part, ok := new(big.Rat).SetString(session.ComputedCost.USD)
		if !ok {
			out.Reason = "accepted session " + session.SessionID + " has a computed cost that is not a decimal"
			out.Status = "unknown"
			out.USD = ""
			return out
		}
		acceptedCost.Add(&acceptedCost, part)
		for _, commit := range session.Outcome.Commits {
			shas[commit.SHA]++
		}
	}
	if unknownAccepted := countAcceptedUnknown(sessions); unknownAccepted > 0 {
		out.Reason = fmt.Sprintf("%d accepted sessions have unknown computed cost", unknownAccepted)
		out.USD = ""
		return out
	}
	if out.UnknownOutcome > 0 {
		out.Reason = fmt.Sprintf("%d sessions have no known outcome", out.UnknownOutcome)
		return out
	}
	for _, n := range shas {
		if n > 1 {
			out.Reason = "the same commit is linked to more than one accepted session"
			return out
		}
	}
	if accepted == 0 {
		out.Reason = "no merged outcome"
		return out
	}
	total, ok := decimalString(&acceptedCost)
	if !ok {
		out.Reason = "accepted cost is not a terminating decimal"
		return out
	}
	each := new(big.Rat).Quo(&acceptedCost, big.NewRat(int64(accepted), 1))
	text, exact := decimalString(each)
	if !exact {
		text = roundDecimal(each, 12)
		out.Note = "ratio rounded to 12 decimal places"
	}
	out.Status = "known"
	out.USD = text
	out.AcceptedOutcomes = accepted
	out.AcceptedCostUSD = total
	out.Reason = ""
	if out.UnknownCost > 0 {
		out.Note = joinNote(out.Note, fmt.Sprintf("%d sessions have unknown computed cost and are not accepted", out.UnknownCost))
	}
	return out
}

func countAcceptedUnknown(sessions []summary.SessionSummary) int {
	n := 0
	for _, session := range sessions {
		if session.Outcome == nil || session.Outcome.Accepted == nil || !*session.Outcome.Accepted {
			continue
		}
		if session.ComputedCost == nil || session.ComputedCost.Status != "known" || session.ComputedCost.USD == "" {
			n++
		}
	}
	return n
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

func decimalString(rat *big.Rat) (string, bool) {
	if rat.Sign() == 0 {
		return "0", true
	}
	neg := rat.Sign() < 0
	num := new(big.Int).Abs(rat.Num())
	den := new(big.Int).Set(rat.Denom())
	two := factor(den, 2)
	five := factor(den, 5)
	if den.Cmp(big.NewInt(1)) != 0 {
		return "", false
	}
	scale := two
	if five > scale {
		scale = five
	}
	if scale > 24 {
		return "", false
	}
	scaled := new(big.Int).Set(num)
	if scale > two {
		scaled.Mul(scaled, new(big.Int).Exp(big.NewInt(2), big.NewInt(int64(scale-two)), nil))
	}
	if scale > five {
		scaled.Mul(scaled, new(big.Int).Exp(big.NewInt(5), big.NewInt(int64(scale-five)), nil))
	}
	digits := scaled.String()
	var text string
	if scale == 0 {
		text = digits
	} else if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
		point := len(digits) - scale
		text = digits[:point] + "." + strings.TrimRight(digits[point:], "0")
	} else {
		point := len(digits) - scale
		frac := strings.TrimRight(digits[point:], "0")
		text = digits[:point]
		if frac != "" {
			text += "." + frac
		}
	}
	if neg {
		return "-" + text, true
	}
	return text, true
}

func roundDecimal(rat *big.Rat, places int) string {
	return strings.TrimRight(strings.TrimRight(rat.FloatString(places), "0"), ".")
}

func factor(n *big.Int, p int64) int {
	base := big.NewInt(p)
	count := 0
	mod := new(big.Int)
	for {
		mod.Mod(n, base)
		if mod.Sign() != 0 {
			return count
		}
		n.Quo(n, base)
		count++
	}
}
