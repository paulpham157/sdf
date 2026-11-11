package setup

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const hookMarker = "# sdf-git-hook:v1"

// ErrHooksNotActivated means the checkout's active hooks directory belongs to
// husky, lefthook, or pre-commit. The chainable scripts were still written.
// core.hooksPath is not changed.
var ErrHooksNotActivated = errors.New("git hooks were not installed into the active hooks directory")

var hookNames = []string{"post-commit", "post-rewrite", "prepare-commit-msg"}

// InstallGitHooks writes post-commit, post-rewrite, and prepare-commit-msg
// into the hooks directory Git will run for this checkout. An existing hook
// is chained, not overwritten. A symlink is left untouched. A second run
// with the same endpoint rewrites only the sdf script.
func InstallGitHooks(dir string, ep Endpoint) (Result, error) {
	url, err := hookLogsURL(ep)
	if err != nil {
		return Result{}, err
	}
	top, err := checkoutRoot(dir)
	if err != nil {
		return Result{}, err
	}
	hooksDir, err := hooksDirectory(top)
	if err != nil {
		return Result{}, err
	}
	configPath, _ := gitConfig(top, "core.hooksPath")
	var b strings.Builder
	if note := worktreeNote(top, hooksDir); note != "" {
		b.WriteString(note)
	}
	if manager := hookManager(configPath, hooksDir); manager != "" {
		chain, err := writeChainScripts(top, url)
		if err != nil {
			return Result{Text: b.String()}, err
		}
		b.WriteString(managerInstructions(manager, configPath, chain))
		return Result{Text: b.String()}, ErrHooksNotActivated
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return Result{}, err
	}
	changed := false
	for _, name := range hookNames {
		did, err := installOne(filepath.Join(hooksDir, name), hookScript(url, name))
		if err != nil {
			return Result{Text: b.String()}, err
		}
		if did {
			changed = true
		}
	}
	if changed {
		fmt.Fprintf(&b, "installed sdf git hooks in %s\n", hooksDir)
	} else {
		fmt.Fprintf(&b, "sdf git hooks already installed in %s\n", hooksDir)
	}
	fmt.Fprintf(&b, "Events go to %s and nowhere else. The body is the commit SHA, checkout path, branch, and agent session id. The diff and the commit message are not sent.\n", url)
	fmt.Fprintf(&b, "A commit still succeeds when that receiver is down. prepare-commit-msg adds an SDF-Session trailer only when a session id is present.\n")
	if hint := idleToolHint(top, configPath); hint != "" {
		b.WriteString(hint)
	}
	b.WriteString("Remove them with: sdf init --uninstall-git-hooks\n")
	return Result{Text: b.String(), Wrote: changed}, nil
}

// UninstallGitHooks removes sdf hooks and puts a chained previous hook back.
// A hook this command did not write is left in place. It does not edit
// husky, lefthook, or pre-commit files.
func UninstallGitHooks(dir string) (Result, error) {
	top, err := checkoutRoot(dir)
	if err != nil {
		return Result{}, err
	}
	var b strings.Builder
	hooksDir, err := hooksDirectory(top)
	if err != nil {
		return Result{}, err
	}
	for _, dir := range []string{hooksDir, chainDir(top)} {
		for _, name := range hookNames {
			line, err := uninstallOne(filepath.Join(dir, name))
			if err != nil {
				return Result{Text: b.String()}, err
			}
			b.WriteString(line)
		}
	}
	_ = os.Remove(chainDir(top))
	if b.Len() == 0 {
		b.WriteString("no sdf git hooks were installed\n")
	}
	b.WriteString("If husky, lefthook, or pre-commit calls those scripts, remove that line yourself.\n")
	return Result{Text: b.String()}, nil
}

