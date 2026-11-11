package setup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrTemplateDirUnset means Git has no user template directory. Nothing was
// written and git config was not changed.
var ErrTemplateDirUnset = errors.New("init.templateDir is unset")

// TemplateOptions is sdf init --git-template. SetConfig is --set-template-dir.
// UnsetConfig is --unset-template-dir on uninstall.
type TemplateOptions struct {
	Home        string
	SetConfig   bool
	UnsetConfig bool
}

// InstallGitTemplate installs the sdf hooks into the git template directory
// new clones and git init copy from. An existing template is not replaced.
// Hooks already in that directory follow the same chain and refusal rules as
// sdf init --git-hooks. Global git config changes only when SetConfig is set
// and init.templateDir is unset, and the change is included in the result text.
func InstallGitTemplate(opts TemplateOptions, ep Endpoint) (Result, error) {
	url, err := hookLogsURL(ep)
	if err != nil {
		return Result{}, err
	}
	home, err := templateHome(opts.Home)
	if err != nil {
		return Result{}, err
	}
	managed, err := managedTemplateDir(home)
	if err != nil {
		return Result{}, err
	}
	env := strings.TrimSpace(os.Getenv("GIT_TEMPLATE_DIR"))
	global, hasGlobal := globalGitConfig("init.templateDir")
	var b strings.Builder
	var dir string
	create := false
	switch {
	case env != "":
		dir = env
		fmt.Fprintf(&b, "GIT_TEMPLATE_DIR is %s. Git uses that directory instead of init.templateDir. sdf did not change git config.\n", env)
		if opts.SetConfig {
			b.WriteString("--set-template-dir was not applied because GIT_TEMPLATE_DIR is set.\n")
		}
	case hasGlobal:
		dir = global
		fmt.Fprintf(&b, "init.templateDir is %s. sdf did not change git config.\n", global)
	default:
		if !opts.SetConfig {
			b.WriteString("No init.templateDir is set, and GIT_TEMPLATE_DIR is unset.\n")
			b.WriteString("sdf did not change git config and did not write a template.\n")
			b.WriteString("Pass --set-template-dir to create ~/.config/sdf/git-template, copy Git's default template into it when that directory is empty, install the sdf hooks there, and set init.templateDir to that directory.\n")
			b.WriteString(templateExistingRepos)
			return Result{Text: b.String()}, ErrTemplateDirUnset
		}
		dir = managed
		create = true
	}
	if err := safeTemplatePath(dir); err != nil {
		return Result{Text: b.String()}, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Result{Text: b.String()}, err
	}
	dir = abs
	if create || samePath(dir, managed) {
		note, err := seedManagedTemplate(dir)
		if err != nil {
			return Result{Text: b.String()}, err
		}
		b.WriteString(note)
	}
	changed, err := installHookFiles(filepath.Join(dir, "hooks"), url)
	if err != nil {
		return Result{Text: b.String()}, err
	}
	if changed {
		fmt.Fprintf(&b, "installed sdf git hooks in %s\n", filepath.Join(dir, "hooks"))
	} else {
		fmt.Fprintf(&b, "sdf git hooks already installed in %s\n", filepath.Join(dir, "hooks"))
	}
	if create {
		if err := setGlobalGitConfig("init.templateDir", dir); err != nil {
			return Result{Text: b.String()}, err
		}
		fmt.Fprintf(&b, "set git config --global init.templateDir %s\nprevious value: unset\n", dir)
	}
	b.WriteString(templateExistingRepos)
	b.WriteString("Remove the template hooks with: sdf init --uninstall-git-template\n")
	return Result{Text: b.String(), Wrote: changed || create}, nil
}

