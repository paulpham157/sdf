package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

// Input is what a caller declares. Build resolves it against a repository
// into a Spec.
type Input struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Objective string `json:"objective,omitempty"`
	// Base is any revision git can resolve to a commit. Empty means HEAD.
	Base       string           `json:"base,omitempty"`
	Criteria   []CriterionInput `json:"criteria"`
	SecretScan *bool            `json:"secret_scan,omitempty"`
}

// CriterionInput is one declared check. Zero TimeoutS means
// DefaultTimeoutS. Nil ExpectExit means 0.
type CriterionInput struct {
	Name       string   `json:"name"`
	Argv       []string `json:"argv"`
	TimeoutS   int64    `json:"timeout_s,omitempty"`
	ExpectExit *int64   `json:"expect_exit,omitempty"`
	Designated []string `json:"designated,omitempty"`
}

// ParseInput reads an Input from JSON. Unknown fields are refused.
func ParseInput(data []byte) (Input, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var in Input
	if err := dec.Decode(&in); err != nil {
		return Input{}, fmt.Errorf("task input: %w", err)
	}
	if dec.More() {
		return Input{}, errors.New("task input: trailing data")
	}
	return in, nil
}

// makefiles are the files make reads by default. When a criterion runs
// make, the one present at the base revision is designated.
var makefiles = []string{"GNUmakefile", "makefile", "Makefile"}

