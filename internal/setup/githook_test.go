package setup

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestGitHookInstallChainsAndUninstalls(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	hooks := hooksPath(t, dir)
	proof := filepath.Join(t.TempDir(), "proof")
	original := "#!/bin/sh\nprintf '%s\\n' chained >> \"$SDF_CHAIN_PROOF\"\n"
	if err := os.WriteFile(filepath.Join(hooks, "post-commit"), []byte(original), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, got := recordingServer(t)
	res, err := InstallGitHooks(dir, endpointOf(t, srv))
	if err != nil || !res.Wrote || !strings.Contains(res.Text, "installed sdf git hooks") {
		t.Fatalf("%v %s", err, res.Text)
	}
	if !strings.Contains(res.Text, "diff and the commit message are not sent") {
		t.Fatal(res.Text)
	}
	again, err := InstallGitHooks(dir, endpointOf(t, srv))
	if err != nil || again.Wrote || !strings.Contains(again.Text, "already installed") {
		t.Fatalf("second %v %s", err, again.Text)
	}
	if _, err := os.Stat(filepath.Join(hooks, "post-commit.sdf-previous")); err != nil {
		t.Fatal(err)
	}
	for _, name := range hookNames {
		body, err := os.ReadFile(filepath.Join(hooks, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, hookMarker) || strings.Contains(text, "git diff") || strings.Contains(text, "git show") {
			t.Fatalf("%s missing marker or reads the diff", name)
		}
		cmd := exec.Command("sh", "-n", filepath.Join(hooks, name))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sh -n %s: %v %s", name, err, out)
		}
		info, err := os.Stat(filepath.Join(hooks, name))
		if err != nil || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s not executable", name)
		}
	}
	sha := commitIn(t, dir, "commit-message-not-posted", "SDF_CHAIN_PROOF="+proof, "OPENCODE_SESSION_ID=ses_payload")
	proofBody, err := os.ReadFile(proof)
	if err != nil || !strings.Contains(string(proofBody), "chained") {
		t.Fatalf("previous hook did not run: %s %v", proofBody, err)
	}
	msg := commitLog(t, dir, sha)
	if !strings.Contains(msg, "SDF-Session: ses_payload") || strings.Count(msg, "SDF-Session:") != 1 {
		t.Fatalf("trailer %q", msg)
	}
	bodies := got.all()
	if len(bodies) != 1 {
		t.Fatalf("posts %d", len(bodies))
	}
	if strings.Contains(string(bodies[0]), "commit-message-not-posted") {
		t.Fatalf("payload contained the commit message: %s", bodies[0])
	}
	attrs := payloadAttrs(t, bodies[0])
	if attrs["session.id"] != "ses_payload" || attrs["event.name"] != "sdf.git.commit" || attrs["vcs.ref.head.revision"] != sha || attrs["sdf.git.agent"] != "opencode" {
		t.Fatalf("%v", attrs)
	}
	if _, ok := attrs["conversation.id"]; ok {
		t.Fatal("opencode payload set conversation.id")
	}
	off, err := UninstallGitHooks(dir)
	restoredHook := filepath.Join(hooks, "post-commit")
	if real, e := filepath.EvalSymlinks(restoredHook); e == nil {
		restoredHook = real
	}
	if err != nil || !strings.Contains(off.Text, "restored "+restoredHook) {
		t.Fatalf("%v %s", err, off.Text)
	}
	restored, err := os.ReadFile(filepath.Join(hooks, "post-commit"))
	if err != nil || string(restored) != original {
		t.Fatalf("restored %q %v", restored, err)
	}
	if _, err := os.Stat(filepath.Join(hooks, "post-rewrite")); !os.IsNotExist(err) {
		t.Fatal("post-rewrite still installed")
	}
}

