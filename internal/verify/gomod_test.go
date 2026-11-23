package verify

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/task"
)

// Review round 2 (non-blocking): the docs said any new or changed go.mod
// fails a go test criterion, but only the root's (and the -C directory's)
// was checked. A go.mod, go.sum, go.work or go.work.sum anywhere, new,
// changed or deleted, now fails like a new test file.
func TestNestedGoModChangesFail(t *testing.T) {
	cases := map[string]func(f *fixture, t *testing.T) string{
		"new nested go.mod": func(f *fixture, t *testing.T) string {
			return f.agentCommit(t, map[string]string{"a/a.go": goodAdd, "a/sub/go.mod": "module example.com/sub\n\ngo 1.21\n"})
		},
		"new go.work in a subdirectory": func(f *fixture, t *testing.T) string {
			return f.agentCommit(t, map[string]string{"a/a.go": goodAdd, "tools/go.work": "go 1.21\n\nuse .\n"})
		},
		"changed nested go.mod": func(f *fixture, t *testing.T) string {
			return f.agentCommit(t, map[string]string{"a/a.go": goodAdd, "tools/go.mod": "module example.com/tools\n\ngo 1.22\n"})
		},
		"deleted nested go.mod": func(f *fixture, t *testing.T) string {
			f.agentCommit(t, map[string]string{"a/a.go": goodAdd})
			g(t, f.repo, "rm", "-q", "tools/go.mod")
			g(t, f.repo, "commit", "-q", "-m", "rm")
			return strings.TrimSpace(g(t, f.repo, "rev-parse", "HEAD"))
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			base := map[string]string{"tools/go.mod": "module example.com/tools\n\ngo 1.21\n", "tools/t.go": "package tools\n"}
			for k, v := range goBase {
				base[k] = v
			}
			f := newFixtureFiles(t, base, task.CriterionInput{Name: "unit", Argv: []string{"go", "test", "./a/"}, TimeoutS: 300})
			res := mustRun(t, goOpts(t, f, mk(f, t)))
			if res.Status != StatusFail || !hasCode(res, CodeDesignatedFileAdded) {
				t.Fatalf("%s %+v", res.Status, res.Reasons)
			}
		})
	}
	t.Run("honest", func(t *testing.T) {
		base := map[string]string{"tools/go.mod": "module example.com/tools\n\ngo 1.21\n", "tools/t.go": "package tools\n"}
		for k, v := range goBase {
			base[k] = v
		}
		f := newFixtureFiles(t, base, task.CriterionInput{Name: "unit", Argv: []string{"go", "test", "./a/"}, TimeoutS: 300})
		if res := mustRun(t, goOpts(t, f, f.agentCommit(t, map[string]string{"a/a.go": goodAdd}))); res.Status != StatusPass {
			t.Fatalf("%s %+v", res.Status, res.Reasons)
		}
	})
}
