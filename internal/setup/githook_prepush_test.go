package setup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSDF writes an sdf stand-in that records its arguments and stdin and
// exits with $FAKE_SDF_EXIT.
func fakeSDF(t *testing.T, dir string) (bin, log string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "sdf")
	log = filepath.Join(t.TempDir(), "fake-sdf.log")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf 'arg=%s\\n' \"$a\"; done >> '" + log + "'\ncat >> '" + log + "'\nexit \"${FAKE_SDF_EXIT:-0}\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func runPrePushHook(t *testing.T, dir, hook, stdin string, env ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(hook, "origin", "/srv/remote.git")
	cmd.Dir = dir
	cmd.Env = gitEnv(append([]string{"PATH=/usr/bin:/bin"}, env...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), string(out)
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0, string(out)
}

func TestPrePushHookRunsTheRecordedBinaryAndFailsOpen(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	hooks := hooksPath(t, dir)
	// Paths with a space and a quote must reach sdf unchanged.
	bin, log := fakeSDF(t, filepath.Join(t.TempDir(), "o'neil bin"))
	db := filepath.Join(t.TempDir(), "my store", "it's.sqlite")
	res, err := InstallGitHooksWith(dir, Endpoint{Host: "127.0.0.1", HTTPPort: 9}, HookOptions{Bin: bin, DB: db})
	if err != nil || !strings.Contains(res.Text, "pre-push reads the store "+db) || !strings.Contains(res.Text, "pre-push warns") {
		t.Fatalf("%v %s", err, res.Text)
	}
	hook := filepath.Join(hooks, "pre-push")
	body := readFile(t, hook)
	if !strings.Contains(body, hookMarker) || strings.Contains(body, "git diff") || strings.Contains(body, "git show") || strings.Contains(body, "%B") {
		t.Fatal(body)
	}
	if out, err := exec.Command("sh", "-n", hook).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v %s", err, out)
	}
	line := "refs/heads/main " + strings.Repeat("a", 40) + " refs/heads/main " + strings.Repeat("0", 40) + "\n"

	code, out := runPrePushHook(t, dir, hook, line)
	got := readFile(t, log)
	want := "arg=decision\narg=pre-push\narg=--remote\narg=origin\narg=--db\narg=" + db + "\n" + line
	if code != 0 || got != want {
		t.Fatalf("exit %d out %q\nsdf saw:\n%s\nwant:\n%s", code, out, got, want)
	}

	// Exit 1 is the block verdict and fails the push.
	if code, _ := runPrePushHook(t, dir, hook, line, "FAKE_SDF_EXIT=1"); code != 1 {
		t.Fatalf("block exit %d", code)
	}
	// Anything else (an old sdf without the command exits 2) lets it go.
	if code, out := runPrePushHook(t, dir, hook, line, "FAKE_SDF_EXIT=2"); code != 0 || !strings.Contains(out, "could not run (sdf exited 2)") {
		t.Fatalf("exit %d %s", code, out)
	}
	// SDF_PRE_PUSH=off does not start sdf at all.
	_ = os.Remove(log)
	if code, out := runPrePushHook(t, dir, hook, line, "SDF_PRE_PUSH=off", "FAKE_SDF_EXIT=1"); code != 0 || out != "" {
		t.Fatalf("off: exit %d %q", code, out)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("sdf ran with SDF_PRE_PUSH=off")
	}
	// SDF_PRE_PUSH_DB overrides the recorded store.
	if code, _ := runPrePushHook(t, dir, hook, line, "SDF_PRE_PUSH_DB=/elsewhere.sqlite"); code != 0 || !strings.Contains(readFile(t, log), "arg=/elsewhere.sqlite\n") {
		t.Fatalf("SDF_PRE_PUSH_DB: exit %d\n%s", code, readFile(t, log))
	}
	// No recorded binary and no sdf on PATH: a note, and the push goes on.
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if code, out := runPrePushHook(t, dir, hook, line, "FAKE_SDF_EXIT=1"); code != 0 || !strings.Contains(out, "sdf is not installed or not on PATH") {
		t.Fatalf("missing sdf: exit %d %s", code, out)
	}
	// A previous hook that fails stops the push before sdf runs.
	bin2, log2 := fakeSDF(t, filepath.Join(t.TempDir(), "bin2"))
	if _, err := UninstallGitHooks(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ncat > /dev/null\nexit 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallGitHooksWith(dir, Endpoint{Host: "127.0.0.1", HTTPPort: 9}, HookOptions{Bin: bin2}); err != nil {
		t.Fatal(err)
	}
	if code, _ := runPrePushHook(t, dir, hook, line); code != 5 {
		t.Fatalf("previous hook exit %d, want 5", code)
	}
	if _, err := os.Stat(log2); !os.IsNotExist(err) {
		t.Fatal("sdf ran after the previous hook failed")
	}
	// Without --db the hook passes no --db.
	if err := os.WriteFile(hook+".sdf-previous", []byte("#!/bin/sh\ncat > /dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _ := runPrePushHook(t, dir, hook, line); code != 0 || strings.Contains(readFile(t, log2), "--db") {
		t.Fatalf("exit %d\n%s", code, readFile(t, log2))
	}
}

func TestPrePushIsInTheTemplateAndTheChainInstructions(t *testing.T) {
	home, _ := templateEnv(t)
	res, err := InstallGitTemplate(TemplateOptions{Home: home, SetConfig: true, Hook: HookOptions{DB: "/var/sdf/db.sqlite"}}, Endpoint{Host: "127.0.0.1", HTTPPort: 4318})
	if err != nil {
		t.Fatalf("%v %s", err, res.Text)
	}
	body := readFile(t, filepath.Join(home, ".config", "sdf", "git-template", "hooks", "pre-push"))
	if !strings.Contains(body, "SDF_HOOK_KIND='pre-push'") || !strings.Contains(body, "SDF_HOOK_DB='/var/sdf/db.sqlite'") {
		t.Fatal(body)
	}

	isolateGit(t)
	dir := testRepo(t)
	husky := filepath.Join(dir, ".husky")
	if err := os.MkdirAll(husky, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "config", "core.hooksPath", ".husky")
	text, err := InstallGitHooks(dir, Endpoint{Host: "127.0.0.1", HTTPPort: 9})
	if err != ErrHooksNotActivated {
		t.Fatal(err)
	}
	chain := filepath.Join(dir, ".git", "sdf-hooks", "pre-push")
	if !strings.Contains(text.Text, "sh "+chain+" \"$@\"") || !strings.Contains(text.Text, ".husky/pre-push") || !strings.Contains(text.Text, "pre-push script sends nothing") {
		t.Fatal(text.Text)
	}
	if _, err := os.Stat(chain); err != nil {
		t.Fatal(err)
	}
}

func TestHookOptionsRefuseANewlineInTheStorePath(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	if _, err := InstallGitHooksWith(dir, Endpoint{Host: "127.0.0.1", HTTPPort: 9}, HookOptions{DB: "/tmp/a\nb"}); err == nil {
		t.Fatal("newline accepted")
	}
	if _, err := os.Stat(filepath.Join(hooksPath(t, dir), "pre-push")); !os.IsNotExist(err) {
		t.Fatal("a hook was written")
	}
}