// UninstallGitTemplate removes sdf hooks from the active git template directory.
// UnsetConfig unsets init.templateDir only when it is the directory sdf created
// (~/.config/sdf/git-template). Another template directory is left configured.
func UninstallGitTemplate(opts TemplateOptions) (Result, error) {
	home, err := templateHome(opts.Home)
	if err != nil {
		return Result{}, err
	}
	managed, err := managedTemplateDir(home)
	if err != nil {
		return Result{}, err
	}
	env := strings.TrimSpace(os.Getenv("GIT_TEMPLATE_DIR"))
	global, hasGlobal := globalGitConfig("init.templateDir")
	var b strings.Builder
	var dir string
	switch {
	case env != "":
		dir = env
		if opts.UnsetConfig {
			b.WriteString("GIT_TEMPLATE_DIR is set. sdf did not change git config.\n")
		}
	case hasGlobal:
		dir = global
	default:
		if info, err := os.Stat(managed); err == nil && info.IsDir() {
			dir = managed
			b.WriteString("init.templateDir is unset. sdf did not change git config.\n")
		}
	}
	if dir != "" {
		if err := safeTemplatePath(dir); err != nil {
			return Result{Text: b.String()}, err
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return Result{Text: b.String()}, err
		}
		for _, name := range hookNames {
			line, err := uninstallOne(filepath.Join(abs, "hooks", name))
			if err != nil {
				return Result{Text: b.String()}, err
			}
			b.WriteString(line)
		}
	}
	if opts.UnsetConfig {
		switch {
		case env != "":
		case hasGlobal && samePath(global, managed):
			if err := unsetGlobalGitConfig("init.templateDir"); err != nil {
				return Result{Text: b.String()}, err
			}
			fmt.Fprintf(&b, "unset git config --global init.templateDir\nprevious value: %s\n", global)
			b.WriteString("The template directory was left in place.\n")
		case hasGlobal:
			fmt.Fprintf(&b, "init.templateDir is %s, which sdf did not create. sdf did not unset it.\n", global)
		default:
			b.WriteString("init.templateDir is unset. sdf did not change git config.\n")
		}
	}
	if !strings.Contains(b.String(), "removed ") && !strings.Contains(b.String(), "restored ") {
		b.WriteString("no sdf git hooks were installed in a git template\n")
	}
	b.WriteString("Existing repositories keep hooks already copied into them. Remove those with sdf init --uninstall-git-hooks.\n")
	return Result{Text: b.String()}, nil
}

const templateExistingRepos = "Existing repositories are unchanged. Run sdf init --git-hooks in each one, or re-run git init there. git init copies template hooks that are missing and does not replace a hook that already exists. New clones and new git init use the template. GIT_TEMPLATE_DIR, when set, overrides init.templateDir.\n"

func templateHome(home string) (string, error) {
	if home != "" {
		return home, nil
	}
	return os.UserHomeDir()
}

func managedTemplateDir(home string) (string, error) {
	if home == "" {
		return "", errors.New("home directory is unknown")
	}
	return filepath.Join(home, ".config", "sdf", "git-template"), nil
}

func seedManagedTemplate(dir string) (string, error) {
	empty, err := dirEmpty(dir)
	if err != nil {
		return "", err
	}
	if !empty {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src, err := defaultGitTemplate()
	if err != nil {
		return "Git's default template directory was not found, so the new template has the sdf hooks only. New repositories will not get the stock sample hooks or info/exclude.\n", nil
	}
	if err := copyTemplate(src, dir); err != nil {
		return "", err
	}
	return "copied Git's default template from " + src + "\n", nil
}

func dirEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func defaultGitTemplate() (string, error) {
	cmd := exec.Command("git", "--exec-path")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	candidates := []string{"/usr/share/git-core/templates"}
	if err == nil {
		execPath := strings.TrimSpace(string(out))
		candidates = append([]string{
			filepath.Clean(filepath.Join(execPath, "..", "..", "share", "git-core", "templates")),
		}, candidates...)
	}
	for _, candidate := range candidates {
		info, statErr := os.Stat(filepath.Join(candidate, "hooks"))
		if statErr == nil && info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("default git template directory was not found")
}

func copyTemplate(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := dest
		if rel != "." {
			target = filepath.Join(dest, rel)
		}
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if _, err := os.Lstat(target); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		return os.WriteFile(target, body, mode)
	})
}

func safeTemplatePath(path string) error {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\n\r") || strings.HasPrefix(path, "-") {
		return fmt.Errorf("refusing template path %q", path)
	}
	return nil
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

func globalGitConfig(key string) (string, bool) {
	if strings.HasPrefix(key, "-") {
		return "", false
	}
	cmd := exec.Command("git", "config", "--global", "--get", key)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	value := strings.TrimSpace(string(out))
	if value == "" || strings.ContainsAny(value, "\n\r") {
		return "", false
	}
	return value, true
}

func setGlobalGitConfig(key, value string) error {
	if strings.HasPrefix(key, "-") || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("refusing git config %s", key)
	}
	cmd := exec.Command("git", "config", "--global", key, value)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git config --global %s: %s", key, msg)
	}
	return nil
}

func unsetGlobalGitConfig(key string) error {
	if strings.HasPrefix(key, "-") {
		return fmt.Errorf("refusing git config %s", key)
	}
	cmd := exec.Command("git", "config", "--global", "--unset", key)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git config --global --unset %s: %s", key, msg)
	}
	return nil
}
