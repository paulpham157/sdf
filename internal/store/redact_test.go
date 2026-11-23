package store

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/samplepath"
)

func TestInsertDropsQwenToolArgumentsAndErrorText(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "t.sqlite")
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	var sent []record.Record
	for signal, name := range map[string]string{"logs": "qwen_logs.json", "traces": "qwen_traces.json"} {
		body, err := os.ReadFile(filepath.Join(samplepath.Dir(), name))
		if err != nil {
			t.Fatal(err)
		}
		recs, err := otlp.Decode(signal, body, "application/json")
		if err != nil {
			t.Fatal(err)
		}
		sent = append(sent, recs...)
	}
	argsBefore := 0
	for _, rec := range sent {
		if _, ok := rec.Attributes["function_args"]; ok {
			argsBefore++
		}
	}
	if argsBefore != 5 {
		t.Fatalf("decoded %d function_args, want 5", argsBefore)
	}
	if _, err := s.Insert(sent); err != nil {
		t.Fatal(err)
	}
	// The caller's records are not modified.
	argsAfter := 0
	for _, rec := range sent {
		if _, ok := rec.Attributes["function_args"]; ok {
			argsAfter++
		}
	}
	if argsAfter != argsBefore {
		t.Fatalf("Insert changed the caller's records: %d of %d function_args left", argsAfter, argsBefore)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, rec := range loaded {
		for _, key := range []string{"function_args", "error", "error.message"} {
			if _, ok := rec.Attributes[key]; ok && (rec.Attributes["event.name"] == "qwen-code.tool_call" || key == "function_args" || strings.HasPrefix(deref(rec.Name), "qwen-code.tool")) {
				t.Fatalf("%s kept on %s", key, deref(rec.Name))
			}
		}
		if rec.Attributes["event.name"] != "qwen-code.tool_call" {
			continue
		}
		calls++
		if rec.Attributes["function_name"] == nil || rec.Attributes["call_id"] == nil || rec.Attributes["duration_ms"] == nil || rec.Attributes["success"] == nil {
			t.Fatalf("tool call lost its metadata: %v", rec.Attributes)
		}
		if n, ok := rec.Attributes["function_args_bytes"].(int64); !ok || n <= 2 {
			t.Fatalf("function_args_bytes %v", rec.Attributes["function_args_bytes"])
		}
		if rec.Attributes["function_name"] == "run_shell_command" {
			if rec.Attributes["error.type"] != "tool_error" || rec.Attributes["sdf.redacted"] != "body,error,error.message,function_args" {
				t.Fatalf("failed call: %v", rec.Attributes)
			}
		}
	}
	if calls != 5 {
		t.Fatalf("%d tool_call logs", calls)
	}
	files, _ := filepath.Glob(db + "*")
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"SDF-FIXTURE-SECRET", "go test ./internal/summary", "func reset()"} {
			if bytes.Contains(raw, []byte(banned)) {
				t.Fatalf("%s contains %q", filepath.Base(path), banned)
			}
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Workspace or system Qwen settings can turn includeSensitiveSpanAttributes
// and logPrompts back on over what sdf init writes. The GenAI content
// attributes and the hook error text never reach SQLite.
func TestInsertDropsGenAIContentAndHookErrors(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "t.sqlite")
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	var sent []record.Record
	for signal, name := range map[string]string{"logs": "qwen_sensitive_logs.json", "traces": "qwen_sensitive_traces.json"} {
		body, err := os.ReadFile(filepath.Join(samplepath.Dir(), name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(body, []byte("SDF-FIXTURE-SECRET")) {
			t.Fatalf("%s has no marker", name)
		}
		recs, err := otlp.Decode(signal, body, "application/json")
		if err != nil {
			t.Fatal(err)
		}
		sent = append(sent, recs...)
	}
	if _, err := s.Insert(sent); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(sent) {
		t.Fatalf("loaded %d of %d", len(loaded), len(sent))
	}
	hooks := 0
	for _, rec := range loaded {
		for _, key := range record.RedactedKeys {
			if _, ok := rec.Attributes[key]; ok {
				t.Fatalf("%s kept on %s", key, deref(rec.Name))
			}
		}
		if deref(rec.Name) == "qwen-code.hook" {
			hooks++
			if rec.Attributes["error"] != nil || deref(rec.StatusMessage) != "hook error" || rec.StatusCode != "ERROR" {
				t.Fatalf("hook span: %v %q %q", rec.Attributes, deref(rec.StatusMessage), rec.StatusCode)
			}
		}
		if deref(rec.Name) == "qwen-code.llm_request" && rec.Attributes["gen_ai.usage.input_tokens"] == nil {
			t.Fatalf("llm_request lost its usage: %v", rec.Attributes)
		}
	}
	if hooks != 1 {
		t.Fatalf("%d hook spans", hooks)
	}
	files, _ := filepath.Glob(db + "*")
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"SDF-FIXTURE-SECRET", "sk-fixture", "BEGIN KEY", "token=abc"} {
			if bytes.Contains(raw, []byte(banned)) {
				t.Fatalf("%s contains %q", filepath.Base(path), banned)
			}
		}
	}
}