func TestGitHookSessionIDsAndSilence(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	srv, got := recordingServer(t)
	if _, err := InstallGitHooks(dir, endpointOf(t, srv)); err != nil {
		t.Fatal(err)
	}
	stem := "2026-01-01T00-00-00-000Z_sample-pi-session"
	commitIn(t, dir, "pi subject",
		"PI_SESSION_FILE=/tmp/pi dir/"+stem+".jsonl",
		"PI_SESSION_ID=sample-pi-session",
		"http_proxy=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
	)
	commitIn(t, dir, "codex subject",
		"CODEX_THREAD_ID=019d2bae-7c39-7c73-b058-7b45e6aabef7",
		"CODEX_SESSION_ID=root-session-not-the-thread",
	)
	commitIn(t, dir, "human subject")
	commitIn(t, dir, "two agents",
		"OPENCODE_SESSION_ID=ses_one",
		"CODEX_THREAD_ID=019d2bae-7c39-7c73-b058-7b45e6aabef7",
	)
	bodies := got.all()
	if len(bodies) != 2 {
		t.Fatalf("want pi and codex only, got %d", len(bodies))
	}
	pi := payloadAttrs(t, bodies[0])
	if pi["session.id"] != stem || pi["pi.session.id"] != stem || pi["gen_ai.conversation.id"] != stem || pi["sdf.git.agent"] != "pi" || pi["gen_ai.system"] != "pi" || pi["resource.service.name"] != "pi" {
		t.Fatalf("pi %+v", pi)
	}
	codex := payloadAttrs(t, bodies[1])
	if codex["conversation.id"] != "019d2bae-7c39-7c73-b058-7b45e6aabef7" || codex["sdf.git.agent"] != "codex" || codex["resource.service.name"] != "codex_exec" {
		t.Fatalf("codex %+v", codex)
	}
	if _, ok := codex["session.id"]; ok {
		t.Fatal("codex payload set session.id")
	}
	if strings.Contains(string(bodies[1]), "root-session-not-the-thread") {
		t.Fatal("sent CODEX_SESSION_ID")
	}
	for _, body := range bodies {
		if strings.Contains(string(body), "subject") {
			t.Fatalf("payload contained a subject: %s", body)
		}
	}
	human := commitLog(t, dir, "HEAD~1")
	if strings.Contains(human, "SDF-Session:") {
		t.Fatalf("human commit gained a trailer: %s", human)
	}
	both := commitLog(t, dir, "HEAD")
	if strings.Contains(both, "SDF-Session:") {
		t.Fatalf("ambiguous agents gained a trailer: %s", both)
	}
	piMsg := commitLog(t, dir, "HEAD~3")
	if !strings.Contains(piMsg, "SDF-Session: "+stem) {
		t.Fatalf("pi trailer %q", piMsg)
	}

	old := strings.Repeat("ab", 20)
	next := strings.Repeat("cd", 20)
	cmd := exec.Command(filepath.Join(hooksPath(t, dir), "post-rewrite"), "rebase")
	cmd.Dir = dir
	cmd.Env = gitEnv("OPENCODE_SESSION_ID=ses_rewrite")
	cmd.Stdin = strings.NewReader(old + " " + next + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("post-rewrite: %v %s", err, out)
	}
	rewritten := payloadAttrs(t, got.all()[2])
	if rewritten["session.id"] != "ses_rewrite" || rewritten["vcs.ref.head.revision"] != next || rewritten["sdf.git.rewritten_from"] != old {
		t.Fatalf("%+v", rewritten)
	}
}

// GROK_SESSION_ID is what Grok CLI puts in its shell tool's environment
// (grok-build xai-grok-shell session/agent_rebuild.rs). The id below is a
// made-up UUID, not a capture.
func TestGitHookGrokSessionID(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	srv, got := recordingServer(t)
	if _, err := InstallGitHooks(dir, endpointOf(t, srv)); err != nil {
		t.Fatal(err)
	}
	id := "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	sha := commitIn(t, dir, "grok subject", "GROK_SESSION_ID="+id)
	commitIn(t, dir, "grok and codex", "GROK_SESSION_ID="+id, "CODEX_THREAD_ID=019d2bae-7c39-7c73-b058-7b45e6aabef7")
	commitIn(t, dir, "bad id", "GROK_SESSION_ID=has space")
	bodies := got.all()
	if len(bodies) != 1 {
		t.Fatalf("want the grok commit only, got %d", len(bodies))
	}
	attrs := payloadAttrs(t, bodies[0])
	if attrs["session.id"] != id || attrs["sdf.git.agent"] != "grok-cli" || attrs["resource.service.name"] != "grok-cli" || attrs["vcs.ref.head.revision"] != sha {
		t.Fatalf("%+v", attrs)
	}
	if _, ok := attrs["conversation.id"]; ok {
		t.Fatal("grok payload set conversation.id")
	}
	if strings.Contains(string(bodies[0]), "subject") {
		t.Fatalf("payload contained the subject: %s", bodies[0])
	}
	if msg := commitLog(t, dir, sha); !strings.Contains(msg, "SDF-Session: "+id) || strings.Count(msg, "SDF-Session:") != 1 {
		t.Fatalf("trailer %q", msg)
	}
	if both := commitLog(t, dir, "HEAD~1"); strings.Contains(both, "SDF-Session:") {
		t.Fatalf("ambiguous agents gained a trailer: %s", both)
	}
	if bad := commitLog(t, dir, "HEAD"); strings.Contains(bad, "SDF-Session:") {
		t.Fatalf("an invalid id gained a trailer: %s", bad)
	}
}

