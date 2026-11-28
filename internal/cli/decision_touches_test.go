package cli

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

func runSDF(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := mainWith(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "sdf test")
	t.Setenv("GIT_AUTHOR_EMAIL", "sdf-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "sdf test")
	t.Setenv("GIT_COMMITTER_EMAIL", "sdf-test@example.com")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	gitAt(t, dir, "init", "-q", "-b", "main")
	gitAt(t, dir, "config", "user.email", "sdf-test@example.com")
	gitAt(t, dir, "config", "user.name", "sdf test")
	gitAt(t, dir, "config", "commit.gpgsign", "false")
	t.Chdir(dir)
	return dir
}

func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitFile(t *testing.T, dir, name, body, message string) string {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), body)
	gitAt(t, dir, "add", "--", name)
	gitAt(t, dir, "commit", "-q", "-m", message)
	return gitAt(t, dir, "rev-parse", "HEAD")
}

func zeroSHA() string { return strings.Repeat("0", 40) }

func dropDecisionPaths(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`DROP TABLE IF EXISTS decision_paths`)
	return err
}

func pushLine(local, remote string) string {
	return fmt.Sprintf("refs/heads/main %s refs/heads/main %s\n", local, remote)
}

func withPrePushStdin(t *testing.T, in string) {
	t.Helper()
	prev := prePushStdin
	prePushStdin = strings.NewReader(in)
	t.Cleanup(func() { prePushStdin = prev })
}

func addDecision(t *testing.T, db, id, statement, status string, paths ...string) {
	t.Helper()
	args := []string{"decision", "add", "--db", db, "--id", id, "--statement", statement, "--status", status, "--decided-by", "ada"}
	for _, p := range paths {
		args = append(args, "--path", p)
	}
	code, stdout, stderr := runSDF(args...)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("add %s: %d %q %q", id, code, stdout, stderr)
	}
}

