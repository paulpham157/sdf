package ui_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// Returning to now hides the Now button; focus that was on it moves to the
// slider, which stays in the tab order.
func TestReplayNowButtonGivesFocusToSlider(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	res := renderPage(t, srv, "#/decisions/ch-capture?at=5", "focus=.dg-now,click=.dg-now")
	if !strings.Contains(res.Markup, `aria-valuenow="8"`) {
		t.Fatalf("Now did not return to now:\n%s", res.Markup)
	}
	if !strings.Contains(res.Active, `class="dg-slider"`) {
		t.Errorf("focus after Now: %q, want the slider", res.Active)
	}
	if tag := regexp.MustCompile(`<button [^>]*data-focus-key="replay:now"[^>]*>`).FindString(res.Markup); !strings.Contains(tag, `hidden=""`) {
		t.Errorf("Now button not hidden at now:\n%s", res.Markup)
	}
	if strings.Contains(res.Text, "null") {
		t.Errorf("page draws \"null\" back at now:\n%s", res.Text)
	}
}

func liveText(res pageResult) string {
	m := regexp.MustCompile(`<p [^>]*aria-live="polite"[^>]*>(.*?)</p>`).FindStringSubmatch(res.Outside)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(m[1], "\u00a0", ""))
}

// The live region announces the replay starting, pausing, and finishing;
// the steps in between are in the slider's aria-valuetext only.
func TestReplayLiveRegionStartPauseFinishOnly(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	// Playback steps every 0.9 s: after 2 s it has stepped at least once.
	playing := renderPage(t, srv, "#/decisions/ch-capture?at=2", "key= @.dg-slider,wait=2000")
	now := regexp.MustCompile(`aria-valuenow="(\d)"`).FindStringSubmatch(playing.Markup)
	if now == nil || now[1] == "2" {
		t.Fatalf("playback did not step: %v", now)
	}
	if got := liveText(playing); !strings.HasPrefix(got, "Replaying from 2 of 8") {
		t.Errorf("live region while playing = %q, want only the start", got)
	}
	paused := renderPage(t, srv, "#/decisions/ch-capture?at=2", "key= @.dg-slider,wait=1000,key= @.dg-slider")
	if got := liveText(paused); !strings.HasPrefix(got, "Replay paused at ") {
		t.Errorf("live region after pause = %q", got)
	}
	finished := renderPage(t, srv, "#/decisions/ch-capture?at=6", "key= @.dg-slider,wait=3200")
	if got := liveText(finished); !strings.HasPrefix(got, "Replay finished: 8 of 8") {
		t.Errorf("live region after the end = %q", got)
	}
	if !strings.Contains(finished.Markup, `aria-valuenow="8"`) || strings.Contains(finished.Text, "null") {
		t.Errorf("finished replay is not cleanly at now:\n%s", finished.Text)
	}
}

// An exception inside a replay step stops playback and redraws the tab with
// render() instead of escaping the event handler.
func TestReplayStepErrorFallsBackToRender(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	_, _, html := get(t, srv.URL())
	_, _, script := get(t, srv.URL()+"app.js")
	const fn = "function dgProject(chapter, n, prev) {"
	if !strings.Contains(script, fn) {
		t.Fatal("dgProject not found in app.js")
	}
	broken := strings.Replace(script, fn, fn+` if (n === 3 && prev === 4) throw new Error("boom");`, 1)
	for _, flags := range []string{"key=[@.dg-slider", "key=["} {
		res := runPage(t, srv.URL(), html, broken, "#/decisions/ch-capture?at=4", flags)
		if !strings.Contains(res.Markup, `class="dg-slider"`) || !strings.Contains(res.Text, "Chapter · connected decisions") {
			t.Errorf("%s: tab not redrawn after a failing step:\n%s", flags, res.Text)
		}
	}
}

// ?at=N names a point in the linked chapter; on the fallback to the newest
// chapter it is ignored. The ? sheet lists [ and ]. A decision not recorded
// yet at the point gets no state chips.
func TestReplayNits(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	fallback := renderPage(t, srv, "#/decisions/ch-missing?at=2", "")
	if !strings.Contains(fallback.Markup, `aria-valuenow="8"`) || strings.Contains(fallback.Text, "As of ") {
		t.Errorf("?at= applied after a fallback chapter:\n%s", fallback.Text)
	}
	help := renderPage(t, srv, "#/decisions/ch-capture", "click=~?")
	if !strings.Contains(help.Text, "[ and ] step back and forward") {
		t.Errorf("? sheet does not list [ and ]:\n%s", help.Text)
	}
	future := renderPage(t, srv, "#/decisions/ch-capture/silent?at=2", "")
	panel := regexp.MustCompile(`(?s)<aside [^>]*class="dg-panel".*?</aside>`).FindString(future.Markup)
	if !strings.Contains(panel, "Not recorded yet") {
		t.Fatalf("future node panel:\n%s", panel)
	}
	if strings.Contains(panel, "dg-pchip") {
		t.Errorf("future node shows current-state chips:\n%s", panel)
	}
}

// Times order to the full stored fraction, a time with no zone is read as
// UTC, an unreadable time sorts last and reads "time unknown", and labels
// use the browser's zone.
func TestReplayTimeOrderingAndBadTimes(t *testing.T) {
	t.Setenv("TZ", "Asia/Ho_Chi_Minh")
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)
	_, _, html := get(t, srv.URL())
	_, _, script := get(t, srv.URL()+"app.js")
	bodies := map[string]string{}
	for _, path := range []string{"api/snapshot", "api/decisions", "api/summary", "api/decisions/graph"} {
		_, _, body := get(t, srv.URL()+path)
		bodies["/"+path] = body
	}
	var graph map[string]any
	if err := json.Unmarshal([]byte(bodies["/api/decisions/graph"]), &graph); err != nil {
		t.Fatal(err)
	}
	times := map[string]string{
		"capture":     "2026-11-22T09:14:00.000002+00:00",
		"status-ui":   "2026-11-22T09:14:00.000001+00:00", // same millisecond, earlier
		"merge-first": "2026-11-22T09:15:00",              // no zone: UTC
		"silent":      "not a time",
	}
	for _, ch := range graph["chapters"].([]any) {
		for _, n := range ch.(map[string]any)["nodes"].([]any) {
			node := n.(map[string]any)
			if rec, ok := node["decision"].(map[string]any); ok {
				if at, ok := times[node["id"].(string)]; ok {
					rec["recorded_at"] = at
				}
			}
		}
	}
	patched, _ := json.Marshal(graph)
	bodies["/api/decisions/graph"] = string(patched)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer fake.Close()
	valuetext := func(at string) string {
		res := runPage(t, fake.URL+"/", html, script, "#/decisions/ch-capture"+at, "")
		m := regexp.MustCompile(`aria-valuetext="([^"]*)"`).FindStringSubmatch(res.Markup)
		if m == nil {
			t.Fatalf("no slider at %q:\n%s", at, res.Markup)
		}
		return m[1]
	}
	for at, want := range map[string]string{
		"?at=1": "1 of 8, Nov 22 · 16:14:00 GMT+7, status-ui recorded",
		"?at=2": "2 of 8, Nov 22 · 16:14:00 GMT+7, capture recorded",
		"?at=3": "3 of 8, Nov 22 · 16:15:00 GMT+7, merge-first recorded",
		"":      "8 of 8, time unknown, silent recorded, now",
	} {
		if got := valuetext(at); got != want {
			t.Errorf("%q: valuetext %q, want %q", at, got, want)
		}
	}
}
