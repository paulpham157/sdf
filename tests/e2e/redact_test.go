//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/samplepath"
)

// Qwen Code exports every tool call's arguments (function_args) and the
// tool's error text whatever logPrompts says. The qwen samples carry a shell
// command, a write_file body, and stderr text marked SDF-FIXTURE-SECRET.
// None of it may reach the database, over either protocol.
func TestQwenToolArgumentsNeverReachTheDatabase(t *testing.T) {
	files := []string{"qwen_logs.json", "qwen_traces.json", "qwen_metrics.json"}
	for _, name := range files[:2] {
		body, err := os.ReadFile(filepath.Join(samplepath.Dir(), name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(body, []byte("SDF-FIXTURE-SECRET")) {
			t.Fatalf("%s has no SDF-FIXTURE-SECRET marker; the test would prove nothing", name)
		}
	}
	for _, protocol := range []string{"http", "grpc"} {
		db := ingest(t, files, protocol)
		matches, err := filepath.Glob(db + "*")
		if err != nil || len(matches) == 0 {
			t.Fatalf("%s: no database files: %v", protocol, err)
		}
		for _, path := range matches {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, banned := range []string{"SDF-FIXTURE-SECRET", "go test ./internal/summary", "func reset()", "reset_test.go:12"} {
				if bytes.Contains(raw, []byte(banned)) {
					t.Fatalf("%s: %s contains %q", protocol, filepath.Base(path), banned)
				}
			}
		}
		out := summarize(t, db, "--json")
		for _, want := range []string{`"session_id": "sample-qwen-1"`, `"run_shell_command"`, `"write_file"`} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s: summary lacks %s", protocol, want)
			}
		}
	}
}

// Workspace or system Qwen settings can turn on includeSensitiveSpanAttributes
// and logPrompts over what sdf init wrote. The span and log GenAI content
// (tool call arguments and results, input, output, and system messages) and
// the hook error text marked SDF-FIXTURE-SECRET never reach the database.
func TestQwenSensitiveSpanAttributesNeverReachTheDatabase(t *testing.T) {
	files := []string{"qwen_sensitive_logs.json", "qwen_sensitive_traces.json"}
	for _, name := range files {
		body, err := os.ReadFile(filepath.Join(samplepath.Dir(), name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(body, []byte("SDF-FIXTURE-SECRET")) {
			t.Fatalf("%s has no SDF-FIXTURE-SECRET marker; the test would prove nothing", name)
		}
	}
	for _, protocol := range []string{"http", "grpc"} {
		db := ingest(t, files, protocol)
		matches, err := filepath.Glob(db + "*")
		if err != nil || len(matches) == 0 {
			t.Fatalf("%s: no database files: %v", protocol, err)
		}
		for _, path := range matches {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, banned := range []string{"SDF-FIXTURE-SECRET", "sk-fixture", "BEGIN KEY", "token=abc"} {
				if bytes.Contains(raw, []byte(banned)) {
					t.Fatalf("%s: %s contains %q", protocol, filepath.Base(path), banned)
				}
			}
		}
		out := summarize(t, db, "--json")
		for _, want := range []string{`"session_id": "sample-qwen-sensitive"`, `"run_shell_command"`} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s: summary lacks %s\n%s", protocol, want, out)
			}
		}
	}
}

var leakSamples = []string{"leak_logs.json", "leak_traces.json", "leak_metrics.json"}

func dbHas(t *testing.T, db, needle string) bool {
	t.Helper()
	matches, err := filepath.Glob(db + "*")
	if err != nil || len(matches) == 0 {
		t.Fatalf("no database files: %v", err)
	}
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(needle)) {
			return true
		}
	}
	return false
}

// Review's /tmp/r212 harness as samples: content from every agent, in every
// place it can hide. By default none of it is stored, over either protocol,
// and the summary still has the session, its tools, and the commit.
func TestNoContentReachesTheDatabaseByDefault(t *testing.T) {
	for _, protocol := range []string{"http", "grpc"} {
		db := ingest(t, leakSamples, protocol)
		if dbHas(t, db, "SDF-LEAK-") {
			t.Fatalf("%s: content reached the database", protocol)
		}
		out := summarize(t, db, "--json")
		for _, want := range []string{`"session_id": "sample-leak-1"`, `"0123456789abcdef0123456789abcdef01234567"`} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s: summary lacks %s", protocol, want)
			}
		}
	}
}

// --store-content and SDF_STORE_CONTENT are the only ways to keep it.
func TestStoreContentOptIn(t *testing.T) {
	srv := startServeArgs(t, "--store-content")
	postSamples(t, srv.httpBase, leakSamples)
	srv.stop(t)
	if !dbHas(t, srv.db, "SDF-LEAK-01-") || !dbHas(t, srv.db, `"sdf.content":"kept"`) {
		t.Fatal("--store-content did not keep content")
	}
	if !strings.Contains(srv.stderr.String(), "storing prompt, response, tool, and error text for every agent (--store-content)") {
		t.Fatalf("no opt-in notice: %s", srv.stderr.String())
	}
	t.Setenv("SDF_STORE_CONTENT", "claude-code")
	srv = startServeArgs(t)
	postSamples(t, srv.httpBase, leakSamples)
	srv.stop(t)
	if !dbHas(t, srv.db, "claude_code_log_prompt") || dbHas(t, srv.db, "qwen_code_log_prompt") {
		t.Fatal("SDF_STORE_CONTENT=claude-code should keep Claude Code content only")
	}
	if !strings.Contains(srv.stderr.String(), "for claude-code (SDF_STORE_CONTENT)") {
		t.Fatalf("no opt-in notice: %s", srv.stderr.String())
	}
}
