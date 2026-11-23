package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Grok and Qwen used to write without the shared precheck (checkWritable,
// checkAllWritable). These tests pin that they now refuse the same files the
// other agents refuse, and leave no file or backup behind.

func grokWriteReq(home string) Request {
	return Request{Agent: "grok", Home: home, GrokHome: filepath.Join(home, ".grok"), Write: true, Endpoint: endpoint()}
}

// A config.toml with no write bit is refused before anything is written,
// for this user and for root alike (the mode-bit check does not depend on
// who runs sdf). Before, copyAside left an orphan .sdf-bak and, as root,
// os.WriteFile overwrote the file.
func TestGrokReadOnlyConfigIsRefused(t *testing.T) {
	for _, root := range []bool{false, true} {
		for _, mode := range []os.FileMode{0o400, 0o444} {
			asRoot(t, root)
			clearGrokEnv(t)
			home := t.TempDir()
			config := filepath.Join(home, ".grok", "config.toml")
			body := "model = \"grok-4\"\n"
			writeFileMode(t, config, body, mode)
			_, err := Run(grokWriteReq(home))
			if err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("root=%v mode=%04o: want a read-only error, got %v", root, mode, err)
			}
			assertUntouched(t, config, body)
			if _, err := os.Stat(filepath.Join(home, ".grok", "hooks", "sdf-session.json")); !os.IsNotExist(err) {
				t.Fatalf("hook written after the refused config: %v", err)
			}
		}
	}
}

func TestGrokModeZeroConfigIsRefused(t *testing.T) {
	clearGrokEnv(t)
	home := t.TempDir()
	config := filepath.Join(home, ".grok", "config.toml")
	writeFileMode(t, config, "x = 1\n", 0)
	t.Cleanup(func() { _ = os.Chmod(config, 0o600) })
	_, err := Run(grokWriteReq(home))
	if err == nil || !strings.Contains(err.Error(), "mode 0000") || !strings.Contains(err.Error(), "chmod u+rw") {
		t.Fatal(err)
	}
	_ = os.Chmod(config, 0o600)
	assertUntouched(t, config, "x = 1\n")
}

// hooks/ is 0555: the session hook cannot be written, so config.toml must
// not get its [telemetry] table (or a backup) first.
func TestGrokUnwritableHooksDirWritesNothing(t *testing.T) {
	skipIfRoot(t)
	clearGrokEnv(t)
	home := t.TempDir()
	config := filepath.Join(home, ".grok", "config.toml")
	writeFileMode(t, config, "model = \"grok-4\"\n", 0o600)
	chmodDir(t, filepath.Join(home, ".grok", "hooks"), 0o555)
	_, err := Run(grokWriteReq(home))
	if err == nil || !strings.Contains(err.Error(), "sdf-session.json") || !strings.Contains(err.Error(), "not writable (mode 0555") {
		t.Fatal(err)
	}
	assertUntouched(t, config, "model = \"grok-4\"\n")
}

// A config.toml that already has [telemetry] is left alone, but the hook is
// still checked before it is written.
func TestGrokExistingTelemetryStillChecksTheHook(t *testing.T) {
	clearGrokEnv(t)
	home := t.TempDir()
	config := filepath.Join(home, ".grok", "config.toml")
	writeFileMode(t, config, "[telemetry]\notel_enabled = true\n", 0o600)
	hook := filepath.Join(home, ".grok", "hooks", "sdf-session.json")
	writeFileMode(t, hook, "{}\n", 0o444)
	_, err := Run(grokWriteReq(home))
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatal(err)
	}
	assertUntouched(t, hook, "{}\n")
}

func TestGrokSymlinkOutsideHomeIsRefusedAsRoot(t *testing.T) {
	asRoot(t, true)
	clearGrokEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	real := filepath.Join(t.TempDir(), "etc", "config.toml")
	writeFileMode(t, real, "x = 1\n", 0o600)
	config := filepath.Join(home, ".grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, config); err != nil {
		t.Fatal(err)
	}
	_, err := Run(grokWriteReq(home))
	if err == nil || !strings.Contains(err.Error(), "refusing to write through it as root") {
		t.Fatal(err)
	}
	if got := mustReadString(t, real); got != "x = 1\n" {
		t.Fatalf("target changed: %s", got)
	}
	if backups, _ := filepath.Glob(config + ".sdf-bak*"); len(backups) != 0 {
		t.Fatal(backups)
	}
}

