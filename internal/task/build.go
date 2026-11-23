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

// ParseInput reads an Input from JSON. Unknown fields are refused, and so
// are a repeated key and a key that is not all lowercase: encoding/json
// matches keys case-insensitively and keeps the last duplicate, so "ID" or
// a second "id" would otherwise be read silently.
func ParseInput(data []byte) (Input, error) {
	if err := checkKeys(data); err != nil {
		return Input{}, fmt.Errorf("task input: %w", err)
	}
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
		pinned := len(c.Designated)
		for _, p := range argvPaths(c.Argv, blobs) {
			designated[p] = true
			pinned++
		}
		for _, p := range goTestPaths(c.Argv, paths) {
			designated[p] = true
			pinned++
		}
		if pinned == 0 {
			return Spec{}, fmt.Errorf("criterion %q designates no files: name what it depends on (its tests, fixtures, and scripts) with a designated glob; otherwise a submission can weaken the check without verify noticing", c.Name)
		}
		spec.Criteria = append(spec.Criteria, c)
	}
	sortCriteria(spec.Criteria)

	// A designated symlink pins its target string; the file it points at
	// is designated too, so a changed target file is caught. A link out of
	// the repository, to a directory, or to nothing is refused: verify
	// never follows it.
	queue := make([]string, 0, len(designated))
	for p := range designated {
		queue = append(queue, p)
	}
	for hops := 0; len(queue) > 0; hops++ {
		if hops > 10000 {
			return Spec{}, errors.New("too many symlinks among designated files")
		}
		p := queue[0]
		queue = queue[1:]
		b := blobs[p]
		if b.mode != "120000" {
			continue
		}
		target, err := g.blob(ctx, b.oid)
		if err != nil {
			return Spec{}, err
		}
		resolved, err := linkTarget(p, string(target))
		if err != nil {
			return Spec{}, err
		}
		if _, ok := blobs[resolved]; !ok {
			return Spec{}, fmt.Errorf("designated symlink %s points to %s, which is not a file at %s", p, resolved, sha)
		}
		if !designated[resolved] {
			designated[resolved] = true
			queue = append(queue, resolved)
		}
	}

	for _, p := range paths {
		if !designated[p] {
			continue
		}
		b := blobs[p]
		if err := validRepoPath(p); err != nil {
			return Spec{}, fmt.Errorf("designated %w", err)
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

// MatchGlob reports whether the slash path p matches a designated glob.
func MatchGlob(glob, p string) bool { return matchGlob(glob, p) }

// checkKeys walks every JSON object in data and refuses a key that repeats
// or is not lowercase.
func checkKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				k, _ := kt.(string)
				if k != strings.ToLower(k) {
					return fmt.Errorf("key %q is not lowercase", k)
				}
				if seen[k] {
					return fmt.Errorf("key %q repeats", k)
				}
				seen[k] = true
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		}
		_, err = dec.Token() // closing delimiter
		return err
	}
	return walk()
}

// RepoRoot returns the top level of the git work tree containing dir.
func RepoRoot(ctx context.Context, dir string) (string, error) {
	return gitRepo{dir: dir}.out(ctx, "rev-parse", "--show-toplevel")
}

// linkTarget resolves a symlink at repository path p with the given target
// to a repository path, or refuses a target outside the repository.
func linkTarget(p, target string) (string, error) {
	if target == "" || path.IsAbs(target) || strings.Contains(target, "\\") || (len(target) > 1 && target[1] == ':') {
		return "", fmt.Errorf("designated symlink %s points outside the repository (%q)", p, target)
	}
	r := path.Join(path.Dir(p), target)
	if r == "." || r == ".." || strings.HasPrefix(r, "../") {
		return "", fmt.Errorf("designated symlink %s points outside the repository (%q)", p, target)
	}
	return r, nil
}

// GoTestPaths returns, of paths, the ones a `go test` or `go vet` argv
// depends on beyond the code under test: every _test.go file and every file
// under a testdata directory in the packages named by relative patterns
// (".", "./x", "./x/..."; none means "."), plus go.mod, go.sum, go.work and
// go.work.sum at the repository root. An import-path pattern is not
// resolved. Build designates these; verify also refuses a new or changed one
// that the task did not designate.
func GoTestPaths(argv []string, paths []string) []string { return goTestPaths(argv, paths) }

// IsGoTest reports whether argv runs `go test`.
func IsGoTest(argv []string) bool {
	return len(argv) >= 2 && path.Base(argv[0]) == "go" && argv[1] == "test"
}

