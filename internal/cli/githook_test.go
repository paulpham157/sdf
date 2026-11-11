package cli_test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
)

func TestSummaryLinksCommitsFromGitHooks(t *testing.T) {
	isolateAgentGit(t)
	dir := hookRepo(t)
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()
	_, port, err := net.SplitHostPort(srv.HTTPAddress())
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	installed := run(t, "init", "--git-hooks", "--host", "127.0.0.1", "--port", port)
	if installed.code != 0 || !strings.Contains(installed.stdout, "installed sdf git hooks") {
		t.Fatalf("%d %s %s", installed.code, installed.stdout, installed.stderr)
	}
	const (
		opencodeID = "ses_summary"
		piStem     = "2026-01-01T00-00-00-000Z_sample-pi-session"
		piShort    = "sample-pi-session"
		threadID   = "019d2bae-7c39-7c73-b058-7b45e6aabef7"
		rootID     = "root-session-not-the-thread"
		commitText = "commit-message-not-posted"
	)
	base := "http://" + srv.HTTPAddress()
	postSpan(t, base, "opencode", "session.id", opencodeID, "11111111111111111111111111111111", "2222222222222222")
	postSpan(t, base, "pi", "session.id", piStem, "33333333333333333333333333333333", "4444444444444444")
	postSpan(t, base, "pi", "session.id", piShort, "55555555555555555555555555555555", "6666666666666666")
	postSpan(t, base, "codex", "conversation.id", threadID, "77777777777777777777777777777777", "8888888888888888")
	postSpan(t, base, "codex", "conversation.id", rootID, "99999999999999999999999999999999", "aaaaaaaaaaaaaaaa")

	oc := hookCommit(t, dir, commitText, false, "OPENCODE_SESSION_ID="+opencodeID)
	pi := hookCommit(t, dir, "pi change", false, "PI_SESSION_FILE=/tmp/pi dir/"+piStem+".jsonl", "PI_SESSION_ID="+piShort)
	cx := hookCommit(t, dir, "codex change", false, "CODEX_THREAD_ID="+threadID, "CODEX_SESSION_ID="+rootID)
	human := hookCommit(t, dir, "human change", false)

	records, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range records {
		blob := string(mustJSON(t, rec.Attributes)) + deref(rec.Body)
		if strings.Contains(blob, commitText) {
			t.Fatalf("stored the commit message: %s", blob)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	out := run(t, "summary", "--db", dbPath, "--json", "--prices", "bundled")
	if out.code != 0 {
		t.Fatal(out.stderr)
	}
	var report summary.Report
	if err := json.Unmarshal([]byte(out.stdout), &report); err != nil {
		t.Fatalf("%v %s", err, out.stdout)
	}
	byID := map[string]summary.SessionSummary{}
	for _, session := range report.Sessions {
		byID[session.SessionID] = session
	}
	assertHook(t, byID[opencodeID], oc, commitText)
	assertHook(t, byID[piStem], pi, "pi change")
	assertHook(t, byID[threadID], cx, "codex change")
	if byID[piShort].Outcome == nil || byID[piShort].Outcome.Status != "unlinked" {
		t.Fatalf("short pi id was linked: %+v", byID[piShort].Outcome)
	}
	if byID[rootID].Outcome == nil || byID[rootID].Outcome.Status != "unlinked" {
		t.Fatalf("codex root session was linked: %+v", byID[rootID].Outcome)
	}
	for _, session := range report.Sessions {
		for _, commit := range outcomeCommits(session) {
			if commit == human {
				t.Fatalf("untagged commit %s linked to %s", human, session.SessionID)
			}
		}
	}
	if report.CostPerAccepted == nil || report.CostPerAccepted.Status != "unknown" || report.CostPerAccepted.USD != "" {
		t.Fatalf("cost %+v", report.CostPerAccepted)
	}
}

func TestSummaryReplacesAnAmendedHookCommit(t *testing.T) {
	isolateAgentGit(t)
	dir := hookRepo(t)
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := &server.Server{Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0, Store: db, Quiet: true}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()
	_, port, err := net.SplitHostPort(srv.HTTPAddress())
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	installed := run(t, "init", "--git-hooks", "--port", port)
	if installed.code != 0 {
		t.Fatal(installed.stderr)
	}
	const id = "ses_amend"
	postSpan(t, "http://"+srv.HTTPAddress(), "opencode", "session.id", id, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccd")
	first := hookCommit(t, dir, "first message", false, "OPENCODE_SESSION_ID="+id)
	second := hookCommit(t, dir, "amended message", true, "OPENCODE_SESSION_ID="+id)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	out := run(t, "summary", "--db", dbPath, "--json", "--prices", "bundled")
	if out.code != 0 {
		t.Fatal(out.stderr)
	}
	var report summary.Report
	if err := json.Unmarshal([]byte(out.stdout), &report); err != nil {
		t.Fatal(err)
	}
	var session summary.SessionSummary
	for _, item := range report.Sessions {
		if item.SessionID == id {
			session = item
		}
	}
	if session.Outcome == nil || session.Outcome.Match != "hook" || len(session.Outcome.Commits) != 1 || session.Outcome.Commits[0].SHA != second {
		t.Fatalf("got %+v want %s not %s", session.Outcome, second, first)
	}
}

func assertHook(t *testing.T, session summary.SessionSummary, sha, subject string) {
	t.Helper()
	if session.Outcome == nil || session.Outcome.Status != "linked" || session.Outcome.Match != "hook" {
		t.Fatalf("%s %+v", session.SessionID, session.Outcome)
	}
	if len(session.Outcome.Commits) != 1 || session.Outcome.Commits[0].SHA != sha || session.Outcome.Commits[0].Subject != subject {
		t.Fatalf("%s commits %+v", session.SessionID, session.Outcome.Commits)
	}
	if session.Traces < 1 || session.Logs < 1 {
		t.Fatalf("%s traces %d logs %d", session.SessionID, session.Traces, session.Logs)
	}
}

func outcomeCommits(session summary.SessionSummary) []string {
	if session.Outcome == nil {
		return nil
	}
	var out []string
	for _, commit := range session.Outcome.Commits {
		out = append(out, commit.SHA)
	}
	return out
}

func postSpan(t *testing.T, base, service, key, session, traceID, spanID string) {
	t.Helper()
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"` + service + `"}}]},"scopeSpans":[{"spans":[{"traceId":"` + traceID + `","spanId":"` + spanID + `","name":"Tool.execute","startTimeUnixNano":"1000","endTimeUnixNano":"2000","attributes":[{"key":"` + key + `","value":{"stringValue":"` + session + `"}}]}]}]}]}`
	resp, err := http.Post(base+"/v1/traces", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("post span %d", resp.StatusCode)
	}
}

func hookRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "sdf-test@example.com"},
		{"config", "user.name", "sdf test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = agentGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v %s", args, err, out)
		}
	}
	return dir
}

func hookCommit(t *testing.T, dir, message string, amend bool, extra ...string) string {
	t.Helper()
	args := []string{"-C", dir, "commit", "--allow-empty", "-m", message}
	if amend {
		args = []string{"-C", dir, "commit", "--allow-empty", "--amend", "-m", message}
	}
	cmd := exec.Command("git", args...)
	cmd.Env = agentGitEnv(extra...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	cmd = exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	cmd.Env = agentGitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func isolateAgentGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	for _, key := range []string{"PI_SESSION_FILE", "PI_SESSION_ID", "OPENCODE_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SESSION_ID", "AI_AGENT"} {
		t.Setenv(key, "")
	}
}

func agentGitEnv(extra ...string) []string {
	drop := map[string]bool{
		"PI_SESSION_FILE": true, "PI_SESSION_ID": true, "OPENCODE_SESSION_ID": true,
		"CODEX_THREAD_ID": true, "CODEX_SESSION_ID": true, "AI_AGENT": true,
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

func chdir(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
