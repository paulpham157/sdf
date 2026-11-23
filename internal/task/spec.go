// Package task declares a task: an id, a title, and acceptance criteria,
// pinned to a base revision before any agent runs.
//
// A declared task is a Spec. Its canonical form is RFC 8785 JSON (see
// Canonical), and its hash is PayloadHash(KindTask, canonical), which is
// "sha256:" + hex(sha256("sdf.task/v1\n" + canonical)). The Spec names the
// files each criterion depends on (DesignatedFiles), each with its git blob
// id and sha256 at the base revision, so a later verify can tell whether a
// submitted revision changed the tests that judge it.
//
// Nothing here runs a check. sdf verify, later, reads a declared Spec.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/paulpham157/sdf/internal/ledger"
)

// Schema is the value of Spec.Schema and the hash domain.
const Schema = string(KindTask)

// Defaults for a criterion that does not set them.
const (
	DefaultTimeoutS = 600
	MaxTimeoutS     = 86400
)

var (
	idPattern  = regexp.MustCompile(ledger.IDPattern)
	shaPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// Criterion is one acceptance check. Argv is run without a shell. Designated
// is the set of path globs, relative to the repository root, whose files
// this check depends on (usually its tests). "**" matches any number of
// directories.
type Criterion struct {
	Name       string   `json:"name"`
	Argv       []string `json:"argv"`
	TimeoutS   int64    `json:"timeout_s"`
	ExpectExit int64    `json:"expect_exit"`
	Designated []string `json:"designated"`
}

// DesignatedFile is a file at the base revision that a criterion depends
// on: a match of a Designated glob, or a repository file named in Argv.
type DesignatedFile struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	GitBlob string `json:"git_blob"`
	SHA256  string `json:"sha256"`
}

// Spec is a declared task, version sdf.task/v1. Every field is part of the
// hash. The time a task was declared is not: the ledger records that.
type Spec struct {
	Schema          string           `json:"schema"`
	ID              string           `json:"id"`
	Title           string           `json:"title"`
	Objective       string           `json:"objective"`
	BaseSHA         string           `json:"base_sha"`
	BaseTree        string           `json:"base_tree"`
	Criteria        []Criterion      `json:"criteria"`
	DesignatedFiles []DesignatedFile `json:"designated_files"`
	SecretScan      bool             `json:"secret_scan"`
}

// ValidID reports whether id can name a task (or a criterion): valid UTF-8,
// no newline or other control character, and the id pattern, which is
// stricter: ASCII letters, digits, ".", "_", "-", at most 128 bytes, the
// same pattern as the ledger's ValidID. Case is preserved (a ULID stays
// valid); Declare refuses an id that differs from an existing one by case
// only. The first two rules are checked on their own too, so a looser
// pattern later cannot let them through.
func ValidID(id string) bool {
	if !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return idPattern.MatchString(id)
}

