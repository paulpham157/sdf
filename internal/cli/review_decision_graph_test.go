package cli_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// shellWords splits a command made of bare words and POSIX single-quoted
// words (with the '\” escape), the only forms WhatIfCommand emits.
func shellWords(t *testing.T, cmd string) []string {
	t.Helper()
	var out []string
	var cur strings.Builder
	inWord, quoted := false, false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quoted && c == '\'':
			quoted = false
		case quoted:
			cur.WriteByte(c)
		case c == '\'':
			quoted, inWord = true, true
		case c == '\\' && i+1 < len(cmd):
			i++
			cur.WriteByte(cmd[i])
			inWord = true
		case c == ' ':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if quoted {
		t.Fatalf("unterminated quote in %q", cmd)
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

// The copied what-if command must run as printed: for a 128-character
// parent id and an alternative full of shell metacharacters, sdf decision
// add accepts it and stores the alternative text verbatim as a hypothesis.
func TestReviewWhatIfCommandRunsAsPrinted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.sqlite")
	parent := strings.Repeat("p", 128)
	alt := "it's $HOME `id` \"x\"; or not"
	if out := run(t, "decision", "add", "--db", dbPath, "--id", parent, "--status", "decision", "--statement", "Parent.",
		"--alternatives", alt); out.code != 0 {
		t.Fatalf("%d %s", out.code, out.stderr)
	}
	out := run(t, "decision", "graph", "--db="+dbPath, "--json")
	if out.code != 0 {
		t.Fatalf("%d %s", out.code, out.stderr)
	}
	var doc struct {
		Chapters []struct {
			Nodes []struct {
				Kind, Title, Command string
			} `json:"nodes"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal([]byte(out.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	var cmds []string
	var titles []string
	for _, n := range doc.Chapters[0].Nodes {
		if n.Kind == "alternative" {
			cmds = append(cmds, n.Command)
			titles = append(titles, n.Title)
		}
	}
	if len(cmds) != 2 {
		t.Fatalf("alternatives %q", titles)
	}
	for i, cmd := range cmds {
		words := shellWords(t, cmd)
		if len(words) < 3 || words[0] != "sdf" {
			t.Fatalf("%q", cmd)
		}
		args := append(words[1:], "--db", dbPath)
		if res := run(t, args...); res.code != 0 {
			t.Fatalf("what-if %d did not run: %d %s\n%s", i, res.code, res.stderr, cmd)
		}
	}
	list := run(t, "decision", "list", "--db", dbPath, "--json")
	for _, title := range titles {
		if !strings.Contains(list.stdout, strings.ReplaceAll(strings.ReplaceAll(title, `\`, `\\`), `"`, `\"`)) {
			t.Fatalf("stored hypotheses do not carry %q:\n%s", title, list.stdout)
		}
	}
	if strings.Count(list.stdout, `"status": "hypothesis"`) != 2 {
		t.Fatalf("want two hypotheses:\n%s", list.stdout)
	}
}

// Text output names conflicts and challenged items, and --db= and --db
// forms read the same store.
func TestReviewDecisionGraphTextAndFlagForms(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "t.sqlite")
	seedDecisionGraph(t, dbPath)
	for _, args := range [][]string{
		{"--id", "a", "--statement", "A.", "--status", "decision"},
		{"--id", "b", "--statement", "B.", "--status", "decision"},
	} {
		if out := run(t, append([]string{"decision", "add", "--db", dbPath}, args...)...); out.code != 0 {
			t.Fatal(out.stderr)
		}
	}
	for _, l := range [][3]string{
		{"conflicts_with", "decision:b", "decision:a"},
		{"contradicts", "session:sess-1", "decision:a"},
	} {
		if out := run(t, "decision", "link", "--db", dbPath, "--relation", l[0], "--from", l[1], "--to", l[2]); out.code != 0 {
			t.Fatalf("%v %s", l, out.stderr)
		}
	}
	text := run(t, "decision", "graph", "--db", dbPath)
	if text.code != 0 || !strings.Contains(text.stdout, "\tconflict: a ⟷ b, both active") || !strings.Contains(text.stdout, "\tchallenged: a: the latest evidence contradicts it (session sess-1)") {
		t.Fatalf("%d %q %q", text.code, text.stdout, text.stderr)
	}
	if eq := run(t, "decision", "graph", "--db="+dbPath, "--json"); eq.stdout != run(t, "decision", "graph", "--json", "--db", dbPath).stdout {
		t.Fatal("--db= and --db differ")
	}
	if out := run(t, "decision", "graph", "--db", dbPath, "extra"); out.code != 2 || !strings.Contains(out.stderr, "extra") {
		t.Fatalf("positional arg: %d %q", out.code, out.stderr)
	}
	if out := run(t, "decision", "graph", "--db", dbPath, "--prices", "/nonexistent/prices.json"); out.code != 1 || out.stdout != "" {
		t.Fatalf("bad --prices: %d %q %q", out.code, out.stdout, out.stderr)
	}
}
