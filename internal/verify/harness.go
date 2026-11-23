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
//   - go test gets -overlay with a hook package under a random name, and a
//     file in every package of the module that imports it. Go initializes
//     a package's imports before the package, so the hook's init runs
//     before any init in the module's code.
//   - The hook connects to a unix socket the verifier listens on and reads
//     a 32-byte key. The verifier accepts one connection per test binary
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
// The key is in the test binary's memory, so code under test that reads
// its own process's memory, or patches the testing package, can still
// forge a report. That needs code written against this harness; see
// docs/verify.md.

// hookEnv names the variable holding the socket path.
const hookEnv = "SDF_VERIFY_HOOK"

type harness struct {
	dir, overlay, socket string
	hookPkg              string
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
func (r *run) newHarness(c task.Criterion, expected []string) (*harness, error) {
	module, err := modulePath(filepath.Join(r.checkout, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("go.mod: %w", err)
	}
	// A short directory: unix socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "sdfh")
	if err != nil {
		return nil, err
	}
	h := &harness{dir: dir, hookPkg: "z" + randHex(8), expected: expected,
		results: map[string][]string{}, pids: map[int]bool{}, started: make(chan struct{})}
	replace := map[string]string{}
	add := func(target, content string) error {
		src := filepath.Join(dir, "f"+strconv.Itoa(len(replace))+".go")
		if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
			return err
		}
		replace[target] = src
		return nil
	}
	hookImport := module + "/" + h.hookPkg
	// go vet chdirs into a package's directory, so the hook's has to exist.
	hookDir := filepath.Join(r.checkout, h.hookPkg)
	if err := os.Mkdir(hookDir, 0o755); err != nil {
		return nil, err
	}
	if err := add(filepath.Join(hookDir, "hook.go"), hookSource(h.hookPkg)); err != nil {
		return nil, err
	}
	ctx := build.Default
	ctx.CgoEnabled = false
	dirs, err := modulePackageDirs(r.checkout)
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		p, err := ctx.ImportDir(d, build.ImportComment)
		if _, ok := err.(*build.NoGoError); ok {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("package in %s: %w", d, err)
		}
		name := "zz_sdfverify_" + randHex(4) + ".go"
		if err := add(filepath.Join(d, name), fmt.Sprintf("package %s\n\nimport _ %q\n", p.Name, hookImport)); err != nil {
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
		pkg := module
		if d := path.Dir(f.Path); d != "." {
			pkg = module + "/" + d
		}
		src, err := wrapTests(full, pkg, hookImport, h.hookPkg)
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
	h.socket = filepath.Join(dir, "s")
	if h.ln, err = net.Listen("unix", h.socket); err != nil {
		return nil, err
	}
	h.wg.Add(1)
	go h.accept()
	return h, nil
}

// modulePackageDirs lists the directories of the module rooted at root
// that can hold a package: not vendor, testdata, ".x" or "_x", and not
// inside a nested module.
func modulePackageDirs(root string) ([]string, error) {
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
			if n == "vendor" || n == "testdata" || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") {
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
		}
		dirs = append(dirs, p)
		return nil
	})
	return dirs, err
}

// wrapTests returns the source of a test file with each top-level Test
// function renamed and replaced by a wrapper that reports through the
// hook, or "" when the file has none. Everything else is byte for byte.
func wrapTests(file, pkg, hookImport, alias string) (string, error) {
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
	fmt.Fprintf(&out, "\n\nimport %s %q\n", alias, hookImport)
	sort.Slice(rs, func(i, j int) bool { return rs[i].off < rs[j].off })
	for _, x := range rs {
		write(x.off)
		out.WriteString(alias + "_" + x.name)
		last += len(x.name)
	}
	write(len(src))
	for _, x := range rs {
		fmt.Fprintf(&out, "\n\nfunc %s(sdfT %s) { %s.Run(sdfT, %q, %s_%s) }\n", x.name, x.typ, alias, pkg+" "+x.name, alias, x.name)
	}
	return out.String(), nil
}

// hookSource is the hook package. It imports only the standard library.
func hookSource(pkg string) string {
	return `package ` + pkg + `

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
)

var st struct {
	mu   sync.Mutex
	conn net.Conn
	key  []byte
	seq  int
}

func init() {
	p := os.Getenv("` + hookEnv + `")
	if p == "" {
		return
	}
	c, err := net.Dial("unix", p)
	if err != nil {
		return
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(c, key); err != nil {
		c.Close()
		return
	}
	st.conn, st.key = c, key
}

// Run runs a designated test and reports its result to the verifier.
func Run(t *testing.T, name string, f func(*testing.T)) {
	returned := false
	t.Cleanup(func() {
		res := "fail"
		switch {
		case t.Skipped():
			res = "skip"
		case returned && !t.Failed():
			res = "pass"
		}
		send(res + " " + name)
	})
	f(t)
	returned = true
}

func send(msg string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.conn == nil {
		return
	}
	st.seq++
	body := strconv.Itoa(st.seq) + " " + msg
	m := hmac.New(sha256.New, st.key)
	m.Write([]byte(body))
	io.WriteString(st.conn, hex.EncodeToString(m.Sum(nil))+" "+body+"\n")
}
`
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
