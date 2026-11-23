package verify

import (
	"bufio"
	"encoding/json"
	"go/ast"
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

// goTestArgv adds -json and -count=1 to a `go test` argv, right after
// "test", unless the task already sets them. -json lets the verifier see
// which tests passed; -count=1 keeps a cached result from standing in for
// a run.
func goTestArgv(argv []string) []string {
	if !task.IsGoTest(argv) {
		return argv
	}
	hasJSON, hasCount := false, false
	for _, a := range argv[2:] {
		switch {
		case a == "-json" || a == "--json" || a == "-json=true":
			hasJSON = true
		case a == "-count" || a == "--count" || strings.HasPrefix(a, "-count=") || strings.HasPrefix(a, "--count="):
			hasCount = true
		}
	}
	out := append([]string(nil), argv[:2]...)
	if !hasJSON {
		out = append(out, "-json")
	}
	if !hasCount {
		out = append(out, "-count=1")
	}
	return append(out, argv[2:]...)
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
	for _, p := range task.GoTestPaths(c.Argv, paths) {
		if !strings.HasSuffix(p, "_test.go") {
			continue
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

// passedTests reads `go test -json` output and returns the top-level tests
// that reported pass, as "<import path> <name>".
func passedTests(stdoutPath string) (map[string]bool, error) {
	f, err := os.Open(stdoutPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	passed := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var ev struct{ Action, Package, Test string }
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Test == "" || strings.Contains(ev.Test, "/") {
			continue
		}
		key := ev.Package + " " + ev.Test
		switch ev.Action {
		case "pass":
			passed[key] = true
		case "fail", "skip":
			delete(passed, key)
		}
	}
	return passed, sc.Err()
}
