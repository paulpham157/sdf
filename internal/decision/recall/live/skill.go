package live

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

func findSkill(explicit string) (string, error) {
	if explicit == "" {
		explicit = os.Getenv("SDF_RECALL_SKILL")
	}
	if explicit != "" {
		if _, err := os.Stat(filepath.Join(explicit, "SKILL.md")); err != nil {
			return "", errors.New("skill directory has no SKILL.md")
		}
		return explicit, nil
	}
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	p := filepath.Join(root, "skills", "sdf-decisions")
	if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
		return "", errors.New("skills/sdf-decisions/SKILL.md not found (set SDF_RECALL_SKILL)")
	}
	return p, nil
}

func moduleRoot() (string, error) {
	if r := os.Getenv("SDF_RECALL_ROOT"); r != "" {
		return r, nil
	}
	if dir, err := os.Getwd(); err == nil {
		if root := walkGoMod(dir); root != "" {
			return root, nil
		}
	}
	_, file, _, ok := runtime.Caller(0)
	if ok {
		dir := filepath.Dir(file)
		for range 8 {
			if root := walkGoMod(dir); root != "" {
				return root, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", errors.New("cannot find the sdf module root (set SDF_RECALL_ROOT)")
}

func walkGoMod(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func installSkills(spec spec, extra ...string) error {
	dests := []string{filepath.Join(spec.Repo, "skills", "sdf-decisions")}
	dests = append(dests, extra...)
	for _, dest := range dests {
		if err := copyTree(spec.SkillSrc, dest); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(src, dest string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o600
		}
		return copyFile(path, target, mode)
	})
}

func copyFile(src, dest string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	cerr := out.Close()
	if err != nil {
		return err
	}
	return cerr
}