func recordCommit(t *testing.T, dbPath, sha string) {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Insert([]record.Record{{
		Signal:     "logs",
		Name:       record.Str("sdf.git.commit"),
		SessionID:  record.Str("sess-touch"),
		Attributes: map[string]any{"vcs.ref.head.revision": sha},
		Resource:   map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionPathCLI(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db.sqlite")
	addDecision(t, db, "no-cgo", "Do not add cgo to the store", "decision", "internal/store/**", "*.c")
	code, stdout, stderr := runSDF("decision", "list", "--db", db)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "path: internal/store/**") || !strings.Contains(stdout, "path: *.c") {
		t.Fatalf("list %d %q %q", code, stdout, stderr)
	}
	code, stdout, stderr = runSDF("decision", "brief", "--db", db, "--json")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"paths"`) || !strings.Contains(stdout, "internal/store/**") {
		t.Fatalf("brief %d %q %q", code, stdout, stderr)
	}
	code, _, stderr = runSDF("decision", "scope", "--db", db, "--id", "no-cgo", "--path", "docs/**")
	if code != 0 || stderr != "" {
		t.Fatalf("scope %d %q", code, stderr)
	}
	code, stdout, _ = runSDF("decision", "list", "--db", db, "--json")
	if code != 0 || !strings.Contains(stdout, "docs/**") {
		t.Fatalf("json list %d %s", code, stdout)
	}
	code, _, stderr = runSDF("decision", "add", "--db", db, "--id", "bad", "--statement", "x", "--status", "decision", "--decided-by", "ada", "--path", "../x")
	if code != 1 || !strings.Contains(stderr, "invalid path glob") {
		t.Fatalf("invalid %d %q", code, stderr)
	}
	code, stdout, _ = runSDF("decision", "list", "--db", db, "--json")
	if code != 0 || strings.Contains(stdout, `"bad"`) {
		t.Fatal("invalid glob was stored")
	}
	args := []string{"decision", "add", "--db", db, "--id", "too-many", "--statement", "x", "--status", "decision", "--decided-by", "ada"}
	for i := 0; i < 33; i++ {
		args = append(args, "--path", fmt.Sprintf("p%d/**", i))
	}
	code, _, stderr = runSDF(args...)
	if code != 1 || strings.Contains(stderr, "too-many") {
		t.Fatalf("33 paths %d %q", code, stderr)
	}
	code, _, stderr = runSDF("decision", "scope", "--db", db, "--id", "no-cgo", "--path", "*.c")
	if code != 1 || !strings.Contains(stderr, "already recorded") {
		t.Fatalf("dup %d %q", code, stderr)
	}
	code, _, stderr = runSDF("decision", "touches", "--db", db)
	if code != 2 {
		t.Fatalf("usage %d %q", code, stderr)
	}
	code, _, stderr = runSDF("decision", "touches", "--db", db, "--paths", "files")
	if code != 2 {
		t.Fatalf("paths value %d %q", code, stderr)
	}
}

func TestDecisionTouchesPathsAndRange(t *testing.T) {
	dir := gitRepo(t)
	db := filepath.Join(t.TempDir(), "db.sqlite")
	addDecision(t, db, "no-cgo", "Do not add cgo to the store", "decision", "internal/store/**")
	root := commitFile(t, dir, "README.md", "hi", "root")
	head := commitFile(t, dir, "internal/store/store.go", "package store\n", "store")
	var in bytes.Buffer
	in.WriteString("internal/store/store.go\x00docs/a.md\x00")
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	go func() {
		_, _ = w.Write(in.Bytes())
		_ = w.Close()
	}()
	code, stdout, stderr := runSDF("decision", "touches", "--db", db, "--paths", "-")
	os.Stdin = old
	_ = r.Close()
	if code != 3 || stderr != "" || !strings.Contains(stdout, "1 path matches 1 active decision") || !strings.Contains(stdout, "internal/store/store.go") || strings.Contains(stdout, "(commit") {
		t.Fatalf("paths %d %q %q", code, stdout, stderr)
	}
	code, stdout, stderr = runSDF("decision", "touches", "--db", db, "--range", root+".."+head)
	if code != 3 || !strings.Contains(stdout, "1 commit touches paths scoped by 1 active decision") || !strings.Contains(stdout, "internal/store/store.go (commit "+head[:8]+")") {
		t.Fatalf("range %d %q %q", code, stdout, stderr)
	}
	code, stdout, stderr = runSDF("decision", "touches", "--db", db, "--range", root+".."+root)
	if code != 0 || stdout != "" {
		t.Fatalf("no match %d %q %q", code, stdout, stderr)
	}
	code, _, stderr = runSDF("decision", "touches", "--db", db, "--range", "--all")
	if code != 2 {
		t.Fatalf("bad range %d %q", code, stderr)
	}
}

func TestPrePushTouchWarning(t *testing.T) {
	dir := gitRepo(t)
	db := filepath.Join(t.TempDir(), "db.sqlite")
	addDecision(t, db, "no-cgo", "Do not add cgo to the store", "decision", "internal/store/**")
	parent := commitFile(t, dir, "README.md", "hi", "root")
	head := commitFile(t, dir, "internal/store/store.go", "package store\n", "store")
	withPrePushStdin(t, pushLine(head, parent))
	code, _, stderr := runSDF("decision", "pre-push", "--db", db, "--remote", "origin")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	want := "sdf: pre-push: 1 pushed commit touches paths scoped by 1 active decision:\n" +
		"  decision:no-cgo (locked) \"Do not add cgo to the store\"\n" +
		"    internal/store/** matches internal/store/store.go (commit " + head[:8] + ")\n" +
		"Touching is not a violation; link the commit if it follows the decision (SDF-Decision: no-cgo). The push continues. Silence: git config sdf.prePushTouches off.\n"
	if stderr != want {
		t.Fatalf("got %q\nwant %q", stderr, want)
	}
}

func TestPrePushTouchStatusesAndLinks(t *testing.T) {
	dir := gitRepo(t)
	db := filepath.Join(t.TempDir(), "db.sqlite")
	parent := commitFile(t, dir, "README.md", "hi", "root")
	head := commitFile(t, dir, "internal/a.go", "package a\n", "code")
	run := func() string {
		t.Helper()
		withPrePushStdin(t, pushLine(head, parent))
		code, _, stderr := runSDF("decision", "pre-push", "--db", db, "--remote", "origin")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		return stderr
	}
	addDecision(t, db, "guess", "maybe", "hypothesis", "**/*.go")
	if out := run(); out != "" {
		t.Fatalf("hypothesis matched: %s", out)
	}
	addDecision(t, db, "old", "old rule", "decision", "**/*.go")
	addDecision(t, db, "newer", "replacement", "decision")
	code, _, stderr := runSDF("decision", "link", "--db", db, "--relation", "supersedes", "--from", "decision:newer", "--to", "decision:old")
	if code != 0 {
		t.Fatal(stderr)
	}
	if out := run(); strings.Contains(out, "old") || strings.Contains(out, "newer") {
		t.Fatalf("superseded glob inherited or matched: %s", out)
	}
	addDecision(t, db, "parent", "docs stay", "decision", "docs/**")
	addDecision(t, db, "child", "go stays", "decision", "**/*.go")
	code, _, stderr = runSDF("decision", "link", "--db", db, "--relation", "refines", "--from", "decision:child", "--to", "decision:parent")
	if code != 0 {
		t.Fatal(stderr)
	}
	out := run()
	if !strings.Contains(out, "decision:child") || strings.Contains(out, "decision:parent") || strings.Contains(out, "decision:old") {
		t.Fatalf("refines: %s", out)
	}
	code, _, stderr = runSDF("decision", "retract", "--db", db, "--id", "child", "--reason", "done")
	if code != 0 {
		t.Fatal(stderr)
	}
	if out := run(); strings.Contains(out, "touch paths") {
		t.Fatalf("retracted matched: %s", out)
	}
	addDecision(t, db, "live", "live rule", "decision", "**/*.go")
	recordCommit(t, db, head)
	code, _, stderr = runSDF("decision", "link", "--db", db, "--relation", "implements", "--from", "commit:"+head, "--to", "decision:live")
	if code != 0 {
		t.Fatal(stderr)
	}
	if out := run(); strings.Contains(out, "decision:live") {
		t.Fatalf("linked commit reported: %s", out)
	}
	writeFile(t, filepath.Join(dir, "internal/b.go"), "package b\n")
	gitAt(t, dir, "add", "--", "internal/b.go")
	gitAt(t, dir, "commit", "-q", "-m", "trailer", "--trailer", "SDF-Decision: live")
	trailer := gitAt(t, dir, "rev-parse", "HEAD")
	withPrePushStdin(t, pushLine(trailer, head))
	code, _, stderr = runSDF("decision", "pre-push", "--db", db, "--remote", "origin")
	if code != 0 || strings.Contains(stderr, "decision:live") {
		t.Fatalf("trailer suppressed: %d %s", code, stderr)
	}
}

func TestPrePushTouchModes(t *testing.T) {
	dir := gitRepo(t)
	db := filepath.Join(t.TempDir(), "db.sqlite")
	addDecision(t, db, "no-cgo", "Do not add cgo to the store", "decision", "**/*.go")
	parent := commitFile(t, dir, "README.md", "hi", "root")
	head := commitFile(t, dir, "a.go", "package a\n", "code")
	gitAt(t, dir, "config", "sdf.prePush", "block")
	withPrePushStdin(t, pushLine(head, parent))
	code, _, stderr := runSDF("decision", "pre-push", "--db", db, "--remote", "origin")
	if code != 0 || !strings.Contains(stderr, "The push continues.") || strings.Contains(stderr, "Push blocked") {
		t.Fatalf("block+touch exit %d\n%s", code, stderr)
	}
	recordCommit(t, db, head)
	withPrePushStdin(t, pushLine(head, parent))
	code, _, stderr = runSDF("decision", "pre-push", "--db", db, "--remote", "origin")
	if code != 1 || !strings.Contains(stderr, "Push blocked") || !strings.Contains(stderr, "decision:no-cgo") || strings.Contains(stderr, "The push continues.") {
		t.Fatalf("missing link exit %d\n%s", code, stderr)
	}
	gitAt(t, dir, "config", "sdf.prePushTouches", "off")
	withPrePushStdin(t, pushLine(head, parent))
	code, _, stderr = runSDF("decision", "pre-push", "--db", db, "--remote", "origin")
	if code != 1 || strings.Contains(stderr, "decision:no-cgo") || !strings.Contains(stderr, "Push blocked") {
		t.Fatalf("touches off exit %d\n%s", code, stderr)
	}
	t.Setenv("SDF_PRE_PUSH", "off")
	withPrePushStdin(t, pushLine(head, parent))
	code, _, stderr = runSDF("decision", "pre-push", "--db", db, "--remote", "origin")
	if code != 0 || stderr != "" {
		t.Fatalf("SDF_PRE_PUSH=off exit %d %q", code, stderr)
	}
	t.Setenv("SDF_PRE_PUSH", "")
	gitAt(t, dir, "config", "sdf.prePush", "off")
	withPrePushStdin(t, pushLine(head, parent))
	code, _, stderr = runSDF("decision", "pre-push", "--db", db, "--remote", "origin")
	if code != 0 || stderr != "" {
		t.Fatalf("sdf.prePush off exit %d %q", code, stderr)
	}
}

func TestPrePushRanges(t *testing.T) {
	dir := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitAt(t, dir, "init", "-q", "--bare", remote)
	gitAt(t, dir, "remote", "add", "origin", remote)
	root := commitFile(t, dir, "README.md", "hi", "root")
	gitAt(t, dir, "push", "-q", "origin", "main")
	feature := commitFile(t, dir, "a.go", "package a\n", "feature")
	zero := zeroSHA()

	// Update: remote has the parent.
	diff, err := pushedCommits(t.Context(), pushLine(feature, root), "origin")
	if err != nil || len(diff.Commits) != 1 || diff.Commits[0].SHA != feature || len(diff.Paths) != 1 || diff.Paths[0].Path != "a.go" {
		t.Fatalf("update %+v %v", diff, err)
	}
	// New branch: remote sha zeros, compared with origin's tracking branches.
	diff, err = pushedCommits(t.Context(), fmt.Sprintf("refs/heads/feature %s refs/heads/feature %s\n", feature, zero), "origin")
	if err != nil || len(diff.Commits) != 1 || diff.Commits[0].SHA != feature {
		t.Fatalf("new branch %+v %v", diff, err)
	}
	// Unknown remote sha is not passed as ^sha.
	unknown := strings.Repeat("a", 40)
	diff, err = pushedCommits(t.Context(), pushLine(feature, unknown), "origin")
	if err != nil || len(diff.Commits) != 1 || diff.Commits[0].SHA != feature {
		t.Fatalf("unknown remote %+v %v", diff, err)
	}
	// Delete sends nothing.
	diff, err = pushedCommits(t.Context(), pushLine(zero, feature), "origin")
	if err != nil || len(diff.Commits) != 0 {
		t.Fatalf("delete %+v %v", diff, err)
	}
	// Two refs naming the same commit are one commit.
	line := pushLine(feature, root) + fmt.Sprintf("refs/heads/other %s refs/heads/other %s\n", feature, root)
	diff, err = pushedCommits(t.Context(), line, "origin")
	if err != nil || len(diff.Commits) != 1 || len(diff.Paths) != 1 {
		t.Fatalf("dedupe %+v %v", diff, err)
	}
	// Force push: a commit the remote sha does not contain.
	gitAt(t, dir, "checkout", "-q", "-b", "side", root)
	side := commitFile(t, dir, "b.go", "package b\n", "side")
	diff, err = pushedCommits(t.Context(), fmt.Sprintf("refs/heads/side %s refs/heads/side %s\n", side, feature), "origin")
	if err != nil || len(diff.Commits) != 1 || diff.Commits[0].SHA != side || diff.Paths[0].Path != "b.go" {
		t.Fatalf("force %+v %v", diff, err)
	}
	// A merge commit is skipped; its parent on the side is not.
	gitAt(t, dir, "checkout", "-q", "main")
	gitAt(t, dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	merge := gitAt(t, dir, "rev-parse", "HEAD")
	diff, err = pushedCommits(t.Context(), pushLine(merge, feature), "origin")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range diff.Commits {
		if c.SHA == merge {
			t.Fatalf("merge included: %+v", diff.Commits)
		}
	}
	foundSide := false
	for _, c := range diff.Commits {
		if c.SHA == side {
			foundSide = true
		}
	}
	if !foundSide {
		t.Fatalf("side commit missing: %+v", diff.Commits)
	}
}

func TestPrePushRenameSubmoduleAndEscape(t *testing.T) {
	dir := gitRepo(t)
	gitAt(t, dir, "config", "diff.renames", "true")
	commitFile(t, dir, "old.go", "package old\n", "add")
	gitAt(t, dir, "mv", "old.go", "new.go")
	gitAt(t, dir, "commit", "-q", "-m", "rename")
	cmd := exec.Command("git", "log", "-1", "--name-status", "--no-renames", "-z", "--format=%x1e")
	cmd.Dir = dir
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("D\x00old.go\x00")) || !bytes.Contains(raw, []byte("A\x00new.go\x00")) || bytes.Contains(raw, []byte("R100")) {
		t.Fatalf("rename status %q", raw)
	}
	db := filepath.Join(t.TempDir(), "db.sqlite")
	addDecision(t, db, "names", "both names", "decision", "old.go", "new.go")
	parent := gitAt(t, dir, "rev-parse", "HEAD~1")
	head := gitAt(t, dir, "rev-parse", "HEAD")
	code, stdout, stderr := runSDF("decision", "touches", "--db", db, "--range", parent+".."+head)
	if code != 3 || !strings.Contains(stdout, "old.go") || !strings.Contains(stdout, "new.go") {
		t.Fatalf("rename match %d %q %q", code, stdout, stderr)
	}

	sub := t.TempDir()
	gitAt(t, sub, "init", "-q", "-b", "main")
	gitAt(t, sub, "config", "user.email", "sdf-test@example.com")
	gitAt(t, sub, "config", "user.name", "sdf test")
	writeFile(t, filepath.Join(sub, "hidden.go"), "package hidden\n")
	gitAt(t, sub, "add", "hidden.go")
	gitAt(t, sub, "commit", "-q", "-m", "sub")
	subSHA := gitAt(t, sub, "rev-parse", "HEAD")
	gitAt(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+subSHA+",vendor/sub")
	gitAt(t, dir, "commit", "-q", "-m", "gitlink")
	link := gitAt(t, dir, "rev-parse", "HEAD")
	addDecision(t, db, "go", "go files", "decision", "**/*.go")
	code, stdout, stderr = runSDF("decision", "touches", "--db", db, "--range", head+".."+link)
	if code != 0 || strings.Contains(stdout, "hidden.go") || strings.Contains(stdout, "vendor/sub") {
		t.Fatalf("gitlink matched a go glob: %d %q %q", code, stdout, stderr)
	}
	addDecision(t, db, "link", "the gitlink", "decision", "vendor/sub")
	code, stdout, stderr = runSDF("decision", "touches", "--db", db, "--range", head+".."+link)
	if code != 3 || !strings.Contains(stdout, "vendor/sub") || strings.Contains(stdout, "hidden.go") {
		t.Fatalf("gitlink path %d %q %q", code, stdout, stderr)
	}

	weird := "a\nb\x1b.go"
	writeFile(t, filepath.Join(dir, weird), "package weird\n")
	gitAt(t, dir, "add", "--", weird)
	gitAt(t, dir, "commit", "-q", "-m", "weird")
	weirdHead := gitAt(t, dir, "rev-parse", "HEAD")
	addDecision(t, db, "weird", "weird names", "decision", "*.go")
	withPrePushStdin(t, pushLine(weirdHead, link))
	code, _, stderr = runSDF("decision", "pre-push", "--db", db, "--remote", "origin")
	if code != 0 || strings.Contains(stderr, "\x1b") || strings.Contains(stderr, "a\nb") || !strings.Contains(stderr, `\x1b`) || !strings.Contains(stderr, `\n`) {
		t.Fatalf("escape %d %q", code, stderr)
	}
}

func TestPrePushPathCap(t *testing.T) {
	dir := gitRepo(t)
	root := commitFile(t, dir, "README.md", "hi", "root")
	writeFile(t, filepath.Join(dir, ".blob"), "x")
	blob := gitAt(t, dir, "hash-object", "-w", filepath.Join(dir, ".blob"))
	var info bytes.Buffer
	const n = 20001
	for i := 0; i < n; i++ {
		fmt.Fprintf(&info, "100644 %s\tf%d.go\n", blob, i)
	}
	cmd := exec.Command("git", "update-index", "--index-info")
	cmd.Dir = dir
	cmd.Stdin = &info
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	gitAt(t, dir, "commit", "-q", "-m", "many")
	head := gitAt(t, dir, "rev-parse", "HEAD")
	db := filepath.Join(t.TempDir(), "db.sqlite")
	addDecision(t, db, "all", "all go", "decision", "**/*.go")
	withPrePushStdin(t, pushLine(head, root))
	code, _, stderr := runSDF("decision", "pre-push", "--db", db, "--remote", "origin")
	if code != 0 || !strings.Contains(stderr, "only the first 20000 changed paths were checked") || !strings.Contains(stderr, "and 19997 more") {
		t.Fatalf("cap %d\n%s", code, stderr)
	}
}

func TestPrePushFailOpen(t *testing.T) {
	dir := gitRepo(t)
	db := filepath.Join(t.TempDir(), "db.sqlite")
	addDecision(t, db, "no-cgo", "Do not add cgo", "decision", "**/*.go")
	parent := commitFile(t, dir, "README.md", "hi", "root")
	head := commitFile(t, dir, "a.go", "package a\n", "code")
	if err := dropDecisionPaths(db); err != nil {
		t.Fatal(err)
	}
	withPrePushStdin(t, pushLine(head, parent))
	code, _, stderr := runSDF("decision", "pre-push", "--db", db, "--remote", "origin")
	if code != 0 || strings.Contains(stderr, "touch paths") || strings.Contains(stderr, "skipped") {
		t.Fatalf("missing table %d %q", code, stderr)
	}
	bad := filepath.Join(t.TempDir(), "corrupt")
	if err := os.WriteFile(bad, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	withPrePushStdin(t, pushLine(head, zeroSHA()))
	code, _, stderr = runSDF("decision", "pre-push", "--db", bad, "--remote", "origin")
	if code != 0 || !strings.Contains(stderr, "pre-push decision check skipped:") || !strings.Contains(stderr, "The push continues.") {
		t.Fatalf("corrupt %d %q", code, stderr)
	}
}

func TestPrePushStubGitReadsNoContent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "git.log")
	outPath := filepath.Join(t.TempDir(), "out.bin")
	sha := strings.Repeat("ab", 20)
	raw := []byte(sha + "\x1f\x1e\x00\nA\x00internal/store/store.go\x00")
	if err := os.WriteFile(outPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GIT_STUB_LOG\"\ncase \"$1\" in\n  log) cat \"$GIT_STUB_OUT\"; exit 0 ;;\n  rev-parse) exit 0 ;;\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(stub)+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv("GIT_STUB_LOG", logPath)
	t.Setenv("GIT_STUB_OUT", outPath)
	db := filepath.Join(t.TempDir(), "db.sqlite")
	addDecision(t, db, "no-cgo", "Do not add cgo to the store", "decision", "internal/store/**")
	withPrePushStdin(t, pushLine(sha, strings.Repeat("cd", 20)))
	code, _, stderr := runSDF("decision", "pre-push", "--db", db, "--remote", "origin", "--mode", "warn")
	if code != 0 || !strings.Contains(stderr, "internal/store/** matches internal/store/store.go") {
		t.Fatalf("%d %q", code, stderr)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(logged)
	if !strings.Contains(text, "--name-status") || !strings.Contains(text, "--no-renames") || !strings.Contains(text, " -z ") {
		t.Fatalf("log argv: %s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		for _, f := range fields {
			switch f {
			case "-p", "--stat", "--patch", "cat-file", "internal/store/**":
				t.Fatalf("argv contains %s: %s", f, text)
			}
		}
	}
}

func TestPrePushTimeoutFailsOpen(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\ncase \"$1\" in\n  log) sleep 30 ;;\nesac\nexit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(stub)+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	prev := prePushBudget
	prePushBudget = 200 * time.Millisecond
	t.Cleanup(func() { prePushBudget = prev })
	withPrePushStdin(t, pushLine(strings.Repeat("ab", 20), zeroSHA()))
	start := time.Now()
	code, _, stderr := runSDF("decision", "pre-push", "--db", filepath.Join(t.TempDir(), "missing.sqlite"), "--remote", "origin", "--mode", "block")
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if code != 0 || !strings.Contains(stderr, "pre-push decision check skipped:") || !strings.Contains(stderr, "The push continues.") {
		t.Fatalf("%d %q", code, stderr)
	}
}
