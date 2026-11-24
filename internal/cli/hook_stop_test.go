package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stopRemindReason is the one sentence a Stop hook may feed back, once per
// session, when the session committed and did not record the decision.
const stopRemindReason = "phiên này có quyết định nào chưa ghi không?"

const stopSHA = "0123456789abcdef0123456789abcdef01234567"

func TestHookStopBlocksEachSupportedAgent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	ingest(t, dbPath, [][]byte{
		hookPayload("codex_exec", "codex", "sess-1", stopSHA, "/tmp/repo", "main", time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)),
	})
	cases := []struct {
		agent string
		body  string
	}{
		{"claude-code", `{"session_id":"sess-1","stop_hook_active":false}`},
		{"codex", `{"session_id":"sess-1","stop_hook_active":false,"hook_event_name":"Stop"}`},
		{"gemini", `{"session_id":"sess-1","hook_event_name":"AfterAgent","stop_hook_active":false}`},
		{"qwen", `{"session_id":"sess-1","hook_event_name":"Stop","stop_hook_active":false}`},
		{"grok", `{"sessionId":"sess-1","hookEventName":"stop","stopHookActive":false,"reason":"end_turn"}`},
		{"pi", `{"session_id":"sess-1"}`},
		{"copilot", `{"sessionId":"sess-1","stop_hook_active":false}`},
		{"cursor", `{"conversation_id":"sess-1","status":"completed","loop_count":0}`},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			dir := t.TempDir()
			db := filepath.Join(dir, "telemetry.sqlite")
			copyFile(t, dbPath, db)
			out := runHook(t, []string{"hook", "stop", "--agent", tc.agent, "--db", db}, tc.body, nil)
			if out.code != 0 || out.stderr != "" {
				t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
			}
			assertStopBlock(t, tc.agent, out.stdout)
			info, err := os.Stat(db + ".stop-reminded")
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("marker mode %o, want 0600", info.Mode().Perm())
			}
		})
	}
}

func TestHookStopOncePerSession(t *testing.T) {
	db := filepath.Join(t.TempDir(), "telemetry.sqlite")
	ingest(t, db, [][]byte{
		hookPayload("claude-code", "claude-code", "sess-1", stopSHA, "/tmp/repo", "main", time.Now().UTC()),
	})
	body := `{"session_id":"sess-1","stop_hook_active":false}`
	first := runHook(t, []string{"hook", "stop", "--agent", "claude-code", "--db", db}, body, nil)
	if first.code != 0 || first.stderr != "" {
		t.Fatalf("first %d %q %q", first.code, first.stdout, first.stderr)
	}
	assertStopBlock(t, "claude-code", first.stdout)
	second := runHook(t, []string{"hook", "stop", "--agent", "claude-code", "--db", db}, body, nil)
	if second.code != 0 || second.stderr != "" || second.stdout != "" {
		t.Fatalf("second %d %q %q", second.code, second.stdout, second.stderr)
	}
}

func TestHookStopActiveAllowsWithoutMarking(t *testing.T) {
	db := filepath.Join(t.TempDir(), "telemetry.sqlite")
	ingest(t, db, [][]byte{
		hookPayload("claude-code", "claude-code", "sess-1", stopSHA, "/tmp/repo", "main", time.Now().UTC()),
	})
	cases := []struct {
		agent string
		body  string
	}{
		{"claude-code", `{"session_id":"sess-1","stop_hook_active":true}`},
		{"codex", `{"session_id":"sess-1","stop_hook_active":true}`},
		{"gemini", `{"session_id":"sess-1","stop_hook_active":true}`},
		{"qwen", `{"session_id":"sess-1","stop_hook_active":true}`},
		{"grok", `{"sessionId":"sess-1","stopHookActive":true,"reason":"end_turn"}`},
		{"grok", `{"sessionId":"sess-1","stopHookActive":false,"reason":"shutdown"}`},
		{"copilot", `{"sessionId":"sess-1","stop_hook_active":true}`},
		{"cursor", `{"conversation_id":"sess-1","status":"completed","loop_count":1}`},
		{"cursor", `{"conversation_id":"sess-1","status":"aborted","loop_count":0}`},
	}
	for _, tc := range cases {
		t.Run(tc.agent+" "+tc.body, func(t *testing.T) {
			out := runHook(t, []string{"hook", "stop", "--agent", tc.agent, "--db", db}, tc.body, nil)
			if out.code != 0 || out.stderr != "" || out.stdout != "" {
				t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
			}
		})
	}
	if _, err := os.Stat(db + ".stop-reminded"); !os.IsNotExist(err) {
		t.Fatalf("stop_hook_active wrote a marker: %v", err)
	}
	// The same session can still be reminded once the continuation flag is clear.
	again := runHook(t, []string{"hook", "stop", "--agent", "claude-code", "--db", db}, `{"session_id":"sess-1","stop_hook_active":false}`, nil)
	if again.code != 0 || again.stdout == "" {
		t.Fatalf("later stop %d %q %q", again.code, again.stdout, again.stderr)
	}
	assertStopBlock(t, "claude-code", again.stdout)
}