// CLAUDE_CODE_SESSION_ID is what Claude Code (v2.1.132 and later) sets in
// its Bash tool and hook subprocesses; it equals the hook session_id and the
// session.id on Claude Code telemetry. QWEN_CODE_SESSION_ID is what Qwen Code
// (v0.28.4) puts in its shell tool's environment; it is Config.getSessionId(),
// the session.id on Qwen telemetry. Both ids are random UUIDs; the values
// below are made up in that shape, not captures.
func TestGitHookClaudeAndQwenSessionIDs(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	srv, got := recordingServer(t)
	if _, err := InstallGitHooks(dir, endpointOf(t, srv)); err != nil {
		t.Fatal(err)
	}
	const (
		claudeID = "4f6b1c2e-8d3a-4b7e-9f21-3c5d6e7a8b90"
		qwenID   = "b2e7a0d4-5c19-4f8e-a3b6-71d0c9e45f2a"
		grokID   = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
		threadID = "019d2bae-7c39-7c73-b058-7b45e6aabef7"
	)
	claudeSHA := commitIn(t, dir, "claude subject", "CLAUDE_CODE_SESSION_ID="+claudeID)
	qwenSHA := commitIn(t, dir, "qwen subject", "QWEN_CODE_SESSION_ID="+qwenID)
	// A Qwen process started from Claude Code's Bash tool inherits
	// CLAUDE_CODE_SESSION_ID, and a Claude Code started from a Grok shell
	// inherits GROK_SESSION_ID: two ids, so the commit is not attributed.
	conflicts := [][]string{
		{"CLAUDE_CODE_SESSION_ID=" + claudeID, "QWEN_CODE_SESSION_ID=" + qwenID},
		{"CLAUDE_CODE_SESSION_ID=" + claudeID, "GROK_SESSION_ID=" + grokID},
		{"QWEN_CODE_SESSION_ID=" + qwenID, "CODEX_THREAD_ID=" + threadID},
		{"CLAUDE_CODE_SESSION_ID=" + claudeID, "OPENCODE_SESSION_ID=ses_one"},
	}
	for i, env := range conflicts {
		sha := commitIn(t, dir, "ambiguous subject", env...)
		if msg := commitLog(t, dir, sha); strings.Contains(msg, "SDF-Session:") {
			t.Fatalf("conflict %d gained a trailer: %s", i, msg)
		}
	}
	commitIn(t, dir, "bad claude id", "CLAUDE_CODE_SESSION_ID=has space")
	commitIn(t, dir, "bad qwen id", "QWEN_CODE_SESSION_ID=semi;colon")
	bodies := got.all()
	if len(bodies) != 2 {
		t.Fatalf("want the claude and qwen commits only, got %d", len(bodies))
	}
	for i, want := range []struct{ id, agent, sha, subject string }{
		{claudeID, "claude-code", claudeSHA, "claude subject"},
		{qwenID, "qwen-code", qwenSHA, "qwen subject"},
	} {
		attrs := payloadAttrs(t, bodies[i])
		if attrs["session.id"] != want.id || attrs["sdf.git.agent"] != want.agent || attrs["resource.service.name"] != want.agent || attrs["vcs.ref.head.revision"] != want.sha || attrs["event.name"] != "sdf.git.commit" {
			t.Fatalf("%s %+v", want.agent, attrs)
		}
		if _, ok := attrs["conversation.id"]; ok {
			t.Fatalf("%s payload set conversation.id", want.agent)
		}
		if strings.Contains(string(bodies[i]), "subject") {
			t.Fatalf("payload contained the subject: %s", bodies[i])
		}
		if msg := commitLog(t, dir, want.sha); !strings.Contains(msg, "SDF-Session: "+want.id) || strings.Count(msg, "SDF-Session:") != 1 {
			t.Fatalf("%s trailer %q", want.agent, msg)
		}
	}
	for _, rev := range []string{"HEAD", "HEAD~1"} {
		if bad := commitLog(t, dir, rev); strings.Contains(bad, "SDF-Session:") {
			t.Fatalf("an invalid id gained a trailer: %s", bad)
		}
	}

	old := strings.Repeat("ab", 20)
	next := strings.Repeat("cd", 20)
	cmd := exec.Command(filepath.Join(hooksPath(t, dir), "post-rewrite"), "amend")
	cmd.Dir = dir
	cmd.Env = gitEnv("QWEN_CODE_SESSION_ID=" + qwenID)
	cmd.Stdin = strings.NewReader(old + " " + next + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("post-rewrite: %v %s", err, out)
	}
	rewritten := payloadAttrs(t, got.all()[2])
	if rewritten["session.id"] != qwenID || rewritten["resource.service.name"] != "qwen-code" || rewritten["vcs.ref.head.revision"] != next || rewritten["sdf.git.rewritten_from"] != old {
		t.Fatalf("%+v", rewritten)
	}
}

