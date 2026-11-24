package ui_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readRepoFile(t *testing.T, rel string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return body
}

// The UI copy is the file the binary embeds. It has to match design/tokens.css
// byte for byte, and the generator has to agree that file is current.
func TestDesignTokensCopiedByteForByte(t *testing.T) {
	want := readRepoFile(t, "design/tokens.css")
	got := readRepoFile(t, "internal/ui/assets/tokens.css")
	if !bytes.Equal(want, got) {
		t.Fatalf("internal/ui/assets/tokens.css is not a byte-identical copy of design/tokens.css (design %d bytes, ui %d bytes)", len(want), len(got))
	}
	cmd := exec.Command("python3", filepath.Join(repoRoot(t), "design", "tokens.py"), "--check")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python3 design/tokens.py --check: %v\n%s", err, out)
	}
}

// tokens.css is linked as a stylesheet under the UI CSP. It cannot pull
// anything else in, and the asset scan forbids network URLs.
func TestTokensCSSStandsAlone(t *testing.T) {
	body := string(readRepoFile(t, "internal/ui/assets/tokens.css"))
	if body == "" {
		t.Fatal("tokens.css is empty")
	}
	for _, banned := range []string{"@import", "url(", "http://", "https://"} {
		if strings.Contains(body, banned) {
			t.Errorf("tokens.css contains %q", banned)
		}
	}
	if !strings.Contains(body, "--sdf-color-bg:") || !strings.Contains(body, "--sdf-chart-series-1:") {
		t.Error("tokens.css is missing semantic color or chart tokens")
	}
}

func TestTokensStylesheetRoute(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	status, hdr, body := get(t, srv.URL()+"tokens.css")
	if status != 200 {
		t.Fatalf("GET /tokens.css status %d", status)
	}
	if got := hdr.Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("Content-Type %q", got)
	}
	want := string(readRepoFile(t, "design/tokens.css"))
	if body != want {
		t.Fatalf("GET /tokens.css did not match design/tokens.css (got %d bytes, want %d)", len(body), len(want))
	}
	pageStatus, _, page := get(t, srv.URL())
	if pageStatus != 200 {
		t.Fatalf("page %d", pageStatus)
	}
	tok := strings.Index(page, `<link rel="stylesheet" href="/tokens.css">`)
	app := strings.Index(page, `<link rel="stylesheet" href="/app.css">`)
	if tok < 0 || app < 0 || tok > app {
		t.Fatalf("tokens.css must be linked before app.css:\n%s", page)
	}
	if !strings.Contains(page, `rel="icon" href="/favicon.svg"`) || !strings.Contains(page, `rel="icon" href="/favicon.ico"`) {
		t.Fatal("favicon links changed")
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Fatalf("page loads a remote URL: %q", page)
	}
	if !strings.Contains(page, `<a class="skip" href="#main">`) || !strings.Contains(page, `<main id="main">`) {
		t.Fatalf("page is missing a skip link or main landmark:\n%s", page)
	}
	if strings.Contains(page, " style=") {
		t.Fatal("page has an inline style attribute")
	}
}

func TestChromeMarkAndRouteTitle(t *testing.T) {
	logo := string(readRepoFile(t, "docs/images/logo.svg"))
	paths := regexp.MustCompile(`<path d="([^"]+)"`).FindAllStringSubmatch(logo, -1)
	if len(paths) != 7 {
		t.Fatalf("logo.svg has %d paths, want 7", len(paths))
	}
	script := readAsset(t, "app.js")
	if strings.Contains(script, "mark-badge") {
		t.Error("header still uses the SDF letter-box")
	}
	for _, m := range paths {
		if !strings.Contains(script, m[1]) {
			t.Errorf("app.js is missing logo path %q", m[1])
		}
	}
	if strings.Contains(script, "http://") || strings.Contains(script, "https://") || strings.Contains(script, " style=") {
		t.Error("app.js has a URL or an inline style")
	}
	db := openDB(t)
	srv := startUI(t, db)
	cases := []struct {
		hash  string
		title string
	}{
		{"#/overview", "Overview · SDF"},
		{"#/sessions", "Sessions · SDF"},
		{"#/sessions/missing", "Session · SDF"},
		{"#/decisions", "Decisions · SDF"},
	}
	for _, tc := range cases {
		res := renderPage(t, srv, tc.hash, "")
		if res.Title != tc.title {
			t.Errorf("hash %s title %q, want %q", tc.hash, res.Title, tc.title)
		}
		if !strings.Contains(res.Markup, `class="mark-logo"`) {
			t.Errorf("hash %s header has no fabric mark", tc.hash)
		}
		if !strings.Contains(res.Markup, paths[len(paths)-1][1]) {
			t.Errorf("hash %s mark does not draw the rust thread", tc.hash)
		}
	}
}

