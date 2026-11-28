package ui_test

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The outset focus ring on these controls is drawn on the page, not on the
// inverse fill. focus-on-inverse fails 3:1 there. They keep --sdf-color-focus.
func TestUIFocusRingClearsPage(t *testing.T) {
	app := string(readRepoFile(t, "internal/ui/assets/app.css"))
	tokensCSS := string(readRepoFile(t, "internal/ui/assets/tokens.css"))
	light := resolveTheme(tokensCSS, false)
	dark := resolveTheme(tokensCSS, true)

	controls := []struct {
		name, selector, element string
	}{
		{"current nav link", `.nav a[aria-current="page"]:focus-visible`, "a"},
		{"pressed filter", `.filters button[aria-pressed="true"]:focus-visible`, "button"},
		{"pressed graph-toolbar button", `.dg-toolbar button[aria-pressed="true"]:focus-visible`, "button"},
		{"checked graph-toolbar button", `.dg-toolbar button[aria-checked="true"]:focus-visible`, "button"},
		{"Replay button", `.dg-play:focus-visible`, "button"},
		{"skip link", `.skip:focus-visible`, "a"},
	}
	rules := parseStyleRules(app)
	for _, c := range controls {
		color, offset := winningOutline(rules, c.selector, c.element)
		if color != "var(--sdf-color-focus)" {
			t.Errorf("%s (%s) outline-color %q, want var(--sdf-color-focus)", c.name, c.selector, color)
		}
		if offset != "2px" {
			t.Errorf("%s (%s) outline-offset %q, want 2px outset", c.name, c.selector, offset)
		}
		for _, mode := range []struct {
			name string
			vars map[string]string
			want float64
		}{
			{"light", light, 3.81},
			{"dark", dark, 10.76},
		} {
			fg := lookupColor(color, mode.vars)
			bg := mode.vars["--sdf-color-bg"]
			got := contrastRatio(fg, bg)
			t.Logf("%s %s %s on %s = %.2f:1", c.name, mode.name, fg, bg, got)
			if got < 3 {
				t.Errorf("%s %s focus ring %.2f:1, want >= 3:1 against the page", c.name, mode.name, got)
			}
			if math.Abs(got-mode.want) > 0.02 {
				t.Errorf("%s %s ratio %.2f, want %.2f", c.name, mode.name, got, mode.want)
			}
		}
	}

	// The rejected override, measured against the same page, stays under 3:1.
	for _, mode := range []struct {
		name string
		vars map[string]string
		want float64
	}{
		{"light", light, 2.72},
		{"dark", dark, 2.25},
	} {
		fg := mode.vars["--sdf-color-focus-on-inverse"]
		bg := mode.vars["--sdf-color-bg"]
		got := contrastRatio(fg, bg)
		t.Logf("focus-on-inverse %s %s on %s = %.2f:1", mode.name, fg, bg, got)
		if got >= 3 {
			t.Errorf("focus-on-inverse %s is %.2f:1 on the page; recorded page contrast is under 3:1", mode.name, got)
		}
		if math.Abs(got-mode.want) > 0.02 {
			t.Errorf("focus-on-inverse %s ratio %.2f, want %.2f", mode.name, got, mode.want)
		}
	}
}

type styleRule struct {
	selectors []string
	decls     []cssDecl
}

type cssDecl struct {
	prop, val string
}

func parseStyleRules(css string) []styleRule {
	css = stripCSSComments(css)
	var rules []styleRule
	for _, block := range declBlocks(css) {
		prelude, body := block[0], block[1]
		var decls []cssDecl
		for _, part := range strings.Split(body, ";") {
			prop, val, ok := strings.Cut(part, ":")
			if !ok {
				continue
			}
			decls = append(decls, cssDecl{prop: strings.ToLower(strings.TrimSpace(prop)), val: strings.TrimSpace(val)})
		}
		var selectors []string
		for _, sel := range strings.Split(prelude, ",") {
			sel = strings.TrimSpace(sel)
			if sel != "" {
				selectors = append(selectors, sel)
			}
		}
		if len(selectors) > 0 && len(decls) > 0 {
			rules = append(rules, styleRule{selectors: selectors, decls: decls})
		}
	}
	return rules
}

