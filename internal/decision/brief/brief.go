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
//     are locked and are ranked first. Other constraints follow. Insertion
//     order is kept inside each of those two tiers. A locked user decision
//     stays here even when it is also blocked or conflicted.
//  2. Blocked. Record.Blocked, which Load sets for an active decision that
//     still has an open blocks blocker, and that is not itself a constraint.
//  3. Conflicting. Record.Conflicted, which Load sets for an active decision
//     with a conflicts_with edge to another active decision, and that is in
//     neither group above.
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
	order := decision.Active(g)
	active := make(map[string]bool, len(order))
	for _, d := range order {
		active[d.ID] = true
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
			Statement: d.Statement,
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
	out := make([]Item, 0, len(locked)+len(other)+len(blockedItems)+len(conflicting))
	out = append(out, locked...)
	out = append(out, other...)
	out = append(out, blockedItems...)
	out = append(out, conflicting...)
	return out
}

// Select classifies g and applies the caps. maxItems or maxBytes below zero
// means that cap is off. The byte cap is the length of render's result.
// render may be nil when the byte cap is off.
func Select(g decision.Graph, maxItems, maxBytes int, render func(Result) []byte) Result {
	return Trim(Classify(g), maxItems, maxBytes, render)
}

// Trim keeps a prefix of the already-ranked items. maxItems or maxBytes
// below zero means that cap is off. Items are dropped from the end until
// the rendered form fits, and each dropped item counts in Omitted. When
// items are dropped, the omitted line (or the JSON omitted field) is part
// of the rendered form. If that note alone is still over maxBytes, it is
// kept: the count has to stay visible.
func Trim(items []Item, maxItems, maxBytes int, render func(Result) []byte) Result {
	n := len(items)
	if maxItems >= 0 && n > maxItems {
		n = maxItems
	}
	for {
		r := resultOf(items[:n], len(items)-n)
		if maxBytes < 0 || render == nil || len(render(r)) <= maxBytes || n == 0 {
			return r
		}
		n--
	}
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
		by := item.DecidedBy
		if by == "" {
			by = "-"
		}
		b.WriteString(item.Kind)
		b.WriteByte('\t')
		b.WriteString(quoteField(item.ID))
		b.WriteByte('\t')
		b.WriteString(quoteField(by))
		b.WriteByte('\t')
		b.WriteString(quoteField(item.Statement))
		b.WriteByte('\n')
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
	if utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool {
		return r == '\t' || !unicode.IsPrint(r)
	}) < 0 {
		return s
	}
	return strconv.QuoteToASCII(s)
}
