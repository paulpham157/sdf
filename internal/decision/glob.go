package decision

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// maxGlobsPerDecision is how many path globs one decision may record.
	maxGlobsPerDecision = 32
	// maxGlobBytes is the most bytes one glob may be.
	maxGlobBytes = 256
)

// ValidateGlob reports whether glob is a path pattern this package stores.
// It is a small gitignore subset: * and ? match inside one path segment,
// ** matches across segments, and a trailing / matches a directory. There
// is no ! negation. The pattern is case-sensitive. It must be at most 256
// bytes, valid UTF-8, with no NUL or other control character, no backslash,
// no leading /, and no .. segment.
func ValidateGlob(glob string) error {
	if glob == "" {
		return fmt.Errorf("invalid path glob %s: empty", strconv.Quote(glob))
	}
	if len(glob) > maxGlobBytes {
		return fmt.Errorf("invalid path glob: longer than %d bytes", maxGlobBytes)
	}
	if !utf8.ValidString(glob) {
		return fmt.Errorf("invalid path glob: not valid UTF-8")
	}
	for _, r := range glob {
		if r < 0x20 || r == 0x7f || unicode.IsControl(r) {
			return fmt.Errorf("invalid path glob %s: control character", strconv.Quote(glob))
		}
	}
	if strings.Contains(glob, `\`) {
		return fmt.Errorf("invalid path glob %s: backslash", strconv.Quote(glob))
	}
	if strings.HasPrefix(glob, "/") {
		return fmt.Errorf("invalid path glob %s: leading /", strconv.Quote(glob))
	}
	if strings.HasPrefix(glob, "!") {
		return fmt.Errorf("invalid path glob %s: negation is not supported", strconv.Quote(glob))
	}
	body := strings.TrimSuffix(glob, "/")
	if body == "" {
		return fmt.Errorf("invalid path glob %s: empty", strconv.Quote(glob))
	}
	for _, seg := range strings.Split(body, "/") {
		if seg == "" {
			return fmt.Errorf("invalid path glob %s: empty segment", strconv.Quote(glob))
		}
		if seg == ".." {
			return fmt.Errorf("invalid path glob %s: .. segment", strconv.Quote(glob))
		}
	}
	if _, err := compileGlob(glob); err != nil {
		return err
	}
	return nil
}

// ValidatePaths checks globs as a set for one write: each one is valid,
// none is repeated, and there are at most 32.
func ValidatePaths(globs []string) error {
	if len(globs) > maxGlobsPerDecision {
		return fmt.Errorf("a decision can have at most %d paths", maxGlobsPerDecision)
	}
	seen := make(map[string]bool, len(globs))
	for _, g := range globs {
		if err := ValidateGlob(g); err != nil {
			return err
		}
		if seen[g] {
			return fmt.Errorf("duplicate path glob %s", strconv.Quote(g))
		}
		seen[g] = true
	}
	return nil
}

// CompileGlob compiles glob to a RE2 regexp. The caller has validated it,
// or CompileGlob validates it again. A stored glob that no longer validates
// returns an error and is not matched.
func CompileGlob(glob string) (*regexp.Regexp, error) {
	if err := ValidateGlob(glob); err != nil {
		return nil, err
	}
	return compileGlob(glob)
}

// compileGlob turns a structurally valid glob into a regexp. * and ? stay
// inside one segment. ** is the gitignore form: a leading **/ matches in
// every directory, a trailing /** matches everything inside, and /**/
// matches zero or more directories. A pattern with no / (ignoring one
// trailing slash) matches in any directory. A trailing slash matches that
// directory and everything under it. Other characters are literal.
func compileGlob(glob string) (*regexp.Regexp, error) {
	dirOnly := strings.HasSuffix(glob, "/")
	body := strings.TrimSuffix(glob, "/")
	var b strings.Builder
	b.WriteString("^")
	if !strings.Contains(body, "/") {
		b.WriteString(`(?:[^/]+/)*`)
	}
	writeGlobBody(&b, body)
	if dirOnly {
		b.WriteString(`(?:(?s:/.*))?`)
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func writeGlobBody(b *strings.Builder, pattern string) {
	for i := 0; i < len(pattern); {
		atBoundary := i == 0 || pattern[i-1] == '/'
		if atBoundary && strings.HasPrefix(pattern[i:], "**/") {
			b.WriteString(`(?:[^/]+/)*`)
			i += 3
			continue
		}
		if pattern[i] == '/' && strings.HasPrefix(pattern[i:], "/**") && (i+3 == len(pattern) || pattern[i+3] == '/') {
			if i+3 == len(pattern) {
				b.WriteString(`(?s:/.+)`)
				return
			}
			b.WriteString(`(?:/[^/]+)*`)
			b.WriteByte('/')
			i += 4
			continue
		}
		switch pattern[i] {
		case '*':
			b.WriteString(`[^/]*`)
		case '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
		i++
	}
}
