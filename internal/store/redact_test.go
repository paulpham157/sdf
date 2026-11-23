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
			if rec.Attributes["error.type"] != "tool_error" || rec.Attributes["sdf.redacted"] != "error,error.message,function_args" {
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
