package cli_test

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/store"
)

// These tests drive the pre-push hook the way a user does: a real sdf
// binary built from this module, sdf init --git-hooks in a real checkout,
// commits made through the installed hooks into a running receiver, and
// git push to a local bare remote. They use only the binary and the
// store and server packages, so on a tree without the pre-push hook they
// build and fail on behavior.

var (
	prePushBinOnce sync.Once
	prePushBin     string
	prePushBinErr  error
)

func builtSDF(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("git hooks are POSIX sh")
	}
	root := moduleRoot(t)
	prePushBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sdf-prepush-bin")
		if err != nil {
			prePushBinErr = err
			return
		}
		bin := filepath.Join(dir, "sdf")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/sdf")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			prePushBinErr = errors.New("go build: " + err.Error() + "\n" + string(out))
			return
		}
		prePushBin = bin
	})
	if prePushBinErr != nil {
		t.Fatal(prePushBinErr)
	}
	// Each test gets its own copy, so a test can delete it.
	dst := filepath.Join(t.TempDir(), "bin", "sdf")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(prePushBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, body, 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}

type pushFixture struct {
	t      *testing.T
	bin    string
	repo   string
	remote string
	db     string
	port   string
	srv    *server.Server
	store  *store.Store
	path   string
}

// newPushFixture is a checkout with a bare remote "origin", a receiver on
// a free port writing db, and one pushed root commit on main.
func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	isolateAgentGit(t)
	bin := builtSDF(t)
	dir := t.TempDir()
	f := &pushFixture{t: t, bin: bin, repo: filepath.Join(dir, "repo"), remote: filepath.Join(dir, "remote.git"), db: filepath.Join(dir, "telemetry.sqlite")}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// PATH holds git and the shell tools, never sdf: the hook must use the
	// binary sdf init recorded, or report that it has none.
	f.path = filepath.Dir(gitPath) + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin"
	f.git(dir, "init", "-q", "--bare", f.remote)
	f.git(dir, "init", "-q", "-b", "main", f.repo)
	f.git(f.repo, "config", "user.email", "sdf-test@example.com")
	f.git(f.repo, "config", "user.name", "sdf test")
	f.git(f.repo, "config", "commit.gpgsign", "false")
	f.git(f.repo, "remote", "add", "origin", f.remote)
	f.startReceiver()
	f.git(f.repo, "commit", "-q", "--allow-empty", "-m", "root")
	f.git(f.repo, "push", "-q", "--no-verify", "origin", "main")
	return f
}

func (f *pushFixture) startReceiver() {
	f.t.Helper()
	db, err := store.Open(f.db)
	if err != nil {
		f.t.Fatal(err)
	}
	srv := &server.Server{Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true}
	if err := srv.Start(); err != nil {
		f.t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(srv.HTTPAddress())
	if err != nil {
		f.t.Fatal(err)
	}
	f.srv, f.store, f.port = srv, db, port
	f.t.Cleanup(f.stopReceiver)
}

func (f *pushFixture) stopReceiver() {
	if f.srv != nil {
		f.srv.Shutdown()
		_ = f.store.Close()
		f.srv, f.store = nil, nil
	}
}

func (f *pushFixture) env(extra ...string) []string {
	env := agentGitEnv()
	out := env[:0]
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if key == "PATH" || key == "SDF_PRE_PUSH" || key == "SDF_PRE_PUSH_DB" {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "PATH="+f.path)
	return append(out, extra...)
}

type cmdResult struct {
	code   int
	output string
}

func (f *pushFixture) exec(dir string, env []string, name string, args ...string) cmdResult {
	f.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		f.t.Fatalf("%s %v: %v", name, args, err)
	}
	return cmdResult{code, string(out)}
}

func (f *pushFixture) git(dir string, args ...string) string {
	f.t.Helper()
	r := f.exec(dir, f.env(), "git", args...)
	if r.code != 0 {
		f.t.Fatalf("git %v: exit %d\n%s", args, r.code, r.output)
	}
	return strings.TrimSpace(r.output)
}