func hookLogsURL(ep Endpoint) (string, error) {
	host := ep.Host
	switch host {
	case "", "localhost", "127.0.0.1":
		host = "127.0.0.1"
	case "::1":
	default:
		return "", fmt.Errorf("git hooks only post to loopback, not %q", ep.Host)
	}
	port := ep.HTTPPort
	if port == 0 {
		port = 4318
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("git hooks need an HTTP port")
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/v1/logs", nil
}

func installOne(path, body string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return true, writeExec(path, body)
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s is a symlink; sdf did not change it", path)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file; sdf did not change it", path)
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if string(existing) == body {
		return false, nil
	}
	if bytes.Contains(existing, []byte(hookMarker)) {
		return true, writeExec(path, body)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return false, fmt.Errorf("%s exists and is not executable; sdf did not change it", path)
	}
	prev := path + ".sdf-previous"
	if _, err := os.Lstat(prev); err == nil {
		return false, fmt.Errorf("%s is not an sdf hook and %s already exists; sdf did not change it", path, prev)
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := os.Rename(path, prev); err != nil {
		return false, err
	}
	if err := writeExec(path, body); err != nil {
		_ = os.Rename(prev, path)
		return false, err
	}
	return true, nil
}

func uninstallOne(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return path + " left in place (not an sdf hook)\n", nil
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !bytes.Contains(existing, []byte(hookMarker)) {
		return path + " left in place (not an sdf hook)\n", nil
	}
	prev := path + ".sdf-previous"
	if _, err := os.Lstat(prev); err == nil {
		if err := os.Remove(path); err != nil {
			return "", err
		}
		if err := os.Rename(prev, path); err != nil {
			return "", err
		}
		return "restored " + path + "\n", nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return "removed " + path + "\n", nil
}

func writeChainScripts(top, url string) (string, error) {
	dir := chainDir(top)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, name := range hookNames {
		if err := writeExec(filepath.Join(dir, name), hookScript(url, name)); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func chainDir(top string) string {
	common, err := gitOutput(top, "rev-parse", "--git-common-dir")
	if err != nil {
		return filepath.Join(top, ".git", "sdf-hooks")
	}
	return filepath.Join(absFrom(top, common), "sdf-hooks")
}

func writeExec(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".sdf-tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func managerInstructions(manager, configPath, chain string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This checkout uses %s (core.hooksPath=%s).\n", manager, configPath)
	b.WriteString("sdf did not change core.hooksPath and did not edit that tool's files.\n")
	fmt.Fprintf(&b, "Chain scripts were written to %s. Append these lines yourself:\n", chain)
	for _, name := range hookNames {
		script := filepath.Join(chain, name)
		if name == "prepare-commit-msg" {
			fmt.Fprintf(&b, "  sh %s \"$@\"\n", script)
			continue
		}
		fmt.Fprintf(&b, "  sh %s\n", script)
	}
	switch manager {
	case "husky":
		b.WriteString("Put them in the husky files you edit (.husky/post-commit, .husky/post-rewrite, and .husky/prepare-commit-msg), not in the generated .husky/_ copies.\n")
	case "lefthook":
		b.WriteString("Point the matching lefthook.yml commands at those scripts. Re-run lefthook install if it rewrites the active hooks.\n")
	case "pre-commit":
		b.WriteString("pre-commit does not run post-commit unless you add a hook. Call the scripts from the hook files pre-commit installed, or from a local hook entry.\n")
	}
	b.WriteString("The scripts post only to loopback. They send the commit SHA, checkout path, branch, and agent session id, not the diff or the commit message.\n")
	return b.String()
}

func idleToolHint(top, configPath string) string {
	if configPath != "" {
		return ""
	}
	var found []string
	if info, err := os.Stat(filepath.Join(top, ".husky")); err == nil && info.IsDir() {
		found = append(found, ".husky")
	}
	for _, name := range []string{"lefthook.yml", "lefthook.yaml", ".pre-commit-config.yaml"} {
		if _, err := os.Stat(filepath.Join(top, name)); err == nil {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		return ""
	}
	return "This checkout also has " + strings.Join(found, ", ") + ", and core.hooksPath is unset, so Git is not running those hooks. If you point core.hooksPath at one of them later, re-run sdf init --git-hooks and add the chain lines it prints. sdf will not change core.hooksPath.\n"
}

func worktreeNote(top, hooksDir string) string {
	gitDir, err := gitOutput(top, "rev-parse", "--git-dir")
	if err != nil {
		return ""
	}
	common, err := gitOutput(top, "rev-parse", "--git-common-dir")
	if err != nil {
		return ""
	}
	if absFrom(top, gitDir) == absFrom(top, common) {
		return ""
	}
	return "This checkout is a linked worktree. Git runs hooks from " + hooksDir + ", which is shared by the worktrees of this repository. Each event records the toplevel of the worktree that committed.\n"
}

func hookManager(configValue, effective string) string {
	if strings.TrimSpace(configValue) == "" {
		return ""
	}
	text := strings.ToLower(filepath.ToSlash(configValue + "/" + effective))
	switch {
	case strings.Contains(text, "husky"):
		return "husky"
	case strings.Contains(text, "lefthook"):
		return "lefthook"
	case strings.Contains(text, "pre-commit"):
		return "pre-commit"
	}
	if _, err := os.Stat(filepath.Join(effective, "husky.sh")); err == nil {
		return "husky"
	}
	return ""
}

func checkoutRoot(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	top, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not a git checkout", dir)
	}
	return filepath.Clean(top), nil
}

func hooksDirectory(top string) (string, error) {
	raw, err := gitOutput(top, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	return absFrom(top, raw), nil
}

func absFrom(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}

func gitConfig(dir, key string) (string, bool) {
	cmd := exec.Command("git", "-C", dir, "config", "--get", key)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func gitOutput(dir string, args ...string) (string, error) {
	if strings.HasPrefix(dir, "-") {
		return "", fmt.Errorf("refusing git directory %q", dir)
	}
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("git: %s", msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