func TestCodexHookServiceNameFollowsTheParent(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	// A checkout whose path ends in /codex must not be classified as the Codex CLI.
	named := filepath.Join(t.TempDir(), "codex")
	if err := os.MkdirAll(named, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, named, "init")
	runGit(t, named, "config", "user.email", "sdf-test@example.com")
	runGit(t, named, "config", "user.name", "sdf test")
	runGit(t, named, "config", "commit.gpgsign", "false")
	srv, got := recordingServer(t)
	if _, err := InstallGitHooks(dir, endpointOf(t, srv)); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallGitHooks(named, endpointOf(t, srv)); err != nil {
		t.Fatal(err)
	}
	thread := "019d2bae-7c39-7c73-b058-7b45e6aabef7"
	commitAsCodex(t, dir, "exec", "CODEX_THREAD_ID="+thread, "CODEX_SESSION_ID="+thread)
	commitAsCodex(t, dir, "exec", "CODEX_THREAD_ID="+thread, "CODEX_INTERNAL_ORIGINATOR_OVERRIDE=custom.client")
	commitAsCodex(t, dir, "exec", "CODEX_THREAD_ID="+thread, "CODEX_INTERNAL_ORIGINATOR_OVERRIDE=bad name")
	commitAsCodex(t, dir, "", "CODEX_THREAD_ID="+thread, "CODEX_INTERNAL_ORIGINATOR_OVERRIDE=custom.client")
	commitIn(t, named, "path named codex", "CODEX_THREAD_ID="+thread)
	bodies := got.all()
	if len(bodies) != 5 {
		t.Fatalf("posts %d", len(bodies))
	}
	want := []string{"codex_exec", "custom.client", "codex_exec", "codex-app-server", "codex_exec"}
	for i, name := range want {
		attrs := payloadAttrs(t, bodies[i])
		if attrs["resource.service.name"] != name || attrs["conversation.id"] != thread || attrs["sdf.git.agent"] != "codex" {
			t.Fatalf("body %d %+v", i, attrs)
		}
	}
}

func commitAsCodex(t *testing.T, dir, mode string, extra ...string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\ncd \"$SDF_REPO\" || exit 1\ngit commit --allow-empty -m \"codex parent commit\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{}
	if mode != "" {
		args = append(args, mode)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(gitEnv(extra...), "SDF_REPO="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("codex %s: %v %s", mode, err, out)
	}
}

func TestGitHookRefusesToClobber(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	hooks := hooksPath(t, dir)
	link := filepath.Join(hooks, "post-commit")
	if err := os.Symlink(filepath.Join(hooks, "post-commit.sample"), link); err != nil {
		// sample may not exist; point at /bin/sh
		if err := os.Symlink("/bin/sh", link); err != nil {
			t.Fatal(err)
		}
	}
	_, err := InstallGitHooks(dir, Endpoint{Host: "127.0.0.1", HTTPPort: 4318})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = InstallGitHooks(dir, Endpoint{Host: "127.0.0.1", HTTPPort: 4318})
	if err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatal(err)
	}
	body, err := os.ReadFile(link)
	if err != nil || string(body) != "#!/bin/sh\n" {
		t.Fatalf("non-executable hook changed: %s", body)
	}
}

