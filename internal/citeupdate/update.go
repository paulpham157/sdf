package main

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	keyVersion = "version"
	keyDate    = "date-released"
	keyCommit  = "commit"
)

// Status is the outcome of applying one release to CITATION.cff.
type Status string

const (
	// StatusUpdated means the file bytes changed and should be committed.
	StatusUpdated Status = "updated"
	// StatusUnchanged means version, date-released, and commit already match.
	StatusUnchanged Status = "unchanged"
	// StatusSkipped means this release must not replace the file.
	StatusSkipped Status = "skipped"
)

// UpdateResult is a CITATION.cff body and what Update decided.
// Content is the original buffer when Status is not StatusUpdated.
type UpdateResult struct {
	Content []byte
	Status  Status
	Reason  string
}

// versionRE is MAJOR.MINOR.PATCH with no prerelease and no build metadata.
// commitRE is a full Git SHA-1. This repository's objects are SHA-1; peeling
// is what rejects an annotated tag object, which has the same shape.
var (
	versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	commitRE  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// stableVersion accepts a release tag of vMAJOR.MINOR.PATCH and returns the
// version written into CITATION.cff. Suffixes such as -rc.1 are refused:
// goreleaser marks those prereleases, and the citation file cites a stable
// release.
func stableVersion(tag string) (string, bool) {
	ver, ok := strings.CutPrefix(tag, "v")
	if !ok || !versionRE.MatchString(ver) {
		return "", false
	}
	return ver, true
}

func releaseDate(publishedAt string) (string, error) {
	if publishedAt == "" {
		return "", errors.New("published_at is empty")
	}
	if t, err := time.Parse(time.RFC3339, publishedAt); err == nil {
		return t.UTC().Format("2006-01-02"), nil
	}
	if err := validateDate(publishedAt); err != nil {
		return "", fmt.Errorf("published_at %q is not RFC3339 or YYYY-MM-DD", publishedAt)
	}
	return publishedAt, nil
}

func validateDate(s string) error {
	t, err := time.Parse("2006-01-02", s)
	if err != nil || t.Format("2006-01-02") != s {
		return errors.New("date-released must be YYYY-MM-DD")
	}
	return nil
}

func validateCommit(sha string) error {
	if !commitRE.MatchString(sha) {
		return fmt.Errorf("commit %q is not 40 lowercase hex characters", sha)
	}
	return nil
}

func validateVersion(s string) error {
	if _, err := parseVer(s); err != nil {
		return fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", s)
	}
	return nil
}

// Update replaces top-level version, date-released, and commit when the
// release version is equal to or newer than the version already in the file.
// An older release is skipped so publishing a leftover draft cannot move the
// citation backwards. Every other line is copied unchanged. Missing keys and
// values that are not plain scalars are errors: this does not invent a
// citation file or add authors, DOIs, or URLs.
func Update(content []byte, version, date, commit string) (UpdateResult, error) {
	if err := validateVersion(version); err != nil {
		return UpdateResult{}, err
	}
	if err := validateDate(date); err != nil {
		return UpdateResult{}, err
	}
	if err := validateCommit(commit); err != nil {
		return UpdateResult{}, err
	}
	parsed, err := parseFile(content)
	if err != nil {
		return UpdateResult{}, err
	}
	fields := map[string]*scalarLine{}
	for _, key := range []string{keyVersion, keyDate, keyCommit} {
		f := parsed.fields[key]
		if f == nil {
			return UpdateResult{}, fmt.Errorf("CITATION.cff has no top-level %s", key)
		}
		fields[key] = f
	}
	cmp, err := compareVersion(fields[keyVersion].value, version)
	if err != nil {
		return UpdateResult{}, err
	}
	if cmp > 0 {
		return UpdateResult{
			Content: content,
			Status:  StatusSkipped,
			Reason:  fmt.Sprintf("CITATION.cff version %s is newer than %s", fields[keyVersion].value, version),
		}, nil
	}
	next := map[string]string{
		keyVersion: version,
		keyDate:    date,
		keyCommit:  commit,
	}
	if fields[keyVersion].value == version && fields[keyDate].value == date && fields[keyCommit].value == commit {
		return UpdateResult{
			Content: content,
			Status:  StatusUnchanged,
			Reason:  "version, date-released, and commit already match",
		}, nil
	}
	lines := append([][]byte(nil), parsed.lines...)
	for _, key := range []string{keyVersion, keyDate, keyCommit} {
		f := fields[key]
		if f.value == next[key] {
			continue
		}
		lines[f.idx] = formatLine(key, next[key], f.quote, f.comment, f.nl)
	}
	var out bytes.Buffer
	for _, line := range lines {
		out.Write(line)
	}
	return UpdateResult{Content: out.Bytes(), Status: StatusUpdated}, nil
}

func compareVersion(current, next string) (int, error) {
	c, err := parseVer(current)
	if err != nil {
		return 0, fmt.Errorf("CITATION.cff version %q is not MAJOR.MINOR.PATCH", current)
	}
	n, err := parseVer(next)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		if c[i] < n[i] {
			return -1, nil
		}
		if c[i] > n[i] {
			return 1, nil
		}
	}
	return 0, nil
}

