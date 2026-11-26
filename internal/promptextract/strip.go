package promptextract

import (
	"sort"
	"strings"
)

// MaxTagDepth is the deepest nested harness tag of one name Extract will
// strip in a single text value. One more nested opener is a line error
// (ErrTagDepth). The scan itself is O(n); the cap rejects a pathological
// nest instead of spending unbounded stack or output work on it.
const MaxTagDepth = 32

// tagSpec is one harness tag the filter removes when the region is
// well-formed. fold is ASCII case-insensitive. attrs allows whitespace and
// attributes on the opening tag only. Closing tags allow ASCII whitespace
// before ">". An opener with no closer is left in the text.
type tagSpec struct {
	name  string
	fold  bool
	attrs bool
}

// harnessTags are the regions stripped from user-typed text. Whether a live
// Claude Code file uses each spelling is needs verification; the names are
// the filter's contract.
var harnessTags = []tagSpec{
	{name: "system-reminder", fold: true, attrs: true},
	{name: "local-command-stdout", fold: false, attrs: false},
	{name: "local-command-stderr", fold: false, attrs: false},
	{name: "local-command-caveat", fold: false, attrs: false},
	{name: "command-message", fold: false, attrs: false},
	{name: "command-name", fold: false, attrs: false},
	{name: "command-args", fold: false, attrs: false},
	{name: "bash-stdout", fold: false, attrs: false},
	{name: "bash-stderr", fold: false, attrs: false},
	{name: "bash-input", fold: false, attrs: false},
}

type tagHit struct {
	kind  int
	open  bool
	start int
	end   int
}

type openFrame struct {
	kind  int
	start int
}

type cutSpan struct {
	start int
	end   int
}

// stripHarness removes balanced harness-tag regions. An opener with no
// closer stays in the string, including any text after it, so a person
// asking how to close a tag is not truncated. An unmatched closer stays
// too. Nested system-reminder regions are removed as a tree: the outer
// closer ends the outer opener, and the tail inside the outer region does
// not leak.
func stripHarness(s string) (string, error) {
	var cuts []cutSpan
	var stack []openFrame
	depth := make([]int, len(harnessTags))
	for i := 0; i < len(s); {
		if s[i] != '<' {
			i++
			continue
		}
		hit, ok := findTag(s, i)
		if !ok {
			i++
			continue
		}
		if hit.open {
			depth[hit.kind]++
			if depth[hit.kind] > MaxTagDepth {
				return "", ErrTagDepth
			}
			stack = append(stack, openFrame{kind: hit.kind, start: hit.start})
			i = hit.end
			continue
		}
		found := -1
		for si := len(stack) - 1; si >= 0; si-- {
			if stack[si].kind == hit.kind {
				found = si
				break
			}
		}
		if found < 0 {
			i = hit.end
			continue
		}
		cuts = append(cuts, cutSpan{start: stack[found].start, end: hit.end})
		for si := found; si < len(stack); si++ {
			depth[stack[si].kind]--
		}
		stack = stack[:found]
		i = hit.end
	}
	if len(cuts) == 0 {
		return s, nil
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].start < cuts[j].start })
	merged := []cutSpan{cuts[0]}
	for _, sp := range cuts[1:] {
		last := &merged[len(merged)-1]
		if sp.start <= last.end {
			if sp.end > last.end {
				last.end = sp.end
			}
			continue
		}
		merged = append(merged, sp)
	}
	var b strings.Builder
	b.Grow(len(s))
	at := 0
	for _, sp := range merged {
		if sp.start > at {
			b.WriteString(s[at:sp.start])
		}
		if sp.end > at {
			at = sp.end
		}
	}
	if at < len(s) {
		b.WriteString(s[at:])
	}
	return b.String(), nil
}

func findTag(s string, i int) (tagHit, bool) {
	if i >= len(s) || s[i] != '<' {
		return tagHit{}, false
	}
	j := i + 1
	open := true
	if j < len(s) && s[j] == '/' {
		open = false
		j++
	}
	for kind, spec := range harnessTags {
		if j+len(spec.name) > len(s) || !matchTagName(s[j:j+len(spec.name)], spec.name, spec.fold) {
			continue
		}
		k := j + len(spec.name)
		if open {
			if k < len(s) && s[k] == '>' {
				return tagHit{kind: kind, open: true, start: i, end: k + 1}, true
			}
			if spec.attrs && k < len(s) && isASCIISpace(s[k]) {
				end, ok := scanTagAttrs(s, k)
				if ok {
					return tagHit{kind: kind, open: true, start: i, end: end}, true
				}
			}
			continue
		}
		for k < len(s) && isASCIISpace(s[k]) {
			k++
		}
		if k < len(s) && s[k] == '>' {
			return tagHit{kind: kind, open: false, start: i, end: k + 1}, true
		}
	}
	return tagHit{}, false
}

func matchTagName(got, name string, fold bool) bool {
	if !fold {
		return got == name
	}
	for i := 0; i < len(name); i++ {
		a, b := got[i], name[i]
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func isASCIISpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	default:
		return false
	}
}

// scanTagAttrs reads attributes starting at ASCII whitespace and returns
// the index after the closing ">". A ">" inside quotes does not end the
// tag. An opener that never reaches ">" is not a tag.
func scanTagAttrs(s string, k int) (int, bool) {
	var quote byte
	for k < len(s) {
		c := s[k]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			k++
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '>':
			return k + 1, true
		}
		k++
	}
	return 0, false
}

func isInterrupt(s string) bool {
	switch s {
	case "[Request interrupted by user]", "[Request interrupted by user for tool use]":
		return true
	default:
		return false
	}
}