func TestGitHookDoesNotTouchHusky(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	husky := filepath.Join(dir, ".husky")
	if err := os.MkdirAll(husky, 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("#!/bin/sh\necho husky\n")
	if err := os.WriteFile(filepath.Join(husky, "post-commit"), original, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "config", "core.hooksPath", ".husky")
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("config: %v %s", err, out)
	}
	res, err := InstallGitHooks(dir, Endpoint{Host: "127.0.0.1", HTTPPort: 9})
	if err != ErrHooksNotActivated {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "did not change core.hooksPath") || !strings.Contains(res.Text, ".husky/post-commit") || !strings.Contains(res.Text, "before any exit 0") {
		t.Fatal(res.Text)
	}
	got, err := os.ReadFile(filepath.Join(husky, "post-commit"))
	if err != nil || string(got) != string(original) {
		t.Fatalf("husky file changed: %s", got)
	}
	pathCmd := exec.Command("git", "-C", dir, "config", "--get", "core.hooksPath")
	pathCmd.Env = gitEnv()
	out, err := pathCmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != ".husky" {
		t.Fatalf("hooksPath %q %v", out, err)
	}
	chain := filepath.Join(dir, ".git", "sdf-hooks", "post-commit")
	if _, err := os.Stat(chain); err != nil {
		t.Fatal(err)
	}
	again, err := InstallGitHooks(dir, Endpoint{Host: "127.0.0.1", HTTPPort: 9})
	if err != ErrHooksNotActivated || !strings.Contains(again.Text, chain) {
		t.Fatalf("%v %s", err, again.Text)
	}
}

func TestGitHookRejectsAPublicHost(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	_, err := InstallGitHooks(dir, Endpoint{Host: "203.0.113.5", HTTPPort: 4318})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatal(err)
	}
	hooks := hooksPath(t, dir)
	if _, err := os.Stat(filepath.Join(hooks, "post-commit")); !os.IsNotExist(err) {
		t.Fatal("hook written for a public host")
	}
}

func TestGitHookWorksFromALinkedWorktree(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	commitIn(t, dir, "base")
	wt := filepath.Join(t.TempDir(), "wt")
	cmd := exec.Command("git", "-C", dir, "worktree", "add", "--detach", wt, "HEAD")
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree: %v %s", err, out)
	}
	srv, got := recordingServer(t)
	if _, err := InstallGitHooks(dir, endpointOf(t, srv)); err != nil {
		t.Fatal(err)
	}
	commitIn(t, wt, "from worktree", "OPENCODE_SESSION_ID=ses_worktree")
	bodies := got.all()
	if len(bodies) != 1 {
		t.Fatalf("posts %d", len(bodies))
	}
	attrs := payloadAttrs(t, bodies[0])
	if attrs["session.id"] != "ses_worktree" {
		t.Fatal(attrs)
	}
	gotRepo, _ := filepath.EvalSymlinks(attrs["sdf.git.repo"])
	wantRepo, _ := filepath.EvalSymlinks(wt)
	if gotRepo != wantRepo {
		t.Fatalf("repo %s want %s", attrs["sdf.git.repo"], wt)
	}
	mainHooks, _ := filepath.EvalSymlinks(hooksPath(t, dir))
	wtHooks, _ := filepath.EvalSymlinks(hooksPath(t, wt))
	if mainHooks != wtHooks {
		t.Fatalf("worktree hooks dir %s main %s", wtHooks, mainHooks)
	}
}

func TestGitHookCommitSucceedsWhenTheReceiverIsDown(t *testing.T) {
	isolateGit(t)
	dir := testRepo(t)
	if _, err := InstallGitHooks(dir, Endpoint{Host: "127.0.0.1", HTTPPort: 1}); err != nil {
		t.Fatal(err)
	}
	sha := commitIn(t, dir, "offline", "OPENCODE_SESSION_ID=ses_offline")
	msg := commitLog(t, dir, sha)
	if !strings.Contains(msg, "SDF-Session: ses_offline") {
		t.Fatalf("trailer missing while receiver is down: %s", msg)
	}
}

