package verify

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/paulpham157/sdf/internal/task"
)

// The go test harness. Test results are never read from the test binary's
// output, which the code under test can write. Instead, for a `go test`
// criterion:
//
//   - go test gets -overlay that adds the hook, under random names, to the
//     standard library's testing package (in GOROOT, so nothing is written
//     to the checkout), and a file importing "testing" to every package
//     directory in the checkout: the module, nested modules, replace
//     directories and vendor/. Go initializes a package's imports before
//     the package, so the hook's init runs before any init or package
//     variable in the repository's code.
//   - The hook connects to a unix socket the verifier listens on, whose
//     path is compiled into it (no environment variable to redirect), and
//     reads a 32-byte key. The verifier accepts one connection per test binary
//     process, and only from a process whose executable is a test binary
//     and whose parent is the criterion's own go command; a second
//     connection from the same process, or from anything else, is a
//     violation.
//   - Each designated test function is renamed in the overlay and wrapped:
//     the wrapper runs it and, in a t.Cleanup registered first, reports
//     pass only if the function returned and the test neither failed nor
//     skipped. Reports are HMAC-SHA256'd with the key and numbered.
//   - The verifier requires a valid pass for every designated test, and no
//     invalid frame and no fail.
//
// KNOWN GAP: the key and the report function are in the test binary's
// memory, so code under test that reads its own process's memory or calls
// the report function through its symbol table (atk_b1mem: debug/elf +
// debug/gosym + unsafe) can still forge a pass. The tripwire (tripwire.go)
// makes such code an ERROR when it is new; see docs/verify.md.

type harness struct {
	keyed                int // test binaries that got a key
	dir, overlay, socket string
	tag                  string // random suffix of the hook's names
	ln                   net.Listener
	expected             []string

	mu         sync.Mutex
	results    map[string][]string // test -> reports
	violations []string
	pids       map[int]bool
	wg         sync.WaitGroup

	// rootPid is the process the verifier started (pid 1 of the namespace,
	// or go itself when not isolated); set before any test binary runs.
	rootPid  int
	isolated bool
	started  chan struct{}
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// newHarness writes the overlay for criterion c and listens on the socket.
// goroot is the GOROOT of the go command the criterion runs.
func (r *run) newHarness(c task.Criterion, expected []string, goroot string) (*harness, error) {
	testingDir := filepath.Join(goroot, "src", "testing")
	if fi, err := os.Stat(testingDir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("GOROOT %s has no src/testing", goroot)
	}
	// A short directory: unix socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "sdfh")
	if err != nil {
		return nil, err
	}
	h := &harness{dir: dir, tag: randHex(8), expected: expected,
		results: map[string][]string{}, pids: map[int]bool{}, started: make(chan struct{})}
	h.socket = filepath.Join(dir, "s")
	replace := map[string]string{}
	add := func(target, content string) error {
		src := filepath.Join(dir, "f"+strconv.Itoa(len(replace))+".go")
		if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
			return err
		}
		replace[target] = src
		return nil
	}
	if err := add(filepath.Join(testingDir, "zz_sdfverify_"+h.tag+".go"), hookSource(h.tag, h.socket)); err != nil {
		return nil, err
	}
	ctx := build.Default
	ctx.CgoEnabled = false
	dirs, err := packageDirs(r.checkout)
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		p, err := ctx.ImportDir(d, build.ImportComment)
		if _, ok := err.(*build.NoGoError); ok {
			continue
		}
		if err != nil || p.Name == "" || p.Name == "documentation" {
			continue // not a package go would build; nothing to hook
		}
		name := "zz_sdfverify_" + h.tag + ".go"
		if err := add(filepath.Join(d, name), fmt.Sprintf("package %s\n\nimport _ \"testing\"\n", p.Name)); err != nil {
			return nil, err
		}
	}
	// Wrap the designated tests.
	for _, f := range r.spec.DesignatedFiles {
		if f.Mode == "120000" || !strings.HasSuffix(f.Path, "_test.go") || len(task.GoTestPaths(c.Argv, []string{f.Path})) == 0 {
			continue
		}
		full := filepath.Join(r.checkout, filepath.FromSlash(f.Path))
		if ok, _ := ctx.MatchFile(filepath.Dir(full), filepath.Base(full)); !ok {
			continue
		}
		pkg, err := importPath(r.checkout, path.Dir(f.Path))
		if err != nil {
			return nil, err
		}
		src, err := wrapTests(full, pkg, h.tag)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		if src != "" {
			if err := add(full, src); err != nil {
				return nil, err
			}
		}
	}
	b, err := json.Marshal(map[string]any{"Replace": replace})
	if err != nil {
		return nil, err
	}
	h.overlay = filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(h.overlay, b, 0o600); err != nil {
		return nil, err
	}
	if h.ln, err = net.Listen("unix", h.socket); err != nil {
		return nil, err
	}
	h.wg.Add(1)
	go h.accept()
	return h, nil
}

