package cli

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// nameStatusCode is a git --name-status code: one letter, then an optional
// similarity score (R100, C75). --no-renames turns renames into D and A;
// R and C are still accepted so both paths are matched if git emits them.
var nameStatusCode = regexp.MustCompile(`^[A-Z][0-9]*$`)

type parsedCommit struct {
	SHA      string
	Trailers []string
	Paths    []string
}

// parseNameStatusLog reads `git log --name-status --no-renames -z` output
// whose --format record ends with the unit separator (U+001E) and whose
// trailer separator is the unit separator's sibling (U+001F). A record is
// SHA, then trailers, then U+001E, then NUL, then an optional newline, then
// STATUS NUL path NUL pairs. An empty commit has no newline and no pairs.
// Anything else is an error: the caller fails open rather than guessing.
func parseNameStatusLog(raw []byte) ([]parsedCommit, error) {
	var out []parsedCommit
	rest := raw
	for len(rest) > 0 {
		recEnd := bytes.IndexByte(rest, 0x1e)
		if recEnd < 0 {
			return nil, fmt.Errorf("git log: truncated name-status output")
		}
		header := rest[:recEnd]
		rest = rest[recEnd+1:]
		if len(rest) == 0 || rest[0] != 0 {
			return nil, fmt.Errorf("git log: truncated name-status output")
		}
		rest = rest[1:]
		c, err := parseNameStatusHeader(header)
		if err != nil {
			return nil, err
		}
		if len(rest) > 0 && rest[0] == '\n' && !looksLikeCommit(rest[1:]) {
			rest = rest[1:]
		}
		for len(rest) > 0 && !looksLikeCommit(rest) {
			paths, next, err := readNameStatus(rest)
			if err != nil {
				return nil, err
			}
			c.Paths = append(c.Paths, paths...)
			rest = next
		}
		out = append(out, c)
	}
	return out, nil
}

func parseNameStatusHeader(h []byte) (parsedCommit, error) {
	parts := bytes.Split(h, []byte{0x1f})
	sha := strings.ToLower(strings.TrimSpace(string(parts[0])))
	if !prePushSHA.MatchString(sha) {
		return parsedCommit{}, fmt.Errorf("git log: unexpected commit id")
	}
	c := parsedCommit{SHA: sha}
	for _, p := range parts[1:] {
		if v := strings.TrimSpace(string(p)); v != "" {
			c.Trailers = append(c.Trailers, v)
		}
	}
	return c, nil
}

func looksLikeCommit(b []byte) bool {
	j := bytes.IndexByte(b, 0x1f)
	if j <= 0 || bytes.IndexByte(b[:j], 0) >= 0 {
		return false
	}
	return prePushSHA.Match(b[:j])
}

func readNameStatus(b []byte) ([]string, []byte, error) {
	i := bytes.IndexByte(b, 0)
	if i <= 0 {
		return nil, nil, fmt.Errorf("git log: truncated name-status output")
	}
	status := string(b[:i])
	if !nameStatusCode.MatchString(status) {
		return nil, nil, fmt.Errorf("git log: unexpected name-status %q", status)
	}
	n := 1
	if status[0] == 'R' || status[0] == 'C' {
		n = 2
	}
	rest := b[i+1:]
	paths := make([]string, 0, n)
	for k := 0; k < n; k++ {
		i = bytes.IndexByte(rest, 0)
		if i < 0 {
			return nil, nil, fmt.Errorf("git log: truncated name-status output")
		}
		if i == 0 {
			return nil, nil, fmt.Errorf("git log: empty path")
		}
		paths = append(paths, string(rest[:i]))
		rest = rest[i+1:]
	}
	return paths, rest, nil
}
