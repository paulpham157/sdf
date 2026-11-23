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
	"regexp"
	"runtime"
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
//   - go test also gets -exec=<this binary> __sdf-verify-launch, so go
//     starts every test binary through the verifier's launcher (launch.go)
//     instead of directly. The launcher connects to a unix socket the
//     verifier listens on. The verifier accepts it only from a process
//     running this very executable whose parent is the criterion's own go
//     command (started by the namespace's pid 1, which reports its pid; or,
//     not isolated, the go the verifier started), one per package directory (its working
//     directory, which go sets). It then creates a socketpair bound to that
//     package and passes one end to the launcher (SCM_RIGHTS), which hands
//     it to the test binary as fd 3 and nothing else. Isolated, the test
//     binary runs in its own nested user, pid and mount namespaces where
//     every mount, including go's work directory and build cache, is
//     read-only, except a fresh TMPDIR and HOME.
//   - The hook reads a 32-byte key from fd 3, moves the channel to a high
//     descriptor (close-on-exec) and keeps it; it never dials anything. On
//     amd64 and arm64 the channel is sealed (seal.go): only the hook's own
//     write stub can use it, so code under test that reads the key (a
//     heap dump shows it) still cannot send a frame. Any connection to the verifier's socket that is
//     not a launcher, a second launcher for a package, or a report naming
//     another package is a violation.
//   - Each designated test function is renamed in the overlay and wrapped:
//     the wrapper runs it and, in a t.Cleanup registered first, reports
//     pass only if the function returned and the test neither failed nor
//     skipped. Reports are HMAC-SHA256'd with the key and numbered.
//   - The verifier requires a valid pass for every designated test, and no
//     invalid frame and no fail.
//
// Code under test therefore cannot obtain a channel of its own: a process
// it starts is not a child of go (CLONE_PARENT and clone3 are refused by
// the hook's seccomp filter; isolated, it is inside the test binary's own
// pid namespace), and it cannot replace another package's test binary or
// poison the build cache (read-only to it, when isolated).
//
// KNOWN GAP: the report function and the write stub are in the test
// binary, so code under test that calls them through its symbol table
// (atk_b1mem: debug/elf + debug/gosym + unsafe) or writes its own memory
// can still forge a pass; and without the seal (other architectures),
// reading the key is enough. The tripwire (tripwire.go)
// makes such code an ERROR when it is new; see docs/verify.md.

