// Package brief selects the decisions a later command can show or check.
//
// It only reads a decision.Graph. It does not open the store, and it does
// not infer a decision from telemetry. decision.Load must already have set
// EffectiveStatus, Blocked, and Conflicted. A record that is not
// decision.EffectiveActive is left out, so retracted and superseded
// decisions never appear.
//
// A decision is placed in at most one group. The groups, in rank order, are:
//
//  1. Constraints. An active settled decision (status decision) recorded
//     with a human identity, or that is the source of a constrains edge.
//     A human identity is a GitHub login or the legacy word user
//     (decision.LockedDecidedBy). Those decisions are locked and come
//     first, newest first. The exact word agent is not a human identity,
//     so an older agent row stays out of this group unless it is the
//     source of a constrains edge, and then it is not locked. Other
//     constraints follow in insertion order. A locked decision stays here
//     even when it is also blocked or conflicted. Each statement is cut
//     to statementBudget bytes on a rune boundary, ending in "…", so one long rule cannot
//     crowd the others out.
//  2. Blocked. Any active record Load marked Blocked, hypothesis or
//     decision, that is not itself a constraint. An open question the
//     agent should not act on is included.
//  3. Conflicting. Any active record Load marked Conflicted, hypothesis
//     or decision, that is in neither group above.
//
// Challenged (the latest evidence edge is contradicts) is not a group.
// constrains is not the blocks relation: blocks is what makes Record.Blocked.
package brief

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/paulpham157/sdf/internal/decision"
)

const (
	// statementBudget is the most bytes of a statement an item keeps.
	// A longer statement is cut on a rune boundary and ends with "…".
	statementBudget = 200

	// KindConstraint is an active settled decision recorded with a human
	// identity (a GitHub login or legacy user), or the source of a
	// constrains edge. The exact word agent is not a human identity.
	KindConstraint = "constraint"
	// KindBlocked is a decision Load marked Blocked: active, with a blocks
	// edge whose blocker is still unsettled (open_blockers is not empty).
	KindBlocked = "blocked"
	// KindConflicting is a decision Load marked Conflicted: active, and
	// conflicts_with another active decision.
	KindConflicting = "conflicting"
)

// Item is one decision in the brief. Kind is KindConstraint, KindBlocked,
// or KindConflicting. Locked is true only for a constraint recorded with
// a human identity: a GitHub login or legacy user. The exact word agent
// is not locked. DecidedBy is empty when the record named none.
type Item struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Statement       string `json:"statement"`
	Status          string `json:"status"`
	DecidedBy       string `json:"decided_by"`
	DecidedByLegacy bool   `json:"decided_by_legacy,omitempty"`
	Locked          bool   `json:"locked"`
}

// Result is the ranked brief after caps. Items is never nil. Omitted is
// how many ranked items were left off. OmittedLocked is how many of those
// were locked rules (a login or legacy user); it is present even when
// zero. Locked rules are dropped only when they cannot fit at the
// statement floor.
type Result struct {
	Items         []Item `json:"items"`
	Omitted       int    `json:"omitted"`
	OmittedLocked int    `json:"omitted_locked"`
}