// Build resolves in against the git repository at repoDir. It reads the
// base revision's tree, expands each criterion's Designated globs (each
// glob has to match at least one file), adds repository files named in a
// criterion's argv, and hashes every designated file's content at the base.
// Nothing in the working tree is read, so uncommitted edits do not count.
func Build(ctx context.Context, repoDir string, in Input) (Spec, error) {
	if !ValidID(in.ID) {
		return Spec{}, fmt.Errorf("id %q must match %s", in.ID, idPattern)
	}
	if strings.TrimSpace(in.Title) == "" {
		return Spec{}, errors.New("title is required")
	}
	if len(in.Criteria) == 0 {
		return Spec{}, errors.New("at least one criterion is required")
	}
	base := in.Base
	if base == "" {
		base = "HEAD"
	}
	if strings.HasPrefix(base, "-") {
		return Spec{}, fmt.Errorf("base %q is not a revision", base)
	}
	g := gitRepo{dir: repoDir}
	sha, err := g.out(ctx, "rev-parse", "--verify", "--quiet", base+"^{commit}")
	if err != nil {
		return Spec{}, fmt.Errorf("base %q does not resolve to a commit: %w", base, err)
	}
	tree, err := g.out(ctx, "rev-parse", "--verify", "--quiet", sha+"^{tree}")
	if err != nil {
		return Spec{}, fmt.Errorf("base %s has no tree: %w", sha, err)
	}
	blobs, err := g.lsTree(ctx, sha)
	if err != nil {
		return Spec{}, err
	}
	paths := make([]string, 0, len(blobs))
	for p := range blobs {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	spec := Spec{
		Schema:     Schema,
		ID:         in.ID,
		Title:      in.Title,
		Objective:  in.Objective,
		BaseSHA:    sha,
		BaseTree:   tree,
		SecretScan: in.SecretScan == nil || *in.SecretScan,
	}
	seenName := map[string]bool{}
	designated := map[string]bool{}
	for _, ci := range in.Criteria {
		if seenName[ci.Name] {
			return Spec{}, fmt.Errorf("criterion %q is declared twice", ci.Name)
		}
		seenName[ci.Name] = true
		c := Criterion{
			Name:     ci.Name,
			Argv:     append([]string(nil), ci.Argv...),
			TimeoutS: ci.TimeoutS,
		}
		if c.TimeoutS == 0 {
			c.TimeoutS = DefaultTimeoutS
		}
		if ci.ExpectExit != nil {
			c.ExpectExit = *ci.ExpectExit
		}
		if err := c.validate(); err != nil {
			return Spec{}, err
		}
		globs := map[string]bool{}
		for _, raw := range ci.Designated {
			gl := normalizeGlob(raw)
			if err := validGlob(gl); err != nil {
				return Spec{}, fmt.Errorf("criterion %q: %w", c.Name, err)
			}
			matched := 0
			for _, p := range paths {
				if matchGlob(gl, p) {
					designated[p] = true
					matched++
				}
			}
			if matched == 0 {
				return Spec{}, fmt.Errorf("criterion %q: designated %q matches no file at %s", c.Name, gl, sha)
			}
			globs[gl] = true
		}
		for gl := range globs {
			c.Designated = append(c.Designated, gl)
		}
		sort.Strings(c.Designated)
		for _, p := range argvPaths(c.Argv, blobs) {
			designated[p] = true
		}
		spec.Criteria = append(spec.Criteria, c)
	}
	sortCriteria(spec.Criteria)

	for _, p := range paths {
		if !designated[p] {
			continue
		}
		b := blobs[p]
		if !utf8.ValidString(p) {
			return Spec{}, fmt.Errorf("designated path %q is not valid UTF-8", p)
		}
		content, err := g.blob(ctx, b.oid)
		if err != nil {
			return Spec{}, err
		}
		sum := sha256.Sum256(content)
		spec.DesignatedFiles = append(spec.DesignatedFiles, DesignatedFile{
			Path:    p,
			Mode:    b.mode,
			GitBlob: b.oid,
			SHA256:  hex.EncodeToString(sum[:]),
		})
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// argvPaths returns repository files named in argv: any argument that,
// cleaned, is a blob at the base, plus the default makefile when argv[0]
// is make. A path inside a shell string (sh -c "...") is not found.
func argvPaths(argv []string, blobs map[string]treeBlob) []string {
	var out []string
	for i, a := range argv {
		if a == "" || strings.HasPrefix(a, "-") || path.IsAbs(a) {
			continue
		}
		p := path.Clean(a)
		if _, ok := blobs[p]; ok {
			out = append(out, p)
		}
		if i == 0 && path.Base(p) == "make" {
			for _, m := range makefiles {
				if _, ok := blobs[m]; ok {
					out = append(out, m)
					break
				}
			}
		}
	}
	return out
}

func normalizeGlob(g string) string {
	g = strings.TrimPrefix(g, "./")
	return g
}

func validGlob(g string) error {
	if g == "" || path.IsAbs(g) || path.Clean(g) != g {
		return fmt.Errorf("designated %q must be a clean path relative to the repository root", g)
	}
	for _, seg := range strings.Split(g, "/") {
		if seg == ".." || seg == "." {
			return fmt.Errorf("designated %q must stay inside the repository", g)
		}
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("designated %q: %w", g, err)
		}
	}
	return nil
}

// matchGlob matches a slash path against a glob in which each segment is a
// path.Match pattern and "**" matches zero or more segments.
func matchGlob(glob, p string) bool {
	return matchSegs(strings.Split(glob, "/"), strings.Split(p, "/"))
}

func matchSegs(g, p []string) bool {
	for len(g) > 0 {
		if g[0] == "**" {
			for i := 0; i <= len(p); i++ {
				if matchSegs(g[1:], p[i:]) {
					return true
				}
			}
			return false
		}
		if len(p) == 0 {
			return false
		}
		if ok, _ := path.Match(g[0], p[0]); !ok {
			return false
		}
		g, p = g[1:], p[1:]
	}
	return len(p) == 0
}

type treeBlob struct {
	mode string
	oid  string
}

type gitRepo struct{ dir string }

func (g gitRepo) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("git %s: %w", args[0], err)
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return out, nil
}

func (g gitRepo) out(ctx context.Context, args ...string) (string, error) {
	b, err := g.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// lsTree returns every blob (regular file, executable, or symlink) at rev,
// keyed by path. Submodules are not blobs and are left out.
func (g gitRepo) lsTree(ctx context.Context, rev string) (map[string]treeBlob, error) {
	b, err := g.run(ctx, "ls-tree", "-r", "-z", "--full-tree", rev)
	if err != nil {
		return nil, err
	}
	out := map[string]treeBlob{}
	for _, rec := range bytes.Split(b, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, p, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("git ls-tree: unexpected line %q", rec)
		}
		f := strings.Fields(string(meta))
		if len(f) != 3 {
			return nil, fmt.Errorf("git ls-tree: unexpected line %q", rec)
		}
		if f[1] != "blob" {
			continue
		}
		out[string(p)] = treeBlob{mode: f[0], oid: f[2]}
	}
	return out, nil
}

func (g gitRepo) blob(ctx context.Context, oid string) ([]byte, error) {
	return g.run(ctx, "cat-file", "blob", oid)
}