type harness struct {
	keyed                int // test binaries that got a key
	dir, overlay, socket string
	tag                  string // random suffix of the hook's names
	ln                   net.Listener
	expected             []string
	checkout             string
	self                 string // this executable, the launcher
	exec                 string // the -exec value for go test

	mu         sync.Mutex
	results    map[string][]string // test -> reports
	violations []string
	pids       map[int]bool
	pkgs       map[string]bool // package directories that got a channel
	wg         sync.WaitGroup

	// rootPid is the process the verifier started (pid 1 of the namespace,
	// or go itself when not isolated); set before any test binary runs.
	rootPid  int
	goNsPid  int // isolated: go's pid inside the namespace, as pid 1 reported it
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
func (r *run) newHarness(c task.Criterion, expected []string, goroot string, d checkDirs) (*harness, error) {
	testingDir := filepath.Join(goroot, "src", "testing")
	if fi, err := os.Stat(testingDir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("GOROOT %s has no src/testing", goroot)
	}
	// A short directory: unix socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "sdfh")
	if err != nil {
		return nil, err
	}
	h := &harness{dir: dir, tag: randHex(8), expected: expected, checkout: r.checkout,
		results: map[string][]string{}, pids: map[int]bool{}, pkgs: map[string]bool{}, started: make(chan struct{})}
	h.socket = filepath.Join(dir, "s")
	if h.self, err = launcherPath(); err != nil {
		return nil, err
	}
	replace := map[string]string{}
	add := func(target, content string) error {
		src := filepath.Join(dir, "f"+strconv.Itoa(len(replace))+filepath.Ext(target))
		if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
			return err
		}
		replace[target] = src
		return nil
	}
	hook, err := hookFiles(h.tag)
	if err != nil {
		return nil, err
	}
	for name, content := range hook {
		if err := add(filepath.Join(testingDir, name), content); err != nil {
			return nil, err
		}
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
	if h.exec, err = r.writeLaunchConfig(h, c, d); err != nil {
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

// hookFiles are the files the overlay adds to package testing, by base
// name: the hook, and on amd64 and arm64 its write stub (seal.go). Every
// name they declare, including imports, carries the run's random tag, so
// it cannot collide with testing's own and code under test cannot name it
// without knowing the tag. The hook's channel to the verifier is fd 3,
// which only the verifier's launcher gives it.
func hookFiles(tag string) (map[string]string, error) {
	return hookFilesFor(tag, runtime.GOOS, runtime.GOARCH)
}

func hookFilesFor(tag, goos, goarch string) (map[string]string, error) {
	src, err := hookSourceFor(tag, goos, goarch)
	if err != nil {
		return nil, err
	}
	files := map[string]string{"zz_sdfverify_" + tag + ".go": src}
	if goos == "linux" {
		if stub := sealStub(goarch); stub != "" {
			files["zz_sdfverify_"+tag+".s"] = tagNames(stub, tag)
		}
	}
	return files, nil
}

func tagNames(src, tag string) string {
	src = strings.ReplaceAll(src, "SdfVerifyTRun", "SdfVerify"+tag+"Run")
	return hookName.ReplaceAllString(src, "sdf"+tag+"_")
}

// hookName is the T_ prefix of the hook's names (not RLIMIT_NOFILE's).
var hookName = regexp.MustCompile(`\bT_`)

// hookCommon reports designated tests; T_write is per platform.
const hookCommon = `
var T_st struct {
	mu  T_sync.Mutex
	fd  int
	key []byte
	seq int
}

func T_die(msg string) {
	T_os.Stderr.WriteString("sdf verify hook: " + msg + "\n")
	T_os.Exit(3)
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
	if T_st.key == nil {
		return
	}
	T_st.seq++
	body := T_strconv.Itoa(T_st.seq) + " " + msg
	m := T_hmac.New(T_sha256.New, T_st.key)
	m.Write([]byte(body))
	b := []byte(T_hex.EncodeToString(m.Sum(nil)) + " " + body + "\n")
	for len(b) > 0 {
		n, ok := T_write1(T_st.fd, b)
		if !ok {
			return
		}
		b = b[n:]
	}
}
`

// hookSourceFor is the hook for a test binary of goos/goarch (the host's:
// a check's environment does not pass GOOS or GOARCH through).
//
// On Linux, in init, before any code of the module runs: the hook checks
// that fd 3 is a socket, moves it to a high odd descriptor, close-on-exec
// (so no child inherits it), reads the run's key, makes the process
// non-dumpable and, on amd64 and arm64, installs the seccomp filter
// (sealFilter): see seal.go for the seal, and below for the rest. If any
// of it fails the test binary exits 3.
//
// A non-dumpable process's /proc/<pid>/mem (and fd/, environ, ...) belong
// to root, so code under test cannot open /proc/self/mem to patch the
// testing package's code (atk_tw5), and a child cannot ptrace or open it
// either. The filter, applied to every thread with no_new_privs and
// inherited by children, also refuses (EPERM) prctl(PR_SET_DUMPABLE), so
// that cannot be undone; ptrace, process_vm_readv/writev (atk_pvm) and
// pidfd_getfd; clone with CLONE_PARENT and clone3 (ENOSYS, so callers fall
// back to clone), so nothing the test binary starts becomes a child of go
// (atk_cloneparent); and system calls of another ABI. Tests that run
// debuggers or strace fail, and so do sendmsg and io_uring (seal.go). None
// of this stops code that uses unsafe or assembly from reading or writing
// memory directly, or from calling the stub: the tripwire flags those.
func hookSourceFor(tag, goos, goarch string) (string, error) {
	if goos != "linux" {
		// The verifier cannot identify a peer without Linux
		// (peer_other.go); fail visibly instead.
		return tagNames(`package testing

import T_os "os"

func init() {
	T_os.Stderr.WriteString("sdf verify hook: the go test harness needs Linux\n")
	T_os.Exit(3)
}

// SdfVerifyTRun runs a designated test.
func SdfVerifyTRun(t *T, name string, f func(*T)) { f(t) }
`, tag), nil
	}
	prog, err := sealFilter(goarch)
	if err != nil {
		return "", err
	}
	src := `package testing

import (
	T_hmac "crypto/hmac"
	T_sha256 "crypto/sha256"
	T_hex "encoding/hex"
	T_os "os"
	T_strconv "strconv"
	T_sync "sync"
	T_syscall "syscall"
	T_unsafe "unsafe"
)
` + hookCommon + `
func init() {
	var st T_syscall.Stat_t
	if e := T_syscall.Fstat(3, &st); e != nil || st.Mode&T_syscall.S_IFMT != T_syscall.S_IFSOCK {
		T_die("no harness channel on fd 3 (not started by sdf verify's launcher)")
	}
	fd := T_moveFD()
	if e := T_syscall.SetNonblock(fd, false); e != nil {
		T_die("harness channel: " + e.Error())
	}
	key := make([]byte, 32)
	for n := 0; n < len(key); {
		m, e := T_syscall.Read(fd, key[n:])
		if e == T_syscall.EINTR {
			continue
		}
		if e != nil || m <= 0 {
			T_die("no key")
		}
		n += m
	}
	T_st.fd, T_st.key = fd, key
	if msg := T_harden(fd); msg != "" {
		T_die("hardening: " + msg)
	}
}

// T_moveFD moves the channel from fd 3 to a free high odd descriptor,
// close-on-exec.
func T_moveFD() int {
	top := 65535
	var lim T_syscall.Rlimit
	if T_syscall.Getrlimit(T_syscall.RLIMIT_NOFILE, &lim) == nil && lim.Cur <= uint64(top) {
		top = int(lim.Cur) - 1
	}
	if top%2 == 0 {
		top--
	}
	for fd := top; fd > 3; fd -= 2 {
		var st T_syscall.Stat_t
		if T_syscall.Fstat(fd, &st) != T_syscall.EBADF {
			continue
		}
		if e := T_syscall.Dup3(3, fd, T_syscall.O_CLOEXEC); e != nil {
			T_die("moving the harness channel: " + e.Error())
		}
		T_syscall.Close(3)
		return fd
	}
	T_die("no free descriptor for the harness channel")
	return -1
}

type T_sockFilter struct {
	code uint16
	jt   uint8
	jf   uint8
	k    uint32
}

func T_harden(fd int) string {
	const prSetDumpable, prGetDumpable, prSetNoNewPrivs = 4, 3, 38
	if _, _, e := T_syscall.RawSyscall(T_syscall.SYS_PRCTL, prSetDumpable, 0, 0); e != 0 {
		return "PR_SET_DUMPABLE: " + e.Error()
	}
	if msg := T_seal(fd); msg != "" {
		return msg
	}
	if r, _, _ := T_syscall.RawSyscall(T_syscall.SYS_PRCTL, prGetDumpable, 0, 0); r != 0 {
		return "still dumpable"
	}
	return ""
}
`
	if prog == nil {
		src += `
func T_seal(int) string { return "" }

func T_write1(fd int, b []byte) (int, bool) {
	n, e := T_syscall.Write(fd, b)
	if e == T_syscall.EINTR {
		return 0, true
	}
	return n, e == nil && n > 0
}

var _ = T_unsafe.Pointer(nil)
`
	} else {
		src += `
func T_rawwrite(fd uintptr, p *byte, n uintptr) uintptr

func T_rawwritePC() uintptr

func T_write1(fd int, b []byte) (int, bool) {
	r := int(T_rawwrite(uintptr(fd), &b[0], uintptr(len(b))))
	if r == -int(T_syscall.EINTR) {
		return 0, true
	}
	return r, r > 0
}

func T_seal(fd int) string {
	const prSetDumpable, prSetNoNewPrivs = 4, 38
	pc := uint64(T_rawwritePC())
	T_ipHi, T_ipLo, T_ipEnd := uint32(pc>>32), uint32(pc), uint32(pc+32)
	if uint32((pc+32)>>32) != T_ipHi {
		return "the write stub straddles a 4 GiB boundary"
	}
	T_fd := uint32(fd)
	filter := []T_sockFilter{
FILTER	}
	prog := struct {
		n uint16
		f *T_sockFilter
	}{uint16(len(filter)), &filter[0]}
	if _, _, e := T_syscall.RawSyscall6(T_syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		return "PR_SET_NO_NEW_PRIVS: " + e.Error()
	}
	// SECCOMP_SET_MODE_FILTER with SECCOMP_FILTER_FLAG_TSYNC: every thread.
	if r, _, e := T_syscall.RawSyscall(SYS_SECCOMP, 1, 1, uintptr(T_unsafe.Pointer(&prog))); e != 0 || r != 0 {
		return "seccomp: " + e.Error()
	}
	if _, _, e := T_syscall.RawSyscall(T_syscall.SYS_PRCTL, prSetDumpable, 1, 0); e != T_syscall.EPERM {
		return "the seccomp filter does not hold"
	}
	if _, e := T_syscall.Write(fd, nil); e != T_syscall.EPERM {
		return "the channel is not sealed"
	}
	var z byte
	if r := T_rawwrite(uintptr(fd), &z, 0); r != 0 {
		return "the write stub cannot write the channel"
	}
	return ""
}
`
		src = strings.Replace(src, "FILTER", renderBPF(prog), 1)
		src = strings.Replace(src, "SYS_SECCOMP", fmt.Sprint(sysnumsByArch[goarch].seccomp), 1)
	}
	return tagNames(src, tag), nil
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
	dir, err := checkLauncher(pid, h.self, h.rootPid, h.goNsPid, h.isolated)
	if err != nil {
		h.violate("connection from process %d: %v", pid, err)
		return
	}
	rel, err := filepath.Rel(h.checkout, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		h.violate("launcher %d runs in %s, outside the checkout", pid, dir)
		return
	}
	pkg, err := importPath(h.checkout, filepath.ToSlash(rel))
	if err != nil {
		h.violate("launcher %d: %s: %v", pid, rel, err)
		return
	}
	h.mu.Lock()
	dup := h.pids[pid] || h.pkgs[dir]
	h.pids[pid], h.pkgs[dir] = true, true
	h.mu.Unlock()
	if dup {
		h.violate("second launcher for process %d or package %s", pid, pkg)
		return
	}
	mine, theirs, err := socketPair()
	if err != nil {
		h.violate("socketpair: %v", err)
		return
	}
	defer mine.Close()
	err = sendFD(c, theirs)
	theirs.Close()
	if err != nil {
		h.violate("launcher %d: passing the channel: %v", pid, err)
		return
	}
	_ = c.Close()
	h.reports(mine, pkg)
}

// reports reads one test binary's reports from its channel, which is
// bound to package pkg: a report for any other package is a violation.
func (h *harness) reports(c net.Conn, pkg string) {
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
			h.violate("malformed report from package %s", pkg)
			return
		}
		body := strings.Join(f[1:], " ")
		m := hmac.New(sha256.New, key)
		m.Write([]byte(body))
		got, err := hex.DecodeString(f[0])
		n, nerr := strconv.Atoi(f[1])
		if err != nil || !hmac.Equal(got, m.Sum(nil)) || nerr != nil || n != seq+1 {
			h.violate("report from package %s failed authentication", pkg)
			return
		}
		seq = n
		switch f[2] {
		case "pass", "fail", "skip":
		default:
			h.violate("report from package %s has result %q", pkg, f[2])
			return
		}
		if f[3] != pkg {
			h.violate("test binary of package %s reported a test of package %s", pkg, f[3])
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
