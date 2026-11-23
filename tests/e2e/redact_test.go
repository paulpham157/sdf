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
