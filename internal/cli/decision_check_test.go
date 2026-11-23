package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionCheckForbidsFromTheStatement(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	for _, args := range [][]string{
		{"--id", "dont-push", "--statement", "Do not push until the user says so.", "--status", "decision", "--decided-by", "user"},
		{"--id", "use-sqlite", "--statement", "Keep the collector store in SQLite.", "--status", "decision", "--decided-by", "agent"},
		{"--id", "maybe-merge", "--statement", "Do not merge.", "--status", "hypothesis", "--decided-by", "user"},
	} {
		out := run(t, append([]string{"decision", "add", "--db", dbPath}, args...)...)
		if out.code != 0 || out.stdout != "" || out.stderr != "" {
			t.Fatalf("%v: %d %q %q", args, out.code, out.stdout, out.stderr)
		}
	}

	forbid := run(t, "decision", "check", "push", "--db", dbPath)
	if forbid.code != 1 || forbid.stderr != "" {
		t.Fatalf("%d %q %q", forbid.code, forbid.stdout, forbid.stderr)
	}
	if !strings.Contains(forbid.stdout, "forbidden: push\n") || !strings.Contains(forbid.stdout, "dont-push\tuser\tdo not push\t") {
		t.Fatalf("%q", forbid.stdout)
	}
	if strings.Contains(forbid.stdout, "use-sqlite") || strings.Contains(forbid.stdout, "maybe-merge") {
		t.Fatalf("non-constraint listed\n%s", forbid.stdout)
	}

	ok := run(t, "decision", "check", "--db", dbPath, "merge")
	if ok.code != 0 || ok.stderr != "" || ok.stdout != "allowed: merge\n" {
		t.Fatalf("%d %q %q", ok.code, ok.stdout, ok.stderr)
	}

	js := run(t, "decision", "check", "--json", "--db="+dbPath, "push")
	if js.code != 1 || js.stderr != "" {
		t.Fatalf("%d %q %q", js.code, js.stdout, js.stderr)
	}
	var doc struct {
		Action   string `json:"action"`
		Kind     string `json:"kind"`
		Allowed  bool   `json:"allowed"`
		Blocking []struct {
			ID        string `json:"id"`
			DecidedBy string `json:"decided_by"`
			Statement string `json:"statement"`
			Match     string `json:"match"`
		} `json:"blocking"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Action != "push" || doc.Kind != "push" || doc.Allowed || len(doc.Blocking) != 1 {
		t.Fatalf("%+v", doc)
	}
	hit := doc.Blocking[0]
	if hit.ID != "dont-push" || hit.DecidedBy != "user" || hit.Match != "do not push" || hit.Statement != "Do not push until the user says so." {
		t.Fatalf("%+v", hit)
	}

	arch := run(t, "decision", "check", "--db", dbPath, "replace sqlite")
	if arch.code != 0 || arch.stdout != "allowed: replace sqlite (architecture)\n" {
		t.Fatalf("%d %q %q", arch.code, arch.stdout, arch.stderr)
	}
}

func TestDecisionCheckMissingStoreIsAllowedAndUncreated(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	out := run(t, "decision", "check", "--db", dbPath, "deploy")
	if out.code != 0 || out.stderr != "" || out.stdout != "allowed: deploy\n" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("check created a store")
	}
}

func TestDecisionCheckUsageAndStoreError(t *testing.T) {
	for _, args := range [][]string{
		{"decision", "check"},
		{"decision", "check", "   "},
		{"decision", "check", "push", "merge"},
		{"decision", "check", "--bogus", "push"},
		{"decision", "check", "--db"},
	} {
		out := run(t, args...)
		if out.code != 2 || out.stdout != "" {
			t.Fatalf("%v: %d %q %q", args, out.code, out.stdout, out.stderr)
		}
	}
	help := run(t, "decision", "check", "--help")
	if help.code != 0 || help.stdout != "usage: sdf decision check <action> [--db PATH] [--json]\n" {
		t.Fatalf("%d %q", help.code, help.stdout)
	}
	missing := filepath.Join(t.TempDir(), "missing", "telemetry.sqlite")
	out := run(t, "decision", "check", "--db", missing, "push")
	if out.code != 3 || out.stdout != "" || !strings.Contains(out.stderr, "does not exist") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}