func declBlocks(css string) [][2]string {
	var blocks [][2]string
	for i := 0; i < len(css); {
		start := strings.IndexByte(css[i:], '{')
		if start < 0 {
			break
		}
		start += i
		depth, j := 1, start+1
		for j < len(css) && depth > 0 {
			switch css[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
			j++
		}
		body := css[start+1 : j-1]
		if strings.Contains(body, "{") {
			blocks = append(blocks, declBlocks(body)...)
		} else {
			blocks = append(blocks, [2]string{strings.TrimSpace(css[i:start]), body})
		}
		i = j
	}
	return blocks
}

func stripCSSComments(css string) string {
	re := regexp.MustCompile(`(?s)/\*.*?\*/`)
	return re.ReplaceAllString(css, "")
}

func selectorMatches(selector, element, target string) bool {
	switch selector {
	case target, element + ":focus-visible", ":focus-visible":
		return true
	default:
		return false
	}
}

func specificity(selector string) (int, int, int) {
	ids := strings.Count(selector, "#")
	classes := strings.Count(selector, ".") + strings.Count(selector, "[")
	pseudos := 0
	for i := 0; i < len(selector); i++ {
		if selector[i] != ':' {
			continue
		}
		if i+1 < len(selector) && selector[i+1] == ':' {
			i++
			continue
		}
		pseudos++
	}
	elements := 0
	cleaned := regexp.MustCompile(`\[[^\]]*\]|:[a-zA-Z-]+`).ReplaceAllString(selector, " ")
	for _, field := range strings.FieldsFunc(cleaned, func(r rune) bool {
		return r == ' ' || r == '>' || r == '+' || r == '~' || r == '.' || r == '#'
	}) {
		if field != "" && field != "*" {
			elements++
		}
	}
	return ids, classes + pseudos, elements
}

type scoredDecl struct {
	val     string
	a, b, c int
	order   int
	set     bool
}

func better(next, cur scoredDecl) bool {
	if !cur.set {
		return true
	}
	if next.a != cur.a {
		return next.a > cur.a
	}
	if next.b != cur.b {
		return next.b > cur.b
	}
	if next.c != cur.c {
		return next.c > cur.c
	}
	return next.order > cur.order
}

var customProp = regexp.MustCompile(`var\(--sdf-[a-z0-9-]+\)`)

func outlineColor(value string) string {
	found := customProp.FindAllString(value, -1)
	if len(found) == 0 {
		return strings.TrimSpace(value)
	}
	return found[len(found)-1]
}

func winningOutline(rules []styleRule, target, element string) (color, offset string) {
	var bestColor, bestOffset scoredDecl
	order := 0
	for _, rule := range rules {
		var matched string
		var ma, mb, mc int
		found := false
		for _, sel := range rule.selectors {
			if !selectorMatches(sel, element, target) {
				continue
			}
			a, b, c := specificity(sel)
			if !found || a > ma || (a == ma && b > mb) || (a == ma && b == mb && c > mc) {
				matched, ma, mb, mc, found = sel, a, b, c, true
			}
		}
		if !found {
			continue
		}
		_ = matched
		for _, d := range rule.decls {
			order++
			switch d.prop {
			case "outline":
				next := scoredDecl{val: outlineColor(d.val), a: ma, b: mb, c: mc, order: order, set: true}
				if better(next, bestColor) {
					bestColor = next
				}
			case "outline-color":
				next := scoredDecl{val: outlineColor(d.val), a: ma, b: mb, c: mc, order: order, set: true}
				if better(next, bestColor) {
					bestColor = next
				}
			case "outline-offset":
				next := scoredDecl{val: d.val, a: ma, b: mb, c: mc, order: order, set: true}
				if better(next, bestOffset) {
					bestOffset = next
				}
			}
		}
	}
	return bestColor.val, bestOffset.val
}

func resolveTheme(css string, dark bool) map[string]string {
	root := blockProps(css, ":root")
	if !dark {
		return resolveProps(root)
	}
	over := blockProps(css, `[data-sdf-theme="dark"]`)
	for k, v := range over {
		root[k] = v
	}
	return resolveProps(root)
}

func blockProps(css, preludeNeedle string) map[string]string {
	css = stripCSSComments(css)
	props := map[string]string{}
	for _, block := range declBlocks(css) {
		if !strings.Contains(block[0], preludeNeedle) {
			continue
		}
		for _, part := range strings.Split(block[1], ";") {
			prop, val, ok := strings.Cut(part, ":")
			if !ok {
				continue
			}
			props[strings.TrimSpace(prop)] = strings.TrimSpace(val)
		}
		if preludeNeedle == ":root" {
			break
		}
	}
	return props
}

func resolveProps(raw map[string]string) map[string]string {
	resolved := map[string]string{}
	var rec func(name string, seen map[string]bool) string
	rec = func(name string, seen map[string]bool) string {
		if v, ok := resolved[name]; ok {
			return v
		}
		if seen[name] {
			return ""
		}
		seen[name] = true
		v := raw[name]
		if strings.HasPrefix(v, "var(") && strings.HasSuffix(v, ")") {
			inner := strings.TrimSpace(v[len("var(") : len(v)-1])
			v = rec(inner, seen)
		}
		resolved[name] = v
		return v
	}
	for name := range raw {
		rec(name, map[string]bool{})
	}
	return resolved
}

func lookupColor(ref string, vars map[string]string) string {
	if strings.HasPrefix(ref, "var(") && strings.HasSuffix(ref, ")") {
		return vars[strings.TrimSpace(ref[len("var("):len(ref)-1])]
	}
	return ref
}

func contrastRatio(fg, bg string) float64 {
	la, lb := relLum(fg), relLum(bg)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func relLum(hex string) float64 {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) != 6 {
		return 0
	}
	ch := func(i int) float64 {
		v, err := strconv.ParseUint(hex[i:i+2], 16, 8)
		if err != nil {
			return 0
		}
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(0) + 0.7152*ch(2) + 0.0722*ch(4)
}
