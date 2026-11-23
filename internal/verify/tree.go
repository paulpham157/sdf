package verify

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/paulpham157/sdf/internal/task"
)

// diskEntry is the git mode and blob id of the file at p, read without
// following a symlink and without git: a check cannot hide a change behind
// the index (skip-worktree, assume-unchanged) or a filter.
func diskEntry(p string, oidLen int) (treeEntry, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return treeEntry{}, err
	}
	var mode string
	var b []byte
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		mode = "120000"
		t, err := os.Readlink(p)
		if err != nil {
			return treeEntry{}, err
		}
		b = []byte(filepath.ToSlash(t))
	case info.Mode().IsRegular():
		mode = "100644"
		if info.Mode()&0o100 != 0 {
			mode = "100755"
		}
		if b, err = os.ReadFile(p); err != nil {
			return treeEntry{}, err
		}
	default:
		return treeEntry{mode: info.Mode().Type().String()}, nil
	}
	var h hash.Hash = sha1.New()
	if oidLen == 64 {
		h = sha256.New()
	}
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return treeEntry{mode: mode, oid: hex.EncodeToString(h.Sum(nil))}, nil
}

// watched reports whether a path no check may add: a designated file, a
// match of a criterion's designated globs, or a Go test input of a go
// test/vet criterion.
func (r *run) watched(p string) bool {
	for _, f := range r.spec.DesignatedFiles {
		if f.Path == p {
			return true
		}
	}
	for _, c := range r.spec.Criteria {
		for _, g := range c.Designated {
			if task.MatchGlob(g, p) {
				return true
			}
		}
		if goInput(c.Argv, p) {
			return true
		}
	}
	return false
}

// checkDisk compares the checkout on disk with the submitted tree, held in
// memory since checkIntegrity: every tracked file must have the tree's mode
// and blob, and no new file may be a watched path. It returns the problems.
func (r *run) checkDisk() []string {
	var problems []string
	seen := map[string]bool{}
	oidLen := 40
	for _, e := range r.treeAtSHA {
		oidLen = len(e.oid)
		break
	}
	err := filepath.WalkDir(r.checkout, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			problems = append(problems, err.Error())
			return nil
		}
		rel, _ := filepath.Rel(r.checkout, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			if r.gitHidden {
				// A check made a new one: later git commands in the checkout
				// would read its config (core.fsmonitor, hooks, ...).
				problems = append(problems, ".git: added")
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		seen[rel] = true
		want, tracked := r.treeAtSHA[rel]
		if !tracked {
			if r.watched(rel) {
				problems = append(problems, rel+": added")
			}
			return nil
		}
		got, err := diskEntry(p, oidLen)
		switch {
		case err != nil:
			problems = append(problems, rel+": "+err.Error())
		case got.mode != want.mode:
			problems = append(problems, fmt.Sprintf("%s: mode %s -> %s", rel, want.mode, got.mode))
		case got.oid != want.oid:
			problems = append(problems, rel+": content changed")
		}
		return nil
	})
	if err != nil {
		problems = append(problems, err.Error())
	}
	for p := range r.treeAtSHA {
		if !seen[p] {
			problems = append(problems, p+": deleted")
		}
	}
	sort.Strings(problems)
	return problems
}

// hideGitDir moves the checkout's .git next to it, outside the directory
// checks run in. An isolated check does not see it at all (the work
// directory is covered, and only the checkout and the criterion's own
// directory are bound back); an unisolated one could find it. What matters
// is that the verifier no longer reads the index or HEAD after this point:
// checkDisk compares the disk with the tree it read before any check ran,
// and a .git a check creates in the checkout is a change.
func (r *run) hideGitDir() error {
	if err := os.Rename(filepath.Join(r.checkout, ".git"), filepath.Join(filepath.Dir(r.checkout), "git")); err != nil {
		return err
	}
	r.gitHidden = true
	return nil
}

// removeTree removes dir after making every directory in it writable (the
// hidden .git and a module cache have read-only mode bits, which the same user can undo).
func removeTree(dir string) error {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

func isSymlinkOutside(p, target string) bool {
	if target == "" || filepath.IsAbs(target) || strings.HasPrefix(target, "/") || strings.Contains(target, "\\") || (len(target) > 1 && target[1] == ':') {
		return true
	}
	joined := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(p)), filepath.FromSlash(target))))
	return joined == ".." || strings.HasPrefix(joined, "../")
}