func (f *pushFixture) sdf(args ...string) cmdResult {
	f.t.Helper()
	return f.exec(f.repo, f.env(), f.bin, args...)
}

func (f *pushFixture) mustSDF(args ...string) string {
	f.t.Helper()
	r := f.sdf(args...)
	if r.code != 0 {
		f.t.Fatalf("sdf %v: exit %d\n%s", args, r.code, r.output)
	}
	return r.output
}

func (f *pushFixture) install(extra ...string) string {
	f.t.Helper()
	return f.mustSDF(append([]string{"init", "--git-hooks", "--port", f.port, "--db", f.db}, extra...)...)
}

// commit makes an empty commit through the installed hooks. A non-empty
// session commits as a Grok CLI session, which the hooks send to the
// receiver; trailers are added with git commit --trailer.
func (f *pushFixture) commit(session, message string, trailers ...string) string {
	f.t.Helper()
	args := []string{"commit", "-q", "--allow-empty", "-m", message}
	for _, tr := range trailers {
		args = append(args, "--trailer", tr)
	}
	var extra []string
	if session != "" {
		extra = append(extra, "GROK_SESSION_ID="+session)
	}
	r := f.exec(f.repo, f.env(extra...), "git", args...)
	if r.code != 0 {
		f.t.Fatalf("commit: %s", r.output)
	}
	return f.git(f.repo, "rev-parse", "HEAD")
}

func (f *pushFixture) push(env []string, args ...string) cmdResult {
	f.t.Helper()
	return f.exec(f.repo, f.env(env...), "git", append([]string{"push"}, args...)...)
}

func (f *pushFixture) remoteHead(ref string) string {
	f.t.Helper()
	r := f.exec(f.remote, f.env(), "git", "rev-parse", "--verify", "-q", ref)
	return strings.TrimSpace(r.output)
}

func TestPrePushWarnsAboutAnAgentCommitAndStillPushes(t *testing.T) {
	f := newPushFixture(t)
	installed := f.install()
	if !strings.Contains(installed, "pre-push warns") || !strings.Contains(installed, "pre-push reads the store "+f.db) {
		t.Fatal(installed)
	}
	hook, err := os.ReadFile(filepath.Join(f.repo, ".git", "hooks", "pre-push"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hook), "# sdf-git-hook:v1") || strings.Contains(string(hook), "git diff") || strings.Contains(string(hook), "git show") {
		t.Fatalf("pre-push hook:\n%s", hook)
	}
	agent := f.commit("grok-prepush-1", "agent change")
	human := f.commit("", "human change")
	r := f.push(nil, "origin", "main")
	if r.code != 0 {
		t.Fatalf("warn mode failed the push: exit %d\n%s", r.code, r.output)
	}
	if !strings.Contains(r.output, "1 of 1 pushed commits recorded in SDF implement no decision") ||
		!strings.Contains(r.output, "commit:"+agent) || !strings.Contains(r.output, "session: grok-prepush-1") ||
		!strings.Contains(r.output, "The push continues (warn mode)") {
		t.Fatalf("warning:\n%s", r.output)
	}
	if strings.Contains(r.output, human) {
		t.Fatalf("a commit SDF never recorded was judged:\n%s", r.output)
	}
	if got := f.remoteHead("refs/heads/main"); got != human {
		t.Fatalf("remote main %s, want %s", got, human)
	}
}

