package ui_test

import (
	"encoding/json"
	"github.com/paulpham157/sdf/internal/ui"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// A malformed percent escape in the hash (decodeURIComponent throws on it)
// must not leave the page undrawn: a bad chapter or node falls back to the
// newest chapter's view, and a bad session id reads as an unknown session.
func TestPageSurvivesMalformedHash(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	for _, hash := range []string{"#/decisions/ch-capture/%E0%A4%A", "#/decisions/%zz"} {
		page := renderedPageAt(t, srv, hash)
		if !strings.Contains(page, "Chapters") || !strings.Contains(page, "feature-x") || !strings.Contains(page, "Explored") {
			t.Errorf("%s did not fall back to the chapter view:\n%s", hash, page)
		}
	}
	// A bad chapter with a good node focuses that node in the newest chapter.
	if page := renderedPageAt(t, srv, "#/decisions/%/capture"); !strings.Contains(page, "Capture live spans.") || !strings.Contains(page, "← Chapter") {
		t.Errorf("bad chapter with a good node:\n%s", page)
	}
	for _, hash := range []string{"#/sessions/%E0%A4%A", "#/sessions/%"} {
		page := renderedPageAt(t, srv, hash)
		if !strings.Contains(page, "No session with that id.") {
			t.Errorf("%s did not draw the unknown-session page:\n%s", hash, page)
		}
	}
	// A well-formed escape still decodes.
	if page := renderedPageAt(t, srv, "#/sessions/ses%2Da"); strings.Contains(page, "No session with that id.") {
		t.Errorf("escaped session id did not decode:\n%s", page)
	}
}

// Every node, whatever its state, and every ghost carries the pre-drawn
// keyboard ring, and only the :focus-visible rule strokes it.
func TestDecisionNodesCarryKeyboardRing(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	for _, hash := range []string{"#/decisions", "#/decisions/ch-capture/status-ui"} {
		res := renderPage(t, srv, hash, "")
		nodes := regexp.MustCompile(`<g [^>]*class="dg-node[ "]`).FindAllString(res.Markup, -1)
		rings := strings.Count(res.Markup, `class="dg-kb-ring"`)
		if len(nodes) == 0 || len(nodes) != rings {
			t.Errorf("%s: %d nodes, %d keyboard rings", hash, len(nodes), rings)
		}
		for _, state := range []string{"dg-on-path", "dg-role-locked_ending", "dg-role-hypothesis", "dg-kind-ending", "dg-kind-start"} {
			if !strings.Contains(res.Markup, state) {
				t.Errorf("%s: no node in state %s to check", hash, state)
			}
		}
		if hash != "#/decisions" && !strings.Contains(res.Markup, "Not taken: Warnings only") {
			t.Errorf("%s: no ghost drawn", hash)
		}
	}
	css := readAsset(t, "app.css")
	if !strings.Contains(css, ".dg-node:focus-visible .dg-kb-ring { stroke: var(--green); stroke-width: 2.5; }") {
		t.Error("app.css has no keyboard ring rule")
	}
	for _, m := range regexp.MustCompile(`(?m)^[^{}\n]*dg-kb-ring[^{}\n]*\{`).FindAllString(css, -1) {
		if m != ".dg-kb-ring {" && m != ".dg-node:focus-visible .dg-kb-ring {" {
			t.Errorf("another rule styles the keyboard ring: %s", m)
		}
	}
}

// One live region for the page, outside the drawn tree, kept across draws,
// cleared on navigation; focus survives each draw and returns to the node
// when its panel closes.
func TestDecisionsLiveRegionAndFocusKeep(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	live := func(res pageResult) string {
		if n := strings.Count(res.Outside, "aria-live"); n != 1 {
			t.Errorf("%d live regions outside the app root: %s", n, res.Outside)
		}
		if strings.Contains(res.Markup, "aria-live") || strings.Contains(res.Markup, `role="status"`) {
			t.Errorf("a live region inside the redrawn tree:\n%s", res.Markup)
		}
		return res.Outside
	}

	copied := renderPage(t, srv, "#/decisions/ch-capture/status-ui", `focus=~Copy command,click=~Copy command`)
	if out := live(copied); !strings.Contains(out, "Copy failed") {
		t.Errorf("copy status not announced: %s", out)
	}
	if !strings.Contains(copied.Active, `data-focus-key="copy:`) {
		t.Errorf("focus left the Copy button: %q", copied.Active)
	}
	moved := renderPage(t, srv, "#/decisions/ch-capture/status-ui", `focus=~Copy command,click=~Copy command,hash=#/decisions/ch-capture`)
	if out := live(moved); strings.Contains(out, "Copy failed") {
		t.Errorf("navigation kept the old status: %s", out)
	}

	for _, flags := range []string{"key=Escape", `click=[aria-label="Close"]`, "click=~← Chapter"} {
		res := renderPage(t, srv, "#/decisions/ch-capture/status-ui", flags)
		if res.Hash != "#/decisions/ch-capture" || !strings.Contains(res.Active, `data-node="status-ui"`) {
			t.Errorf("%s: hash %q, focus %q; want the chapter with focus on status-ui", flags, res.Hash, res.Active)
		}
	}

	toggled := renderPage(t, srv, "#/decisions", "focus=~Show tensions,click=~Show tensions")
	if !strings.Contains(toggled.Active, `data-focus-key="tb:Show tensions"`) || !strings.Contains(toggled.Markup, `aria-pressed="true" data-focus-key="tb:Show tensions"`) {
		t.Errorf("toggle lost focus: %q", toggled.Active)
	}

	missed := renderPage(t, srv, "#/decisions", "focus=.dg-jump,input=zzz,key=Enter")
	if !strings.Contains(missed.Active, `class="dg-jump"`) || !strings.Contains(missed.Active, `value="zzz"`) {
		t.Errorf("Jump lost focus or its value: %q", missed.Active)
	}
	if out := live(missed); !strings.Contains(out, "No node matches zzz.") || !strings.Contains(missed.Text, "No node matches zzz.") {
		t.Errorf("no-match status: %s", out)
	}
}

// The You/Agents filter recolours the other decider's nodes with the dim
// palette, never opacity, and takes them out of the tab order.
func TestDecisionFilterMutesWithoutOpacity(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	res := renderPage(t, srv, "#/decisions", "click=~You")
	tag := func(id string) string {
		return regexp.MustCompile(`<g [^>]*data-node="` + id + `"[^>]*>`).FindString(res.Markup)
	}
	for _, id := range []string{"merge-first", "silent"} {
		if g := tag(id); !strings.Contains(g, "dg-muted") || !strings.Contains(g, `tabindex="-1"`) || !strings.Contains(g, "filtered out") {
			t.Errorf("agent node %s under You: %q", id, g)
		}
	}
	for _, id := range []string{"capture", "status-ui"} {
		if g := tag(id); strings.Contains(g, "dg-muted") || !strings.Contains(g, `tabindex="0"`) {
			t.Errorf("user node %s under You: %q", id, g)
		}
	}
	css := readAsset(t, "app.css")
	if regexp.MustCompile(`dg-muted[^{]*\{[^}]*opacity`).MatchString(css) {
		t.Error("muted nodes still fade with opacity")
	}
	if !strings.Contains(css, ".dg-node.dg-muted .dg-title, .dg-node.dg-muted .dg-detail { fill: #3a352e; }") {
		t.Error("muted node text is not recoloured with the dim palette")
	}
}

// Everyone / You / Agents is a radiogroup with a roving tabindex: one tab
// stop, and the arrows, Home and End move and select.
func TestDecisionShowFilterIsRadiogroup(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	radios := regexp.MustCompile(`<button [^>]*role="radio"[^>]*>`)
	check := func(flags, want string) {
		t.Helper()
		res := renderPage(t, srv, "#/decisions", flags)
		if !strings.Contains(res.Markup, `role="radiogroup" aria-label="Decided by"`) {
			t.Fatalf("no radiogroup:\n%s", res.Markup)
		}
		all := radios.FindAllString(res.Markup, -1)
		if len(all) != 3 || strings.Count(strings.Join(all, ""), `tabindex="0"`) != 1 || strings.Count(strings.Join(all, ""), `aria-checked="true"`) != 1 {
			t.Fatalf("%s: radios %q", flags, all)
		}
		for _, r := range all {
			on := strings.Contains(r, `data-focus-key="show:`+want+`"`)
			if on != strings.Contains(r, `aria-checked="true"`) || on != strings.Contains(r, `tabindex="0"`) {
				t.Errorf("%s: want %s checked: %q", flags, want, r)
			}
		}
		if flags != "" && !strings.Contains(res.Active, `data-focus-key="show:`+want+`"`) {
			t.Errorf("%s: focus %q, want show:%s", flags, res.Active, want)
		}
	}
	check("", "all")
	check(`key=ArrowRight@[data-focus-key="show:all"]`, "user")
	check(`key=ArrowLeft@[data-focus-key="show:all"]`, "agent")
	check(`key=End@[data-focus-key="show:all"]`, "agent")
	check(`key=ArrowRight@[data-focus-key="show:all"],key=ArrowDown,key=ArrowDown`, "all")
	check(`click=~Agents,key=Home`, "all")
}

// / focuses Jump, t toggles tensions and ? the shortcut sheet, from
// anywhere on the tab but a text field, and never with Ctrl, Meta or Alt.
func TestDecisionShortcutKeys(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	node := `@[data-node="capture"]`
	tensions := func(res pageResult) bool {
		return strings.Contains(res.Markup, `aria-pressed="true" data-focus-key="tb:Show tensions"`)
	}
	help := func(res pageResult) bool { return strings.Contains(res.Text, "Shortcuts") }

	if res := renderPage(t, srv, "#/decisions", "key=/"+node); !strings.Contains(res.Active, `class="dg-jump"`) {
		t.Errorf("/ did not focus Jump: %q", res.Active)
	}
	if res := renderPage(t, srv, "#/decisions", `key=/@[data-focus-key="tb:Not taken"]`); !strings.Contains(res.Active, `class="dg-jump"`) {
		t.Errorf("/ from the toolbar did not focus Jump: %q", res.Active)
	}
	for _, mod := range []string{"ctrl+", "meta+", "alt+"} {
		if res := renderPage(t, srv, "#/decisions", "key="+mod+"/"+node); strings.Contains(res.Active, `class="dg-jump"`) {
			t.Errorf("%s/ focused Jump", mod)
		}
		if res := renderPage(t, srv, "#/decisions", "key="+mod+"t"+node); tensions(res) {
			t.Errorf("%st toggled tensions", mod)
		}
		if res := renderPage(t, srv, "#/decisions", "key="+mod+"shift+?"+node); help(res) {
			t.Errorf("%s? opened the help", mod)
		}
	}
	if res := renderPage(t, srv, "#/decisions", "key=t"+node); !tensions(res) {
		t.Error("t did not toggle tensions")
	}
	if res := renderPage(t, srv, "#/decisions", "key=shift+?"+node); !help(res) {
		t.Error("? did not open the help")
	}
	// Typed into Jump, the keys are text.
	if res := renderPage(t, srv, "#/decisions", "focus=.dg-jump,key=t,key=shift+?"); tensions(res) || help(res) {
		t.Error("t or ? in the Jump field acted as a shortcut")
	}
}

// graphPage draws the page against srv with the graph document passed
// through edit first, so a test can hold data the store would not produce.
func graphPage(t *testing.T, srv *ui.Server, edit func(doc map[string]any), hash, flags string) pageResult {
	t.Helper()
	bodies := map[string]string{}
	for _, path := range []string{"", "app.js", "api/snapshot", "api/decisions", "api/summary", "api/decisions/graph"} {
		status, _, body := get(t, srv.URL()+path)
		if status != 200 {
			t.Fatalf("%s: %d", path, status)
		}
		bodies["/"+path] = body
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(bodies["/api/decisions/graph"]), &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc)
	graph, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	bodies["/api/decisions/graph"] = string(graph)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(fake.Close)
	return runPage(t, fake.URL+"/", bodies["/"], bodies["/app.js"], hash, flags)
}

// eachEnding calls fn on every ending node of every chapter.
func eachEnding(doc map[string]any, fn func(node map[string]any)) {
	chapters, _ := doc["chapters"].([]any)
	for _, c := range chapters {
		nodes, _ := c.(map[string]any)["nodes"].([]any)
		for _, n := range nodes {
			node := n.(map[string]any)
			if node["kind"] == "ending" {
				fn(node)
			}
		}
	}
}

// An ending's pull request link is an href only for a GitHub https URL,
// and it opens with rel="noopener noreferrer".
func TestEndingLinksAllowOnlyGitHub(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	github := "https" + "://github.com/o/r/pull/7"
	var id string
	graphPage(t, srv, func(doc map[string]any) {
		eachEnding(doc, func(node map[string]any) {
			if id == "" && node["ending"] != nil {
				id = node["id"].(string)
			}
		})
	}, "", "")
	if id == "" {
		t.Fatal("seed has no ending")
	}
	for url, allowed := range map[string]bool{
		github:                              true,
		"javascript:alert(1)":               false,
		"JavaScript:alert(1)":               false,
		"data:text/html,<b>x</b>":           false,
		"http" + "://github.com/o/r/pull/7": false,
		"https" + "://github.com.evil.example/o/r/pull/7": false,
		"https" + "://evil.example/?https://github.com/":  false,
		"//github.com/o/r/pull/7":                         false,
		" " + github:                                      false,
	} {
		res := graphPage(t, srv, func(doc map[string]any) {
			eachEnding(doc, func(node map[string]any) {
				if node["id"] == id {
					end := node["ending"].(map[string]any)
					end["url"], end["pull_request"] = url, 7
				}
			})
		}, "#/decisions/ch-capture/"+id, "")
		link := regexp.MustCompile(`<a [^>]*>Open pull request #7</a>`).FindString(res.Markup)
		if allowed {
			if !strings.Contains(link, `href="`+github+`"`) || !strings.Contains(link, `rel="noopener noreferrer"`) {
				t.Errorf("%q: link %q", url, link)
			}
		} else if link != "" || !strings.Contains(res.Text, "Pull request #7") {
			t.Errorf("%q became a link: %q", url, link)
		}
	}
}
