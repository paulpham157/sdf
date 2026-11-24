package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionListSessionFilter(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	seedDecisionGraph(t, dbPath)

	out := run(t, "decision", "list", "--db", dbPath, "--session", "sess-1", "--json")
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	doc := decisionsDoc(t, out.stdout)
	if len(doc.Decisions) != 1 || doc.Decisions[0].ID != "use-sqlite" {
		t.Fatalf("decisions %+v", doc.Decisions)
	}
	if len(doc.Edges) != 1 || doc.Edges[0].Relation != "validates" || doc.Edges[0].From.Kind != "session" || doc.Edges[0].From.Ref != "sess-1" {
		t.Fatalf("edges %+v", doc.Edges)
	}

	other := run(t, "decision", "list", "--db", dbPath, "--session=other", "--json")
	if other.code != 0 || other.stderr != "" {
		t.Fatalf("%d %q %q", other.code, other.stdout, other.stderr)
	}
	empty := decisionsDoc(t, other.stdout)
	if len(empty.Decisions) != 0 || len(empty.Edges) != 0 {
		t.Fatalf("%+v", empty)
	}
	if !strings.Contains(other.stdout, `"decisions": []`) || !strings.Contains(other.stdout, `"edges": []`) {
		t.Fatalf("%s", other.stdout)
	}

	text := run(t, "decision", "list", "--db", dbPath, "--session", "sess-1")
	if text.code != 0 || !strings.Contains(text.stdout, "use-sqlite") || strings.Contains(text.stdout, "pure-go") {
		t.Fatalf("%d %q %q", text.code, text.stdout, text.stderr)
	}

	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	asJSON := run(t, "decision", "list", "--db", missing, "--session", "sess-1", "--json")
	if asJSON.code != 0 || asJSON.stderr != "" {
		t.Fatalf("%d %q %q", asJSON.code, asJSON.stdout, asJSON.stderr)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("created the store: %v", err)
	}

	bad := run(t, "decision", "list", "--session")
	if bad.code != 2 || !strings.Contains(bad.stderr, "--session") {
		t.Fatalf("%d %q", bad.code, bad.stderr)
	}
}
