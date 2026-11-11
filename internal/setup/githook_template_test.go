package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitTemplateRefusesToSetConfigUntilAsked(t *testing.T) {
	home, cfg := templateEnv(t)
	res, err := InstallGitTemplate(TemplateOptions{Home: home}, Endpoint{Host: "127.0.0.1", HTTPPort: 4318})
	if err != ErrTemplateDirUnset || !strings.Contains(res.Text, "--set-template-dir") || !strings.Contains(res.Text, "did not change git config") {
		t.Fatalf("%v %s", err, res.Text)
	}
	if strings.Contains(readFile(t, cfg), "templateDir") {
		t.Fatal(readFile(t, cfg))
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "sdf", "git-template")); !os.IsNotExist(err) {
		t.Fatal("template directory was created")
	}
}

func TestGitTemplateInstallsForNewReposAndUninstalls(t *testing.T) {
	home, cfg := templateEnv(t)
	old := testRepo(t)
	ep := Endpoint{Host: "127.0.0.1", HTTPPort: 4318}
	res, err := InstallGitTemplate(TemplateOptions{Home: home, SetConfig: true}, ep)
	if err != nil || !res.Wrote {
		t.Fatalf("%v %s", err, res.Text)
	}
	managed := filepath.Join(home, ".config", "sdf", "git-template")
	if !strings.Contains(res.Text, "set git config --global init.templateDir "+managed) || !strings.Contains(res.Text, "previous value: unset") || !strings.Contains(res.Text, "Existing repositories are unchanged") {
		t.Fatal(res.Text)
	}
	if !strings.Contains(res.Text, "copied Git's default template") {
		t.Fatal(res.Text)
	}
	hook := filepath.Join(managed, "hooks", "post-commit")
	body := readFile(t, hook)
	if !strings.Contains(body, hookMarker) || !strings.Contains(body, "http://127.0.0.1:4318/v1/logs") {
		t.Fatal(body)
	}
	if _, err := os.Stat(filepath.Join(managed, "hooks", "pre-commit.sample")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(hooksPath(t, old), "post-commit")); !os.IsNotExist(err) {
		t.Fatal("existing repo hook was written")
	}
	if got := globalTemplate(t); got != managed {
		t.Fatalf("config %q", got)
	}
	if strings.Contains(readFile(t, cfg), "hooksPath") {
		t.Fatal(readFile(t, cfg))
	}
	again, err := InstallGitTemplate(TemplateOptions{Home: home, SetConfig: true}, ep)
	if err != nil || again.Wrote || !strings.Contains(again.Text, "already installed") || !strings.Contains(again.Text, "did not change git config") {
		t.Fatalf("%v wrote=%v %s", err, again.Wrote, again.Text)
	}
	if strings.Count(readFile(t, cfg), "templateDir") != 1 {
		t.Fatal(readFile(t, cfg))
	}
	fresh := filepath.Join(t.TempDir(), "fresh")
	cmd := exec.Command("git", "init", fresh)
	cmd.Env = templateGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	copied := readFile(t, filepath.Join(fresh, ".git", "hooks", "post-commit"))
	if !strings.Contains(copied, hookMarker) {
		t.Fatal(copied)
	}
	// git init does not replace a hook that is already there.
	custom := "#!/bin/sh\necho keep\n"
	if err := os.WriteFile(filepath.Join(old, ".git", "hooks", "post-commit"), []byte(custom), 0o755); err != nil {
		t.Fatal(err)
	}
	reinit := exec.Command("git", "init", old)
	reinit.Env = templateGitEnv()
	if out, err := reinit.CombinedOutput(); err != nil {
		t.Fatalf("reinit: %v %s", err, out)
	}
	if got := readFile(t, filepath.Join(old, ".git", "hooks", "post-commit")); got != custom {
		t.Fatalf("existing hook replaced: %s", got)
	}
	off, err := UninstallGitTemplate(TemplateOptions{Home: home, UnsetConfig: true})
	if err != nil || !strings.Contains(off.Text, "removed "+hook) || !strings.Contains(off.Text, "unset git config --global init.templateDir") || !strings.Contains(off.Text, "previous value: "+managed) {
		t.Fatalf("%v %s", err, off.Text)
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatal("hook remains")
	}
	if globalTemplate(t) != "" {
		t.Fatalf("config still %q", globalTemplate(t))
	}
	if _, err := os.Stat(managed); err != nil {
		t.Fatal("template directory was deleted")
	}
}

