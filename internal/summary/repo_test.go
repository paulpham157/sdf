package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

func TestRepositoryFactsUseOnlyExportedAttributes(t *testing.T) {
	full := strings.Repeat("ab", 20)
	short := "abc123"
	facts := repositoryFacts([]record.Record{
		{
			Signal: "logs", Name: record.Str("pi.session.start"), TimeUnixNano: record.Int64(5),
			Attributes: map[string]any{"pi.cwd": "/work/pi"},
		},
		{
			Signal: "logs", Name: record.Str("claude_code.tool_result"), TimeUnixNano: record.Int64(9),
			Attributes: map[string]any{
				"vcs.ref.head.revision": full,
				"git_commit_id":         short,
				"tool_parameters":       `{"git_commit_id":"` + strings.Repeat("cd", 20) + `","bash_command":"git commit"}`,
				"vcs.owner.name":        "example",
				"vcs.repository.name":   "demo",
				"vcs.provider.name":     "github",
			},
		},
		{
			Signal: "logs", Name: record.Str("claude_code.user_prompt"),
			Attributes: map[string]any{"vcs.ref.head.revision": strings.Repeat("ef", 20)},
			Resource:   map[string]any{"pi.cwd": "/work/from-resource"},
		},
	})
	if strings.Join(facts.WorkDirs, ",") != "/work/from-resource,/work/pi" && strings.Join(facts.WorkDirs, ",") != "/work/pi,/work/from-resource" {
		t.Fatal(facts.WorkDirs)
	}
	joined := strings.Join(facts.CommitSHAs, ",")
	if !strings.Contains(joined, full) || !strings.Contains(joined, strings.Repeat("cd", 20)) || strings.Contains(joined, short) || strings.Contains(joined, strings.Repeat("ef", 20)) {
		t.Fatal(facts.CommitSHAs)
	}
	if facts.Owner != "example" || facts.RepositoryName != "demo" || facts.Provider != "github" {
		t.Fatalf("%+v", facts)
	}
	if facts.StartUnixNano == nil || *facts.StartUnixNano != 5 || *facts.EndUnixNano != 9 {
		t.Fatalf("%v %v", facts.StartUnixNano, facts.EndUnixNano)
	}
}

func TestSeveralWorkspacePathsAreAmbiguous(t *testing.T) {
	facts := repositoryFacts([]record.Record{{
		Signal: "logs", Name: record.Str("claude_code.tool_result"),
		Attributes: map[string]any{"workspace.host_paths": []any{"/a", "/b"}},
	}})
	if !facts.AmbiguousWorkDirs || len(facts.WorkDirs) != 0 {
		t.Fatalf("%+v", facts)
	}
}

func TestSamplePiSessionKeepsItsWorkingDirectory(t *testing.T) {
	session := Summarize(piRows(t), false).Sessions[0]
	if len(session.Repository.WorkDirs) != 1 || session.Repository.WorkDirs[0] != "/tmp/sample" {
		t.Fatal(session.Repository.WorkDirs)
	}
	if len(session.Repository.CommitSHAs) != 0 {
		t.Fatal(session.Repository.CommitSHAs)
	}
}
