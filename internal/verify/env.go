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

// checkEnvNames is what a check inherits. Everything else is dropped.
// PATH is not inherited: it is set from Options.Path.
var checkEnvNames = []string{
	"CI", "GOCACHE", "GOFLAGS", "GOMODCACHE", "GONOSUMDB", "GOPATH", "GOPRIVATE",
	"GOPROXY", "GOTOOLCHAIN", "HOME", "LANG", "LC_ALL", "LOGNAME",
	"SYSTEMROOT", "TEMP", "TMP", "TMPDIR", "TZ", "USER",
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
func checkEnv(pathValue string) ([]string, []string, string) {
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
	env = append(env, "SDF_VERIFY=1", "PATH="+pathValue)
	names = append(names, "SDF_VERIFY", "PATH")
	sort.Strings(env)
	sort.Strings(names)
	h := sha256.New()
	for _, kv := range env {
		h.Write([]byte(kv + "\n"))
	}
	return env, names, "sha256:" + hex.EncodeToString(h.Sum(nil))
}