func insertLeakSamples(t *testing.T, keep func(record.Record) bool) (string, []record.Record, *Store) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "t.sqlite")
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	s.KeepContent(keep)
	var sent []record.Record
	for _, signal := range []string{"logs", "traces", "metrics"} {
		body, err := os.ReadFile(filepath.Join(samplepath.Dir(), "leak_"+signal+".json"))
		if err != nil {
			t.Fatal(err)
		}
		recs, err := otlp.Decode(signal, body, "application/json")
		if err != nil {
			t.Fatal(err)
		}
		sent = append(sent, recs...)
	}
	if _, err := s.Insert(sent); err != nil {
		t.Fatal(err)
	}
	return db, sent, s
}

func dbContains(t *testing.T, db, needle string) bool {
	t.Helper()
	files, _ := filepath.Glob(db + "*")
	for _, path := range files {
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

// Review's harness (/tmp/r212) as samples: prompt, tool, message, and error
// text from every agent, in attributes, nested maps, the resource, the log
// body, span status, and metric points. None of it is stored by default.
func TestInsertStoresNoContentByDefault(t *testing.T) {
	db, _, s := insertLeakSamples(t, nil)
	known, err := s.KnownCommit("0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !known {
		t.Fatal("the commit in tool_parameters is no longer known")
	}
	if dbContains(t, db, "SDF-LEAK-") {
		t.Fatal("content reached the database")
	}
	kept := 0
	for _, rec := range loaded {
		if rec.Attributes["input_tokens"] == int64(1200) || rec.Attributes["gen_ai.usage.input_tokens"] == int64(1200) {
			kept++
		}
		if rec.Attributes[record.ContentKept] != nil {
			t.Fatalf("marked kept: %v", rec.Attributes)
		}
	}
	if kept != 9 {
		t.Fatalf("%d records kept their token counts, want 9", kept)
	}
}

func TestInsertKeepsContentOnlyWhenAsked(t *testing.T) {
	db, sent, s := insertLeakSamples(t, func(rec record.Record) bool { return deref(rec.ServiceName) == "claude-code" })
	_ = s.Close()
	claude, other := 0, 0
	for _, rec := range sent {
		for _, kv := range rec.Attributes {
			if text, ok := kv.(string); ok && strings.HasPrefix(text, "SDF-LEAK-") {
				if !dbContains(t, db, text) == (deref(rec.ServiceName) == "claude-code") {
					t.Fatalf("%s from %s: stored=%v", text, deref(rec.ServiceName), dbContains(t, db, text))
				}
				if deref(rec.ServiceName) == "claude-code" {
					claude++
				} else {
					other++
				}
			}
		}
	}
	if claude == 0 || other == 0 {
		t.Fatalf("claude %d other %d markers checked", claude, other)
	}
}
