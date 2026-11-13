package summary

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/pricing"
)

// LoadBudget reads one local ceiling from path.
// An empty path means no budget is configured and returns a nil budget.
// The file names one ceiling in USD and one window, "store" or "day".
// A day window also names the calendar day as YYYY-MM-DD, read in UTC.
func LoadBudget(path string) (*Budget, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("budget: %w", err)
	}
	var doc struct {
		CeilingUSD string `json:"ceiling_usd"`
		Window     string `json:"window"`
		Day        string `json:"day"`
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("budget: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("budget: extra data after the object")
	}
	ceiling := strings.TrimSpace(doc.CeilingUSD)
	if ceiling == "" {
		return nil, fmt.Errorf("budget: ceiling_usd is required")
	}
	amount, ok := new(big.Rat).SetString(ceiling)
	if !ok || amount.Sign() < 0 || strings.HasPrefix(ceiling, "+") {
		return nil, fmt.Errorf("budget: ceiling_usd %q is not a non-negative decimal", doc.CeilingUSD)
	}
	window := doc.Window
	if window != "store" && window != "day" {
		return nil, fmt.Errorf("budget: window %q is not store or day", window)
	}
	budget := &Budget{CeilingUSD: ceiling, Window: window}
	if window == "day" {
		day, err := time.ParseInLocation("2006-01-02", doc.Day, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("budget: day: %w", err)
		}
		budget.Day = day
	} else if strings.TrimSpace(doc.Day) != "" {
		return nil, fmt.Errorf("budget: day is only used when window is day")
	}
	return budget, nil
}

// ApplyBudget compares computed session cost with budget and sets report.Budget.
// A nil budget leaves the report unchanged. The comparison is reporting only.
func ApplyBudget(report *Report, budget *Budget) {
	if report == nil || budget == nil {
		return
	}
	status := evaluateBudget(report.Sessions, budget)
	report.Budget = &status
}

func evaluateBudget(sessions []SessionSummary, budget *Budget) BudgetStatus {
	out := BudgetStatus{
		Status:     "unknown",
		CeilingUSD: budget.CeilingUSD,
		Window:     budget.Window,
	}
	if budget.Window == "day" {
		out.Day = budget.Day.UTC().Format("2006-01-02")
	}
	windowed := sessions
	if budget.Window == "day" {
		windowed = sessionsOnDay(sessions, budget.Day)
	}
	out.Sessions = len(windowed)
	if len(windowed) == 0 {
		out.Reason = "no sessions"
		return out
	}
	ceiling, ok := new(big.Rat).SetString(budget.CeilingUSD)
	if !ok || ceiling.Sign() < 0 {
		out.Reason = "ceiling is not a non-negative decimal"
		return out
	}
	var total big.Rat
	for _, session := range windowed {
		usd, known := knownComputedUSD(session.ComputedCost)
		if !known {
			out.UnknownCost++
			continue
		}
		part, ok := new(big.Rat).SetString(usd)
		if !ok {
			out.Reason = "session " + session.SessionID + " has a computed cost that is not a decimal"
			out.TotalUSD = ""
			return out
		}
		total.Add(&total, part)
	}
	if out.UnknownCost > 0 {
		out.Reason = unknownCostPhrase(out.UnknownCost)
		out.TotalUSD = ""
		return out
	}
	text, ok := budgetDecimal(&total)
	if !ok {
		out.Reason = "computed cost total is not a terminating decimal"
		return out
	}
	out.TotalUSD = text
	out.Reason = ""
	if total.Cmp(ceiling) > 0 {
		out.Status = "over"
		return out
	}
	out.Status = "within"
	return out
}

func sessionsOnDay(sessions []SessionSummary, day time.Time) []SessionSummary {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	var out []SessionSummary
	for _, session := range sessions {
		if session.Repository.StartUnixNano == nil {
			continue
		}
		when := time.Unix(0, *session.Repository.StartUnixNano).UTC()
		if !when.Before(start) && when.Before(end) {
			out = append(out, session)
		}
	}
	return out
}

func unknownCostPhrase(n int) string {
	if n == 1 {
		return "1 session has unknown computed cost"
	}
	return fmt.Sprintf("%d sessions have unknown computed cost", n)
}

// budgetDecimal renders rat as a plain decimal. A fraction that does not
// terminate in base 10 is not a cost this report will print.
func budgetDecimal(rat *big.Rat) (string, bool) {
	if rat == nil || rat.Sign() < 0 {
		return "", false
	}
	if rat.Sign() == 0 {
		return "0", true
	}
	den := new(big.Int).Set(rat.Denom())
	two, five, zero := big.NewInt(2), big.NewInt(5), big.NewInt(0)
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
	text := strings.TrimRight(rat.FloatString(24), "0")
	text = strings.TrimRight(text, ".")
	if text == "" {
		return "", false
	}
	back, ok := new(big.Rat).SetString(text)
	if !ok || back.Cmp(rat) != 0 {
		return "", false
	}
	return text, true
}

func knownComputedUSD(cost *pricing.Cost) (string, bool) {
	if cost == nil || cost.Status != "known" || cost.USD == "" {
		return "", false
	}
	return cost.USD, true
}