// The write still goes through replaceFile: the file is replaced, the mode
// kept, the backup made.
func TestGrokWriteKeepsModeAndBacksUp(t *testing.T) {
	clearGrokEnv(t)
	home := t.TempDir()
	config := filepath.Join(home, ".grok", "config.toml")
	writeFileMode(t, config, "model = \"grok-4\"\n", 0o640)
	res, err := Run(grokWriteReq(home))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	got := mustReadString(t, config)
	if !strings.HasPrefix(got, "model = \"grok-4\"\n\n") || !strings.Contains(got, "[telemetry]") {
		t.Fatal(got)
	}
	if info, _ := os.Stat(config); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", info.Mode())
	}
	if mustReadString(t, config+".sdf-bak") != "model = \"grok-4\"\n" {
		t.Fatal("backup")
	}
}

func TestQwenReadOnlyTargetIsRefusedForRootToo(t *testing.T) {
	for _, root := range []bool{false, true} {
		for _, mode := range []os.FileMode{0o400, 0o444} {
			asRoot(t, root)
			clearQwenEnv(t)
			home := t.TempDir()
			path := writeQwen(t, home, "{}\n", mode)
			_, err := Run(qwenRequest(home, true))
			if err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("root=%v mode=%04o: %v", root, mode, err)
			}
			assertUntouched(t, path, "{}\n")
		}
	}
}

func TestQwenModeZeroIsRefusedWithAFriendlyError(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	path := writeQwen(t, home, "{}\n", 0o600)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	_, err := Run(qwenRequest(home, true))
	if err == nil || !strings.Contains(err.Error(), "mode 0000") || !strings.Contains(err.Error(), "chmod u+rw") {
		t.Fatal(err)
	}
	_ = os.Chmod(path, 0o600)
	assertUntouched(t, path, "{}\n")
}

func qwenOutsideLink(t *testing.T) (home, path, real string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	real = filepath.Join(t.TempDir(), "elsewhere", "settings.json")
	writeFileMode(t, real, "{}\n", 0o600)
	path = filepath.Join(home, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	return home, path, real
}

func TestQwenSymlinkOutsideHomeIsRefusedAsRoot(t *testing.T) {
	asRoot(t, true)
	clearQwenEnv(t)
	home, path, real := qwenOutsideLink(t)
	_, err := Run(qwenRequest(home, true))
	if err == nil || !strings.Contains(err.Error(), "refusing to write through it as root") {
		t.Fatal(err)
	}
	if got := mustReadString(t, real); got != "{}\n" {
		t.Fatalf("target changed: %s", got)
	}
	for _, p := range []string{path, real} {
		if backups, _ := filepath.Glob(p + ".sdf-bak*"); len(backups) != 0 {
			t.Fatal(backups)
		}
	}
}

func TestQwenSymlinkOutsideHomeWarns(t *testing.T) {
	notRoot(t)
	clearQwenEnv(t)
	home, _, real := qwenOutsideLink(t)
	res, err := Run(qwenRequest(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	if !strings.Contains(res.Text, "warning: ") || !strings.Contains(res.Text, "outside") {
		t.Fatal(res.Text)
	}
	if !strings.Contains(mustReadString(t, real), `"otlpEndpoint"`) {
		t.Fatal("target not written")
	}
}

// Only a target ending in exactly .json is accepted.
func TestQwenSymlinkExtensionIsCaseSensitive(t *testing.T) {
	clearQwenEnv(t)
	home := t.TempDir()
	real := filepath.Join(home, "dotfiles", "Settings.JSON")
	writeFileMode(t, real, "{}\n", 0o600)
	path := filepath.Join(home, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(qwenRequest(home, true)); err == nil || !strings.Contains(err.Error(), "not a .json file") {
		t.Fatal(err)
	}
	if got := mustReadString(t, real); got != "{}\n" {
		t.Fatalf("target changed: %s", got)
	}
}

// An existing empty settings file is refused (Qwen Code cannot load it
// either), as the other JSON agents refuse a file that is not JSON. A
// missing file is still created.
func TestQwenEmptySettingsFileIsRefused(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "whitespace": " \n\t\n", "bom": "\xEF\xBB\xBF"} {
		clearQwenEnv(t)
		home := t.TempDir()
		path := writeQwen(t, home, body, 0o600)
		res, err := Run(qwenRequest(home, true))
		if err == nil || !strings.Contains(err.Error(), "it is empty") || res.Wrote {
			t.Fatalf("%s: %v %s", name, err, res.Text)
		}
		if dry, err := Run(qwenRequest(home, false)); err != nil || !strings.Contains(dry.Text, "it is empty") {
			t.Fatalf("%s dry run: %v %s", name, err, dry.Text)
		}
		assertUntouched(t, path, body)
	}
}
