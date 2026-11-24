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

func TestDecisionCheckKeepsAgentForbidsAndDropsOutOfForce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	add := func(id, statement, status, by string) {
		t.Helper()
		out := run(t, "decision", "add", "--db", dbPath,
			"--id", id, "--statement", statement, "--status", status, "--decided-by", by)
		if out.code != 0 || out.stdout != "" || out.stderr != "" {
			t.Fatalf("%s: %d %q %q", id, out.code, out.stdout, out.stderr)
		}
	}
	link := func(rel, from, to string) {
		t.Helper()
		out := run(t, "decision", "link", "--db", dbPath, "--relation", rel, "--from", "decision:"+from, "--to", "decision:"+to)
		if out.code != 0 || out.stdout != "" || out.stderr != "" {
			t.Fatalf("%s: %d %q %q", rel, out.code, out.stdout, out.stderr)
		}
	}
	add("dont-push", "Do not push.", "decision", "user")
	add("maybe", "Do not push either.", "hypothesis", "agent")
	add("old-merge", "Do not merge.", "decision", "user")
	add("new-merge", "Merging is allowed.", "decision", "user")
	link("supersedes", "new-merge", "old-merge")
	// An agent decision with no constrains edge. brief.Classify would drop it.
	add("agent-merge", "Do not merge the branch.", "decision", "agent")
	add("no-release", "Do not release.", "decision", "agent")
	retract := run(t, "decision", "retract", "--db", dbPath, "--id", "no-release", "--retracted-by", "user")
	if retract.code != 0 || retract.stderr != "" {
		t.Fatalf("retract: %d %q", retract.code, retract.stderr)
	}
	add("wait", "Waiting on review.", "hypothesis", "user")
	add("no-deploy", "Do not deploy.", "decision", "agent")
	link("blocks", "wait", "no-deploy")

	push := run(t, "decision", "check", "--db", dbPath, "push")
	if push.code != 1 || !strings.Contains(push.stdout, "dont-push\tuser\t") || strings.Contains(push.stdout, "maybe") {
		t.Fatalf("push %d %q %q", push.code, push.stdout, push.stderr)
	}
	merge := run(t, "decision", "check", "--db", dbPath, "merge")
	if merge.code != 1 || !strings.Contains(merge.stdout, "agent-merge\tagent\t") || strings.Contains(merge.stdout, "old-merge") {
		t.Fatalf("merge %d %q %q", merge.code, merge.stdout, merge.stderr)
	}
	release := run(t, "decision", "check", "--db", dbPath, "release")
	if release.code != 0 || release.stdout != "allowed: release\n" {
		t.Fatalf("release %d %q %q", release.code, release.stdout, release.stderr)
	}
	deploy := run(t, "decision", "check", "--db", dbPath, "deploy")
	if deploy.code != 1 || !strings.Contains(deploy.stdout, "no-deploy\tagent\t") {
		t.Fatalf("deploy %d %q %q", deploy.code, deploy.stdout, deploy.stderr)
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
	if help.code != 0 || !strings.Contains(help.stdout, "usage: sdf decision check <action> [--db PATH] [--json]") || !strings.Contains(help.stdout, "Exit 0 is allowed") || !strings.Contains(help.stdout, "Exit 1 is forbidden") || !strings.Contains(help.stdout, "Exit 2 is a usage error") || !strings.Contains(help.stdout, "Exit 3 is a store or write error") || !strings.Contains(help.stdout, "200ms busy timeout") || !strings.Contains(help.stdout, "sdf-decisions") {
		t.Fatalf("%d %q", help.code, help.stdout)
	}
	if strings.Contains(help.stdout, "500ms") {
		t.Fatalf("check --help should not mention brief's 500ms load deadline: %q", help.stdout)
	}
	missing := filepath.Join(t.TempDir(), "missing", "telemetry.sqlite")
	out := run(t, "decision", "check", "--db", missing, "push")
	if out.code != 3 || out.stdout != "" || !strings.Contains(out.stderr, "does not exist") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}
