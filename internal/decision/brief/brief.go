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
//  1. Constraints. An active settled decision (status decision) that the
//     user made, or that is the source of a constrains edge. User decisions
//     are locked and come first, newest first. Other constraints follow in
//     insertion order. A locked user decision stays here even when it is
//     also blocked or conflicted. Each statement is cut to statementBudget
//     bytes on a rune boundary, ending in "…", so one long rule cannot
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

	// KindConstraint is an active settled decision the user made, or the
	// source of a constrains edge.
	KindConstraint = "constraint"
	// KindBlocked is a decision Load marked Blocked: active, with a blocks
	// edge whose blocker is still unsettled (open_blockers is not empty).
	KindBlocked = "blocked"
	// KindConflicting is a decision Load marked Conflicted: active, and
	// conflicts_with another active decision.
	KindConflicting = "conflicting"
)

// Item is one decision in the brief. Kind is KindConstraint, KindBlocked,
// or KindConflicting. Locked is true only for a constraint the user made.
// DecidedBy is empty when the record named none.
type Item struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Statement string `json:"statement"`
	Status    string `json:"status"`
	DecidedBy string `json:"decided_by"`
	Locked    bool   `json:"locked"`
}

// Result is the ranked brief after caps. Items is the prefix that was kept
// and is never nil. Omitted is how many ranked items were left off.
type Result struct {
	Items   []Item `json:"items"`
	Omitted int    `json:"omitted"`
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
		return d.DecidedBy == decision.DecidedByUser || constrainsFrom[d.ID]
	}

	var locked, other, blockedItems, conflicting []Item
	for _, d := range order {
		item := Item{
			ID:        d.ID,
			Statement: limitStatement(d.Statement),
			Status:    d.Status,
			DecidedBy: d.DecidedBy,
		}
		switch {
		case isConstraint(d):
			item.Kind = KindConstraint
			item.Locked = d.DecidedBy == decision.DecidedByUser
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

// limitStatement cuts s to statementBudget bytes. The cut is on a rune
// boundary and the result ends with "…". A short statement is unchanged.
func limitStatement(s string) string {
	if len(s) <= statementBudget {
		return s
	}
	const ellipsis = "…"
	budget := statementBudget - len(ellipsis)
	if budget < 0 {
		return ellipsis
	}
	i := budget
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + ellipsis
}

// Select classifies g and applies the caps. maxItems or maxBytes below zero
// means that cap is off. The byte cap is the length of render's result.
// render may be nil when the byte cap is off.
func Select(g decision.Graph, maxItems, maxBytes int, render func(Result) []byte) Result {
	return Trim(Classify(g), maxItems, maxBytes, render)
}

// Trim keeps items in the order given, which is Classify's priority order.
// maxItems or maxBytes below zero means that cap is off. It fills greedily:
// an item that does not fit is skipped and counted in Omitted, and a later
// smaller item can still be kept. The byte check is one pass. It adds each
// item's rendered size to a fixed envelope and footer reserve, and does not
// render the whole document again for every skip. When items are skipped,
// the omitted line (or the JSON omitted field) is part of that reserve. If
// the note alone is still over maxBytes, it is kept: the count has to stay
// visible. render is only used to tell text from JSON. Nil means no byte cap.
func Trim(items []Item, maxItems, maxBytes int, render func(Result) []byte) Result {
	if maxItems < 0 || maxItems > len(items) {
		maxItems = len(items)
	}
	jsonOut := maxBytes >= 0 && jsonRender(render)
	reserve := 0
	if maxBytes >= 0 {
		reserve = envelopeReserve(len(items), jsonOut)
	}
	kept := make([]Item, 0, maxItems)
	used := reserve
	omitted := 0
	for _, item := range items {
		if len(kept) >= maxItems {
			omitted++
			continue
		}
		cost := itemBytes(item, len(kept) > 0, jsonOut)
		if maxBytes >= 0 && used+cost > maxBytes {
			omitted++
			continue
		}
		kept = append(kept, item)
		used += cost
	}
	return resultOf(kept, omitted)
}

// envelopeReserve is the fixed bytes that are not an item: the JSON
// document around the array, or the text footer at its widest for n items.
func envelopeReserve(n int, jsonOut bool) int {
	digits := len(strconv.Itoa(n))
	if !jsonOut {
		return digits + len(" more: run sdf decision list\n")
	}
	// {\n  "items": [\n ... \n  ],\n  "omitted": N\n}\n
	return len("{\n  \"items\": [\n") + len("\n  ],\n  \"omitted\": ") + digits + len("\n}\n")
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
	by := item.DecidedBy
	if by == "" {
		by = "-"
	}
	return item.Kind + "\t" + quoteField(item.ID) + "\t" + quoteField(by) + "\t" + quoteField(item.Statement) + "\n"
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

func resultOf(items []Item, omitted int) Result {
	kept := append([]Item(nil), items...)
	if kept == nil {
		kept = []Item{}
	}
	return Result{Items: kept, Omitted: omitted}
}

// FormatText renders r as one line per item, then, when items were cut,
// "N more: run sdf decision list". An empty brief with nothing omitted is
// the empty string. A field that could break the line is Go-quoted.
func FormatText(r Result) string {
	r = ready(r)
	var b strings.Builder
	for _, item := range r.Items {
		b.WriteString(textLine(item))
	}
	if r.Omitted > 0 {
		b.WriteString(strconv.Itoa(r.Omitted))
		b.WriteString(" more: run sdf decision list\n")
	}
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
	if !strings.HasPrefix(s, `"`) && utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool {
		return r == '\t' || !unicode.IsPrint(r)
	}) < 0 {
		return s
	}
	return strconv.Quote(s)
}
