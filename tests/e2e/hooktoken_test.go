//go:build e2e

package e2e

import (
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHooksReachATokenProtectedReceiver installs the Grok and Claude Code
// session hooks and the git hooks with the real binary, runs them against an
// sdf serve that requires a token, and checks that the token comes from the
// hook's environment at run time and is never written into a hook file.
func TestHooksReachATokenProtectedReceiver(t *testing.T) {
	const token = "e2e-hook-token-5d1f0c"
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "otlp-token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := startServeArgs(t, "--otlp-token-file", tokenFile)
	parsed, err := url.Parse(srv.httpBase)
	if err != nil {
		t.Fatal(err)
	}
	port := parsed.Port()
	home := filepath.Join(dir, "home")
	grokHome := filepath.Join(dir, "grok")
	base := cleanEnv("HOME="+home, "GROK_HOME="+grokHome)

	run := func(env []string, stdin string, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return string(out)
	}
	run(base, "", binPath(t), "init", "grok", "--write", "--port", port)
	run(base, "", binPath(t), "init", "claude-code", "--write", "--port", port)
	grokCmd := hookCommand(t, filepath.Join(grokHome, "hooks", "sdf-session.json"), "SessionStart")
	claudeCmd := hookCommand(t, filepath.Join(home, ".claude", "settings.json"), "SessionStart")

	withFile := append(append([]string{}, base...), "SDF_OTLP_TOKEN_FILE="+tokenFile)
	withEnv := append(append([]string{}, base...), "SDF_OTLP_TOKEN="+token)
	run(withFile, `{"sessionId":"grok-file","cwd":"/tmp/a b"}`, "sh", "-c", grokCmd)
	run(withEnv, `{"sessionId":"grok-env","cwd":"/tmp/a b"}`, "sh", "-c", grokCmd)
	run(base, `{"sessionId":"grok-none"}`, "sh", "-c", grokCmd)
	run(withEnv, `{"session_id":"claude-env","source":"startup"}`, "sh", "-c", claudeCmd)
	run(base, `{"session_id":"claude-none","source":"startup"}`, "sh", "-c", claudeCmd)

	repo := filepath.Join(dir, "my repo")
	gitEnv := append(append([]string{}, base...), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	run(gitEnv, "", "git", "init", "-q", repo)
	run(gitEnv, "", "git", "-C", repo, "config", "user.email", "e2e@example.com")
	run(gitEnv, "", "git", "-C", repo, "config", "user.name", "e2e")
	cmd := exec.Command(binPath(t), "init", "--git-hooks", "--port", port)
	cmd.Dir = repo
	cmd.Env = gitEnv
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init --git-hooks: %v\n%s", err, out)
	}
	run(append(append([]string{}, gitEnv...), "GROK_SESSION_ID=grok-file", "SDF_OTLP_TOKEN_FILE="+tokenFile), "", "git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "linked")
	run(append(append([]string{}, gitEnv...), "GROK_SESSION_ID=grok-none"), "", "git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "rejected")

	for _, path := range []string{
		filepath.Join(grokHome, "hooks", "sdf-session.json"),
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(repo, ".git", "hooks", "post-commit"),
		filepath.Join(repo, ".git", "hooks", "prepare-commit-msg"),
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), token) {
			t.Fatalf("%s contains the token", path)
		}
	}
	srv.stop(t)

	for _, id := range []string{"grok-file", "grok-env", "claude-env"} {
		if out := summarize(t, srv.db, "--session", id); !strings.Contains(out, "sdf.session.open x1") {
			t.Fatalf("%s: no accepted open\n%s", id, out)
		}
	}
	if out := summarize(t, srv.db, "--session", "grok-file"); !strings.Contains(out, "sdf.git.commit x1") || !strings.Contains(out, "repo="+repo) {
		t.Fatalf("commit was not accepted and linked\n%s", out)
	}
	for _, id := range []string{"grok-none", "claude-none"} {
		cmd := exec.Command(binPath(t), "summary", "--db", srv.db, "--prices", "bundled", "--session", id)
		out, _ := cmd.CombinedOutput()
		if strings.Contains(string(out), "sdf.session.open") || strings.Contains(string(out), "sdf.git.commit") {
			t.Fatalf("%s was stored without the token\n%s", id, out)
		}
	}
}

func hookCommand(t *testing.T, path, event string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	for _, group := range doc.Hooks[event] {
		for _, hook := range group.Hooks {
			if strings.Contains(hook.Command, "/v1/sessions") {
				return hook.Command
			}
		}
	}
	t.Fatalf("no sdf %s hook in %s", event, path)
	return ""
}

// cleanEnv is the process environment without agent session ids, receiver
// tokens, or Grok's hook marker, plus extra.
func cleanEnv(extra ...string) []string {
	var out []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "HOME", "GROK_HOME", "SDF_OTLP_TOKEN", "SDF_OTLP_TOKEN_FILE", "GROK_HOOK_EVENT", "GROK_SESSION_ID",
			"CODEX_THREAD_ID", "CODEX_SESSION_ID", "OPENCODE_SESSION_ID", "PI_SESSION_FILE", "PI_SESSION_ID", "AI_AGENT",
			"CLAUDE_CODE_SESSION_ID", "QWEN_CODE_SESSION_ID":
			continue
		}
		out = append(out, item)
	}
	return append(out, extra...)
}
