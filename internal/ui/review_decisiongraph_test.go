package ui_test

import (
	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// GET /api/decisions/graph answers like the other /api routes: HEAD has
// headers and no body, writes are refused with Allow, a foreign Host is
// refused, and the security headers are set.
func TestReviewDecisionGraphRouteHTTP(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	url := srv.URL() + "api/decisions/graph"

	resp, err := http.Head(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(body) != 0 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("HEAD %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, _ := http.NewRequest(method, url, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(resp.Header.Get("Allow"), "GET") {
			t.Fatalf("%s answered %d Allow=%q", method, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Host = "evil.example:80"
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host answered %d", resp.StatusCode)
	}

	status, header, _ := get(t, url)
	if status != 200 {
		t.Fatalf("GET %d", status)
	}
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Cache-Control"} {
		if header.Get(h) == "" {
			t.Fatalf("missing %s", h)
		}
	}
	// Query parameters are not filters; they must not change the document.
	_, _, plain := get(t, url)
	_, _, withQuery := get(t, url+"?chapter=nope&limit=-1")
	if plain != withQuery {
		t.Fatal("query parameters changed the graph document")
	}
	// A trailing slash is not the route.
	if s, _, _ := get(t, url+"/"); s != http.StatusNotFound {
		t.Fatalf("trailing slash answered %d", s)
	}
}

// Decision text is drawn as text, never markup: a statement and an
// alternative carrying HTML reach the page as literal characters.
func TestReviewDecisionGraphPageEscapesText(t *testing.T) {
	db := openDB(t)
	seedGraphWith(t, db, "<img src=x onerror=alert(1)>", "<script>alert(2)</script>; plain")
	srv := startUI(t, db)
	res := renderPage(t, srv, "#/decisions/ch-xss/xss", "")
	if !strings.Contains(res.Text, "<img src=x onerror=alert(1)>") || !strings.Contains(res.Text, "<script>alert(2)</script>") {
		t.Fatalf("text not shown literally:\n%s", res.Text)
	}
	// The runner closes every element with </tag>; attribute values may hold
	// the raw text (setAttribute is safe), so look for real elements only.
	if strings.Contains(res.Markup, "</img>") || regexp.MustCompile(`</script>[^"]`).MatchString(res.Markup) {
		t.Fatalf("decision text became markup:\n%s", res.Markup)
	}
}

func seedGraphWith(t *testing.T, db *store.Store, statement, alternatives string) {
	t.Helper()
	if _, err := decision.Add(db, decision.RecordInput{ID: "xss", Statement: statement, Status: decision.StatusDecision,
		DecidedBy: "user", Alternatives: alternatives}); err != nil {
		t.Fatal(err)
	}
}