func TestPrePushAcceptsALinkedCommitOrADecisionTrailer(t *testing.T) {
	f := newPushFixture(t)
	f.install()
	f.mustSDF("decision", "add", "--db", f.db, "--id", "store-columns", "--status", "decision", "--statement", "Store hot attributes in columns.")
	f.mustSDF("decision", "add", "--db", f.db, "--id", "old-idea", "--status", "decision", "--statement", "Dropped.")
	f.mustSDF("decision", "retract", "--db", f.db, "--id", "old-idea", "--reason", "replaced")

	linked := f.commit("grok-prepush-2", "linked change")
	f.mustSDF("decision", "link", "--db", f.db, "--relation", "implements", "--from", "commit:"+linked, "--to", "decision:store-columns")
	f.commit("grok-prepush-2", "trailer change", "SDF-Decision: store-columns")
	r := f.push(nil, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "2 of 2 pushed commits are recorded in SDF, and each implements a decision") || strings.Contains(r.output, "implement no decision") {
		t.Fatalf("exit %d\n%s", r.code, r.output)
	}

	// The session is linked now, so its decision is offered as the target.
	f.mustSDF("decision", "link", "--db", f.db, "--relation", "implements", "--from", "session:grok-prepush-2", "--to", "decision:store-columns")
	unknown := f.commit("grok-prepush-2", "typo trailer", "SDF-Decision: no-such-decision")
	retracted := f.commit("grok-prepush-2", "retracted trailer", "SDF-Decision: old-idea")
	r = f.push(nil, "origin", "main")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.output)
	}
	for _, want := range []string{
		"2 of 2 pushed commits recorded in SDF implement no decision",
		"commit:" + unknown, "SDF-Decision trailer names no-such-decision, which is not a recorded decision",
		"commit:" + retracted, "decision:old-idea is retracted",
		"its session is linked to: decision:store-columns",
		"--from commit:" + unknown + " --to decision:store-columns",
	} {
		if !strings.Contains(r.output, want) {
			t.Fatalf("missing %q in\n%s", want, r.output)
		}
	}
}

func TestPrePushBlockModeAndBypasses(t *testing.T) {
	f := newPushFixture(t)
	f.install()
	f.git(f.repo, "config", "sdf.prePush", "block")
	agent := f.commit("grok-prepush-3", "agent change")
	r := f.push(nil, "origin", "main")
	if r.code == 0 || !strings.Contains(r.output, "Push blocked") {
		t.Fatalf("block mode let the push through: exit %d\n%s", r.code, r.output)
	}
	if f.remoteHead("refs/heads/main") == agent {
		t.Fatal("blocked push updated the remote")
	}
	r = f.push([]string{"SDF_PRE_PUSH=off"}, "origin", "main")
	if r.code != 0 || strings.Contains(r.output, "sdf:") {
		t.Fatalf("SDF_PRE_PUSH=off: exit %d\n%s", r.code, r.output)
	}
	second := f.commit("grok-prepush-3", "second change")
	r = f.push(nil, "--no-verify", "origin", "main")
	if r.code != 0 || strings.Contains(r.output, "sdf:") || f.remoteHead("refs/heads/main") != second {
		t.Fatalf("--no-verify: exit %d\n%s", r.code, r.output)
	}
	third := f.commit("grok-prepush-3", "third change")
	r = f.push([]string{"SDF_PRE_PUSH=warn"}, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "commit:"+third) || f.remoteHead("refs/heads/main") != third {
		t.Fatalf("SDF_PRE_PUSH=warn over git config block: exit %d\n%s", r.code, r.output)
	}
	// Block mode never blocks because SDF is unavailable.
	fourth := f.commit("grok-prepush-3", "fourth change")
	r = f.push([]string{"SDF_PRE_PUSH_DB=" + filepath.Join(t.TempDir(), "missing.sqlite")}, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "pre-push decision check skipped: no SDF store at") || f.remoteHead("refs/heads/main") != fourth {
		t.Fatalf("missing store in block mode: exit %d\n%s", r.code, r.output)
	}
}