// packageDirs lists every directory under root that can hold a package
// go would build: everything but testdata, ".x" and "_x". Nested modules,
// replace directories and vendor/ are included, so their init functions
// also wait for the hook.
func packageDirs(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != root {
			n := d.Name()
			if n == "testdata" || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") {
				return filepath.SkipDir
			}
		}
		dirs = append(dirs, p)
		return nil
	})
	return dirs, err
}

// importPath is the import path of the package in dir (relative to the
// checkout, slash-separated): the nearest go.mod at or above it, within the
// checkout, plus the rest of the path.
func importPath(checkout, dir string) (string, error) {
	d := path.Clean(dir)
	rest := ""
	for {
		m, err := modulePath(filepath.Join(checkout, filepath.FromSlash(d), "go.mod"))
		if err == nil {
			if rest == "" {
				return m, nil
			}
			return m + "/" + rest, nil
		}
		if d == "." {
			return "", fmt.Errorf("no go.mod at or above %s", dir)
		}
		if rest == "" {
			rest = path.Base(d)
		} else {
			rest = path.Base(d) + "/" + rest
		}
		d = path.Dir(d)
	}
}

// wrapTests returns the source of a test file with each top-level Test
// function renamed and replaced by a wrapper that reports through the
// hook, or "" when the file has none. Everything else is byte for byte.
func wrapTests(file, pkg, tag string) (string, error) {
	alias := "sdf" + tag
	src, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return "", err
	}
	type rename struct {
		off       int
		name, typ string
	}
	var rs []rename
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !isTestName(fn.Name.Name) || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || fn.Type.TypeParams != nil {
			continue
		}
		p := fn.Type.Params.List[0]
		if len(p.Names) > 1 {
			continue
		}
		typ := string(src[fset.Position(p.Type.Pos()).Offset:fset.Position(p.Type.End()).Offset])
		rs = append(rs, rename{fset.Position(fn.Name.Pos()).Offset, fn.Name.Name, typ})
	}
	if len(rs) == 0 {
		return "", nil
	}
	var out strings.Builder
	pkgEnd := fset.Position(f.Name.End()).Offset
	last := 0
	write := func(upTo int) {
		out.Write(src[last:upTo])
		last = upTo
	}
	write(pkgEnd)
	fmt.Fprintf(&out, "\n\nimport %s \"testing\"\n", alias)
	sort.Slice(rs, func(i, j int) bool { return rs[i].off < rs[j].off })
	for _, x := range rs {
		write(x.off)
		out.WriteString(alias + "_" + x.name)
		last += len(x.name)
	}
	write(len(src))
	for _, x := range rs {
		fmt.Fprintf(&out, "\n\nfunc %s(sdfT %s) { %s.SdfVerify%sRun(sdfT, %q, %s_%s) }\n", x.name, x.typ, alias, tag, pkg+" "+x.name, alias, x.name)
	}
	return out.String(), nil
}

