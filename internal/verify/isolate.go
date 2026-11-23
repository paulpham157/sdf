package verify

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/paulpham157/sdf/internal/task"
)

// nsInitArg, as os.Args[1], makes this binary (sdf, or a test binary that
// links this package) act as pid 1 of a criterion's namespace instead of
// running normally. See isolate_linux.go.
const nsInitArg = "__sdf-verify-ns-init"

func init() {
	if len(os.Args) > 1 && os.Args[1] == nsInitArg {
		os.Exit(nsInitMain(os.Args[2:]))
	}
}

// nsConfig is what a criterion's namespace hides and keeps.
type nsConfig struct {
	mask    []string // directories covered with an empty tmpfs
	keep    []string // directories (under a mask) bound back in
	hostNet bool     // keep the host network instead of loopback only
}

// Isolation names how criteria were run: IsolationNamespace (Linux user,
// pid and mount namespaces, everything killed when the check exits) or
// IsolationNone.
const (
	IsolationNamespace = "pid-namespace"
	IsolationNone      = "none"
)

var (
	probeMu    sync.Mutex
	probeDone  bool
	probeOK    bool
	probeError string
)

// isolationAvailable reports, once per process, whether a criterion can run
// in its own namespaces here.
var isolationAvailable = func() (bool, string) {
	probeMu.Lock()
	defer probeMu.Unlock()
	if !probeDone {
		probeOK, probeError = probeIsolation()
		probeDone = true
	}
	return probeOK, probeError
}

// sandboxConfig is the namespace config for criterion c: which host
// directories to cover and which of the verifier's own to bind back.
// Masked: /tmp, /var/tmp, /run (and /var/run), /dev/shm, the verifier's
// TMPDIR, XDG_RUNTIME_DIR and real HOME. Kept: the verification's work
// directory (checkout, HOME, GOPATH, GOTMPDIR, TMPDIR), the Go caches, the
// harness directory, every PATH entry and the Go toolchain. A mask that a
// kept directory equals or contains cannot be kept covered; it is dropped
// and listed in unmasked.
func (r *run) sandboxConfig(c task.Criterion, command, goroot string, h *harness) (cfg nsConfig, unmasked []string) {
	resolve := func(p string) string {
		if p == "" || !filepath.IsAbs(p) {
			return ""
		}
		if q, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Clean(q)
		}
		return ""
	}
	seen := map[string]bool{}
	var keep []string
	addKeep := func(p string) {
		if p = resolve(p); p != "" && p != "/" && !seen[p] {
			seen[p] = true
			keep = append(keep, p)
		}
	}
	addKeep(filepath.Dir(r.checkout))
	addKeep(r.dirs.gocache)
	addKeep(r.dirs.gomodcache)
	if h != nil {
		addKeep(h.dir)
	}
	for _, d := range filepath.SplitList(r.opt.Path) {
		addKeep(d)
	}
	if goroot != "" {
		addKeep(goroot)
	} else if real := resolve(command); real != "" {
		addKeep(filepath.Dir(filepath.Dir(real)))
	}
	maskSeen := map[string]bool{}
	for _, m := range []string{"/tmp", "/var/tmp", "/run", "/var/run", "/dev/shm", os.TempDir(), os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("HOME")} {
		m = resolve(m)
		if m == "" || m == "/" || maskSeen[m] {
			continue
		}
		maskSeen[m] = true
		if fi, err := os.Stat(m); err != nil || !fi.IsDir() {
			continue
		}
		covered := false
		for _, k := range keep {
			if k == m || strings.HasPrefix(m, k+"/") {
				covered = true
				break
			}
		}
		if covered {
			unmasked = append(unmasked, m)
			continue
		}
		cfg.mask = append(cfg.mask, m)
	}
	cfg.keep = keep
	for _, n := range r.opt.AllowNetwork {
		if n == c.Name {
			cfg.hostNet = true
		}
	}
	return cfg, unmasked
}
