package summary

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/record"
)

// toolSpan is an OpenCode Tool.execute span, the shape of the deep chain in
// internal/ui's TestSessionPageRendersDeepSpanTrees.
func toolSpan(id string, parent string, start int) record.Record {
	rec := record.Record{
		Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("ses-deep"),
		Name: record.Str("Tool.execute"), SpanID: record.Str(id),
		StartUnixNano: record.Int64(int64(start) * 1_000_000), EndUnixNano: record.Int64(int64(start+1) * 1_000_000),
		StatusCode: "OK",
		Attributes: map[string]any{"gen_ai.tool.name": "read", "gen_ai.tool.call.id": id},
		Resource:   map[string]any{},
	}
	if parent != "" {
		rec.ParentSpanID = record.Str(parent)
	}
	return rec
}

func chain(depth int) []record.Record {
	var recs []record.Record
	parent := ""
	for i := 0; i < depth; i++ {
		id := "d" + strconv.Itoa(i)
		recs = append(recs, toolSpan(id, parent, i))
		parent = id
	}
	recs[depth-1].StatusCode = "ERROR"
	recs[depth-1].StatusMessage = record.Str("tool error")
	return recs
}

// TestDeepChainSummaryJSONEncodes covers sdf summary --json, which shares
// the tree with /api/summary: at 5500 levels the encoder used to stop with
// "exceeded max depth" (#267).
func TestDeepChainSummaryJSONEncodes(t *testing.T) {
	report := Summarize(chain(5500), false)
	var buf bytes.Buffer
	if err := WriteJSON(&buf, report); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if !json.Valid(buf.Bytes()) {
		t.Fatal("WriteJSON wrote invalid JSON")
	}
	text := Format(report)
	if !strings.Contains(text, "(5467 deeper spans listed flat below; depth limit 32)") {
		t.Fatalf("text summary has no flattened note:\n%.2000s", text)
	}
	if !strings.Contains(text, "call_id=d5499 1ms status=ERROR (tool error)") {
		t.Fatal("text summary lost the deep ERROR leaf")
	}
}

// TestSpanTreeFlattensBelowMaxDepth checks the cap on a branching tree:
// nesting stops at depth MaxSpanDepth and the node there lists every
// descendant once, depth-first in span order.
func TestSpanTreeFlattensBelowMaxDepth(t *testing.T) {
	recs := chain(MaxSpanDepth + 1) // d0..d32; d32 is at the cap
	recs[len(recs)-1].StatusCode = "OK"
	recs[len(recs)-1].StatusMessage = nil
	cap := "d" + strconv.Itoa(MaxSpanDepth)
	// Below the cap: a (b (c), d) then e.
	recs = append(recs,
		toolSpan("e", cap, 500),
		toolSpan("a", cap, 100),
		toolSpan("b", "a", 110),
		toolSpan("c", "b", 120),
		toolSpan("d", "a", 130),
	)
	nodes, hidden := spanTree(recs, false)
	if hidden != 0 || len(nodes) != 1 {
		t.Fatalf("roots %d hidden %d", len(nodes), hidden)
	}
	node := nodes[0]
	for level := 0; level < MaxSpanDepth; level++ {
		if node.FlattenedDescendants != 0 || len(node.Children) != 1 {
			t.Fatalf("level %d: flattened %d children %d", level, node.FlattenedDescendants, len(node.Children))
		}
		node = node.Children[0]
	}
	if deref(node.CallID) != cap || node.FlattenedDescendants != 5 {
		t.Fatalf("cap node %s flattened %d, want %s with 5", deref(node.CallID), node.FlattenedDescendants, cap)
	}
	var order []string
	for _, kid := range node.Children {
		if kid.Children == nil || len(kid.Children) != 0 || kid.FlattenedDescendants != 0 {
			t.Fatalf("flattened %s should be a leaf with children []", deref(kid.CallID))
		}
		order = append(order, deref(kid.CallID))
	}
	if got := strings.Join(order, ","); got != "a,b,c,d,e" {
		t.Fatalf("flattened order %s, want a,b,c,d,e", got)
	}
}

// TestShallowTreeJSONHasNoFlattenedKey keeps the JSON shape of a normal tree.
func TestShallowTreeJSONHasNoFlattenedKey(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, Summarize(chain(MaxSpanDepth), false)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "flattened_descendants") {
		t.Fatal("a tree within the cap should not carry flattened_descendants")
	}
}

// TestSpanTreeRepeatedSpanIDCycleEnds: two stored spans share id "a" (one a
// root, one under "b", whose parent is "a"), which made the old recursive
// build expand a -> b -> a forever.
func TestSpanTreeRepeatedSpanIDCycleEnds(t *testing.T) {
	recs := []record.Record{
		toolSpan("a", "", 0),
		toolSpan("b", "a", 1),
		toolSpan("a", "b", 2),
	}
	done := make(chan []SpanNode, 1)
	go func() {
		nodes, _ := spanTree(recs, false)
		done <- nodes
	}()
	select {
	case nodes := <-done:
		if len(nodes) != 1 {
			t.Fatalf("roots %d, want 1", len(nodes))
		}
		n := nodes[0]
		if deref(n.CallID) != "a" || len(n.Children) != 1 || deref(n.Children[0].CallID) != "b" ||
			len(n.Children[0].Children) != 1 || len(n.Children[0].Children[0].Children) != 0 {
			t.Fatalf("tree %+v, want a > b > a (leaf)", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("spanTree did not finish on a repeated-id cycle")
	}
}
