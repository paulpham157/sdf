package setup

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hasSessionStartCommand(t *testing.T, path, want string) bool {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	for _, g := range root.Hooks["SessionStart"] {
		for _, h := range g.Hooks {
			if h.Command == want {
				return true
			}
		}
	}
	return false
}

func requireSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not available")
	}
}

func briefHook(command string) map[string]any {
	return map[string]any{"type": "command", "command": command}
}

func sessionStartBriefTimeouts(t *testing.T, path string) []int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string  `json:"type"`
				Command string  `json:"command"`
				Timeout float64 `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, g := range root.Hooks["SessionStart"] {
		for _, h := range g.Hooks {
			if isSdfDecisionBriefHook(map[string]any{"type": h.Type, "command": h.Command}) {
				out = append(out, int(h.Timeout))
			}
		}
	}
	return out
}

func TestClaudeAndCodexInitInstallTheDecisionBriefHook(t *testing.T) {
	home := t.TempDir()
	claudePath := filepath.Join(home, ".claude", "settings.json")
	writeSettings(t, claudePath, userSettings)
	if res := claudeInit(t, home, 4318); !res.Wrote {
		t.Fatal(res.Text)
	}
	sameList(t, "claude SessionStart", eventCommands(t, claudePath, "SessionStart"), []string{
		"startup|echo user-start-1", "|echo user-start-2", "|sdf http://127.0.0.1:4318/v1/sessions", "|sdf-brief",
	})
	sameList(t, "claude PreToolUse", eventCommands(t, claudePath, "PreToolUse"), []string{"Bash|echo pre"})
	if got := sessionStartBriefTimeouts(t, claudePath); len(got) != 1 || got[0] != decisionBriefTimeout {
		t.Fatalf("claude brief timeouts %v, want [%d]", got, decisionBriefTimeout)
	}
	if decisionBriefCommandFor("claude-code") != claudeGuard+decisionBriefCommand {
		t.Fatal("claude brief command lost the GROK_HOOK_EVENT guard")
	}
	if !hasSessionStartCommand(t, claudePath, decisionBriefCommandFor("claude-code")) {
		t.Fatal("claude brief hook is missing the GROK_HOOK_EVENT guard")
	}

	codexHome := filepath.Join(home, "codex")
	codexPath := filepath.Join(codexHome, "hooks.json")
	res, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	sameList(t, "codex SessionStart", eventCommands(t, codexPath, "SessionStart"), []string{
		"|sdf http://127.0.0.1:4318/v1/sessions", "|sdf-brief",
	})
	if got := sessionStartBriefTimeouts(t, codexPath); len(got) != 1 || got[0] != decisionBriefTimeout {
		t.Fatalf("codex brief timeouts %v, want [%d]", got, decisionBriefTimeout)
	}
	codexBody, _ := os.ReadFile(codexPath)
	if strings.Contains(string(codexBody), "GROK_HOOK_EVENT") {
		t.Fatal("codex brief hook should not have the Claude Grok guard")
	}
	if strings.Contains(string(codexBody), "PreToolUse") {
		t.Fatal("codex init should not write PreToolUse")
	}
}

func TestDecisionBriefInitRerunIsANoOp(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	claudeInit(t, home, 4318)
	before, _ := os.ReadFile(path)
	if res := claudeInit(t, home, 4318); res.Wrote {
		t.Fatal(res.Text)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("second claude init changed settings.json")
	}

	codexHome := filepath.Join(home, "codex")
	res, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	hooks := filepath.Join(codexHome, "hooks.json")
	before, _ = os.ReadFile(hooks)
	again, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || again.Wrote {
		t.Fatal(err, again.Text)
	}
	after, _ = os.ReadFile(hooks)
	if string(before) != string(after) {
		t.Fatal("second codex init changed hooks.json")
	}
}

func TestDecisionBriefUpdatesOldHookInPlaceAndDedupes(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	// An earlier unreleased form without exec or timeout, plus a duplicate,
	// sitting inside the user's matcher group.
	old := claudeGuard + decisionBriefCommandV1
	settings := map[string]any{"hooks": map[string]any{
		"SessionStart": []any{
			map[string]any{"matcher": "startup", "hooks": []any{
				map[string]any{"type": "command", "command": "echo user-start"},
				map[string]any{"type": "command", "command": old},
			}},
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": old}}},
			map[string]any{"matcher": "resume", "hooks": []any{map[string]any{"type": "command", "command": "echo resume"}}},
		},
		"PreToolUse": []any{
			map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "echo pre"}}},
		},
	}}
	body, _ := json.MarshalIndent(settings, "", "  ")
	writeSettings(t, path, string(body))
	if res := claudeInit(t, home, 4318); !res.Wrote {
		t.Fatal(res.Text)
	}
	sameList(t, "SessionStart", eventCommands(t, path, "SessionStart"), []string{
		"startup|echo user-start", "startup|sdf-brief", "resume|echo resume", "|sdf http://127.0.0.1:4318/v1/sessions",
	})
	sameList(t, "PreToolUse", eventCommands(t, path, "PreToolUse"), []string{"Bash|echo pre"})
	if got := sessionStartBriefTimeouts(t, path); len(got) != 1 || got[0] != decisionBriefTimeout {
		t.Fatalf("brief timeouts after upgrade %v, want [%d]", got, decisionBriefTimeout)
	}
	if res := claudeInit(t, home, 4318); res.Wrote {
		t.Fatal("rerun after the brief upgrade changed the file")
	}
}

func TestDecisionBriefDoesNotTakeOverAUserBriefHook(t *testing.T) {
	user := "sdf decision brief"
	if isSdfDecisionBriefHook(briefHook(user)) {
		t.Fatal("a bare `sdf decision brief` was treated as sdf's hook")
	}
	wrapped := decisionBriefCommand + "; echo mine"
	if isSdfDecisionBriefHook(briefHook(wrapped)) {
		t.Fatal("a wrapped brief command was treated as sdf's hook")
	}
	prefixed := "echo mine; " + decisionBriefCommand
	if isSdfDecisionBriefHook(briefHook(prefixed)) {
		t.Fatal("a prefixed brief command was treated as sdf's hook")
	}

	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	writeSettings(t, path, `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "sdf decision brief"}]}]}}`)
	claudeInit(t, home, 4318)
	sameList(t, "SessionStart", eventCommands(t, path, "SessionStart"), []string{
		"|sdf decision brief", "|sdf http://127.0.0.1:4318/v1/sessions", "|sdf-brief",
	})
}

func TestDecisionBriefRecognizerMatchesWrittenFormsOnly(t *testing.T) {
	forms := sdfDecisionBriefForms(t)
	if len(forms) != 6 {
		t.Fatalf("testdata has %d forms, want 6 (current pair, then the exec and || exit 0 pairs)", len(forms))
	}
	for _, form := range forms {
		hook := briefHook(form[1])
		if !isSdfDecisionBriefHook(hook) {
			t.Errorf("%s brief hook sdf wrote is not recognized: %s", form[0], form[1])
		}
		if isSdfSessionHook(hook, form[0]) {
			t.Errorf("%s brief hook was treated as a session-edge hook", form[0])
		}
	}
	if decisionBriefCommandFor("claude-code") != forms[0][1] || decisionBriefCommandFor("codex") != forms[1][1] {
		t.Fatal("testdata does not match the current generators")
	}
	for _, url := range []string{"http://127.0.0.1:4318/v1/sessions", "http://example.test:4318/v1/sessions"} {
		for tool, command := range claudeHooks(url) {
			if isSdfDecisionBriefHook(briefHook(command)) {
				t.Errorf("claude %s edge hook recognized as a brief hook", tool)
			}
		}
		for event, command := range codexHookCommands(url) {
			if isSdfDecisionBriefHook(briefHook(command)) {
				t.Errorf("codex %s edge hook recognized as a brief hook", event)
			}
		}
	}
	if isSdfDecisionBriefHook(map[string]any{"type": "prompt", "command": decisionBriefCommand}) {
		t.Error("a non-command hook was treated as the brief hook")
	}
}

func sdfDecisionBriefForms(t *testing.T) [][2]string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "sdf_decision_brief_hooks.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	var out [][2]string
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tool, command, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("bad line %q", line)
		}
		out = append(out, [2]string{tool, command})
	}
	return out
}

func TestDecisionBriefCommandFailOpen(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	sdfBin := filepath.Join(dir, "sdf")
	marker := filepath.Join(dir, "ran")

	writeFakeSdf := func(script string) {
		t.Helper()
		if err := os.WriteFile(sdfBin, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	without := func(key string, env []string) []string {
		prefix := key + "="
		var out []string
		for _, item := range env {
			if !strings.HasPrefix(item, prefix) {
				out = append(out, item)
			}
		}
		return out
	}
	run := func(command string, extraEnv []string) (stdout string, err error) {
		t.Helper()
		cmd := exec.Command("sh", "-c", command)
		env := without("GROK_HOOK_EVENT", without("PATH", os.Environ()))
		env = append(env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd.Env = append(env, extraEnv...)
		cmd.Dir = dir
		out, err := cmd.Output()
		return string(out), err
	}

	writeFakeSdf("#!/bin/sh\necho 'constraint: do not push'\n")
	out, err := run(decisionBriefCommand, nil)
	if err != nil || out != "constraint: do not push\n" {
		t.Fatalf("success: err=%v out=%q", err, out)
	}

	const unavailable = "sdf decision brief unavailable: run sdf decision list\n"
	writeFakeSdf("#!/bin/sh\necho 'sdf decision brief unavailable: run sdf decision list'\n")
	out, err = run(decisionBriefCommand, nil)
	if err != nil || out != unavailable {
		t.Fatalf("unavailable line: err=%v out=%q", err, out)
	}

	const tsv = "constraint\tkeep-local\tuser\tDo not push.\nblocked\tship-it\tagent\tPush now.\n"
	writeFakeSdf("#!/bin/sh\nprintf '%s' " + shQuote(tsv) + "\n")
	out, err = run(decisionBriefCommand, nil)
	if err != nil || out != tsv {
		t.Fatalf("tsv: err=%v out=%q want %q", err, out, tsv)
	}

	payload := strings.Repeat("A", 2047) + "\n"
	payloadPath := filepath.Join(dir, "payload")
	if err := os.WriteFile(payloadPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFakeSdf("#!/bin/sh\ncat " + shQuote(payloadPath) + "\n")
	out, err = run(decisionBriefCommand, nil)
	if err != nil || out != payload {
		t.Fatalf("2048-byte payload: err=%v len=%d want 2048", err, len(out))
	}

	writeFakeSdf("#!/bin/sh\necho 'usage: sdf decision brief [--db PATH]'\nexit 2\n")
	out, err = run(decisionBriefCommand, nil)
	if err != nil || out != "" {
		t.Fatalf("usage exit 2: err=%v out=%q", err, out)
	}

	writeFakeSdf("#!/bin/sh\necho leaked\nexit 1\n")
	out, err = run(decisionBriefCommand, nil)
	if err != nil || out != "" {
		t.Fatalf("exit 1: err=%v out=%q", err, out)
	}

	writeFakeSdf("#!/bin/sh\necho leaked\nexit 139\n")
	out, err = run(decisionBriefCommand, nil)
	if err != nil || out != "" {
		t.Fatalf("exit 139: err=%v out=%q", err, out)
	}

	writeFakeSdf("#!/bin/sh\necho leaked\nkill -9 $$\n")
	out, err = run(decisionBriefCommand, nil)
	if err != nil || out != "" {
		t.Fatalf("kill 137: err=%v out=%q", err, out)
	}

	writeFakeSdf("#!/bin/sh\nexit 0\n")
	out, err = run(decisionBriefCommand, nil)
	if err != nil || out != "" {
		t.Fatalf("empty: err=%v out=%q", err, out)
	}

	emptyPath := filepath.Join(dir, "empty")
	if err := os.Mkdir(emptyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", decisionBriefCommand)
	cmd.Env = append(envWithout("PATH"), "PATH="+emptyPath)
	out2, err := cmd.Output()
	if err != nil || string(out2) != "" {
		t.Fatalf("missing sdf: err=%v out=%q", err, out2)
	}

	writeFakeSdf("#!/bin/sh\ntouch " + marker + "\necho leaked\n")
	_ = os.Remove(marker)
	out, err = run(claudeGuard+decisionBriefCommand, []string{"GROK_HOOK_EVENT=session_start"})
	if err != nil || out != "" {
		t.Fatalf("grok guard: err=%v out=%q", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("claude brief hook ran sdf under GROK_HOOK_EVENT")
	}
	out, err = run(claudeGuard+decisionBriefCommand, []string{"GROK_HOOK_EVENT="})
	if err != nil || out != "leaked\n" {
		t.Fatalf("claude brief without grok: err=%v out=%q", err, out)
	}
}

func TestGrokInitDoesNotInstallADecisionBriefHook(t *testing.T) {
	home := t.TempDir()
	res, err := Run(Request{Agent: "grok", Home: home, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	path := filepath.Join(home, ".grok", "hooks", "sdf-session.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "decision brief") || strings.Contains(text, "sdf-brief") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "SessionStart") || !strings.Contains(text, "/v1/sessions") {
		t.Fatal(text)
	}
}

func TestOpenCodeAndPiPluginsAreUnchangedByTheBriefHook(t *testing.T) {
	home := t.TempDir()
	res, err := Run(Request{Agent: "opencode", Home: home, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	plugin, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "plugins", "sdf-session.js"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plugin), "decision brief") {
		t.Fatal("opencode plugin mentions decision brief")
	}

	dir := t.TempDir()
	res, err = Run(Request{Agent: "pi", Home: home, ProjectDir: dir, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	ext, err := os.ReadFile(filepath.Join(dir, ".pi", "extensions", "sdf-session.js"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ext), "decision brief") {
		t.Fatal("pi extension mentions decision brief")
	}
}

func TestDecisionBriefKeepsAUserTunedTimeout(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	settings := map[string]any{"hooks": map[string]any{
		"SessionStart": []any{
			map[string]any{"hooks": []any{map[string]any{
				"type":    "command",
				"command": claudeGuard + decisionBriefCommandV1,
				"timeout": 5,
			}}},
		},
	}}
	body, _ := json.MarshalIndent(settings, "", "  ")
	writeSettings(t, path, string(body))
	if res := claudeInit(t, home, 4318); !res.Wrote {
		t.Fatal(res.Text)
	}
	if !hasSessionStartCommand(t, path, decisionBriefCommandFor("claude-code")) {
		t.Fatal("old brief command was not upgraded in place")
	}
	if got := sessionStartBriefTimeouts(t, path); len(got) != 1 || got[0] != 5 {
		t.Fatalf("brief timeouts %v, want [5]", got)
	}
	if res := claudeInit(t, home, 4318); res.Wrote {
		t.Fatal("rerun reset a user-tuned timeout")
	}
	if got := sessionStartBriefTimeouts(t, path); len(got) != 1 || got[0] != 5 {
		t.Fatalf("rerun brief timeouts %v, want [5]", got)
	}
}

func TestCodexInitAsksToTrustHooks(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	req := Request{Agent: "codex", Home: home, CodexHome: codexHome, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}}
	preview, err := Run(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.Text, "Review and trust the new or changed sdf hooks in Codex's /hooks") {
		t.Fatalf("dry-run missing trust note:\n%s", preview.Text)
	}
	if !strings.Contains(preview.Text, "trust is tied to the hook hash") {
		t.Fatalf("dry-run missing hash wording:\n%s", preview.Text)
	}
	if !strings.Contains(preview.Text, "sdf-telemetry.sqlite in the session working directory") {
		t.Fatalf("dry-run missing store-path note:\n%s", preview.Text)
	}

	req.Write = true
	res, err := Run(req)
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	if !strings.Contains(res.Text, "Review and trust the new or changed sdf hooks in Codex's /hooks") {
		t.Fatalf("write missing trust note:\n%s", res.Text)
	}

	again, err := Run(req)
	if err != nil || again.Wrote {
		t.Fatal(err, again.Text)
	}
	if strings.Contains(again.Text, "Review and trust the new or changed sdf hooks in Codex's /hooks") {
		t.Fatalf("no-op rerun still asked to re-trust unchanged hooks:\n%s", again.Text)
	}
	if !strings.Contains(again.Text, "sdf-telemetry.sqlite in the session working directory") {
		t.Fatalf("no-op rerun dropped the store-path note:\n%s", again.Text)
	}
}

func TestDecisionBriefUpgradesEveryUnreleasedForm(t *testing.T) {
	forms := []struct {
		name, command string
	}{
		{"v1 || exit 0 (933ea35)", decisionBriefCommandV1},
		{"v2 exec (99e874a)", decisionBriefCommandV2},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".claude", "settings.json")
			settings := map[string]any{"hooks": map[string]any{
				"SessionStart": []any{
					map[string]any{"hooks": []any{
						map[string]any{"type": "command", "command": claudeGuard + form.command, "timeout": 5},
						map[string]any{"type": "command", "command": claudeGuard + decisionBriefCommandV1},
					}},
				},
			}}
			body, _ := json.MarshalIndent(settings, "", "  ")
			writeSettings(t, path, string(body))
			if res := claudeInit(t, home, 4318); !res.Wrote {
				t.Fatal(res.Text)
			}
			if !hasSessionStartCommand(t, path, decisionBriefCommandFor("claude-code")) {
				t.Fatal("old brief command was not upgraded in place")
			}
			if got := sessionStartBriefTimeouts(t, path); len(got) != 1 || got[0] != 5 {
				t.Fatalf("want one brief hook with timeout 5, got %v", got)
			}
		})
	}
}

func TestDecisionBriefHookStdoutClosesIfShellIsKilled(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	pidFile := filepath.Join(dir, "sdf.pid")
	bin := filepath.Join(dir, "sdf")
	// Signal start with a shell redirect, not touch(1). PATH keeps the
	// temp dir first so this fake sdf wins, then the process PATH so
	// sleep(1) still resolves. A PATH of only the temp dir left the
	// start marker unwritten and failed the test with "fake sdf did
	// not start".
	script := "#!/bin/sh\nprintf '%s\\n' $$ > " + shQuote(pidFile) + "\n: > " + shQuote(ready) + "\nsleep 30\necho leaked\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", "-c", decisionBriefCommand)
	cmd.Dir = dir
	sysPath := os.Getenv("PATH")
	if sysPath == "" {
		sysPath = "/bin:/usr/bin"
	}
	cmd.Env = append(envWithout("PATH"), "PATH="+dir+string(os.PathListSeparator)+sysPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if body, err := os.ReadFile(pidFile); err == nil {
			if proc, err := os.FindProcess(atoiPID(t, string(body))); err == nil {
				_ = proc.Kill()
			}
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake sdf did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(stdout)
		done <- b
	}()
	select {
	case out := <-done:
		if len(out) != 0 {
			t.Fatalf("hook stdout after killing sh: %q", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook stdout stayed open after sh was killed")
	}
}

func atoiPID(t *testing.T, s string) int {
	t.Helper()
	s = strings.TrimSpace(s)
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("pid %q", s)
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		t.Fatalf("pid %q", s)
	}
	return n
}

// TestDecisionBriefHookCommandWithRealBinaryEmptyDir checks the missing-DB
// case: no sdf-telemetry.sqlite in an empty directory still prints nothing,
// exits 0, and does not create a store. It does not cover an existing but
// unreadable store (that path now prints one "unavailable" line and exits 0).
func TestDecisionBriefHookCommandWithRealBinaryEmptyDir(t *testing.T) {
	requireSh(t)
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "sdf")
	build := exec.Command(goBin, "build", "-o", bin, "./cmd/sdf")
	build.Dir = moduleRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	work := t.TempDir()
	direct := exec.Command(bin, "decision", "brief")
	direct.Dir = work
	var directOut, directErr bytes.Buffer
	direct.Stdout, direct.Stderr = &directOut, &directErr
	if err := direct.Run(); err != nil {
		t.Fatalf("sdf decision brief: %v\nstdout=%q stderr=%q", err, directOut.String(), directErr.String())
	}
	if directOut.Len() != 0 || directErr.Len() != 0 {
		t.Fatalf("brief in an empty dir printed stdout=%q stderr=%q", directOut.String(), directErr.String())
	}

	cmd := exec.Command("sh", "-c", decisionBriefCommand)
	cmd.Dir = work
	cmd.Env = append(envWithout("PATH"), "PATH="+binDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("hook exited: %v\nstdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("hook printed stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(work, "sdf-telemetry.sqlite")); !os.IsNotExist(err) {
		t.Fatal("brief created a store in the empty directory")
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
