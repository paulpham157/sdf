package cli_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionRetract(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	for _, id := range []string{"use-sqlite", "use-wal"} {
		if out := run(t, "decision", "add", "--db", dbPath, "--id", id, "--statement", "Store: "+id, "--status", "decision"); out.code != 0 {
			t.Fatal(out.stderr)
		}
	}
	if out := run(t, "decision", "link", "--db", dbPath, "--relation", "supersedes", "--from", "decision:use-wal", "--to", "decision:use-sqlite"); out.code != 0 {
		t.Fatal(out.stderr)
	}
	out := run(t, "decision", "retract", "--db", dbPath, "--id", "use-sqlite", "--reason", "Benchmark was\twrong", "--retracted-by", "user")
	if out.code != 0 || out.stdout != "" || out.stderr != "" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}

	again := run(t, "decision", "retract", "--db", dbPath, "--id", "use-sqlite")
	if again.code != 1 || again.stdout != "" || !strings.HasPrefix(again.stderr, "sdf decision retract: decision already retracted: use-sqlite at ") {
		t.Fatalf("%d %q %q", again.code, again.stdout, again.stderr)
	}
	unknown := run(t, "decision", "retract", "--db", dbPath, "--id", "nope")
	if unknown.code != 1 || unknown.stderr != "sdf decision retract: unknown reference: decision nope\n" {
		t.Fatalf("%d %q", unknown.code, unknown.stderr)
	}
	linked := run(t, "decision", "link", "--db", dbPath, "--relation", "constrains", "--from", "decision:use-wal", "--to", "decision:use-sqlite")
	if linked.code != 1 || linked.stderr != "sdf decision link: decision retracted: decision:use-sqlite takes no new edges\n" {
		t.Fatalf("%d %q", linked.code, linked.stderr)
	}
	for _, args := range [][]string{
		{"decision", "retract"},
		{"decision", "retract", "--id"},
		{"decision", "retract", "--id", "x", "--status", "y"},
	} {
		if bad := run(t, args...); bad.code != 2 || bad.stdout != "" {
			t.Fatalf("%v: %d %q %q", args, bad.code, bad.stdout, bad.stderr)
		}
	}
	help := run(t, "decision", "retract", "--help")
	if help.code != 0 || help.stdout != "usage: sdf decision retract --id ID [--reason TEXT] [--retracted-by user|agent] [--db PATH]\n" {
		t.Fatalf("%d %q", help.code, help.stdout)
	}
	if top := run(t, "decision", "--help"); !strings.Contains(top.stdout, "<add|link|list|retract|audit|brief>") {
		t.Fatalf("%q", top.stdout)
	}

	table := run(t, "decision", "list", "--db", dbPath)
	lines := strings.Split(table.stdout, "\n")
	if got := strings.Fields(lines[2]); len(got) < 6 || strings.Join(got[:6], " ") != "use-sqlite decision retracted, superseded by use-wal" {
		t.Fatalf("%q\n%s", lines[2], table.stdout)
	}
	if !strings.Contains(lines[3], "retracted: ") || !strings.HasSuffix(lines[3], `by user; reason: "Benchmark was\twrong"`) {
		t.Fatalf("%q\n%s", lines[3], table.stdout)
	}
	asJSON := run(t, "decision", "list", "--db", dbPath, "--json")
	srv := serveStore(t, openStore(t, dbPath))
	api := httpGet(t, srv.URL()+"api/decisions")
	if asJSON.stdout != api {
		t.Fatalf("cli:\n%s\napi:\n%s", asJSON.stdout, api)
	}
	var doc struct {
		Decisions []struct {
			ID              string   `json:"id"`
			EffectiveStatus string   `json:"effective_status"`
			SupersededBy    []string `json:"superseded_by"`
			Retraction      *struct {
				RetractedAt string `json:"retracted_at"`
				Reason      string `json:"reason"`
				RetractedBy string `json:"retracted_by"`
			} `json:"retraction"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(api), &doc); err != nil {
		t.Fatal(err)
	}
	d := doc.Decisions[0]
	if d.EffectiveStatus != "retracted" || d.Retraction == nil || d.Retraction.Reason != "Benchmark was\twrong" || d.Retraction.RetractedBy != "user" || d.Retraction.RetractedAt == "" || strings.Join(d.SupersededBy, ",") != "use-wal" {
		t.Fatalf("%+v %+v", d, d.Retraction)
	}
	if doc.Decisions[1].EffectiveStatus != "active" || doc.Decisions[1].Retraction != nil {
		t.Fatalf("%+v", doc.Decisions[1])
	}
}
