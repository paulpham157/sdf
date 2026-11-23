package ui_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
	"github.com/paulpham157/sdf/internal/ui"
)

// deepChain stores one OpenCode session whose Tool.execute spans form a
// single parent->child chain, the shape TestSessionPageRendersDeepSpanTrees
// uses. The leaf is an ERROR span.
func deepChain(t *testing.T, db *store.Store, session string, depth int) {
	t.Helper()
	var recs []record.Record
	var parent *string
	for i := 0; i < depth; i++ {
		id := "d" + strconv.Itoa(i)
		rec := record.Record{
			Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str(session),
			Name: record.Str("Tool.execute"), SpanID: record.Str(id), ParentSpanID: parent,
			StartUnixNano: record.Int64(int64(i) * 1_000_000), EndUnixNano: record.Int64(int64(i+1) * 1_000_000),
			StatusCode: "OK",
			Attributes: map[string]any{"gen_ai.tool.name": "read", "gen_ai.tool.call.id": id},
			Resource:   map[string]any{},
		}
		if i == depth-1 {
			rec.StatusCode = "ERROR"
			rec.StatusMessage = record.Str("deep chain failed")
		}
		recs = append(recs, rec)
		idCopy := id
		parent = &idCopy
	}
	if _, err := db.Insert(recs); err != nil {
		t.Fatal(err)
	}
}

// jsonDepth returns the deepest object/array nesting in body.
func jsonDepth(t *testing.T, body string) int {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(body)))
	depth, deepest := 0, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return deepest
		}
		if err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
			if depth > deepest {
				deepest = depth
			}
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
}