func TestAppCSSUsesSemanticTokens(t *testing.T) {
	css := readAsset(t, "app.css")
	for _, banned := range []string{"--paper", "--ink", "--green", "--amber", "--line-strong", "--sdf-color-rust-500"} {
		if strings.Contains(css, banned) {
			t.Errorf("app.css still uses %s", banned)
		}
	}
	// url(#dg-hatch) is an in-document paint server, not a color.
	if regexp.MustCompile(`#[0-9A-Fa-f]{3,8}`).MatchString(css) {
		t.Error("app.css still has a one-off hex color")
	}
	for _, want := range []string{
		"--sdf-font-sans",
		"--sdf-font-serif",
		"--sdf-font-mono",
		"--sdf-color-link",
		"--sdf-color-accent-text",
		"--sdf-color-focus",
		"--sdf-color-bg",
		"--sdf-color-text",
		"var(--sdf-table-font-size)",
		"var(--sdf-table-cell-padding-y-dense)",
		"var(--sdf-table-cell-padding-x-dense)",
		"var(--sdf-table-font-size-dense)",
		"var(--sdf-table-line-height-dense)",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css is missing %s", want)
		}
	}
	rules := []*regexp.Regexp{
		regexp.MustCompile(`\.dg-edge\.dg-on-path\s*\{[^}]*stroke:\s*var\(--sdf-graph-chosen\)[^}]*stroke-width:\s*var\(--sdf-border-width-heavy\)`),
		regexp.MustCompile(`\.dg-node\.dg-on-path \.dg-shape\s*\{[^}]*fill:\s*var\(--sdf-graph-chosen-fill\)[^}]*stroke:\s*var\(--sdf-graph-chosen\)`),
		regexp.MustCompile(`\.dg-node\.dg-on-path \.dg-title\s*\{[^}]*fill:\s*var\(--sdf-color-text-inverse\)`),
		regexp.MustCompile(`\.dg-role-not_taken \.dg-shape\s*\{[^}]*stroke:\s*var\(--sdf-graph-not-taken\)[^}]*stroke-dasharray:\s*5 4`),
		regexp.MustCompile(`\.dg-node\.dg-future \.dg-shape\s*\{[^}]*stroke:\s*var\(--sdf-graph-future\)[^}]*stroke-dasharray:\s*3 3`),
		regexp.MustCompile(`\.dg-edge\.dg-crack\s*\{[^}]*stroke:\s*var\(--sdf-graph-conflict\)[^}]*stroke-dasharray:\s*9 3 2 3`),
		regexp.MustCompile(`\.chart-bar-a\s*\{[^}]*fill:\s*var\(--sdf-chart-series-1\)`),
		regexp.MustCompile(`\.chart-bar-b\s*\{[^}]*fill:\s*var\(--sdf-chart-series-2\)`),
		regexp.MustCompile(`\.chart-line\s*\{[^}]*stroke:\s*var\(--sdf-chart-series-2\)`),
		regexp.MustCompile(`input:focus-visible,\s*select:focus-visible`),
	}
	for _, re := range rules {
		if !re.MatchString(css) {
			t.Errorf("app.css is missing rule %s", re)
		}
	}
	if !strings.Contains(css, "a { color: var(--sdf-color-link)") && !regexp.MustCompile(`a\s*\{[^}]*color:\s*var\(--sdf-color-link\)`).MatchString(css) {
		t.Error("links do not use the link token")
	}
}

func TestChartColumnGapAndLegend(t *testing.T) {
	script := readAsset(t, "app.js")
	if !strings.Contains(script, "CHART_COLUMN_GAP") || !strings.Contains(script, "barW - CHART_COLUMN_GAP") {
		t.Error("grouped bars do not keep a 1px gap between columns")
	}
	if !strings.Contains(script, "chart-legend") || !strings.Contains(script, "chart-table") {
		t.Error("charts dropped the legend or the data table")
	}
	if regexp.MustCompile(`#[0-9A-Fa-f]{3,8}`).MatchString(script) {
		t.Error("app.js still hard-codes a hex color")
	}
	for _, old := range []string{"#8a4b08", "#5e584e", "#8a8275", "#b9b0a0", "#a3301d", "#0c6b4d", "#3a352e"} {
		if strings.Contains(script, old) {
			t.Errorf("app.js still uses %s", old)
		}
	}
}