func TestGitHookEscapesTheCheckoutPath(t *testing.T) {
	isolateGit(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, `qu"ote`)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "init")
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	for _, kv := range [][2]string{{"user.email", "sdf-test@example.com"}, {"user.name", "sdf test"}, {"commit.gpgsign", "false"}} {
		cmd = exec.Command("git", "-C", dir, "config", kv[0], kv[1])
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("config: %v %s", err, out)
		}
	}
	srv, got := recordingServer(t)
	if _, err := InstallGitHooks(dir, endpointOf(t, srv)); err != nil {
		t.Fatal(err)
	}
	commitIn(t, dir, "quoted", "OPENCODE_SESSION_ID=ses_quote")
	if len(got.all()) != 1 {
		t.Fatalf("posts %d", len(got.all()))
	}
	attrs := payloadAttrs(t, got.all()[0])
	// git rev-parse resolves the /var -> /private/var symlink on macOS, so
	// compare the real paths. The quote in the directory name is the point.
	gotRepo, _ := filepath.EvalSymlinks(attrs["sdf.git.repo"])
	wantRepo, _ := filepath.EvalSymlinks(dir)
	if gotRepo != wantRepo || attrs["session.id"] != "ses_quote" {
		t.Fatalf("%+v", attrs)
	}
}

type capture struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *capture) all() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.bodies))
	copy(out, c.bodies)
	return out
}

func recordingServer(t *testing.T) (*httptest.Server, *capture) {
	t.Helper()
	got := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.mu.Lock()
		got.bodies = append(got.bodies, body)
		got.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func endpointOf(t *testing.T, srv *httptest.Server) Endpoint {
	t.Helper()
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return Endpoint{Host: host, HTTPPort: n}
}

type otlpAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

func payloadAttrs(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var doc struct {
		ResourceLogs []struct {
			Resource struct {
				Attributes []otlpAttr `json:"attributes"`
			} `json:"resource"`
			ScopeLogs []struct {
				LogRecords []struct {
					Body struct {
						StringValue string `json:"stringValue"`
					} `json:"body"`
					Attributes []otlpAttr `json:"attributes"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("payload is not JSON: %v %s", err, body)
	}
	if len(doc.ResourceLogs) != 1 || len(doc.ResourceLogs[0].ScopeLogs) != 1 || len(doc.ResourceLogs[0].ScopeLogs[0].LogRecords) != 1 {
		t.Fatalf("shape %s", body)
	}
	rec := doc.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.Body.StringValue != "sdf.git.commit" {
		t.Fatalf("body %q", rec.Body.StringValue)
	}
	out := map[string]string{}
	for _, item := range doc.ResourceLogs[0].Resource.Attributes {
		out["resource."+item.Key] = item.Value.StringValue
	}
	for _, item := range rec.Attributes {
		out[item.Key] = item.Value.StringValue
	}
	if out["resource.service.name"] == "" {
		t.Fatalf("no service name %s", body)
	}
	return out
}

func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	for _, key := range []string{"PI_SESSION_FILE", "PI_SESSION_ID", "OPENCODE_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SESSION_ID", "GROK_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "QWEN_CODE_SESSION_ID", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE", "AI_AGENT"} {
		t.Setenv(key, "")
	}
}

func gitEnv(extra ...string) []string {
	drop := map[string]bool{
		"PI_SESSION_FILE": true, "PI_SESSION_ID": true, "OPENCODE_SESSION_ID": true,
		"CODEX_THREAD_ID": true, "CODEX_SESSION_ID": true, "GROK_SESSION_ID": true, "CLAUDE_CODE_SESSION_ID": true, "QWEN_CODE_SESSION_ID": true,
		"CODEX_INTERNAL_ORIGINATOR_OVERRIDE": true, "AI_AGENT": true,
	}
	var out []string
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if drop[key] {
			continue
		}
		out = append(out, env)
	}
	out = append(out, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	return append(out, extra...)
}

func testRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "sdf-test@example.com")
	runGit(t, dir, "config", "user.name", "sdf test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v %s", args, err, out)
	}
}

func hooksPath(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-path", "hooks")
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func commitIn(t *testing.T, dir, message string, extra ...string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", message)
	cmd.Env = gitEnv(extra...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	cmd = exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func commitLog(t *testing.T, dir, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "log", "-1", "--format=%B", rev)
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("log: %v %s", err, out)
	}
	return string(out)
}
