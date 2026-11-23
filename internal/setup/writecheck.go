package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// runningAsRoot reports whether sdf runs as root; tests replace it.
var runningAsRoot = func() bool { return os.Geteuid() == 0 }

// linkEscape describes a symlinked path whose target is outside both the
// link's own directory (the agent's config dir) and $HOME, or returns "".
// A stow-style link into ~/dotfiles stays inside $HOME and is not reported.
func linkEscape(path, target string) string {
	if target == path {
		return ""
	}
	real := realPath(target)
	roots := []string{filepath.Dir(path)}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, home)
	}
	for _, root := range roots {
		if within(real, realPath(root)) {
			return ""
		}
	}
	return fmt.Sprintf("%s is a symlink to %s, outside %s and $HOME", path, real, filepath.Dir(path))
}

// danglingOutside refuses a symlink to a missing file whose directory does
// not exist either and would be created outside the link's directory (the
// agent's config dir) and $HOME: writeAtomic creates the target's parent
// directories, and sdf should not create directories elsewhere
// (~/.grok/config.toml -> /outside/missing/config.toml). A missing target in
// a directory that exists (stow-style, into a dotfiles checkout) is fine, and
// so is one whose missing directories are inside the config dir or $HOME.
// This is the rule sdf init qwen already applied (qwenCheckLinkTarget).
func danglingOutside(path, target string) string {
	if target == path {
		return ""
	}
	dir := filepath.Dir(target)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return ""
	}
	real := realPath(dir)
	roots := []string{filepath.Dir(path)}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, home)
	}
	for _, root := range roots {
		if within(real, realPath(root)) {
			return ""
		}
	}
	return fmt.Sprintf("%s is a symlink to %s, whose directory %s does not exist and is outside %s and $HOME; create it first (sdf does not create directories there); it was not changed",
		path, target, dir, filepath.Dir(path))
}

// loopErr is the error for a symlink chain that never ends.
func loopErr(path string) error {
	return fmt.Errorf("%s is a symlink loop (or a chain of more than 40 links); fix or remove the link and rerun; it was not changed", path)
}

// realPath resolves the symlinks of the longest existing prefix of p, so a
// file that does not exist yet still compares by its real directory.
func realPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkDirs checks that sdf can create files where it will: the target's
// directory (temp file and rename) and the link's directory (backup). A
// directory that does not exist yet is checked at its nearest existing
// ancestor, where MkdirAll would create it. The probe creates and removes a
// temp file, because mode bits alone do not say what this user may do.
func checkDirs(path, target string) error {
	dirs := []string{filepath.Dir(target)}
	if d := filepath.Dir(path); d != dirs[0] {
		dirs = append(dirs, d)
	}
	for _, dir := range dirs {
		if err := probeDir(path, dir); err != nil {
			return err
		}
	}
	return nil
}

func probeDir(path, dir string) error {
	probe := dir
	for {
		info, err := os.Stat(probe)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s cannot be written: %s is not a directory; it was not changed", path, probe)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s cannot be written: %v; it was not changed", path, err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return fmt.Errorf("%s cannot be written: %v; it was not changed", path, err)
		}
		probe = parent
	}
	f, err := os.CreateTemp(probe, ".sdf-probe-*")
	if err != nil {
		return fmt.Errorf("%s cannot be written: directory %s is not writable (mode %04o, %v); fix its permissions and rerun; it was not changed",
			path, probe, fileMode(probe, 0).Perm(), trimPathErr(err))
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// readErr makes a failed read of an existing settings file say what to fix
// instead of a bare "permission denied".
func readErr(path string, err error) error {
	if errors.Is(err, syscall.ELOOP) {
		return loopErr(path)
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%s cannot be read (mode %04o, %v); fix its permissions (chmod u+rw) and rerun; it was not changed",
			path, fileMode(path, 0).Perm(), trimPathErr(err))
	}
	return err
}
