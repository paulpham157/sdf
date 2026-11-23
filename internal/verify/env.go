package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
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
var checkEnvNames = []string{
	"CI", "GOCACHE", "GOFLAGS", "GOMODCACHE", "GONOSUMDB", "GOPATH", "GOPRIVATE",
	"GOPROXY", "GOTOOLCHAIN", "HOME", "LANG", "LC_ALL", "LOGNAME", "PATH",
	"SYSTEMROOT", "TEMP", "TMP", "TMPDIR", "TZ", "USER",
}

// checkEnv returns the environment for a check, its sorted variable names,
// and the sha256 of "name=value\n" for each, in name order. Values are not
// stored in evidence; the digest shows whether two runs had the same ones.
func checkEnv() ([]string, []string, string) {
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
	env = append(env, "SDF_VERIFY=1")
	names = append(names, "SDF_VERIFY")
	sort.Strings(env)
	sort.Strings(names)
	h := sha256.New()
	for _, kv := range env {
		h.Write([]byte(kv + "\n"))
	}
	return env, names, "sha256:" + hex.EncodeToString(h.Sum(nil))
}
