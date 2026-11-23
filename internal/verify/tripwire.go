package verify

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/paulpham157/sdf/internal/task"
)

// The tripwire. The go test harness (harness.go) resists naive cheating
// only; it is not a security boundary against code in the test binary,
// which can read its own memory or call the hook's report function
// through the symbol table (atk_b1mem: debug/elf + debug/gosym + unsafe,
// about 40 lines). That is a known gap. The hook makes the test process
// non-dumpable (so /proc/self/mem cannot be opened: atk_tw5), but code with
// unsafe or assembly can still write memory directly. What such code
// usually needs is recognisable, so a task with a `go test` criterion gives
// ERROR TAMPER_CAPABLE_CODE when a file added or changed since the base:
//
//   - is assembly, C, C++, Objective-C, Fortran, SWIG or a .syso object
//     (code outside Go's init order and type system);
//   - is a .go file go/parser cannot read (the tripwire cannot check it);
//   - imports "unsafe" (which //go:linkname also needs), "C" (cgo),
//     "runtime/cgo", "plugin", or any "debug/..." package, or dot-imports
//     syscall, golang.org/x/sys/unix, reflect or runtime;
//   - uses raw system calls or ptrace through syscall or
//     golang.org/x/sys/unix (Syscall*, RawSyscall*, Mmap, Mprotect,
//     ProcessVM*, Ptrace*), called or not;
//   - uses runtime.FuncForPC, or a reflect Pointer/UnsafePointer/UnsafeAddr
//     method (code addresses);
//   - has a string constant, after folding concatenations of literals, that
//     contains "/proc" or "proc/" or is "proc".
//
// It catches known patterns only: a string can be built at run time, and a
// file that already had one of these at the base is flagged only when it
// changes. ERROR, not FAIL: legitimate code uses these too, and a person
// decides. Options.AllowTamperCapable records the findings as a warning
// instead.

var tamperExts = map[string]bool{
	".s": true, ".S": true, ".sx": true, ".syso": true,
	".c": true, ".cc": true, ".cpp": true, ".cxx": true, ".h": true, ".hh": true, ".hpp": true, ".hxx": true,
	".m": true, ".f": true, ".F": true, ".for": true, ".f90": true, ".swig": true, ".swigcxx": true,
}

var tamperImports = map[string]bool{"unsafe": true, "C": true, "runtime/cgo": true, "plugin": true}

var rawSyscalls = map[string]bool{
	"Syscall": true, "Syscall6": true, "Syscall9": true, "RawSyscall": true, "RawSyscall6": true,
	"RawSyscallNoError": true, "Mmap": true, "Mprotect": true, "ProcessVMReadv": true, "ProcessVMWritev": true,
}

var dotRisky = map[string]bool{"syscall": true, "golang.org/x/sys/unix": true, "reflect": true, "runtime": true, "unsafe": true}

var reflectPointers = map[string]bool{"Pointer": true, "UnsafePointer": true, "UnsafeAddr": true}

// foldString is the value of a string literal or a + of them.
func foldString(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return foldString(x.X)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		a, ok1 := foldString(x.X)
		b, ok2 := foldString(x.Y)
		return a + b, ok1 && ok2
	}
	return "", false
}

func procString(s string) bool {
	return s == "proc" || strings.Contains(s, "/proc") || strings.Contains(s, "proc/")
}

// tamperFindings returns "path: why" for one file's content.
func tamperFindings(p string, src []byte) []string {
	if tamperExts[path.Ext(p)] {
		return []string{p + ": assembly, C or object file"}
	}
	if !strings.HasSuffix(p, ".go") {
		return nil
	}
	f, err := parser.ParseFile(token.NewFileSet(), p, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		// go may still build it (a newer syntax than this verifier's
		// parser, or a file a build tag excludes); unread is unchecked.
		return []string{p + ": cannot be parsed, so the tripwire cannot check it"}
	}
	seen := map[string]bool{}
	var out []string
	note := func(why string) {
		if !seen[why] {
			seen[why] = true
			out = append(out, p+": "+why)
		}
	}
	sysNames := map[string]bool{}
	runtimeName, reflectImported := "", false
	for _, im := range f.Imports {
		ip, _ := strconv.Unquote(im.Path.Value)
		if tamperImports[ip] || strings.HasPrefix(ip, "debug/") {
			note("imports " + strconv.Quote(ip))
		}
		if im.Name != nil && im.Name.Name == "." && dotRisky[ip] {
			note("dot-imports " + strconv.Quote(ip))
		}
		local := path.Base(ip)
		if im.Name != nil {
			local = im.Name.Name
		}
		switch ip {
		case "syscall", "golang.org/x/sys/unix":
			sysNames[local] = true
		case "runtime":
			runtimeName = local
		case "reflect":
			reflectImported = true
		}
	}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:linkname") {
				note("uses //go:linkname")
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			switch {
			case ok && sysNames[id.Name] && (rawSyscalls[x.Sel.Name] || strings.HasPrefix(x.Sel.Name, "Ptrace")):
				note("uses " + id.Name + "." + x.Sel.Name)
			case ok && runtimeName != "" && id.Name == runtimeName && x.Sel.Name == "FuncForPC":
				note("uses runtime.FuncForPC")
			case reflectImported && reflectPointers[x.Sel.Name]:
				note("uses a reflect ." + x.Sel.Name + " method")
			}
		case *ast.BinaryExpr, *ast.BasicLit:
			if s, ok := foldString(x.(ast.Expr)); ok && procString(s) {
				note(`has a "/proc" string`)
				return false
			}
		}
		return true
	})
	return out
}

// checkTamperCapable runs the tripwire over files added or changed since
// the base, when a criterion runs go test.
func (r *run) checkTamperCapable(at, atBase map[string]treeEntry) {
	goTest := false
	for _, c := range r.spec.Criteria {
		goTest = goTest || task.IsGoTest(c.Argv)
	}
	if !goTest {
		return
	}
	var paths []string
	for p, e := range at {
		if e.mode == "160000" || e.mode == "120000" {
			continue
		}
		if b, ok := atBase[p]; ok && b.oid == e.oid {
			continue
		}
		if tamperExts[path.Ext(p)] || strings.HasSuffix(p, ".go") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	var findings []string
	for _, p := range paths {
		src, err := git(r.ctx, r.checkout, "cat-file", "blob", at[p].oid)
		if err != nil {
			id := r.addEvidence("tamper_tripwire", "", map[string]any{"status": "error", "error": err.Error()})
			r.reason(CodeInfraError, id, "tripwire: %v", err)
			return
		}
		findings = append(findings, tamperFindings(p, src)...)
	}
	if len(findings) == 0 {
		r.addEvidence("tamper_tripwire", "", map[string]any{"status": "pass", "files_checked": int64(len(paths)), "findings": []string{}})
		return
	}
	st := "error"
	if r.opt.AllowTamperCapable {
		st = "warn"
	}
	id := r.addEvidence("tamper_tripwire", "", map[string]any{"status": st, "files_checked": int64(len(paths)), "findings": findings, "allowed": r.opt.AllowTamperCapable})
	msg := "new or changed code could forge the go test harness's reports (known gap): " + strings.Join(findings, "; ")
	if r.opt.AllowTamperCapable {
		r.warnings = append(r.warnings, msg+" (allowed by --allow-tamper-capable)")
		return
	}
	r.reason(CodeTamperCapable, id, "%s; review it, then --allow-tamper-capable if it is legitimate", msg)
}
