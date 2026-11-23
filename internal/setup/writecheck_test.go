package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func asRoot(t *testing.T, root bool) {
	t.Helper()
	old := runningAsRoot
	runningAsRoot = func() bool { return root }
	t.Cleanup(func() { runningAsRoot = old })
}

func notRoot(t *testing.T) { t.Helper(); asRoot(t, false) }

// skipIfRoot skips tests that rely on directory permissions: root may create
// files in a 0555 directory.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
}

func chmodDir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// OpenCode writes ~/.config/sdf/opencode.env, then the plugin under
// ~/.config/opencode/plugins. The plugin does not exist and its parent is
// 0555, so the precheck must refuse before the env file is written.
func TestMissingSecondFileUnderUnwritableDirWritesNothing(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	chmodDir(t, filepath.Join(home, ".config", "opencode"), 0o555)
	_, err := Run(Request{Agent: "opencode", Home: home, Write: true, Endpoint: endpoint()})
	if err == nil || !strings.Contains(err.Error(), "is not writable (mode 0555") || !strings.Contains(err.Error(), "sdf-session.js") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "sdf", "opencode.env")); !os.IsNotExist(err) {
		t.Fatal("opencode.env was written before the refused plugin", err)
	}
}

func TestMissingSecondFileUnderUnwritableDirPiAndCodex(t *testing.T) {
	skipIfRoot(t)
	// Pi: project settings are fine, the extensions directory is missing
	// under a 0555 .pi.
	dir := t.TempDir()
	settings := filepath.Join(dir, ".pi", "settings.json")
	writeFileMode(t, settings, "{}\n", 0o644)
	chmodDir(t, filepath.Join(dir, ".pi"), 0o555)
	if _, err := Run(Request{Agent: "pi", Home: t.TempDir(), ProjectDir: dir, Write: true, Endpoint: endpoint()}); err == nil || !strings.Contains(err.Error(), "mode 0555") {
		t.Fatal(err)
	}
	assertUntouched(t, settings, "{}\n")
	// Codex: config.toml exists in a writable file, hooks.json is missing,
	// and CODEX_HOME is 0555 (the rename of config.toml would fail too).
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	config := filepath.Join(codexHome, "config.toml")
	writeFileMode(t, config, "model = \"x\"\n", 0o600)
	chmodDir(t, codexHome, 0o555)
	if _, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: endpoint()}); err == nil || !strings.Contains(err.Error(), "mode 0555") {
		t.Fatal(err)
	}
	assertUntouched(t, config, "model = \"x\"\n")
}

func TestExistingFileInReadOnlyDirIsRefusedWithAFriendlyError(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "settings.json")
	writeFileMode(t, path, "{}\n", 0o600)
	chmodDir(t, filepath.Dir(path), 0o555)
	_, err := Run(geminiRequest(home, true))
	if err == nil || !strings.Contains(err.Error(), "directory "+filepath.Dir(path)+" is not writable (mode 0555") || !strings.Contains(err.Error(), "fix its permissions") {
		t.Fatal(err)
	}
	assertUntouched(t, path, "{}\n")
}

func TestModeZeroFileHasAFriendlyError(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "settings.json")
	writeFileMode(t, path, "{}\n", 0)
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	_, err := Run(geminiRequest(home, true))
	// Non-root fails the read ("cannot be read"), root the mode check.
	if err == nil || !strings.Contains(err.Error(), "mode 0000") || !strings.Contains(err.Error(), "chmod u+rw") {
		t.Fatal(err)
	}
	_ = os.Chmod(path, 0o600)
	assertUntouched(t, path, "{}\n")
}

func outsideLink(t *testing.T) (home, path, real string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	real = filepath.Join(t.TempDir(), "elsewhere", "settings.json")
	writeFileMode(t, real, "{}\n", 0o600)
	path = filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	return home, path, real
}

func TestSymlinkOutsideConfigAndHomeWarns(t *testing.T) {
	notRoot(t)
	home, _, real := outsideLink(t)
	res, err := Run(geminiRequest(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	if !strings.Contains(res.Text, "warning: ") || !strings.Contains(res.Text, "outside") || !strings.Contains(res.Text, realPath(real)) {
		t.Fatal(res.Text)
	}
	if !strings.Contains(mustReadString(t, real), `"otlpEndpoint"`) {
		t.Fatal("target not written")
	}
}

func TestSymlinkOutsideConfigAndHomeIsRefusedAsRoot(t *testing.T) {
	asRoot(t, true)
	home, path, real := outsideLink(t)
	_, err := Run(geminiRequest(home, true))
	if err == nil || !strings.Contains(err.Error(), "refusing to write through it as root") {
		t.Fatal(err)
	}
	if got := mustReadString(t, real); got != "{}\n" {
		t.Fatalf("target changed: %s", got)
	}
	if backups, _ := filepath.Glob(path + ".sdf-bak*"); len(backups) != 0 {
		t.Fatal(backups)
	}
	// A second file is refused before the first is written, the same way.
	codexHome := filepath.Join(home, "codex")
	hooks := filepath.Join(codexHome, "hooks.json")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, hooks); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: endpoint()}); err == nil || !strings.Contains(err.Error(), "as root") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(codexHome, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("config.toml written before the refused hooks.json", err)
	}
}

func TestSymlinkInsideHomeOrConfigDirIsQuiet(t *testing.T) {
	asRoot(t, true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	real := filepath.Join(home, "dotfiles", "gemini.json")
	writeFileMode(t, real, "{}\n", 0o600)
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	res, err := Run(geminiRequest(home, true))
	if err != nil || strings.Contains(res.Text, "warning: ") {
		t.Fatal(err, res.Text)
	}
	// Inside the config dir with $HOME elsewhere is fine too.
	t.Setenv("HOME", t.TempDir())
	if msg := linkEscape(path, filepath.Join(filepath.Dir(path), "real.json")); msg != "" {
		t.Fatal(msg)
	}
	if msg := linkEscape(path, real); msg == "" {
		t.Fatal("outside both roots was not reported")
	}
}
