package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/paulpham157/sdf/internal/task"
)

// launchArg, as os.Args[1], makes this binary act as the go test -exec
// launcher: go runs "<this binary> __sdf-verify-launch CONFIG pkg.test
// args..." in the package directory. See launch_linux.go.
const launchArg = "__sdf-verify-launch"

// launchConfig is what a launcher needs; it holds no secret (code under
// test can read it, and running a launcher itself gets it nothing).
type launchConfig struct {
	Socket   string   `json:"socket"`
	Isolated bool     `json:"isolated"`
	Mask     []string `json:"mask"` // fresh tmpfs in the test binary's namespace
	RW       []string `json:"rw"`   // bound back writable there
}

// launcherPath is this executable, resolved.
func launcherPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(self)
}

// writeLaunchConfig writes the launcher's config into the harness
// directory and returns go test's -exec value.
func (r *run) writeLaunchConfig(h *harness, c task.Criterion, d checkDirs) (string, error) {
	cfg := launchConfig{Socket: h.socket, Isolated: r.isolation == IsolationNamespace}
	// The test binary's namespace: everything read-only (go's work
	// directory, the build and module caches, the toolchain, the
	// checkout for go test) but a new TMPDIR and HOME.
	cfg.Mask = []string{d.tmp, d.home}
	if contains(r.opt.WritableCheckout, c.Name) {
		cfg.RW = []string{r.checkout}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	p := filepath.Join(h.dir, "launch.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", err
	}
	return shellWords(h.self, launchArg, p), nil
}

// shellWords joins words for go's -exec flag, which splits on spaces and
// honors single and double quotes.
func shellWords(words ...string) string {
	out := make([]string, len(words))
	for i, w := range words {
		if strings.ContainsAny(w, " \t\n'\"") {
			if strings.Contains(w, "'") {
				w = `"` + w + `"`
			} else {
				w = "'" + w + "'"
			}
		}
		out[i] = w
	}
	return strings.Join(out, " ")
}

func readLaunchConfig(p string) (launchConfig, error) {
	var cfg launchConfig
	b, err := os.ReadFile(p)
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(b, &cfg)
}