func TestPrePushChainsTheUserHookAndReinstallsIdempotently(t *testing.T) {
	f := newPushFixture(t)
	hooks := filepath.Join(f.repo, ".git", "hooks")
	proof := filepath.Join(t.TempDir(), "proof")
	user := "#!/bin/sh\nprintf 'remote=%s\\n' \"$1\" >> \"$PREPUSH_PROOF\"\ncat >> \"$PREPUSH_PROOF\"\nexit \"${PREPUSH_EXIT:-0}\"\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-push"), []byte(user), 0o755); err != nil {
		t.Fatal(err)
	}
	first := f.install()
	if !strings.Contains(first, "installed sdf git hooks") {
		t.Fatal(first)
	}
	installedBody, err := os.ReadFile(filepath.Join(hooks, "pre-push"))
	if err != nil {
		t.Fatal(err)
	}
	again := f.install()
	if !strings.Contains(again, "sdf git hooks already installed") {
		t.Fatalf("re-install:\n%s", again)
	}
	afterBody, _ := os.ReadFile(filepath.Join(hooks, "pre-push"))
	prevBody, err := os.ReadFile(filepath.Join(hooks, "pre-push.sdf-previous"))
	if err != nil || string(prevBody) != user || string(afterBody) != string(installedBody) {
		t.Fatalf("re-install changed the hooks: %v\nprevious:\n%s", err, prevBody)
	}
	matches, _ := filepath.Glob(filepath.Join(hooks, "pre-push.sdf-*"))
	if len(matches) != 1 {
		t.Fatalf("hook files %v", matches)
	}

	agent := f.commit("grok-prepush-4", "agent change")
	r := f.push([]string{"PREPUSH_PROOF=" + proof}, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "commit:"+agent) {
		t.Fatalf("exit %d\n%s", r.code, r.output)
	}
	got, err := os.ReadFile(proof)
	if err != nil || !strings.Contains(string(got), "remote=origin") || !strings.Contains(string(got), "refs/heads/main "+agent+" refs/heads/main ") {
		t.Fatalf("user hook did not get the pre-push arguments and stdin: %v\n%s", err, got)
	}

	second := f.commit("grok-prepush-4", "second change")
	r = f.push([]string{"PREPUSH_PROOF=" + proof, "PREPUSH_EXIT=7"}, "origin", "main")
	if r.code == 0 || f.remoteHead("refs/heads/main") == second {
		t.Fatalf("user hook's exit 7 did not stop the push: exit %d\n%s", r.code, r.output)
	}

	off := f.mustSDF("init", "--uninstall-git-hooks")
	if !strings.Contains(off, "restored") {
		t.Fatal(off)
	}
	restored, err := os.ReadFile(filepath.Join(hooks, "pre-push"))
	if err != nil || string(restored) != user {
		t.Fatalf("uninstall did not restore the user hook: %v\n%s", err, restored)
	}
}

func TestPrePushSucceedsWhenSDFIsUnavailable(t *testing.T) {
	f := newPushFixture(t)
	f.install()
	f.commit("grok-prepush-5", "agent change")
	f.stopReceiver()

	// The receiver is down, but the store is still readable: the check runs.
	r := f.push(nil, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "implement no decision") {
		t.Fatalf("receiver down: exit %d\n%s", r.code, r.output)
	}

	// A store file that is not SQLite.
	if err := os.WriteFile(f.db, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"-wal", "-shm"} {
		_ = os.Remove(f.db + name)
	}
	f.commit("grok-prepush-5", "second change")
	r = f.push(nil, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "pre-push decision check skipped") {
		t.Fatalf("broken store: exit %d\n%s", r.code, r.output)
	}

	// The store is gone.
	if err := os.Remove(f.db); err != nil {
		t.Fatal(err)
	}
	f.commit("", "third change")
	r = f.push(nil, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "no SDF store at") {
		t.Fatalf("missing store: exit %d\n%s", r.code, r.output)
	}

	// The sdf binary is gone and none is on PATH.
	if err := os.Remove(f.bin); err != nil {
		t.Fatal(err)
	}
	if r := f.exec(f.repo, f.env(), "sh", "-c", "command -v sdf"); r.code == 0 {
		t.Skipf("an sdf is on the test PATH: %s", r.output)
	}
	fourth := f.commit("", "fourth change")
	r = f.push([]string{"SDF_PRE_PUSH=block"}, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "sdf is not installed or not on PATH") || f.remoteHead("refs/heads/main") != fourth {
		t.Fatalf("missing sdf: exit %d\n%s", r.code, r.output)
	}
}

