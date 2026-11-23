package cli_test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
)

// A commit made from Claude Code's Bash tool (CLAUDE_CODE_SESSION_ID) or Qwen
// Code's shell tool (QWEN_CODE_SESSION_ID) links to the session that agent's
// telemetry exported, and is not split into a second session. The telemetry
// is tests/samples/claude_logs.json and qwen_logs.json with the sample
// session id replaced by a UUID in the shape both agents generate (made up,
// not a capture).
func TestSummaryLinksClaudeAndQwenGitHookCommits(t *testing.T) {
	isolateAgentGit(t)
	samples, err := filepath.Abs(filepath.Join("..", "..", "tests", "samples"))
	if err != nil {
		t.Fatal(err)
	}
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
	if installed.code != 0 {
		t.Fatalf("%d %s %s", installed.code, installed.stdout, installed.stderr)
	}
	const (
		claudeID = "4f6b1c2e-8d3a-4b7e-9f21-3c5d6e7a8b90"
		qwenID   = "b2e7a0d4-5c19-4f8e-a3b6-71d0c9e45f2a"
	)
	base := "http://" + srv.HTTPAddress()
	postSampleLogs(t, base, samples, "claude_logs.json", "sample-claude-1", claudeID)
	postSampleLogs(t, base, samples, "qwen_logs.json", "sample-qwen-1", qwenID)

	claude := hookCommit(t, dir, "claude change", false, "CLAUDE_CODE_SESSION_ID="+claudeID)
	qwen := hookCommit(t, dir, "qwen change", false, "QWEN_CODE_SESSION_ID="+qwenID)
	both := hookCommit(t, dir, "claude and qwen", false, "CLAUDE_CODE_SESSION_ID="+claudeID, "QWEN_CODE_SESSION_ID="+qwenID)

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
	count := map[string]int{}
	byID := map[string]summary.SessionSummary{}
	for _, session := range report.Sessions {
		count[session.SessionID]++
		byID[session.SessionID] = session
		for _, sha := range outcomeCommits(session) {
			if sha == both {
				t.Fatalf("ambiguous commit linked to %s", session.SessionID)
			}
		}
	}
	for _, want := range []struct{ id, tool, sha, subject string }{
		{claudeID, "claude-code", claude, "claude change"},
		{qwenID, "qwen-code", qwen, "qwen change"},
	} {
		session := byID[want.id]
		if count[want.id] != 1 || session.Tool != want.tool {
			t.Fatalf("%s: %d sessions, tool %q", want.id, count[want.id], session.Tool)
		}
		if session.Outcome == nil || session.Outcome.Status != "linked" || session.Outcome.Match != "hook" {
			t.Fatalf("%s %+v", want.tool, session.Outcome)
		}
		if len(session.Outcome.Commits) != 1 || session.Outcome.Commits[0].SHA != want.sha || session.Outcome.Commits[0].Subject != want.subject {
			t.Fatalf("%s commits %+v", want.tool, session.Outcome.Commits)
		}
		// The agent's own logs are in the same session as the hook event.
		if session.Logs < 2 {
			t.Fatalf("%s logs %d", want.tool, session.Logs)
		}
	}
}

func postSampleLogs(t *testing.T, base, dir, name, sampleID, id string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(raw), `"`+sampleID+`"`, `"`+id+`"`)
	resp, err := http.Post(base+"/v1/logs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("post %s %d", name, resp.StatusCode)
	}
}
