package ui_test

import (
	"encoding/json"
	"github.com/paulpham157/sdf/internal/ui"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
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
	if !strings.Contains(css, ".dg-node:focus-visible .dg-kb-ring { stroke: var(--sdf-color-focus); stroke-width: 2.5; }") {
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

	// The region is there, empty, before anything is announced.
	if out := live(renderPage(t, srv, "#/decisions", "")); !strings.Contains(out, `role="status"`) {
		t.Errorf("no live region before the first announcement: %q", out)
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
	res := renderPage(t, srv, "#/decisions", "click=~user (legacy)")
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
	if !strings.Contains(css, ".dg-node.dg-muted .dg-title, .dg-node.dg-muted .dg-detail { fill: var(--sdf-graph-muted-text); }") {
		t.Error("muted node text is not recoloured with the dim palette")
	}
}

// Everyone / user (legacy) / agent (legacy) / Logins is a radiogroup with a
// roving tabindex: one tab stop, and the arrows, Home and End move and select.
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
		if len(all) != 4 || strings.Count(strings.Join(all, ""), `tabindex="0"`) != 1 || strings.Count(strings.Join(all, ""), `aria-checked="true"`) != 1 {
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
	check(`key=ArrowLeft@[data-focus-key="show:all"]`, "login")
	check(`key=End@[data-focus-key="show:all"]`, "login")
	check(`key=ArrowRight@[data-focus-key="show:all"],key=ArrowDown,key=ArrowDown,key=ArrowDown`, "all")
	check(`click=~Logins,key=Home`, "all")
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

// Ghost alternatives work from the keyboard: Enter or Space focuses one,
// its parent's ghosts stay drawn while it has focus, and Esc returns focus
// to the parent.
func TestGhostAlternativesKeyboard(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	base := renderPage(t, srv, "#/decisions/ch-capture/status-ui", "")
	ghosts := regexp.MustCompile(`<g [^>]*class="dg-node dg-kind-alternative[^"]*dg-ghost-node"[^>]*>`).FindAllString(base.Markup, -1)
	if len(ghosts) != 2 {
		t.Fatalf("ghosts %q", ghosts)
	}
	id := regexp.MustCompile(`data-node="([^"]+)"`).FindStringSubmatch(ghosts[0])
	if id == nil || !strings.Contains(ghosts[0], `tabindex="0"`) || !strings.Contains(ghosts[0], `role="button"`) {
		t.Fatalf("ghost %q", ghosts[0])
	}
	sel := `@[data-node="` + id[1] + `"]`
	for _, key := range []string{"Enter", " "} {
		res := renderPage(t, srv, "#/decisions/ch-capture/status-ui", "key="+key+sel)
		if res.Hash != "#/decisions/ch-capture/"+strings.ReplaceAll(id[1], "#", "%23") {
			t.Errorf("%q on a ghost: hash %q", key, res.Hash)
		}
		if !strings.Contains(res.Active, `data-node="`+id[1]+`"`) {
			t.Errorf("%q on a ghost: focus %q", key, res.Active)
		}
		if !strings.Contains(res.Markup, "Not taken: Warnings only") || !strings.Contains(res.Markup, "Not taken: A check command") {
			t.Errorf("%q on a ghost: the parent's ghosts vanished", key)
		}
		if !strings.Contains(res.Text, "Copy command") {
			t.Errorf("%q on a ghost: no what-if panel", key)
		}
	}
	back := renderPage(t, srv, "#/decisions/ch-capture/status-ui", "key=Enter"+sel+",key=Escape")
	if back.Hash != "#/decisions/ch-capture" || !strings.Contains(back.Active, `data-node="status-ui"`) {
		t.Errorf("Esc from a ghost: hash %q focus %q", back.Hash, back.Active)
	}
}

// A long title is cut to the room the semibold title font has (about
// 7.1px a character), not the detail font's 6.1px, so it stays inside the
// node; the full text stays in the node's <title>.
func TestLongNodeTitleFitsItsNode(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	long := "push-when-green-and-the-reviewer-has-approved-every-check"
	res := graphPage(t, srv, func(doc map[string]any) {
		for _, c := range doc["chapters"].([]any) {
			for _, n := range c.(map[string]any)["nodes"].([]any) {
				if node := n.(map[string]any); node["id"] == "capture" {
					node["title"] = long
				}
			}
		}
	}, "#/decisions", "")
	m := regexp.MustCompile(`<text [^>]*class="dg-title">(push-when[^<]*)</text>`).FindStringSubmatch(res.Markup)
	if m == nil {
		t.Fatalf("no long title drawn:\n%s", res.Markup)
	}
	// The spine cap is 236px: 236 - 34 (badge) - 10 (pad) = 192px of room.
	if n := len([]rune(m[1])); n > 192/7 || !strings.HasSuffix(m[1], "…") {
		t.Errorf("title %q (%d chars) overflows 192px at 7.1px a character", m[1], n)
	}
	if !strings.Contains(res.Markup, "<title>"+long+": ") {
		t.Error("the full title is not in the node's <title>")
	}
}

// A same-row edge shorter than 40px drops its mid-edge glyph (the node's
// own lock icon and the amber dash already say it); a longer one keeps it.
func TestShortSameRowEdgeDropsGlyph(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	// merge-first (right lane) blocks status-ui (spine) on the same row.
	if res := renderPage(t, srv, "#/decisions", ""); strings.Count(res.Markup, `class="dg-glyph-lock"`) != 1 {
		t.Errorf("the 60px blocks edge lost its glyph:\n%s", res.Markup)
	}
	wide := graphPage(t, srv, func(doc map[string]any) {
		for _, c := range doc["chapters"].([]any) {
			for _, n := range c.(map[string]any)["nodes"].([]any) {
				if node := n.(map[string]any); node["id"] == "merge-first" {
					node["detail"] = "Merge main first, then rebase every open branch."
				}
			}
		}
	}, "#/decisions", "")
	if n := strings.Count(wide.Markup, `class="dg-glyph-lock"`); n != 0 {
		t.Errorf("a blocks edge under 40px still draws %d glyphs", n)
	}
}

// The List toggle draws the chapter as an ordered list of the chosen path
// with each node's alternatives and link sentences, then the rest.
func TestDecisionListView(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	res := renderPage(t, srv, "#/decisions", "focus=~List,click=~List")
	if !strings.Contains(res.Markup, `aria-pressed="true" data-focus-key="tb:List"`) || !strings.Contains(res.Active, `data-focus-key="tb:List"`) {
		t.Fatalf("List toggle not on or lost focus: %q", res.Active)
	}
	if strings.Contains(res.Markup, `class="dg-svg`) {
		t.Error("List view still draws the graph")
	}
	ol := regexp.MustCompile(`<ol [^>]*class="dg-list"[^>]*>.*?</ol>`).FindString(res.Markup)
	if ol == "" {
		t.Fatalf("no ordered list:\n%s", res.Markup)
	}
	// Path order: start, capture, status-ui, silent; each a button.
	var order []string
	for _, m := range regexp.MustCompile(`<button [^>]*data-node="([^"]+)"[^>]*class="dg-linkish dg-list-node"`).FindAllStringSubmatch(ol, -1) {
		order = append(order, m[1])
	}
	want := []string{"start:ch-capture", "capture", "status-ui", "silent"}
	if got := strings.Join(order, " "); got != strings.Join(want, " ") {
		t.Errorf("path order %q, want %q first", got, want)
	}
	text := res.Text
	for _, s := range []string{"Chosen path, in order", "Not taken Parse the log", "Blocked by merge-first", "Conflicts with silent", "Constrained by capture", "Endings", "Locked ending", "ending, reached", "Off the path", "merge-first", "by user (legacy)", "blocked", "category implementation"} {
		if !strings.Contains(text, s) {
			t.Errorf("List view is missing %q:\n%s", s, text)
		}
	}
	// Choosing a node from the list focuses it, and focus stays in the list.
	focused := renderPage(t, srv, "#/decisions", `click=~List,click=[data-node="capture"]`)
	if focused.Hash != "#/decisions/ch-capture/capture" || !strings.Contains(focused.Active, `data-node="capture"`) || !strings.Contains(focused.Active, "dg-list-node") {
		t.Errorf("list choice: hash %q focus %q", focused.Hash, focused.Active)
	}
}

// Logins mutes legacy words and an empty decided_by, and keeps a login.
// A login tick is dg-tick-login. An empty decided_by is not painted as user.
func TestLoginsFilterMutesLegacyAndTicksLogins(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	setBy := func(node map[string]any, by string) {
		rec, _ := node["decision"].(map[string]any)
		if by == "" {
			delete(node, "decided_by")
			if rec != nil {
				delete(rec, "decided_by")
				delete(rec, "decided_by_legacy")
			}
			return
		}
		node["decided_by"] = by
		if rec != nil {
			rec["decided_by"] = by
			if by == "user" || by == "agent" {
				rec["decided_by_legacy"] = true
			} else {
				delete(rec, "decided_by_legacy")
			}
		}
	}
	res := graphPage(t, srv, func(doc map[string]any) {
		for _, c := range doc["chapters"].([]any) {
			for _, n := range c.(map[string]any)["nodes"].([]any) {
				node := n.(map[string]any)
				switch node["id"] {
				case "status-ui":
					setBy(node, "octocat")
				case "silent":
					setBy(node, "")
				}
			}
		}
	}, "#/decisions", "click=~Logins")
	tag := func(id string) string {
		t.Helper()
		got := regexp.MustCompile(`<g [^>]*data-node="` + regexp.QuoteMeta(id) + `"[^>]*>`).FindString(res.Markup)
		if got == "" {
			t.Fatalf("no node %s:\n%s", id, res.Markup)
		}
		return got
	}
	if strings.Contains(tag("status-ui"), "dg-muted") {
		t.Errorf("login was muted: %s", tag("status-ui"))
	}
	if !strings.Contains(tag("capture"), "dg-muted") || !strings.Contains(tag("merge-first"), "dg-muted") || !strings.Contains(tag("silent"), "dg-muted") {
		t.Errorf("legacy or empty was not muted:\n%s\n%s\n%s", tag("capture"), tag("merge-first"), tag("silent"))
	}
	tick := func(label string) string {
		t.Helper()
		got := regexp.MustCompile(`<circle [^>]*class="([^"]*)"[^>]*>\s*<title>[^<]*` + regexp.QuoteMeta(label) + `</title>`).FindStringSubmatch(res.Markup)
		if got == nil {
			t.Fatalf("no tick for %s:\n%s", label, res.Markup)
		}
		return got[1]
	}
	if cls := tick("status-ui recorded"); !strings.Contains(cls, "dg-tick-login") || strings.Contains(cls, "dg-tick-user") {
		t.Errorf("login tick %q", cls)
	}
	if cls := tick("silent recorded"); strings.Contains(cls, "dg-tick-user") || strings.Contains(cls, "dg-tick-login") || strings.Contains(cls, "dg-tick-agent") {
		t.Errorf("empty decided_by tick %q", cls)
	}
	css := readAsset(t, "app.css")
	if !strings.Contains(css, "grid-template-columns: minmax(0, 1fr) minmax(0, 1fr)") || !strings.Contains(css, ".dg-key-label { min-width: 0; overflow-wrap: anywhere; }") {
		t.Error("legend labels do not wrap inside the key")
	}
	if !strings.Contains(css, ".dg-list-item > .detail { font-size: var(--sdf-font-size-xs); color: var(--sdf-color-text-muted); overflow-wrap: anywhere; }") {
		t.Error("list detail does not wrap a long decided_by")
	}
}

// Login badges stay unique in the chapter. A shared prefix is extended,
// and a one-character login is never the bare letter U or A.
func TestLoginBadgesStayUnique(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	setBy := func(node map[string]any, by string) {
		node["decided_by"] = by
		if rec, ok := node["decision"].(map[string]any); ok {
			rec["decided_by"] = by
			delete(rec, "decided_by_legacy")
		}
	}
	res := graphPage(t, srv, func(doc map[string]any) {
		for _, c := range doc["chapters"].([]any) {
			for _, n := range c.(map[string]any)["nodes"].([]any) {
				node := n.(map[string]any)
				switch node["id"] {
				case "capture":
					setBy(node, "paul")
				case "status-ui":
					setBy(node, "paula")
				case "silent":
					setBy(node, "u")
				}
			}
		}
	}, "#/decisions", "")
	badges := regexp.MustCompile(`class="dg-badge-text"[^>]*>([^<]*)</text>`).FindAllStringSubmatch(res.Markup, -1)
	gotMarks := map[string]int{}
	for _, m := range badges {
		gotMarks[m[1]]++
	}
	for _, want := range []string{"PAUL2", "PAULA", "U2"} {
		if gotMarks[want] == 0 {
			t.Errorf("missing badge %s in %v", want, gotMarks)
		}
	}
	if gotMarks["U"] != 0 {
		t.Errorf("one-character login used bare U: %v", gotMarks)
	}
}

// Cheap review polish: costs at 2 decimals, "1 of 1 ending reached", and
// long ids cut with an ellipsis and a title holding the whole id.
func TestDecisionPanelPolish(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	var ending string
	edit := func(doc map[string]any) {
		c := doc["chapters"].([]any)[0].(map[string]any)
		c["cost"] = map[string]any{"status": "known", "usd": "2.655"}
		c["facts"].(map[string]any)["endings_total"] = 1
		c["facts"].(map[string]any)["endings_reached"] = 1
		eachEnding(doc, func(node map[string]any) {
			if node["role"] == "reached" {
				ending = node["id"].(string)
				end := node["ending"].(map[string]any)
				end["cost"] = map[string]any{"status": "partial", "usd": "0.855"}
				end["sessions"] = []any{"ses_agent_status_0123456789"}
			}
		})
	}
	sum := graphPage(t, srv, edit, "#/decisions", "")
	for _, want := range []string{"computed 2.66 USD", "1 of 1 ending reached", "0.86 USD"} {
		if !strings.Contains(sum.Text, want) {
			t.Errorf("summary is missing %q:\n%s", want, sum.Text)
		}
	}
	if strings.Contains(sum.Text, "2.655") || strings.Contains(sum.Text, "endings reached") {
		t.Errorf("unrounded cost or plural for one ending:\n%s", sum.Text)
	}
	end := graphPage(t, srv, edit, "#/decisions/ch-capture/"+strings.ReplaceAll(ending, ":", "%3A"), "")
	if !strings.Contains(end.Markup, `title="ses_agent_status_0123456789">session ses_agent_st…</a>`) {
		t.Errorf("long session id not ellipsized with a title:\n%s", end.Markup)
	}
	if !strings.Contains(end.Text, "Computed cost 0.86 USD (partial)") {
		t.Errorf("ending cost:\n%s", end.Text)
	}
}

// No text in the UI is set below 11px, in the stylesheet or in the script.
func TestUITextIsAtLeast11px(t *testing.T) {
	sizes := regexp.MustCompile(`font(?:-size)?\s*:[^;{}"]*?(\d+(?:\.\d+)?)px`)
	for _, name := range []string{"app.css", "app.js"} {
		for _, m := range sizes.FindAllStringSubmatch(readAsset(t, name), -1) {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil && v < 11 {
				t.Errorf("%s sets text below 11px: %s", name, m[0])
			}
		}
	}
}