// Classify ranks the active constraints, blocked decisions, and conflicting
// decisions in g. The result is never nil.
func Classify(g decision.Graph) []Item {
	// Every active record, hypotheses included. Constraints below still
	// require status decision. decision.Active stays settled-only; Guard
	// reads that, not this list.
	order := make([]decision.Record, 0, len(g.Decisions))
	active := make(map[string]bool, len(g.Decisions))
	for _, d := range g.Decisions {
		if d.EffectiveStatus != decision.EffectiveActive {
			continue
		}
		active[d.ID] = true
		order = append(order, d)
	}
	constrainsFrom := map[string]bool{}
	for _, e := range g.Edges {
		if e.Relation != decision.RelConstrains || e.From.Kind != decision.KindDecision {
			continue
		}
		if active[e.From.Ref] {
			constrainsFrom[e.From.Ref] = true
		}
	}
	isConstraint := func(d decision.Record) bool {
		if d.Status != decision.StatusDecision {
			return false
		}
		return decision.LockedDecidedBy(d.DecidedBy) || constrainsFrom[d.ID]
	}

	var locked, other, blockedItems, conflicting []Item
	for _, d := range order {
		item := Item{
			ID:              d.ID,
			Statement:       fitStatement(d.Statement, statementBudget),
			Status:          d.Status,
			DecidedBy:       d.DecidedBy,
			DecidedByLegacy: decision.LegacyDecidedBy(d.DecidedBy),
		}
		switch {
		case isConstraint(d):
			item.Kind = KindConstraint
			item.Locked = decision.LockedDecidedBy(d.DecidedBy)
			if item.Locked {
				locked = append(locked, item)
			} else {
				other = append(other, item)
			}
		case d.Blocked:
			item.Kind = KindBlocked
			blockedItems = append(blockedItems, item)
		case d.Conflicted:
			item.Kind = KindConflicting
			conflicting = append(conflicting, item)
		}
	}
	reverseItems(locked)
	out := make([]Item, 0, len(locked)+len(other)+len(blockedItems)+len(conflicting))
	out = append(out, locked...)
	out = append(out, other...)
	out = append(out, blockedItems...)
	out = append(out, conflicting...)
	return out
}

func reverseItems(items []Item) {
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
}

// statementFloor is the shortest rendered statement budget used while
// trying to keep every locked rule. Only a pathological count drops a
// locked rule, and then the oldest ones go first.
const statementFloor = 40