// Validate checks a Spec is complete and in normal order: criteria sorted
// by name, designated globs and files sorted by path, with no duplicates.
func (s Spec) Validate() error {
	if s.Schema != Schema {
		return fmt.Errorf("schema %q is not %q", s.Schema, Schema)
	}
	if !ValidID(s.ID) {
		return fmt.Errorf("id %q must match %s", s.ID, idPattern)
	}
	if s.Title == "" {
		return errors.New("title is required")
	}
	if !shaPattern.MatchString(s.BaseSHA) {
		return fmt.Errorf("base_sha %q is not a full object id", s.BaseSHA)
	}
	if !shaPattern.MatchString(s.BaseTree) {
		return fmt.Errorf("base_tree %q is not a full object id", s.BaseTree)
	}
	if len(s.Criteria) == 0 {
		return errors.New("at least one criterion is required")
	}
	for i, c := range s.Criteria {
		if err := c.validate(); err != nil {
			return err
		}
		if i > 0 && s.Criteria[i-1].Name >= c.Name {
			return fmt.Errorf("criteria are not sorted by unique name at %q", c.Name)
		}
		for j, g := range c.Designated {
			if err := validGlob(g); err != nil {
				return fmt.Errorf("criterion %q: %w", c.Name, err)
			}
			if j > 0 && c.Designated[j-1] >= g {
				return fmt.Errorf("criterion %q: designated globs are not sorted and unique", c.Name)
			}
		}
	}
	for i, f := range s.DesignatedFiles {
		if err := validRepoPath(f.Path); err != nil {
			return fmt.Errorf("designated file: %w", err)
		}
		if f.Path == "" || f.Mode == "" || !shaPattern.MatchString(f.GitBlob) || !sha256Hex.MatchString(f.SHA256) {
			return fmt.Errorf("designated file %q is incomplete", f.Path)
		}
		if i > 0 && s.DesignatedFiles[i-1].Path >= f.Path {
			return errors.New("designated files are not sorted by unique path")
		}
	}
	return nil
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// validRepoPath accepts a clean, relative slash path inside the repository,
// in valid UTF-8 with no control character.
func validRepoPath(p string) error {
	switch {
	case p == "" || !utf8.ValidString(p) || strings.IndexFunc(p, unicode.IsControl) >= 0:
		return fmt.Errorf("path %q is empty, not UTF-8, or has a control character", p)
	case path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../"):
		return fmt.Errorf("path %q is not a clean path inside the repository", p)
	}
	return nil
}

func (c Criterion) validate() error {
	if !ValidID(c.Name) {
		return fmt.Errorf("criterion name %q must match %s", c.Name, idPattern)
	}
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return fmt.Errorf("criterion %q: argv is empty", c.Name)
	}
	if c.TimeoutS < 1 || c.TimeoutS > MaxTimeoutS {
		return fmt.Errorf("criterion %q: timeout_s %d is outside 1..%d", c.Name, c.TimeoutS, MaxTimeoutS)
	}
	if c.ExpectExit < 0 || c.ExpectExit > 255 {
		return fmt.Errorf("criterion %q: expect_exit %d is outside 0..255", c.Name, c.ExpectExit)
	}
	return nil
}

func (s Spec) value() map[string]any {
	criteria := make([]any, len(s.Criteria))
	for i, c := range s.Criteria {
		criteria[i] = map[string]any{
			"name":        c.Name,
			"argv":        nonNil(c.Argv),
			"timeout_s":   c.TimeoutS,
			"expect_exit": c.ExpectExit,
			"designated":  nonNil(c.Designated),
		}
	}
	files := make([]any, len(s.DesignatedFiles))
	for i, f := range s.DesignatedFiles {
		files[i] = map[string]any{
			"path":     f.Path,
			"mode":     f.Mode,
			"git_blob": f.GitBlob,
			"sha256":   f.SHA256,
		}
	}
	return map[string]any{
		"schema":           s.Schema,
		"id":               s.ID,
		"title":            s.Title,
		"objective":        s.Objective,
		"base_sha":         s.BaseSHA,
		"base_tree":        s.BaseTree,
		"criteria":         criteria,
		"designated_files": files,
		"secret_scan":      s.SecretScan,
	}
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// Canonical validates s and returns its RFC 8785 bytes.
func (s Spec) Canonical() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return Canonical(s.value())
}

// Hash validates s and returns its canonical bytes and task hash.
func (s Spec) Hash() ([]byte, string, error) {
	b, err := s.Canonical()
	if err != nil {
		return nil, "", err
	}
	return b, PayloadHash(KindTask, b), nil
}

// Parse reads a stored task. The bytes must be exactly the canonical form,
// optionally followed by one newline. Anything else (reordered keys, extra
// fields, whitespace, floats) is refused, so a hand edit cannot keep the
// same id and silently change what the hash covers.
func Parse(data []byte) (Spec, string, error) {
	body := bytes.TrimSuffix(data, []byte("\n"))
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return Spec{}, "", fmt.Errorf("task file: %w", err)
	}
	if dec.More() {
		return Spec{}, "", errors.New("task file: trailing data")
	}
	canonical, hash, err := s.Hash()
	if err != nil {
		return Spec{}, "", fmt.Errorf("task file: %w", err)
	}
	if !bytes.Equal(canonical, body) {
		return Spec{}, "", errors.New("task file is not in canonical form")
	}
	return s, hash, nil
}

func sortCriteria(cs []Criterion) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
}