// GoChdir returns the directory a `go test`/`go vet` argv runs in, relative
// to the checkout: the -C flag's value ("-C dir" or "-C=dir", which go
// accepts only as the first flag), cleaned, or "." without one. ok is false
// for a -C outside the checkout. rest is the argv after the -C flag.
func GoChdir(argv []string) (dir string, rest []string, ok bool) {
	if len(argv) < 3 {
		return ".", nil, true
	}
	a := argv[2]
	switch {
	case (a == "-C" || a == "--C") && len(argv) > 3:
		dir, rest = argv[3], argv[4:]
	case strings.HasPrefix(a, "-C=") || strings.HasPrefix(a, "--C="):
		_, dir, _ = strings.Cut(a, "=")
		rest = argv[3:]
	default:
		return ".", argv[2:], true
	}
	dir = path.Clean(dir)
	if path.IsAbs(dir) || dir == ".." || strings.HasPrefix(dir, "../") {
		return dir, rest, false
	}
	return dir, rest, true
}

func goTestPaths(argv []string, paths []string) []string {
	if len(argv) < 2 || path.Base(argv[0]) != "go" || (argv[1] != "test" && argv[1] != "vet") {
		return nil
	}
	type pkg struct {
		dir       string
		recursive bool
	}
	chdir, args, ok := GoChdir(argv)
	if !ok {
		return nil
	}
	var pkgs []pkg
	for _, a := range args {
		if a != "." && a != "..." && !strings.HasPrefix(a, "./") {
			continue
		}
		rec := a == "..." || strings.HasSuffix(a, "/...")
		d := path.Clean(strings.TrimSuffix(strings.TrimSuffix(a, "..."), "/"))
		if d == "" {
			d = "."
		}
		d = path.Join(chdir, d)
		if d == ".." || strings.HasPrefix(d, "../") {
			continue
		}
		pkgs = append(pkgs, pkg{d, rec})
	}
	if len(pkgs) == 0 {
		pkgs = []pkg{{chdir, false}}
	}
	var out []string
	for _, p := range paths {
		dir, name := path.Split(p)
		dir = path.Clean(dir)
		if (name == "go.mod" || name == "go.sum" || name == "go.work" || name == "go.work.sum") && (dir == "." || dir == chdir) {
			out = append(out, p)
			continue
		}
		for _, k := range pkgs {
			rel := p
			if k.dir != "." {
				var ok bool
				if rel, ok = strings.CutPrefix(p, k.dir+"/"); !ok {
					continue
				}
			}
			dir, file := path.Split(rel)
			dir = strings.TrimSuffix(dir, "/")
			inTestdata := dir == "testdata" || strings.HasPrefix(dir, "testdata/") ||
				(k.recursive && (strings.Contains("/"+dir+"/", "/testdata/")))
			direct := dir == "" || k.recursive
			if (direct && strings.HasSuffix(file, "_test.go")) || inTestdata {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// Interpreters whose argv files can source or import others that are not
// designated. See Warnings.
var interpreters = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "python": true, "python3": true, "node": true, "ruby": true, "perl": true}

// Warnings returns notes on a Spec's coverage that do not refuse it: a
// criterion run by an interpreter or make with no designated glob pins only
// the files named in its argv (or the makefile), not what they source,
// import or call; an inline -c script pins nothing it names.
func Warnings(s Spec) []string {
	var w []string
	// A designated test file no `go test` criterion runs gets no harness:
	// a script that runs go test reports only its exit code, which code
	// under test can fake (an init that exits 0).
	for _, f := range s.DesignatedFiles {
		if !strings.HasSuffix(f.Path, "_test.go") {
			continue
		}
		covered := false
		for _, c := range s.Criteria {
			if IsGoTest(c.Argv) && len(GoTestPaths(c.Argv, []string{f.Path})) > 0 {
				covered = true
				break
			}
		}
		if !covered {
			w = append(w, fmt.Sprintf("designated test file %s is not run by any `go test` criterion, so its results are not checked by the verifier's harness (a script running go test reports only an exit code, which code under test can fake); add a `go test` criterion, with -C <dir> for a nested module", f.Path))
		}
	}
	for _, c := range s.Criteria {
		base := path.Base(c.Argv[0])
		if len(c.Designated) > 0 {
			continue
		}
		switch {
		case interpreters[base] && hasArg(c.Argv[1:], "-c"):
			w = append(w, fmt.Sprintf("criterion %q runs an inline %s -c script; files it names are not pinned, so designate them", c.Name, base))
		case interpreters[base]:
			w = append(w, fmt.Sprintf("criterion %q pins only the files named in its argv; anything they source or import is not pinned, so designate it", c.Name))
		case base == "make" || base == "gmake":
			w = append(w, fmt.Sprintf("criterion %q pins only the makefile; scripts and files its recipes run are not pinned, so designate them", c.Name))
		}
	}
	return w
}

func hasArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}