type deepSpan struct {
	Name          string     `json:"name"`
	CallID        *string    `json:"call_id"`
	StatusCode    string     `json:"status_code"`
	StatusMessage *string    `json:"status_message"`
	Children      []deepSpan `json:"children"`
	// FlattenedDescendants is the #267 marker on the node at the depth cap.
	FlattenedDescendants int `json:"flattened_descendants"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// walkSpans visits every node without recursion.
func walkSpans(roots []deepSpan, visit func(deepSpan)) {
	stack := append([]deepSpan{}, roots...)
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		visit(node)
		stack = append(stack, node.Children...)
	}
}

// TestDeepSpanTreeAPIsNeverReturnEmpty200 is the regression test for #267:
// above about 4997 span levels both endpoints answered 200 with 0 bytes,
// because Go's encoder stops at 10000 nesting levels and the handler dropped
// the Encode error.
func TestDeepSpanTreeAPIsNeverReturnEmpty200(t *testing.T) {
	const depth = 5500
	db := openDB(t)
	deepChain(t, db, "ses-deep-api", depth)
	srv := startUI(t, db)
	for _, path := range []string{"api/summary", "api/snapshot"} {
		t.Run(path, func(t *testing.T) {
			status, _, body := get(t, srv.URL()+path)
			t.Logf("GET /%s: %d, %d bytes", path, status, len(body))
			if status == 200 && len(body) == 0 {
				t.Fatalf("GET /%s: 200 with an empty body at %d span levels", path, depth)
			}
			if status != 200 {
				if len(body) == 0 {
					t.Fatalf("GET /%s: %d with an empty body", path, status)
				}
				t.Fatalf("GET /%s: %d %s (want bounded JSON)", path, status, body)
			}
			// Linear in span count: about 3 KB per span with the cap,
			// against about 853 MB that never arrived before the fix.
			if len(body) > 24<<20 {
				t.Fatalf("GET /%s: body is %d bytes, want a bounded response", path, len(body))
			}
			if d := jsonDepth(t, body); d > 2*summary.MaxSpanDepth+16 {
				t.Fatalf("GET /%s: JSON nests %d levels, want bounded", path, d)
			}
			var doc struct {
				Sessions []struct {
					SessionID string     `json:"session_id"`
					Spans     []deepSpan `json:"spans"`
					Summary   *struct {
						SessionID string     `json:"session_id"`
						Spans     []deepSpan `json:"spans"`
					} `json:"summary"`
				} `json:"sessions"`
			}
			if err := json.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Sessions) != 1 {
				t.Fatalf("GET /%s: %d sessions, want 1", path, len(doc.Sessions))
			}
			id, roots := doc.Sessions[0].SessionID, doc.Sessions[0].Spans
			if s := doc.Sessions[0].Summary; s != nil {
				id, roots = s.SessionID, s.Spans
			}
			if id != "ses-deep-api" {
				t.Fatalf("GET /%s: session %q", path, id)
			}
			// The chain stays nested down to the cap; the node there lists
			// the rest flat.
			node := roots
			for level := 0; level < summary.MaxSpanDepth; level++ {
				if len(node) != 1 || node[0].FlattenedDescendants != 0 {
					t.Fatalf("GET /%s: level %d = %+v, want one nested span", path, level, node)
				}
				node = node[0].Children
			}
			if len(node) != 1 || node[0].FlattenedDescendants != depth-summary.MaxSpanDepth-1 || len(node[0].Children) != depth-summary.MaxSpanDepth-1 {
				t.Fatalf("GET /%s: cap node has %d flattened, %d children; want %d", path, node[0].FlattenedDescendants, len(node[0].Children), depth-summary.MaxSpanDepth-1)
			}
			if last := node[0].Children[len(node[0].Children)-1]; deref(last.CallID) != "d"+strconv.Itoa(depth-1) || last.StatusCode != "ERROR" {
				t.Fatalf("GET /%s: last flattened span = %+v, want the ERROR leaf", path, last)
			}
			count, errs := 0, 0
			ids := map[string]bool{}
			walkSpans(roots, func(n deepSpan) {
				count++
				if n.CallID != nil {
					ids[*n.CallID] = true
				}
				if n.StatusCode == "ERROR" {
					errs++
				}
			})
			if count != depth || len(ids) != depth {
				t.Fatalf("GET /%s: %d spans (%d distinct), want all %d kept", path, count, len(ids), depth)
			}
			if errs != 1 {
				t.Fatalf("GET /%s: %d ERROR spans, want the deep leaf", path, errs)
			}
		})
	}
}

// TestEncodeFailureIs500WithABody: an encoder error is logged and answered
// with 500 and a JSON error, never 200 with an empty body (#267). The value
// is a chain nested past encoding/json's 10000-level limit, the same error
// a deep span tree hit before the depth cap.
func TestEncodeFailureIs500WithABody(t *testing.T) {
	var deep any = []any{}
	for i := 0; i < 10001; i++ {
		deep = []any{deep}
	}
	var log bytes.Buffer
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	ui.WriteJSONForTest(&log, rec, req, func(w io.Writer) error { return summary.EncodeJSON(w, deep) })
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	var doc struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || !strings.Contains(doc.Error, "exceeded max depth") {
		t.Fatalf("body %q (%v), want a JSON error naming the encoder failure", rec.Body.String(), err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q", ct)
	}
	if !strings.Contains(log.String(), "GET /api/summary: encode response:") {
		t.Fatalf("log %q, want the encode error", log.String())
	}
}

// TestSessionPageDrawsAChainPastTheEncoderLimit: before #267 a 5500-level
// chain reached the page as an empty body. With the server cap the page
// draws the first 32 levels, notes the rest, and the banner still names the
// ERROR leaf from the flattened list.
func TestSessionPageDrawsAChainPastTheEncoderLimit(t *testing.T) {
	db := openDB(t)
	deepChain(t, db, "ses-deep-page", 5500)
	srv := startUI(t, db)
	page := renderPage(t, srv, "#/sessions/ses-deep-page", "")
	if strings.Contains(page.Text, "The page could not be drawn") {
		t.Fatalf("page failed:\n%.2000s", page.Text)
	}
	if !strings.Contains(page.Text, "Deeper spans not shown (depth limit 32).") || !strings.Contains(page.Text, "call_id=d0") {
		t.Fatalf("expected the root and the depth-limit note:\n%.2000s", page.Text)
	}
	if !strings.Contains(page.Markup, `<p class="span-error-message">Tool.execute: tool error</p>`) {
		t.Fatalf("banner lost the deep ERROR leaf:\n%.2000s", page.Text)
	}
	if strings.Contains(page.Text, "call_id=d34 ") || strings.Contains(page.Text, "call_id=d5499") {
		t.Fatal("spans below the UI depth limit were drawn")
	}
}