// fitStatement cuts s so its rendered field (quoted, when quoting applies)
// is at most budget bytes. The cut is on a rune boundary and, when s is
// shortened, the statement ends with "…".
func fitStatement(s string, budget int) string {
	if budget < len(ellipsis) {
		budget = len(ellipsis)
	}
	if renderedLen(s) <= budget {
		return s
	}
	rs := []rune(s)
	lo, hi := 0, len(rs)
	best := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		cand := string(rs[:mid]) + ellipsis
		if renderedLen(cand) <= budget {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best < 0 {
		if renderedLen(ellipsis) <= budget {
			return ellipsis
		}
		return ""
	}
	return string(rs[:best]) + ellipsis
}

const ellipsis = "…"

// Select classifies g and applies the caps. maxItems or maxBytes below zero
// means that cap is off. The byte cap is the length of render's result.
// render may be nil when the byte cap is off.
func Select(g decision.Graph, maxItems, maxBytes int, render func(Result) []byte) Result {
	return Trim(Classify(g), maxItems, maxBytes, render)
}

// Trim keeps items in Classify's priority order. maxItems or maxBytes below
// zero means that cap is off. Locked rules are exempt from maxItems:
// every one is kept, newest first. Other tiers fill the item slots and the
// bytes left after the locked tier. maxBytes is a hard bound. When the
// locked tier does not fit at statementBudget, every locked statement is
// shortened to the same smaller budget, down to statementFloor, before any
// locked rule is dropped. Only when the floor still does not fit are the
// oldest locked rules dropped. A non-locked item that does not fit is
// skipped, and a later smaller one can still be kept. The byte check is one
// pass over each tier plus a binary search on the locked budget. render is
// only used to tell text from JSON. Nil means no byte cap.
func Trim(items []Item, maxItems, maxBytes int, render func(Result) []byte) Result {
	locked, rest := splitLocked(items)
	jsonOut := maxBytes >= 0 && jsonRender(render)
	if maxBytes < 0 {
		keptRestN := keptRest(maxItems, len(locked), len(rest))
		kept := append(append([]Item{}, locked...), takeRest(rest, keptRestN)...)
		return resultOf(kept, len(rest)-keptRestN, 0)
	}
	keptLocked, dropped := fitLocked(locked, len(rest), maxBytes, jsonOut)
	slots := slotsLeft(maxItems, len(keptLocked))
	frame := frameSize(len(items), dropped, jsonOut)
	used := itemsCost(keptLocked, jsonOut) + frame
	kept := append([]Item{}, keptLocked...)
	omittedRest := 0
	keptRestN := 0
	for _, item := range rest {
		if keptRestN >= slots {
			omittedRest++
			continue
		}
		cost := itemBytes(item, len(kept) > 0, jsonOut)
		if used+cost > maxBytes {
			omittedRest++
			continue
		}
		kept = append(kept, item)
		used += cost
		keptRestN++
	}
	return resultOf(kept, dropped+omittedRest, dropped)
}

// slotsLeft is how many non-locked items maxItems still allows after
// keptLocked locked rules. A negative maxItems means no item cap.
func slotsLeft(maxItems, keptLocked int) int {
	if maxItems < 0 {
		return int(^uint(0) >> 1)
	}
	n := maxItems - keptLocked
	if n < 0 {
		return 0
	}
	return n
}

func keptRest(maxItems, keptLocked, nRest int) int {
	slots := slotsLeft(maxItems, keptLocked)
	if slots > nRest {
		return nRest
	}
	return slots
}

func takeRest(rest []Item, slots int) []Item {
	if slots > len(rest) {
		slots = len(rest)
	}
	if slots < 0 {
		slots = 0
	}
	return rest[:slots]
}

// fitLocked keeps every locked item when it can. It first shortens every
// locked statement to one shared budget, from statementBudget down to
// statementFloor. Past the floor it drops the oldest items, which are last
// because the tier is newest-first. restN is how many non-locked items may
// still be omitted, so the footer reserved beside the locked tier is wide
// enough for them.
func fitLocked(locked []Item, restN, maxBytes int, jsonOut bool) ([]Item, int) {
	if len(locked) == 0 {
		return nil, 0
	}
	tryAll := func(budget int) ([]Item, bool) {
		fitted := refit(locked, budget)
		size := outputSize(fitted, restN, 0, jsonOut)
		return fitted, size <= maxBytes
	}
	if fitted, ok := tryAll(statementBudget); ok {
		return fitted, 0
	}
	best := -1
	var bestItems []Item
	lo, hi := statementFloor, statementBudget-1
	for lo <= hi {
		mid := (lo + hi) / 2
		if fitted, ok := tryAll(mid); ok {
			best = mid
			bestItems = fitted
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best >= 0 {
		return bestItems, 0
	}
	floorItems := refit(locked, statementFloor)
	bestN := 0
	lo, hi = 0, len(floorItems)
	for lo <= hi {
		mid := (lo + hi) / 2
		dropped := len(locked) - mid
		size := outputSize(floorItems[:mid], dropped+restN, dropped, jsonOut)
		if size <= maxBytes {
			bestN = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return floorItems[:bestN], len(locked) - bestN
}

func refit(items []Item, budget int) []Item {
	out := make([]Item, len(items))
	for i, item := range items {
		item.Statement = fitStatement(item.Statement, budget)
		out[i] = item
	}
	return out
}

func splitLocked(items []Item) (locked, rest []Item) {
	for _, item := range items {
		if item.Locked {
			locked = append(locked, item)
		} else {
			rest = append(rest, item)
		}
	}
	return locked, rest
}

// outputSize is the rendered size of kept with this omitted count. It
// matches FormatText and JSONBytes, including the footer only when
// omitted is greater than zero.
func outputSize(kept []Item, omitted, omittedLocked int, jsonOut bool) int {
	if jsonOut {
		return sizeJSON(kept, omitted, omittedLocked)
	}
	n := 0
	for _, item := range kept {
		n += len(textLine(item))
	}
	n += len(footerText(omitted, omittedLocked))
	return n
}

func footerText(omitted, omittedLocked int) string {
	if omitted <= 0 {
		return ""
	}
	if omittedLocked > 0 {
		return strconv.Itoa(omitted) + " more (" + strconv.Itoa(omittedLocked) + " locked): run sdf decision list\n"
	}
	return strconv.Itoa(omitted) + " more: run sdf decision list\n"
}

func sizeJSON(items []Item, omitted, omittedLocked int) int {
	if len(items) == 0 {
		return len("{\n  \"items\": [],\n  \"omitted\": ") + len(strconv.Itoa(omitted)) +
			len(",\n  \"omitted_locked\": ") + len(strconv.Itoa(omittedLocked)) + len("\n}\n")
	}
	n := len("{\n  \"items\": [\n")
	for i, item := range items {
		if i > 0 {
			n += len(",\n")
		}
		n += len(jsonItem(item))
	}
	n += len("\n  ],\n  \"omitted\": ") + len(strconv.Itoa(omitted)) +
		len(",\n  \"omitted_locked\": ") + len(strconv.Itoa(omittedLocked)) + len("\n}\n")
	return n
}

func itemsCost(items []Item, jsonOut bool) int {
	n := 0
	for i, item := range items {
		n += itemBytes(item, i > 0, jsonOut)
	}
	return n
}

// frameSize is the non-item bytes reserved for the widest footer or JSON
// envelope this brief might need. Digit width uses nItems so a later,
// smaller omitted count still fits in the reserve.
func frameSize(nItems, omittedLocked int, jsonOut bool) int {
	if nItems < 1 {
		nItems = 1
	}
	if !jsonOut {
		return len(footerText(nItems, omittedLocked))
	}
	return len("{\n  \"items\": [\n") + len("\n  ],\n  \"omitted\": ") + len(strconv.Itoa(nItems)) +
		len(",\n  \"omitted_locked\": ") + len(strconv.Itoa(omittedLocked)) + len("\n}\n")
}

// itemBytes is the rendered size of one item. A later JSON item also pays
// for the comma and newline that separate it from the one before.
func itemBytes(item Item, later, jsonOut bool) int {
	if !jsonOut {
		return len(textLine(item))
	}
	n := len(jsonItem(item))
	if later {
		n += len(",\n")
	}
	return n
}

func textLine(item Item) string {
	return item.Kind + "\t" + quoteField(item.ID) + "\t" + quoteField(decision.DecidedByText(item.DecidedBy)) + "\t" + quoteField(item.Statement) + "\n"
}

func jsonItem(item Item) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(item); err != nil {
		return nil
	}
	raw := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	lines := bytes.Split(raw, []byte("\n"))
	out := make([]byte, 0, len(raw)+2*len(lines))
	for i, line := range lines {
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, ' ', ' ', ' ', ' ')
		out = append(out, line...)
	}
	return out
}

func jsonRender(render func(Result) []byte) bool {
	if render == nil {
		return false
	}
	b := render(Result{})
	return len(b) > 0 && b[0] == '{'
}

func resultOf(items []Item, omitted, omittedLocked int) Result {
	kept := append([]Item(nil), items...)
	if kept == nil {
		kept = []Item{}
	}
	return Result{Items: kept, Omitted: omitted, OmittedLocked: omittedLocked}
}

// FormatText renders r as one line per item. When items were cut it ends
// with "N more: run sdf decision list", or "N more (K locked): run sdf
// decision list" when K locked rules were dropped. An empty brief with
// nothing omitted is the empty string.
func FormatText(r Result) string {
	r = ready(r)
	var b strings.Builder
	for _, item := range r.Items {
		b.WriteString(textLine(item))
	}
	b.WriteString(footerText(r.Omitted, r.OmittedLocked))
	return b.String()
}

// WriteJSON writes r as indented JSON. items is an array and omitted is a
// number, both always present. The encoding does not escape &, <, or >.
func WriteJSON(w io.Writer, r Result) error {
	r = ready(r)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// JSONBytes is WriteJSON into a buffer. It returns a non-nil slice.
func JSONBytes(r Result) []byte {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		return []byte("{}\n")
	}
	return buf.Bytes()
}

func ready(r Result) Result {
	if r.Items == nil {
		r.Items = []Item{}
	}
	return r
}

func quoteField(s string) string {
	if !needsQuote(s) {
		return s
	}
	return strconv.Quote(s)
}

// needsQuote reports whether s must be Go-quoted so the text columns stay
// intact. Format characters such as ZWJ (U+200D) are left as written.
// Quoting is for a leading '"', invalid UTF-8, or a C0/C1 control
// (which includes tab, newline, and ESC).
func needsQuote(s string) bool {
	if strings.HasPrefix(s, `"`) || !utf8.ValidString(s) {
		return true
	}
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

func renderedLen(s string) int {
	if needsQuote(s) {
		return len(strconv.Quote(s))
	}
	return len(s)
}