// hookSource is the file the overlay adds to package testing. Every name
// it declares, including its imports, carries the run's random tag, so it
// cannot collide with testing's own and code under test cannot name it
// without knowing the tag. The socket path is a constant in it.
func hookSource(tag, socket string) string {
	src := `package testing

import (
	T_hmac "crypto/hmac"
	T_sha256 "crypto/sha256"
	T_hex "encoding/hex"
	T_io "io"
	T_net "net"
	T_os "os"
	T_strconv "strconv"
	T_sync "sync"
)

var T_st struct {
	mu   T_sync.Mutex
	conn T_net.Conn
	key  []byte
	seq  int
}

func init() {
	// This file exists only in the verifier's overlay, so failing to reach
	// the verifier is fatal (and visible: go test shows a failing
	// package's output).
	die := func(msg string) {
		T_os.Stderr.WriteString("sdf verify hook: " + msg + "\n")
		T_os.Exit(3)
	}
	c, err := T_net.Dial("unix", SOCKET)
	if err != nil {
		die(err.Error())
	}
	key := make([]byte, 32)
	if _, err := T_io.ReadFull(c, key); err != nil {
		die("no key: " + err.Error())
	}
	T_st.conn, T_st.key = c, key
}

// SdfVerifyTRun runs a designated test and reports its result.
func SdfVerifyTRun(t *T, name string, f func(*T)) {
	returned := false
	t.Cleanup(func() {
		res := "fail"
		switch {
		case t.Skipped():
			res = "skip"
		case returned && !t.Failed():
			res = "pass"
		}
		T_send(res + " " + name)
	})
	f(t)
	returned = true
}

func T_send(msg string) {
	T_st.mu.Lock()
	defer T_st.mu.Unlock()
	if T_st.conn == nil {
		return
	}
	T_st.seq++
	body := T_strconv.Itoa(T_st.seq) + " " + msg
	m := T_hmac.New(T_sha256.New, T_st.key)
	m.Write([]byte(body))
	T_io.WriteString(T_st.conn, T_hex.EncodeToString(m.Sum(nil))+" "+body+"\n")
}
`
	src = strings.ReplaceAll(src, "SOCKET", strconv.Quote(socket))
	src = strings.ReplaceAll(src, "SdfVerifyTRun", "SdfVerify"+tag+"Run")
	return strings.ReplaceAll(src, "T_", "sdf"+tag+"_")
}

func (h *harness) violate(format string, args ...any) {
	h.mu.Lock()
	h.violations = append(h.violations, fmt.Sprintf(format, args...))
	h.mu.Unlock()
}

func (h *harness) accept() {
	defer h.wg.Done()
	for {
		c, err := h.ln.Accept()
		if err != nil {
			return
		}
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			defer c.Close()
			h.serve(c)
		}()
	}
}

func (h *harness) serve(c net.Conn) {
	<-h.started
	pid, err := peerPID(c)
	if err != nil {
		h.violate("connection with no peer process: %v", err)
		return
	}
	if err := checkTestBinary(pid, h.rootPid, h.isolated); err != nil {
		h.violate("connection from process %d: %v", pid, err)
		return
	}
	h.mu.Lock()
	dup := h.pids[pid]
	h.pids[pid] = true
	h.mu.Unlock()
	if dup {
		h.violate("second connection from test binary process %d", pid)
		return
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		h.violate("key: %v", err)
		return
	}
	if _, err := c.Write(key); err != nil {
		return
	}
	h.mu.Lock()
	h.keyed++
	h.mu.Unlock()
	sc := bufio.NewScanner(c)
	seq := 0
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 5 {
			h.violate("malformed report from process %d", pid)
			return
		}
		body := strings.Join(f[1:], " ")
		m := hmac.New(sha256.New, key)
		m.Write([]byte(body))
		got, err := hex.DecodeString(f[0])
		n, nerr := strconv.Atoi(f[1])
		if err != nil || !hmac.Equal(got, m.Sum(nil)) || nerr != nil || n != seq+1 {
			h.violate("report from process %d failed authentication", pid)
			return
		}
		seq = n
		switch f[2] {
		case "pass", "fail", "skip":
		default:
			h.violate("report from process %d has result %q", pid, f[2])
			return
		}
		name := f[3] + " " + f[4]
		h.mu.Lock()
		h.results[name] = append(h.results[name], f[2])
		h.mu.Unlock()
	}
}

// finish stops listening, waits for every connection to end (the test
// binaries are gone by now), and returns the designated tests that did not
// pass and the violations.
func (h *harness) finish() (notPassed, violations []string) {
	_ = h.ln.Close()
	select {
	case <-h.started:
	default:
		close(h.started)
	}
	h.wg.Wait()
	_ = os.RemoveAll(h.dir)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, w := range h.expected {
		rs := h.results[w]
		ok := len(rs) > 0
		for _, x := range rs {
			ok = ok && x == "pass"
		}
		if !ok {
			notPassed = append(notPassed, w)
		}
	}
	return notPassed, h.violations
}