func TestHookStopFailOpenOnMissingOrUnreadableDB(t *testing.T) {
	dir := t.TempDir()
	missingDir := filepath.Join(dir, "nope", "telemetry.sqlite")
	missingFile := filepath.Join(dir, "missing.sqlite")
	garbage := filepath.Join(dir, "garbage.sqlite")
	if err := os.WriteFile(garbage, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"session_id":"sess-1","stop_hook_active":false}`
	for _, db := range []string{missingDir, missingFile, garbage} {
		out := runHook(t, []string{"hook", "stop", "--agent", "claude-code", "--db", db}, body, nil)
		if out.code != 0 || out.stderr != "" || out.stdout != "" {
			t.Fatalf("%s: %d %q %q", db, out.code, out.stdout, out.stderr)
		}
		if _, err := os.Stat(db); db == missingFile && !os.IsNotExist(err) {
			t.Fatalf("created missing store: %v", err)
		}
		if _, err := os.Stat(db + ".stop-reminded"); !os.IsNotExist(err) {
			t.Fatalf("marker beside %s: %v", db, err)
		}
	}
}

func TestHookStopNoSessionAllows(t *testing.T) {
	db := filepath.Join(t.TempDir(), "telemetry.sqlite")
	ingest(t, db, [][]byte{
		hookPayload("claude-code", "claude-code", "sess-1", stopSHA, "/tmp/repo", "main", time.Now().UTC()),
	})
	out := runHook(t, []string{"hook", "stop", "--agent", "claude-code", "--db", db}, `{}`, nil)
	if out.code != 0 || out.stderr != "" || out.stdout != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if _, err := os.Stat(db + ".stop-reminded"); !os.IsNotExist(err) {
		t.Fatal("marker without a session id")
	}
}

func TestHookStopSessionFromEnv(t *testing.T) {
	db := filepath.Join(t.TempDir(), "telemetry.sqlite")
	ingest(t, db, [][]byte{
		hookPayload("claude-code", "claude-code", "from-env", stopSHA, "/tmp/repo", "main", time.Now().UTC()),
	})
	out := runHook(t, []string{"hook", "stop", "--agent", "claude-code", "--db", db}, `{}`, map[string]string{
		"CLAUDE_CODE_SESSION_ID": "from-env",
	})
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	assertStopBlock(t, "claude-code", out.stdout)
}

func TestHookStopCodexPrefersThreadID(t *testing.T) {
	db := filepath.Join(t.TempDir(), "telemetry.sqlite")
	ingest(t, db, [][]byte{
		hookPayload("codex_exec", "codex", "thread-1", stopSHA, "/tmp/repo", "main", time.Now().UTC()),
	})
	// A descendant's payload session_id is the root id. Commits are stored
	// under CODEX_THREAD_ID, which is the OTLP conversation id.
	out := runHook(t, []string{"hook", "stop", "--agent", "codex", "--db", db}, `{"session_id":"root-session","stop_hook_active":false}`, map[string]string{
		"CODEX_THREAD_ID": "thread-1",
	})
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	assertStopBlock(t, "codex", out.stdout)
}

func TestHookStopWarrant(t *testing.T) {
	const (
		shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	toolResult := func(session, sha string) []byte {
		return []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"claude-code"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"1","body":{"stringValue":"claude_code.tool_result"},"attributes":[` +
			`{"key":"session.id","value":{"stringValue":"` + session + `"}},` +
			`{"key":"event.name","value":{"stringValue":"claude_code.tool_result"}},` +
			`{"key":"sdf.git_commit_id","value":{"stringValue":"` + sha + `"}}]}]}]}]}`)
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, db string)
		block bool
	}{
		{
			name: "hook commit and no decision",
			setup: func(t *testing.T, db string) {
				ingest(t, db, [][]byte{hookPayload("claude-code", "claude-code", "sess-1", shaA, "/tmp/repo", "main", at)})
			},
			block: true,
		},
		{
			name: "no commits",
			setup: func(t *testing.T, db string) {
				ingest(t, db, nil)
				mustRun(t, "decision", "add", "--db", db, "--id", "use-sqlite", "--statement", "Use SQLite.", "--status", "decision")
			},
			block: false,
		},
		{
			name: "tool result commit is not the hook",
			setup: func(t *testing.T, db string) {
				ingest(t, db, [][]byte{toolResult("sess-1", shaA)})
			},
			block: false,
		},
		{
			name: "other session committed",
			setup: func(t *testing.T, db string) {
				ingest(t, db, [][]byte{hookPayload("claude-code", "claude-code", "other", shaA, "/tmp/repo", "main", at)})
			},
			block: false,
		},
		{
			name: "session linked but no implements edge",
			setup: func(t *testing.T, db string) {
				ingest(t, db, [][]byte{hookPayload("claude-code", "claude-code", "sess-1", shaA, "/tmp/repo", "main", at)})
				mustRun(t, "decision", "add", "--db", db, "--id", "use-sqlite", "--statement", "Use SQLite.", "--status", "decision")
				mustRun(t, "decision", "link", "--db", db, "--relation", "validates", "--from", "session:sess-1", "--to", "decision:use-sqlite")
			},
			block: true,
		},
		{
			name: "commit implements but session is not linked",
			setup: func(t *testing.T, db string) {
				ingest(t, db, [][]byte{hookPayload("claude-code", "claude-code", "sess-1", shaA, "/tmp/repo", "main", at)})
				mustRun(t, "decision", "add", "--db", db, "--id", "use-sqlite", "--statement", "Use SQLite.", "--status", "decision")
				mustRun(t, "decision", "link", "--db", db, "--relation", "implements", "--from", "commit:"+shaA, "--to", "decision:use-sqlite")
			},
			block: true,
		},
		{
			name: "session implements",
			setup: func(t *testing.T, db string) {
				ingest(t, db, [][]byte{hookPayload("claude-code", "claude-code", "sess-1", shaA, "/tmp/repo", "main", at)})
				mustRun(t, "decision", "add", "--db", db, "--id", "use-sqlite", "--statement", "Use SQLite.", "--status", "decision")
				mustRun(t, "decision", "link", "--db", db, "--relation", "implements", "--from", "session:sess-1", "--to", "decision:use-sqlite")
			},
			block: false,
		},
		{
			name: "session linked and a commit implements",
			setup: func(t *testing.T, db string) {
				ingest(t, db, [][]byte{
					hookPayload("claude-code", "claude-code", "sess-1", shaA, "/tmp/repo", "main", at),
					hookPayload("claude-code", "claude-code", "sess-1", shaB, "/tmp/repo", "main", at),
				})
				mustRun(t, "decision", "add", "--db", db, "--id", "use-sqlite", "--statement", "Use SQLite.", "--status", "decision")
				mustRun(t, "decision", "link", "--db", db, "--relation", "validates", "--from", "session:sess-1", "--to", "decision:use-sqlite")
				mustRun(t, "decision", "link", "--db", db, "--relation", "implements", "--from", "commit:"+shaB, "--to", "decision:use-sqlite")
			},
			block: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "telemetry.sqlite")
			tc.setup(t, db)
			out := runHook(t, []string{"hook", "stop", "--agent", "claude-code", "--db", db}, `{"session_id":"sess-1"}`, nil)
			if out.code != 0 || out.stderr != "" {
				t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
			}
			if tc.block {
				assertStopBlock(t, "claude-code", out.stdout)
			} else if out.stdout != "" {
				t.Fatalf("blocked: %s", out.stdout)
			}
		})
	}
}

func assertStopBlock(t *testing.T, agent, stdout string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("%s: %v\n%s", agent, err, stdout)
	}
	switch agent {
	case "cursor":
		if got["followup_message"] != stopRemindReason || len(got) != 1 {
			t.Fatalf("%#v", got)
		}
	case "pi":
		if got["continue"] != true {
			t.Fatalf("%#v", got)
		}
		entries, _ := got["entries"].([]any)
		if len(entries) != 1 {
			t.Fatalf("%#v", got)
		}
		entry, _ := entries[0].(map[string]any)
		if entry["type"] != "custom_message" || entry["content"] != stopRemindReason {
			t.Fatalf("%#v", entry)
		}
	default:
		if got["decision"] != "block" || got["reason"] != stopRemindReason {
			t.Fatalf("%#v", got)
		}
	}
}

func runHook(t *testing.T, args []string, stdin string, env map[string]string) result {
	t.Helper()
	for _, key := range []string{
		"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "GROK_SESSION_ID", "PI_SESSION_ID",
		"QWEN_CODE_SESSION_ID", "GEMINI_SESSION_ID",
	} {
		if _, ok := env[key]; !ok {
			t.Setenv(key, "")
		}
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(stdin)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	return run(t, args...)
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
