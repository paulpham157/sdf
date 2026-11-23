package verify

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/paulpham157/sdf/internal/task"
)

// goTestArgv adds -count=1 (unless the task sets -count) and the harness
// overlay to a `go test` argv, right after "test", or after "-C dir", which
// go requires to be the first flag. -count=1 keeps a cached
// result from standing in for a run. With execProg it adds the verifier's
// launcher as -exec. It refuses -overlay, -exec and -toolexec from the task,
// which would replace the harness or put another process between go and
// the test binary.
func goTestArgv(argv []string, overlay, execProg string) ([]string, error) {
	if !task.IsGoTest(argv) {
		return argv, nil
	}
	chdir, rest, ok := task.GoChdir(argv)
	if !ok {
		return nil, fmt.Errorf("go test -C %s is outside the checkout", chdir)
	}
	head := argv[:len(argv)-len(rest)]
	hasCount := false
	for _, a := range rest {
		flag := strings.TrimLeft(a, "-")
		name, _, _ := strings.Cut(flag, "=")
		switch {
		case !strings.HasPrefix(a, "-"):
		case name == "count":
			hasCount = true
		case name == "overlay" || name == "exec" || name == "toolexec":
			return nil, fmt.Errorf("go test %s is not supported: it would bypass the verifier's test harness", a)
		}
	}
	out := append([]string(nil), head...)
	if !hasCount {
		out = append(out, "-count=1")
	}
	if overlay != "" {
		out = append(out, "-overlay="+overlay)
	}
	if execProg != "" {
		out = append(out, "-exec="+execProg)
	}
	return append(out, rest...), nil
}

// isGoTool reports whether argv runs `go test` or `go vet`.
func isGoTool(argv []string) bool {
	return len(argv) >= 2 && path.Base(argv[0]) == "go" && (argv[1] == "test" || argv[1] == "vet")
}

// goInput reports whether p is an input of a go test/vet criterion that has
// to match the base: its test files and testdata (task.GoTestPaths); a
// go.mod, go.sum, go.work or go.work.sum anywhere in the checkout (a nested
// one makes a directory its own module, or a workspace, with its own
// requirements and replace directives); and anything under vendor/ at the
// root or in the -C directory, which replaces the module cache (and
// go.sum's checks) when present.
func goInput(argv []string, p string) bool {
	if !isGoTool(argv) {
		return false
	}
	if isGoModFile(p) || strings.HasPrefix(p, "vendor/") {
		return true
	}
	if dir, _, ok := task.GoChdir(argv); ok && dir != "." && strings.HasPrefix(p, dir+"/vendor/") {
		return true
	}
	return len(task.GoTestPaths(argv, []string{p})) > 0
}

// designatedTests returns the top-level Test functions declared in the
// designated _test.go files a `go test` criterion covers, as
// "<import path> <name>". The files were already checked against the task,
// so they are read from the checkout.
func designatedTests(checkout string, spec task.Spec, c task.Criterion) ([]string, error) {
	var paths []string
	for _, f := range spec.DesignatedFiles {
		if f.Mode != "120000" {
			paths = append(paths, f.Path)
		}
	}
	var out []string
	ctx := build.Default
	ctx.CgoEnabled = false
	for _, p := range task.GoTestPaths(c.Argv, paths) {
		full := filepath.Join(checkout, filepath.FromSlash(p))
		if !strings.HasSuffix(p, "_test.go") {
			continue
		}
		if ok, _ := ctx.MatchFile(filepath.Dir(full), filepath.Base(full)); !ok {
			continue // not built here, so neither wrapped nor required
		}
		pkg, err := importPath(checkout, path.Dir(p))
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(checkout, filepath.FromSlash(p)), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if ok && fn.Recv == nil && isTestName(fn.Name.Name) && fn.Type.Params != nil && len(fn.Type.Params.List) == 1 {
				out = append(out, pkg+" "+fn.Name.Name)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// isTestName is go test's rule: "Test", or "Test" and a rune that is not a
// lower-case letter.
func isTestName(n string) bool {
	rest, ok := strings.CutPrefix(n, "Test")
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLower(r)
}

func modulePath(gomod string) (string, error) {
	b, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if i := strings.Index(l, "//"); i >= 0 {
			l = strings.TrimSpace(l[:i])
		}
		if rest, ok := strings.CutPrefix(l, "module"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == '"') {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	return "", os.ErrNotExist
}
