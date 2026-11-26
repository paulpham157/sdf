package promptextract

import (
	"sort"
	"strings"
)

// MaxTagDepth is how many open harness tags of one name the scanner
// tracks in one text value. An opener past that cap is literal text: it
// is not stripped, and it does not fail the line. Unclosed mentions
// therefore never drop a prompt. A closed nest is stripped only for the
// openers that were tracked.
const MaxTagDepth = 32

// maxTagAttrBytes is the longest opening-tag attribute list scanTagAttrs
// will accept. A longer list, or one that never reaches ">", is not a tag.
// The cap keeps one attempt O(1); the watermark below keeps a whole text
// value O(n).
const maxTagAttrBytes = 1024

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
// not leak. A self-closing opener is not a tag.
func stripHarness(s string) string {
	var cuts []cutSpan
	var stack []openFrame
	depth := make([]int, len(harnessTags))
	mark := scanMark{lastGt: -1}
	for i := 0; i < len(s); {
		if s[i] != '<' {
			i++
			continue
		}
		hit, resume, ok := findTag(s, i, &mark)
		if !ok {
			if resume > i {
				i = resume
			} else {
				i++
			}
			continue
		}
		if hit.open {
			// One more unclosed opener must not drop the prompt. Leave it
			// in the text and keep tracking the openers already on the stack.
			if depth[hit.kind] >= MaxTagDepth {
				i = hit.end
				continue
			}
			depth[hit.kind]++
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
		return s
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
	return b.String()
}

// scanMark remembers how far a search for '>' has already read. lastGt is
// the rightmost '>' at an index below cursor, or -1 when none has been
// seen. cursor only moves forward, so a failed attribute scan does not
// read the same tail again.
type scanMark struct {
	cursor int
	lastGt int
}

// hasGt reports whether s[from:to] contains '>'. Each byte of s is read at
// most once across every call on the same mark.
func (m *scanMark) hasGt(s string, from, to int) bool {
	if to > len(s) {
		to = len(s)
	}
	if from >= to {
		return false
	}
	if m.lastGt >= from && m.lastGt < to {
		return true
	}
	if m.cursor < from {
		for m.cursor < from {
			if s[m.cursor] == '>' {
				m.lastGt = m.cursor
			}
			m.cursor++
		}
		if m.lastGt >= from && m.lastGt < to {
			return true
		}
	}
	for m.cursor < to {
		c := s[m.cursor]
		m.cursor++
		if c == '>' {
			m.lastGt = m.cursor - 1
			return true
		}
	}
	return false
}

func findTag(s string, i int, mark *scanMark) (tagHit, int, bool) {
	if i >= len(s) || s[i] != '<' {
		return tagHit{}, i + 1, false
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
				return tagHit{kind: kind, open: true, start: i, end: k + 1}, 0, true
			}
			if spec.attrs && k < len(s) && isASCIISpace(s[k]) {
				end, good, resume := scanTagAttrs(s, k, mark)
				if good {
					return tagHit{kind: kind, open: true, start: i, end: end}, 0, true
				}
				if resume > i {
					return tagHit{}, resume, false
				}
			}
			continue
		}
		for k < len(s) && isASCIISpace(s[k]) {
			k++
		}
		if k < len(s) && s[k] == '>' {
			return tagHit{kind: kind, open: false, start: i, end: k + 1}, 0, true
		}
	}
	return tagHit{}, 0, false
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

// scanTagAttrs reads attributes starting at ASCII whitespace. end is the
// index after the closing ">" when the opener is well-formed. A ">" inside
// quotes does not end the tag. The scan stops at maxTagAttrBytes, at a
// newline, or at an unquoted "<", and then the opener is not a tag.
// resume, when greater than the caller's start, is the index the caller
// should continue from so the stopped-at "<" is still seen. A slash
// immediately before ">", with ASCII spaces between them allowed, is a
// self-closing tag and is not an opener: "<system-reminder />" stays in
// the text.
func scanTagAttrs(s string, k int, mark *scanMark) (end int, ok bool, resume int) {
	start := k
	limit := k + maxTagAttrBytes
	if limit > len(s) {
		limit = len(s)
	}
	// No '>' in the window means this cannot be a tag. The mark proves
	// that without rereading a tail already walked.
	if !mark.hasGt(s, start, limit) {
		return 0, false, 0
	}
	var quote byte
	for k < limit {
		c := s[k]
		if quote != 0 {
			if c == '\n' {
				return 0, false, k
			}
			if c == quote {
				quote = 0
			}
			k++
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
			k++
		case '<', '\n':
			return 0, false, k
		case '/':
			j := k + 1
			for j < limit && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			if j < limit && s[j] == '>' {
				return 0, false, 0
			}
			k++
		case '>':
			return k + 1, true, 0
		default:
			k++
		}
	}
	return 0, false, 0
}

func isInterrupt(s string) bool {
	switch s {
	case "[Request interrupted by user]", "[Request interrupted by user for tool use]":
		return true
	default:
		return false
	}
}
