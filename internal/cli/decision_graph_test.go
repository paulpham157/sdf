package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionGraphJSONIsTheAPIResponse(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	seedDecisionGraph(t, dbPath)
	for _, args := range [][]string{
		{"--id", "merge-first", "--statement", "Merge main first.", "--status", "hypothesis", "--decided-by", "agent"},
		{"--id", "show-status", "--statement", "Show agent status.", "--status", "decision", "--alternatives", "Log only; A check command"},
	} {
		if out := run(t, append([]string{"decision", "add", "--db", dbPath}, args...)...); out.code != 0 {
			t.Fatalf("%v: %s", args, out.stderr)
		}
	}
	for _, l := range [][3]string{
		{"blocks", "decision:merge-first", "decision:show-status"},
		{"conflicts_with", "decision:show-status", "decision:use-sqlite"},
	} {
		if out := run(t, "decision", "link", "--db", dbPath, "--relation", l[0], "--from", l[1], "--to", l[2]); out.code != 0 {
			t.Fatalf("%v: %s", l, out.stderr)
		}
	}

	out := run(t, "decision", "graph", "--db", dbPath, "--json")
	if out.code != 0 || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	srv := serveStore(t, openStore(t, dbPath))
	api := httpGet(t, srv.URL()+"api/decisions/graph")
	if out.stdout != api {
		t.Fatalf("sdf decision graph --json differs from GET /api/decisions/graph\ncli:\n%s\napi:\n%s", out.stdout, api)
	}
	var doc struct {
		Chapters []struct {
			ID    string `json:"id"`
			Facts struct {
				Locked    int `json:"locked"`
				Conflicts int `json:"conflicts"`
			} `json:"facts"`
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"chapters"`
		RelationTypes []struct {
			Name string `json:"name"`
		} `json:"relation_types"`
	}
	if err := json.Unmarshal([]byte(out.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Chapters) != 1 || doc.Chapters[0].Facts.Locked != 1 || doc.Chapters[0].Facts.Conflicts != 1 || len(doc.RelationTypes) == 0 {
		t.Fatalf("%+v", doc)
	}

	text := run(t, "decision", "graph", "--db", dbPath)
	if text.code != 0 || !strings.Contains(text.stdout, "endings reached · 1 locked · 1 conflict") || !strings.Contains(text.stdout, "\tlocked: show-status waits on merge-first") {
		t.Fatalf("%d %q %q", text.code, text.stdout, text.stderr)
	}
}

func TestDecisionGraphOnMissingStoreAndBadFlags(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	out := run(t, "decision", "graph", "--db="+missing, "--json")
	if out.code != 0 || !strings.Contains(out.stdout, "\"chapters\": []") {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("sdf decision graph created the missing store: %v", err)
	}
	if out := run(t, "decision", "graph", "--db", missing); out.code != 0 || out.stdout != "no decision\n" {
		t.Fatalf("%d %q", out.code, out.stdout)
	}
	if out := run(t, "decision", "graph", "--bogus"); out.code != 2 {
		t.Fatalf("unknown flag exit %d", out.code)
	}
	if out := run(t, "decision", "graph", "--db"); out.code != 2 {
		t.Fatalf("missing value exit %d", out.code)
	}
	if out := run(t, "decision", "graph", "--help"); out.code != 0 || !strings.HasPrefix(out.stdout, "usage: sdf decision graph") {
		t.Fatalf("help %d %q", out.code, out.stdout)
	}
}
