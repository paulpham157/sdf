package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func nullDevice() string {
	if runtime.GOOS == "windows" {
		return "NUL"
	}
	return "/dev/null"
}

// cleanGitEnv is the environment without any GIT_ variable, so a variable
// the caller set (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, ...) cannot point
// the verifier's git at another repository.
func cleanGitEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}

// checkEnvNames is what a check inherits. Everything else is dropped:
// HOME, XDG_*, GOFLAGS, GOENV, GOPATH and the Go caches are set by
// checkEnv instead, so nothing the agent wrote under the real home (a
// `go env -w GOFLAGS=-exec=...`, a poisoned build cache, a git or shell
// config) reaches a check. PATH is set from Options.Path.
var checkEnvNames = []string{
	"CI", "GONOPROXY", "GONOSUMDB", "GOPRIVATE", "GOPROXY", "GOSUMDB",
	"LANG", "LC_ALL", "LOGNAME", "SYSTEMROOT", "TZ", "USER",
}

// checkDirs are the per-verification directories a check gets as its home
// and Go state. All are outside the checkout and new, except caches the
// caller passed in Options.
type checkDirs struct {
	home, gopath, gocache, gomodcache, tmp string
}

// absolutePath drops empty and relative entries from a PATH value.
func absolutePath(p string) string {
	var keep []string
	for _, d := range filepath.SplitList(p) {
		if d != "" && filepath.IsAbs(d) {
			keep = append(keep, d)
		}
	}
	return strings.Join(keep, string(os.PathListSeparator))
}

// lookPath resolves argv[0] the way the check will run it: a name with a
// separator is relative to the checkout (and must stay inside it); a bare
// name is looked up in pathValue only, never the verifier's PATH and never
// the checkout.
func lookPath(name, checkout, pathValue string) (string, error) {
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
		if filepath.IsAbs(name) {
			return name, nil
		}
		p := filepath.Join(checkout, filepath.FromSlash(name))
		if rel, err := filepath.Rel(checkout, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%s is outside the checkout", name)
		}
		return p, nil
	}
	for _, d := range filepath.SplitList(pathValue) {
		p := filepath.Join(d, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
			return p, nil
		}
		if runtime.GOOS == "windows" {
			if info, err := os.Stat(p + ".exe"); err == nil && info.Mode().IsRegular() {
				return p + ".exe", nil
			}
		}
	}
	return "", fmt.Errorf("%s not found in PATH %q", name, pathValue)
}

// checkEnv returns the environment for a check, its sorted variable names,
// and the sha256 of "name=value\n" for each, in name order. Values are not
// stored in evidence; the digest shows whether two runs had the same ones.
func checkEnv(pathValue string, d checkDirs) ([]string, []string, string) {
	allowed := map[string]bool{}
	for _, n := range checkEnvNames {
		allowed[n] = true
	}
	var env, names []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if allowed[name] {
			env = append(env, kv)
			names = append(names, name)
		}
	}
	set := [][2]string{
		{"SDF_VERIFY", "1"}, {"PATH", pathValue},
		{"HOME", d.home},
		{"XDG_CONFIG_HOME", filepath.Join(d.home, ".config")},
		{"XDG_CACHE_HOME", filepath.Join(d.home, ".cache")},
		{"XDG_DATA_HOME", filepath.Join(d.home, ".local", "share")},
		{"XDG_STATE_HOME", filepath.Join(d.home, ".local", "state")},
		{"GOENV", "off"}, {"GOFLAGS", ""}, {"GOTOOLCHAIN", "local"},
		{"GOPATH", d.gopath}, {"GOCACHE", d.gocache}, {"GOMODCACHE", d.gomodcache},
		// A new temp directory: /tmp is covered in the namespace.
		{"TMPDIR", d.tmp}, {"TEMP", d.tmp}, {"TMP", d.tmp},
	}
	for _, kv := range set {
		env = append(env, kv[0]+"="+kv[1])
		names = append(names, kv[0])
	}
	sort.Strings(env)
	sort.Strings(names)
	h := sha256.New()
	for _, kv := range env {
		h.Write([]byte(kv + "\n"))
	}
	return env, names, "sha256:" + hex.EncodeToString(h.Sum(nil))
}