func TestPrePushChecksOnlyTheCommitsThePushSends(t *testing.T) {
	f := newPushFixture(t)
	f.install()
	onMain := f.commit("grok-prepush-6", "main change")
	r := f.push(nil, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "commit:"+onMain) {
		t.Fatalf("exit %d\n%s", r.code, r.output)
	}

	// A new branch: the remote sha is all zeros. Commits already on
	// origin/main are not checked again.
	f.git(f.repo, "checkout", "-q", "-b", "feature")
	onFeature := f.commit("grok-prepush-6", "feature change")
	r = f.push(nil, "origin", "feature")
	if r.code != 0 || !strings.Contains(r.output, "1 of 1 pushed commits recorded in SDF implement no decision") ||
		!strings.Contains(r.output, "commit:"+onFeature) || strings.Contains(r.output, onMain) {
		t.Fatalf("new branch: exit %d\n%s", r.code, r.output)
	}

	// A new branch at a commit the remote already has sends nothing.
	r = f.push(nil, "origin", "main:refs/heads/copy")
	if r.code != 0 || strings.Contains(r.output, "sdf:") {
		t.Fatalf("new branch with no new commits: exit %d\n%s", r.code, r.output)
	}

	// A delete: the local sha is all zeros. Even block mode lets it through.
	r = f.push([]string{"SDF_PRE_PUSH=block"}, "origin", "--delete", "feature")
	if r.code != 0 || strings.Contains(r.output, "sdf:") || f.remoteHead("refs/heads/feature") != "" {
		t.Fatalf("delete: exit %d\n%s", r.code, r.output)
	}

	// A merge commit is not judged; the branch commit it brings is.
	f.git(f.repo, "checkout", "-q", "main")
	merge := f.exec(f.repo, f.env(), "git", "merge", "-q", "--no-ff", "-m", "merge feature", "feature")
	if merge.code != 0 {
		t.Fatal(merge.output)
	}
	mergeSHA := f.git(f.repo, "rev-parse", "HEAD")
	r = f.push(nil, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "commit:"+onFeature) || strings.Contains(r.output, mergeSHA) {
		t.Fatalf("merge: exit %d\n%s", r.code, r.output)
	}
}

func TestPrePushWithoutAConfiguredStore(t *testing.T) {
	f := newPushFixture(t)
	installed := f.mustSDF("init", "--git-hooks", "--port", f.port)
	if !strings.Contains(installed, "pre-push has no store to read") {
		t.Fatal(installed)
	}
	agent := f.commit("grok-prepush-7", "agent change")
	r := f.push([]string{"SDF_PRE_PUSH=block"}, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "no SDF store is configured for this hook") || f.remoteHead("refs/heads/main") != agent {
		t.Fatalf("no store: exit %d\n%s", r.code, r.output)
	}
	// SDF_PRE_PUSH_DB names the store at push time.
	second := f.commit("grok-prepush-7", "second change")
	r = f.push([]string{"SDF_PRE_PUSH_DB=" + f.db}, "origin", "main")
	if r.code != 0 || !strings.Contains(r.output, "commit:"+second) || f.remoteHead("refs/heads/main") != second {
		t.Fatalf("SDF_PRE_PUSH_DB: exit %d\n%s", r.code, r.output)
	}
	if r := f.sdf("init", "--db", f.db); r.code != 2 || !strings.Contains(r.output, "--db applies to --git-hooks") {
		t.Fatalf("--db without --git-hooks: exit %d\n%s", r.code, r.output)
	}
}
