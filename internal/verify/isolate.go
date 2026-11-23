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

// nsConfig is what a criterion's namespace hides, keeps and lets it write.
// Inside, every mount is read-only (ns-init remounts each one) except:
// the tmpfs masks, the rw directories and the overlays.
type nsConfig struct {
	mask    []string       // directories covered with an empty, writable tmpfs
	rw      []string       // the criterion's own directories, bound back writable
	ro      []string       // directories the check needs (toolchain, PATH, harness), bound back read-only when under a mask
	overlay []overlayMount // trusted caches: read-only lower, writes go to a per-criterion upper
	hostNet bool           // keep the host network instead of loopback only
}

// overlayMount is an overlayfs at target: lower is never written.
type overlayMount struct{ target, lower, upper, work string }

// Isolation names how criteria were run: IsolationNamespace (Linux user,
// pid and mount namespaces, everything killed when the check exits) or
// IsolationNone.
const (
	IsolationNamespace = "pid-namespace"
	IsolationNone      = "none"
)

// IsolationAvailable reports whether checks can run in their own
// namespaces here, and why not.
func IsolationAvailable() (bool, string) { return isolationAvailable() }

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

// sandboxConfig is the namespace config for criterion c run with
// directories d.
//
// Masked (empty tmpfs): /tmp, /var/tmp, /run (and /var/run), /dev/shm, the
// verifier's TMPDIR, XDG_RUNTIME_DIR, real HOME and the verification's work
// directory (so the hidden .git, other criteria's output and the harness
// are not visible). Writable: the checkout and the criterion's own
// directory d.root (HOME, TMPDIR, GOPATH, GOCACHE, GOMODCACHE, GOTMPDIR),
// both bound back. Read-only: everything else, including the Go toolchain,
// every PATH entry and the harness directory (bound back where a mask
// covers them). Trusted caches (Options.GoCache, GoModCache) are the lower
// layer of an overlay whose upper layer is in d.root, so a check reads them
// and never changes them. A mask that a bound directory equals or contains
// cannot be kept covered; it is dropped and listed in unmasked.
func (r *run) sandboxConfig(c task.Criterion, command, goroot string, h *harness, d checkDirs) (cfg nsConfig, unmasked []string) {
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
	add := func(list *[]string, p string) {
		if p = resolve(p); p != "" && p != "/" && !seen[p] {
			seen[p] = true
			*list = append(*list, p)
		}
	}
	add(&cfg.rw, r.checkout)
	add(&cfg.rw, d.root)
	if h != nil {
		add(&cfg.ro, h.dir)
	}
	for _, p := range filepath.SplitList(r.opt.Path) {
		add(&cfg.ro, p)
	}
	if goroot != "" {
		add(&cfg.ro, goroot)
	} else if real := resolve(command); real != "" {
		add(&cfg.ro, filepath.Dir(filepath.Dir(real)))
	}
	for _, o := range d.overlays {
		if lower := resolve(o.lower); lower != "" {
			o.lower = lower
			cfg.overlay = append(cfg.overlay, o)
		}
	}
	bound := append(append([]string{}, cfg.rw...), cfg.ro...)
	maskSeen := map[string]bool{}
	for _, m := range []string{"/tmp", "/var/tmp", "/run", "/var/run", "/dev/shm", os.TempDir(), os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("HOME"), filepath.Dir(r.checkout)} {
		m = resolve(m)
		if m == "" || m == "/" || maskSeen[m] {
			continue
		}
		maskSeen[m] = true
		if fi, err := os.Stat(m); err != nil || !fi.IsDir() {
			continue
		}
		covered := false
		for _, k := range bound {
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
	for _, n := range r.opt.AllowNetwork {
		if n == c.Name {
			cfg.hostNet = true
		}
	}
	return cfg, unmasked
}
