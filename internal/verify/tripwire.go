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

// The tripwire. The go test harness (harness.go) cannot stop code in the
// test binary from reading its own memory or calling the hook's report
// function through the symbol table (atk_b1mem: debug/elf + debug/gosym +
// unsafe, about 40 lines). That is a known gap. What such code needs is
// recognisable, so a task with a `go test` criterion gives ERROR
// TAMPER_CAPABLE_CODE when a file added or changed since the base:
//
//   - is assembly, C, C++, Objective-C, Fortran, SWIG or a .syso object
//     (code outside Go's init order and type system);
//   - imports "unsafe" (which //go:linkname also needs), "C" (cgo),
//     "runtime/cgo", "plugin", or any "debug/..." package;
//   - calls raw system calls or ptrace through syscall or
//     golang.org/x/sys/unix (Syscall*, RawSyscall*, Mmap, Ptrace*);
//   - has a string literal containing "/proc/" (/proc/self/exe, mem,
//     maps, fd).
//
// It is a heuristic: a string can be built at run time, and a file that
// already had one of these at the base is flagged only when it changes.
// ERROR, not FAIL: legitimate code uses these too, and a person decides.
// Options.AllowTamperCapable records the findings as a warning instead.

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
		return nil // go cannot build it either
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
	for _, im := range f.Imports {
		ip, _ := strconv.Unquote(im.Path.Value)
		if tamperImports[ip] || strings.HasPrefix(ip, "debug/") {
			note("imports " + strconv.Quote(ip))
		}
		if ip == "syscall" || ip == "golang.org/x/sys/unix" {
			name := path.Base(ip)
			if im.Name != nil {
				name = im.Name.Name
			}
			sysNames[name] = true
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
			if id, ok := x.X.(*ast.Ident); ok && sysNames[id.Name] && (rawSyscalls[x.Sel.Name] || strings.HasPrefix(x.Sel.Name, "Ptrace")) {
				note("calls " + id.Name + "." + x.Sel.Name)
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if s, err := strconv.Unquote(x.Value); err == nil && strings.Contains(s, "/proc/") {
					note(`has a "/proc/" string`)
				}
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
