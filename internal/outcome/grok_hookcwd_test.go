package outcome

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/summary"
)

// grokHookSession is a Grok CLI session as sdf stores it when the session
// hook from `sdf init grok --write` is installed: the hook's open and close
// logs carry sdf.session.cwd, and the telemetry in between carries none.
func grokHookSession(session, cwd string, startSec int64) []record.Record {
	edge := func(name string, sec int64) record.Record {
		attrs := map[string]any{"session.id": session}
		if cwd != "" {
			attrs["sdf.session.cwd"] = cwd
		}
		return record.Record{
			Signal: "logs", ServiceName: record.Str("grok-cli"), SessionID: record.Str(session),
			Name: record.Str(name), TimeUnixNano: record.Int64(sec * 1_000_000_000),
			Attributes: attrs, Resource: map[string]any{"service.name": "grok-cli"},
		}
	}
	event := record.Record{
		Signal: "logs", ServiceName: record.Str("grok-cli"), SessionID: record.Str(session),
		Name: record.Str("grok_code.session_end"), TimeUnixNano: record.Int64((startSec + 4) * 1_000_000_000),
		Attributes: map[string]any{"session.id": session, "duration_secs": 4},
		Resource:   map[string]any{"service.name": "grok-cli", "grok_code.schema.version": "v1"},
	}
	return []record.Record{edge("sdf.session.open", startSec), event, edge("sdf.session.close", startSec+5)}
}

func TestGrokHookWorkingDirectoryLinksByHeuristic(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_003, "grok change")
	report := summary.Summarize(grokHookSession("grok-h1", dir, 1_700_000_000), false)
	if len(report.Sessions) != 1 || report.Sessions[0].Tool != "grok-cli" {
		t.Fatalf("%+v", report.Sessions)
	}
	if dirs := report.Sessions[0].Repository.WorkDirs; len(dirs) != 1 || dirs[0] != dir {
		t.Fatalf("work dirs %v", dirs)
	}
	Attach(&report, Options{})
	got := report.Sessions[0].Outcome
	if got.Status != "linked" || got.Match != "heuristic" || len(got.Commits) != 1 || got.Commits[0].SHA != sha {
		t.Fatalf("%+v", got)
	}
}

func TestGrokHookWorkingDirectoryDisagreeingWithRepoStaysUnlinked(t *testing.T) {
	dir := gitRepo(t)
	commitAt(t, dir, 1_700_000_003, "grok change")
	other := gitRepo(t)
	commitAt(t, other, 1_700_000_003, "elsewhere")
	report := summary.Summarize(grokHookSession("grok-h2", dir, 1_700_000_000), false)
	Attach(&report, Options{Repo: other})
	got := report.Sessions[0].Outcome
	if got.Status != "unlinked" || !strings.Contains(got.Reason, "--repo and the working directory are different repositories") {
		t.Fatalf("%+v", got)
	}
}

func TestGrokHookWorkingDirectoryNotACheckoutStaysUnlinked(t *testing.T) {
	plain := t.TempDir()
	report := summary.Summarize(grokHookSession("grok-h3", plain, 1_700_000_000), false)
	Attach(&report, Options{})
	got := report.Sessions[0].Outcome
	if got.Status != "unlinked" || !strings.Contains(got.Reason, "not a git repository") {
		t.Fatalf("%+v", got)
	}
}

func TestSessionCwdIsReadOnlyFromHookEdges(t *testing.T) {
	// sdf.session.cwd on any other record is not a working directory.
	recs := grokHookSession("grok-h4", "", 1_700_000_000)
	recs[1].Attributes["sdf.session.cwd"] = "/not/from/a/hook"
	report := summary.Summarize(recs, false)
	if dirs := report.Sessions[0].Repository.WorkDirs; len(dirs) != 0 {
		t.Fatalf("work dirs %v", dirs)
	}
}

func gitHookEvent(session, repo, sha string, sec int64) record.Record {
	return record.Record{
		Signal: "logs", ServiceName: record.Str("grok-cli"), SessionID: record.Str(session),
		Name: record.Str("sdf.git.commit"), TimeUnixNano: record.Int64(sec * 1_000_000_000),
		Attributes: map[string]any{"session.id": session, "vcs.ref.head.revision": sha, "sdf.git.repo": repo, "sdf.git.agent": "grok-cli"},
		Resource:   map[string]any{"service.name": "grok-cli"},
	}
}

func TestGrokHookWorkingDirectoryVerifiesTheGitHookPath(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_003, "grok change")
	same := append(grokHookSession("grok-h5", dir, 1_700_000_000), gitHookEvent("grok-h5", dir, sha, 1_700_000_003))
	report := summary.Summarize(same, false)
	Attach(&report, Options{})
	if got := report.Sessions[0].Outcome; got.Status != "linked" || got.Match != "hook" || got.Commits[0].SHA != sha {
		t.Fatalf("%+v", got)
	}
	other := gitRepo(t)
	otherSHA := commitAt(t, other, 1_700_000_003, "elsewhere")
	diff := append(grokHookSession("grok-h6", dir, 1_700_000_000), gitHookEvent("grok-h6", other, otherSHA, 1_700_000_003))
	report = summary.Summarize(diff, false)
	Attach(&report, Options{})
	if got := report.Sessions[0].Outcome; got.Status != "unlinked" || !strings.Contains(got.Reason, "different repositories") {
		t.Fatalf("%+v", got)
	}
}