func TestGitTemplateChainsAnExistingTemplateAndDoesNotReplaceIt(t *testing.T) {
	home, cfg := templateEnv(t)
	user := filepath.Join(t.TempDir(), "user-template")
	if err := os.MkdirAll(filepath.Join(user, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "#!/bin/sh\necho user-hook\n"
	hook := filepath.Join(user, "hooks", "post-commit")
	if err := os.WriteFile(hook, []byte(original), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := setGlobalGitConfig("init.templateDir", user); err != nil {
		t.Fatal(err)
	}
	res, err := InstallGitTemplate(TemplateOptions{Home: home, SetConfig: true}, Endpoint{Host: "127.0.0.1", HTTPPort: 4318})
	if err != nil || !strings.Contains(res.Text, "did not change git config") || !strings.Contains(res.Text, user) {
		t.Fatalf("%v %s", err, res.Text)
	}
	if globalTemplate(t) != user {
		t.Fatalf("config changed to %q", globalTemplate(t))
	}
	if !strings.Contains(readFile(t, hook), hookMarker) {
		t.Fatal(readFile(t, hook))
	}
	prev := readFile(t, hook+".sdf-previous")
	if prev != original {
		t.Fatalf("previous %q", prev)
	}
	managed := filepath.Join(home, ".config", "sdf", "git-template")
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatal("sdf template was created beside the user's")
	}
	blocked := filepath.Join(user, "hooks", "post-rewrite")
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = InstallGitTemplate(TemplateOptions{Home: home}, Endpoint{Host: "127.0.0.1", HTTPPort: 4318})
	if err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatal(err)
	}
	if got := readFile(t, blocked); got != "#!/bin/sh\n" {
		t.Fatal(got)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(user, "hooks", "prepare-commit-msg")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", link); err != nil {
		t.Fatal(err)
	}
	_, err = InstallGitTemplate(TemplateOptions{Home: home}, Endpoint{Host: "127.0.0.1", HTTPPort: 4318})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	off, err := UninstallGitTemplate(TemplateOptions{Home: home, UnsetConfig: true})
	if err != nil || !strings.Contains(off.Text, "restored "+hook) || !strings.Contains(off.Text, "which sdf did not create") {
		t.Fatalf("%v %s", err, off.Text)
	}
	if got := readFile(t, hook); got != original {
		t.Fatalf("restored %q", got)
	}
	if globalTemplate(t) != user || !strings.Contains(readFile(t, cfg), user) {
		t.Fatal(readFile(t, cfg))
	}
}

func TestGitTemplateHonorsTheEnvironmentWithoutWritingConfig(t *testing.T) {
	home, cfg := templateEnv(t)
	dir := filepath.Join(t.TempDir(), "from-env")
	t.Setenv("GIT_TEMPLATE_DIR", dir)
	res, err := InstallGitTemplate(TemplateOptions{Home: home, SetConfig: true}, Endpoint{Host: "127.0.0.1", HTTPPort: 9})
	if err != nil || !strings.Contains(res.Text, "GIT_TEMPLATE_DIR is "+dir) || !strings.Contains(res.Text, "did not change git config") {
		t.Fatalf("%v %s", err, res.Text)
	}
	if strings.Contains(readFile(t, cfg), "templateDir") {
		t.Fatal(readFile(t, cfg))
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "hooks", "post-commit")), hookMarker) {
		t.Fatal("env template hook missing")
	}
	off, err := UninstallGitTemplate(TemplateOptions{Home: home, UnsetConfig: true})
	if err != nil || !strings.Contains(off.Text, "GIT_TEMPLATE_DIR is set") || !strings.Contains(off.Text, "removed ") {
		t.Fatalf("%v %s", err, off.Text)
	}
}

func templateEnv(t *testing.T) (string, string) {
	t.Helper()
	isolateGit(t)
	home := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return home, cfg
}

func templateGitEnv() []string {
	var out []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if key == "GIT_CONFIG_GLOBAL" || key == "GIT_CONFIG_NOSYSTEM" || key == "GIT_TERMINAL_PROMPT" {
			continue
		}
		out = append(out, item)
	}
	return append(out, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL="+os.Getenv("GIT_CONFIG_GLOBAL"))
}

func globalTemplate(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "config", "--global", "--get", "init.templateDir")
	cmd.Env = templateGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(body)
}
