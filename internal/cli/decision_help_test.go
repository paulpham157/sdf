package cli

import (
	"strings"
	"testing"
)

// Top-level sdf --help must name every decision subcommand from the dispatch
// table, so graph, audit, or a later addition cannot be left off that line.
func TestTopHelpListsEveryDecisionSubcommand(t *testing.T) {
	var buf strings.Builder
	writeTopHelp(&buf)
	var line string
	for _, l := range strings.Split(buf.String(), "\n") {
		fields := strings.Fields(l)
		if len(fields) > 0 && fields[0] == "decision" {
			line = strings.TrimSpace(l)
			break
		}
	}
	if line == "" {
		t.Fatal("sdf --help has no decision line")
	}
	open, end := strings.LastIndex(line, "("), strings.LastIndex(line, ")")
	if open < 0 || end <= open {
		t.Fatalf("decision help line has no parenthetical list: %q", line)
	}
	var listed []string
	for _, name := range strings.Split(line[open+1:end], ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			listed = append(listed, name)
		}
	}
	want := decisionSubcommands()
	if strings.Join(listed, " ") != strings.Join(want, " ") {
		t.Fatalf("decision help lists %v, dispatch has %v\n%s", listed, want, line)
	}
}