func parseVer(s string) ([3]int, error) {
	if !versionRE.MatchString(s) {
		return [3]int{}, errors.New("not MAJOR.MINOR.PATCH")
	}
	parts := strings.Split(s, ".")
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, err
		}
		n[i] = v
	}
	return n, nil
}

type scalarLine struct {
	idx     int
	value   string
	quote   byte
	comment string
	nl      []byte
}

type parsedCitation struct {
	lines  [][]byte
	fields map[string]*scalarLine
}

func parseFile(content []byte) (parsedCitation, error) {
	lines := splitKeep(content)
	fields := map[string]*scalarLine{}
	for i, line := range lines {
		body, nl := trimNL(line)
		if len(body) == 0 || body[0] == ' ' || body[0] == '\t' || body[0] == '#' {
			continue
		}
		key, rest, ok := splitKey(body)
		if !ok {
			continue
		}
		if key != keyVersion && key != keyDate && key != keyCommit {
			continue
		}
		if _, dup := fields[key]; dup {
			return parsedCitation{}, fmt.Errorf("CITATION.cff has more than one top-level %s", key)
		}
		value, quote, comment, err := parseScalar(rest)
		if err != nil {
			return parsedCitation{}, fmt.Errorf("CITATION.cff %s: %w", key, err)
		}
		fields[key] = &scalarLine{
			idx:     i,
			value:   value,
			quote:   quote,
			comment: comment,
			nl:      nl,
		}
	}
	return parsedCitation{lines: lines, fields: fields}, nil
}

func splitKeep(b []byte) [][]byte {
	if len(b) == 0 {
		return nil
	}
	var lines [][]byte
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			lines = append(lines, append([]byte(nil), b...))
			break
		}
		lines = append(lines, append([]byte(nil), b[:i+1]...))
		b = b[i+1:]
	}
	return lines
}

func trimNL(line []byte) (body, nl []byte) {
	if bytes.HasSuffix(line, []byte("\r\n")) {
		return line[:len(line)-2], line[len(line)-2:]
	}
	if bytes.HasSuffix(line, []byte("\n")) {
		return line[:len(line)-1], line[len(line)-1:]
	}
	return line, nil
}

func splitKey(body []byte) (key, rest string, ok bool) {
	s := string(body)
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return "", "", false
	}
	key = s[:i]
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	for _, r := range key {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return "", "", false
		}
	}
	if key[0] >= '0' && key[0] <= '9' {
		return "", "", false
	}
	rest = s[i+1:]
	rest = strings.TrimLeft(rest, " \t")
	return key, rest, true
}

func parseScalar(rest string) (value string, quote byte, comment string, err error) {
	if strings.TrimSpace(rest) == "" {
		return "", 0, "", errors.New("missing value")
	}
	switch rest[0] {
	case '"', '\'':
		return parseQuoted(rest)
	case '[', '{', '|', '>', '&', '*', '!', '%', '@', '`':
		return "", 0, "", errors.New("unsupported value")
	}
	token := rest
	if i := strings.Index(rest, " #"); i >= 0 {
		token = rest[:i]
		comment = rest[i:]
	} else if i := strings.Index(rest, "\t#"); i >= 0 {
		token = rest[:i]
		comment = rest[i:]
	}
	token = strings.TrimRight(token, " \t")
	if token == "" || strings.ContainsAny(token, " \t#'\"") {
		return "", 0, "", errors.New("unsupported value")
	}
	return token, 0, comment, nil
}

func parseQuoted(rest string) (string, byte, string, error) {
	q := rest[0]
	end := strings.IndexByte(rest[1:], q)
	if end < 0 {
		return "", 0, "", errors.New("unterminated quote")
	}
	end++
	value := rest[1:end]
	if value == "" || strings.Contains(value, "\\") {
		return "", 0, "", errors.New("unsupported quoted value")
	}
	after := rest[end+1:]
	if after == "" {
		return value, q, "", nil
	}
	trimmed := strings.TrimLeft(after, " \t")
	if trimmed == "" {
		return value, q, "", nil
	}
	if trimmed[0] != '#' || (after[0] != ' ' && after[0] != '\t') {
		return "", 0, "", errors.New("unsupported quoted value")
	}
	return value, q, after, nil
}

func formatLine(key, value string, quote byte, comment string, nl []byte) []byte {
	var b bytes.Buffer
	b.WriteString(key)
	b.WriteString(": ")
	if quote != 0 {
		b.WriteByte(quote)
		b.WriteString(value)
		b.WriteByte(quote)
	} else {
		b.WriteString(value)
	}
	b.WriteString(comment)
	b.Write(nl)
	return b.Bytes()
}
