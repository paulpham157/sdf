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
// overlay to a `go test` argv, right after "test". -count=1 keeps a cached
// result from standing in for a run. It refuses -overlay and -exec, which
// would replace the harness or put another process between go and the test
// binary.
func goTestArgv(argv []string, overlay string) ([]string, error) {
	if !task.IsGoTest(argv) {
		return argv, nil
	}
	hasCount := false
	for _, a := range argv[2:] {
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
	out := append([]string(nil), argv[:2]...)
	if !hasCount {
		out = append(out, "-count=1")
	}
	if overlay != "" {
		out = append(out, "-overlay="+overlay)
	}
	return append(out, argv[2:]...), nil
}

// isGoTool reports whether argv runs `go test` or `go vet`.
func isGoTool(argv []string) bool {
	return len(argv) >= 2 && path.Base(argv[0]) == "go" && (argv[1] == "test" || argv[1] == "vet")
}

// goInput reports whether p is an input of a go test/vet criterion that has
// to match the base: its test files and testdata (task.GoTestPaths),
// go.mod, go.sum, go.work, go.work.sum, and anything under vendor/, which
// replaces the module cache (and go.sum's checks) when present.
func goInput(argv []string, p string) bool {
	if !isGoTool(argv) {
		return false
	}
	return strings.HasPrefix(p, "vendor/") || len(task.GoTestPaths(argv, []string{p})) > 0
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
	module := ""
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
		if module == "" {
			m, err := modulePath(filepath.Join(checkout, "go.mod"))
			if err != nil {
				return nil, err
			}
			module = m
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(checkout, filepath.FromSlash(p)), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		pkg := module
		if d := path.Dir(p); d != "." {
			pkg = module + "/" + d
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
