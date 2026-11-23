package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	// An earlier form: current command without timeout, plus a duplicate,
	// sitting inside the user's matcher group.
	old := claudeGuard + decisionBriefCommand
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
	if len(forms) != 2 {
		t.Fatalf("testdata has %d forms, want 2", len(forms))
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

	writeFakeSdf("#!/bin/sh\nexit 1\n")
	out, err = run(decisionBriefCommand, nil)
	if err != nil || out != "" {
		t.Fatalf("nonzero: err=%v out=%q", err, out)
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
