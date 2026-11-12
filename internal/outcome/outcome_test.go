package outcome

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/summary"
)

func TestHeuristicLinkUsesOneCommitAndDoesNotCallGitHub(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "one change")
	offline := &summary.Report{Sessions: []summary.SessionSummary{
		sess("s1", dir, 1_700_000_000, 1_700_000_005, "1"),
	}}
	Attach(offline, Options{})
	got := offline.Sessions[0].Outcome
	if got.Status != "linked" || got.Match != "heuristic" || len(got.Commits) != 1 || got.Commits[0].SHA != sha {
		t.Fatalf("%+v", got)
	}
	if got.Accepted != nil || !strings.Contains(got.AcceptedReason, "not requested") {
		t.Fatalf("%+v", got)
	}
	top, _ := filepath.EvalSymlinks(got.Repo)
	want, _ := filepath.EvalSymlinks(dir)
	if top != want {
		t.Fatalf("repo %s want %s", got.Repo, dir)
	}
}

func TestSeveralCommitsStayUnlinked(t *testing.T) {
	dir := gitRepo(t)
	commitAt(t, dir, 1_700_000_000, "first")
	commitAt(t, dir, 1_700_000_010, "second")
	report := &summary.Report{Sessions: []summary.SessionSummary{
		sess("s1", dir, 1_700_000_000, 1_700_000_020, "1"),
	}}
	Attach(report, Options{Slack: -1})
	got := report.Sessions[0].Outcome
	if got.Status != "unlinked" || !strings.Contains(got.Reason, "2 commits") || len(got.Commits) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestExactSHADoesNotFallBackToADifferentCommit(t *testing.T) {
	dir := gitRepo(t)
	real := commitAt(t, dir, 1_700_000_000, "real subject")
	missing := strings.Repeat("ab", 20)
	windowFar := sess("s1", dir, 10, 20, "1")
	windowFar.Repository.CommitSHAs = []string{real}
	exact := &summary.Report{Sessions: []summary.SessionSummary{windowFar}}
	Attach(exact, Options{})
	if exact.Sessions[0].Outcome.Match != "exact" || exact.Sessions[0].Outcome.Commits[0].SHA != real || exact.Sessions[0].Outcome.Commits[0].Subject != "real subject" {
		t.Fatalf("%+v", exact.Sessions[0].Outcome)
	}
	bad := sess("s2", dir, 1_700_000_000, 1_700_000_005, "1")
	bad.Repository.CommitSHAs = []string{missing}
	miss := &summary.Report{Sessions: []summary.SessionSummary{bad}}
	Attach(miss, Options{})
	got := miss.Sessions[0].Outcome
	if got.Status != "unlinked" || !strings.Contains(got.Reason, "is not in") || strings.Contains(got.Reason, "real subject") {
		t.Fatalf("%+v", got)
	}
}

func TestSharedCommitUnlinksBothSessions(t *testing.T) {
	dir := gitRepo(t)
	commitAt(t, dir, 1_700_000_000, "shared")
	report := &summary.Report{Sessions: []summary.SessionSummary{
		sess("left", dir, 1_700_000_000, 1_700_000_005, "1"),
		sess("right", dir, 1_700_000_000, 1_700_000_005, "1"),
	}}
	Attach(report, Options{})
	for i, id := range []string{"left", "right"} {
		got := report.Sessions[i].Outcome
		other := "right"
		if id == "right" {
			other = "left"
		}
		if got.Status != "unlinked" || !strings.Contains(got.Reason, other) {
			t.Fatalf("%s %+v", id, got)
		}
	}
}

func TestExplicitRepoLinksWhenTelemetryHasNoDirectory(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "from flag")
	report := &summary.Report{Sessions: []summary.SessionSummary{
		sess("s1", "", 1_700_000_000, 1_700_000_005, "1"),
	}}
	Attach(report, Options{Repo: dir})
	got := report.Sessions[0].Outcome
	if got.Status != "linked" || got.Match != "heuristic" || got.Commits[0].SHA != sha {
		t.Fatalf("%+v", got)
	}
}

func TestNoDirectoryStaysUnlinked(t *testing.T) {
	report := &summary.Report{Sessions: []summary.SessionSummary{sess("s", "", 1, 2, "1")}}
	Attach(report, Options{})
	if report.Sessions[0].Outcome.Status != "unlinked" || !strings.Contains(report.Sessions[0].Outcome.Reason, "no working directory") {
		t.Fatal(report.Sessions[0].Outcome.Reason)
	}
	if report.CostPerAccepted.Status != "unknown" || report.CostPerAccepted.USD != "" {
		t.Fatalf("%+v", report.CostPerAccepted)
	}
}

func TestMergedOutcomeSetsCostPerAccepted(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "landed")
	gitCmd(t, dir, "remote", "add", "origin", "https://github.com/example/demo.git")
	merged := sess("ok", dir, 1_700_000_000, 1_700_000_005, "2")
	open := sess("open", dir, 1_700_000_000, 1_700_000_005, "9")
	// Two commits would be ambiguous; give each session the same single commit
	// by using separate repos. The open session is a copy of the checkout.
	report := &summary.Report{Sessions: []summary.SessionSummary{merged}}
	lookup := &scriptedLookup{pulls: map[string][]summary.PullRequest{
		sha: {{Number: 12, URL: "https://github.com/example/demo/pull/12", State: "closed", Merged: true}},
	}, checks: "success"}
	Attach(report, Options{Lookup: lookup})
	got := report.Sessions[0].Outcome
	if got.Accepted == nil || !*got.Accepted || got.PullRequest == nil || got.PullRequest.Number != 12 || got.CI != "success" {
		t.Fatalf("%+v", got)
	}
	if lookup.calls == 0 {
		t.Fatal("lookup was not used")
	}
	if report.CostPerAccepted.Status != "known" || report.CostPerAccepted.USD != "2" || report.CostPerAccepted.AcceptedOutcomes != 1 {
		t.Fatalf("%+v", report.CostPerAccepted)
	}
	_ = open
}

func TestUnmergedAndMissingCostStayUnknown(t *testing.T) {
	no := false
	yes := true
	sessions := []summary.SessionSummary{
		{
			SessionID:    "open",
			ComputedCost: &pricing.Cost{Status: "known", USD: "4"},
			Outcome:      &summary.SessionOutcome{Status: "linked", Accepted: &no, Commits: []summary.LinkedCommit{{SHA: "abc"}}},
		},
	}
	got := CostPerAccepted(sessions)
	if got.Status != "unknown" || got.Reason != "no merged outcome" || got.USD != "" {
		t.Fatalf("%+v", got)
	}
	sessions = append(sessions, summary.SessionSummary{
		SessionID:    "landed",
		ComputedCost: &pricing.Cost{Status: "unknown", Reason: "no price"},
		Outcome:      &summary.SessionOutcome{Status: "linked", Accepted: &yes, Commits: []summary.LinkedCommit{{SHA: strings.Repeat("a", 40)}}},
	})
	got = CostPerAccepted(sessions)
	if got.Status != "unknown" || !strings.Contains(got.Reason, "unknown computed cost") || got.USD != "" {
		t.Fatalf("%+v", got)
	}
}

func TestHookLinkIgnoresOtherCommitsInTheWindow(t *testing.T) {
	dir := gitRepo(t)
	hooked := commitAt(t, dir, 1_700_000_000, "from the agent")
	commitAt(t, dir, 1_700_000_010, "someone else")
	session := sess("ses_hook", dir, 1_700_000_000, 1_700_000_020, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: hooked, Repo: dir}}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{Slack: -1})
	got := report.Sessions[0].Outcome
	if got.Status != "linked" || got.Match != "hook" || len(got.Commits) != 1 || got.Commits[0].SHA != hooked || got.Commits[0].Subject != "from the agent" {
		t.Fatalf("%+v", got)
	}
}

func TestHookDoesNotStealACommitForAnotherSession(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "hooked")
	owner := sess("ses_hook", dir, 1_700_000_000, 1_700_000_005, "1")
	owner.Repository.HookCommits = []summary.HookCommit{{SHA: sha, Repo: dir}}
	other := sess("ses_other", dir, 1_700_000_000, 1_700_000_005, "1")
	report := &summary.Report{Sessions: []summary.SessionSummary{owner, other}}
	Attach(report, Options{})
	if report.Sessions[0].Outcome.Match != "hook" || report.Sessions[0].Outcome.Commits[0].SHA != sha {
		t.Fatalf("owner %+v", report.Sessions[0].Outcome)
	}
	got := report.Sessions[1].Outcome
	if got.Status != "unlinked" || !strings.Contains(got.Reason, "ses_hook") {
		t.Fatalf("other %+v", got)
	}
}

func TestRewrittenHookKeepsTheReplacement(t *testing.T) {
	dir := gitRepo(t)
	old := commitAt(t, dir, 1_700_000_000, "old subject")
	next := commitAt(t, dir, 1_700_000_010, "new subject")
	session := sess("ses_hook", "", 1, 2, "1")
	session.Repository.HookCommits = []summary.HookCommit{
		{SHA: old, Repo: dir},
		{SHA: next, Repo: dir, RewrittenFrom: old},
	}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{})
	got := report.Sessions[0].Outcome
	if got.Status != "linked" || got.Match != "hook" || len(got.Commits) != 1 || got.Commits[0].SHA != next || got.Commits[0].Subject != "new subject" {
		t.Fatalf("%+v", got)
	}
}

func TestMissingExportedSHADoesNotUseTheHook(t *testing.T) {
	dir := gitRepo(t)
	real := commitAt(t, dir, 1_700_000_000, "real")
	session := sess("ses_hook", dir, 1_700_000_000, 1_700_000_005, "1")
	session.Repository.CommitSHAs = []string{strings.Repeat("ab", 20)}
	session.Repository.HookCommits = []summary.HookCommit{{SHA: real, Repo: dir}}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{})
	got := report.Sessions[0].Outcome
	if got.Status != "unlinked" || !strings.Contains(got.Reason, "is not in") || len(got.Commits) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestTrailerLinksAndIsNotAHeuristicForSomeoneElse(t *testing.T) {
	dir := gitRepo(t)
	named := commitMessage(t, dir, 1_700_000_000, "landed\n\nSDF-Session: ses_named\n")
	otherSHA := commitAt(t, dir, 1_700_000_010, "untagged")
	namedSession := sess("ses_named", dir, 1_700_000_000, 1_700_000_020, "1")
	other := sess("ses_other", dir, 1_700_000_000, 1_700_000_020, "1")
	report := &summary.Report{Sessions: []summary.SessionSummary{namedSession, other}}
	Attach(report, Options{Slack: -1})
	got := report.Sessions[0].Outcome
	if got.Status != "linked" || got.Match != "trailer" || len(got.Commits) != 1 || got.Commits[0].SHA != named {
		t.Fatalf("named %+v", got)
	}
	otherGot := report.Sessions[1].Outcome
	if otherGot.Status != "linked" || otherGot.Match != "heuristic" || otherGot.Commits[0].SHA != otherSHA {
		t.Fatalf("other %+v", otherGot)
	}
}

func TestTrailerOnlyCommitIsNotHeuristicForAnotherSession(t *testing.T) {
	dir := gitRepo(t)
	commitMessage(t, dir, 1_700_000_000, "only\n\nSDF-Session: ses_named\n")
	other := sess("ses_other", dir, 1_700_000_000, 1_700_000_005, "1")
	report := &summary.Report{Sessions: []summary.SessionSummary{other}}
	Attach(report, Options{})
	got := report.Sessions[0].Outcome
	if got.Status != "unlinked" || !strings.Contains(got.Reason, "SDF-Session") {
		t.Fatalf("%+v", got)
	}
}

func TestMissingHookSHACanUseTheTrailer(t *testing.T) {
	dir := gitRepo(t)
	sha := commitMessage(t, dir, 1_700_000_000, "squashed locally\n\nSDF-Session: ses_named\n")
	session := sess("ses_named", "", 1, 2, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: strings.Repeat("ab", 20), Repo: dir}}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{})
	got := report.Sessions[0].Outcome
	if got.Status != "linked" || got.Match != "trailer" || len(got.Commits) != 1 || got.Commits[0].SHA != sha {
		t.Fatalf("%+v", got)
	}
}

func TestSquashCoAuthorLineStillLinksWhenHookSHAIsGone(t *testing.T) {
	dir := gitRepo(t)
	message := "squashed (#2)\n\n* feature\n\nSDF-Session: ses_named\n\nCo-authored-by: Example User <user@example.com>\n"
	sha := commitMessage(t, dir, 1_700_000_000, message)
	session := sess("ses_named", "", 1, 2, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: strings.Repeat("ab", 20), Repo: dir, Branch: "feature"}}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{})
	got := report.Sessions[0].Outcome
	if got.Status != "linked" || got.Match != "trailer" || len(got.Commits) != 1 || got.Commits[0].SHA != sha {
		t.Fatalf("%+v", got)
	}
}

func TestSubjectMentionIsNotASessionLine(t *testing.T) {
	dir := gitRepo(t)
	sha := commitMessage(t, dir, 1_700_000_000, "see SDF-Session: ses_named\n")
	session := sess("ses_named", dir, 1_700_000_000, 1_700_000_005, "1")
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{})
	got := report.Sessions[0].Outcome
	if got.Status != "linked" || got.Match != "heuristic" || got.Commits[0].SHA != sha {
		t.Fatalf("%+v", got)
	}
}

func TestExplicitRepoOverridesAnExistingHookCheckout(t *testing.T) {
	hookDir := gitRepo(t)
	sha := commitAt(t, hookDir, 1_700_000_000, "from the agent")
	parent := t.TempDir()
	other := filepath.Join(parent, "clone")
	gitCmd(t, hookDir, "clone", hookDir, other)
	session := sess("ses_hook", "", 1, 2, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: sha, Repo: hookDir, Branch: "main"}}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{Repo: other})
	got := report.Sessions[0].Outcome
	if got.Status != "linked" || got.Match != "hook" || len(got.Commits) != 1 || got.Commits[0].SHA != sha {
		t.Fatalf("%+v", got)
	}
	top, _ := filepath.EvalSymlinks(got.Repo)
	want, _ := filepath.EvalSymlinks(other)
	if top != want {
		t.Fatalf("repo %s want %s", got.Repo, want)
	}
}

func TestExplicitRepoDoesNotUseTheHookCheckout(t *testing.T) {
	hookDir := gitRepo(t)
	original := commitMessage(t, hookDir, 1_700_000_000, "feature\n\nSDF-Session: ses_named\n")
	fresh := gitRepo(t)
	message := "squashed (#2)\n\n* feature\n\nSDF-Session: ses_named\n\nCo-authored-by: Example User <user@example.com>\n"
	squash := commitMessage(t, fresh, 1_700_000_100, message)
	session := sess("ses_named", "", 1, 2, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: original, Repo: hookDir, Branch: "feature"}}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{Repo: fresh})
	got := report.Sessions[0].Outcome
	if got.Status != "linked" || got.Match != "trailer" || len(got.Commits) != 1 || got.Commits[0].SHA != squash {
		t.Fatalf("%+v", got)
	}
	top, _ := filepath.EvalSymlinks(got.Repo)
	want, _ := filepath.EvalSymlinks(fresh)
	if top != want {
		t.Fatalf("repo %s want %s", got.Repo, want)
	}
}

func TestClosedUnmergedPullRequestIsReported(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "not merged")
	gitCmd(t, dir, "remote", "add", "origin", "https://github.com/example/demo.git")
	session := sess("ses_closed", dir, 1_700_000_000, 1_700_000_005, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: sha, Repo: dir, Branch: "scenario-e"}}
	script := &githubScript{
		commitPulls: "[]",
		branchPulls: `[{"number":5,"html_url":"https://github.com/example/demo/pull/5","state":"closed","merged_at":null,"head":{"sha":"` + sha + `"}}]`,
		status:      `{"state":"pending","statuses":[],"total_count":0}`,
		runs:        `{"check_runs":[]}`,
		wantHead:    "example:scenario-e",
	}
	lookup := script.server(t)
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{Lookup: lookup})
	got := report.Sessions[0].Outcome
	if !script.sawCommitPulls || !script.sawBranch {
		t.Fatalf("commit pulls %v branch pulls %v", script.sawCommitPulls, script.sawBranch)
	}
	if got.Accepted == nil || *got.Accepted || got.PullRequest == nil || got.PullRequest.Number != 5 || got.PullRequest.Merged {
		t.Fatalf("%+v", got)
	}
	if got.AcceptedReason != "pull request was closed without merging" || got.CI != "none" {
		t.Fatalf("%+v", got)
	}
	text := summary.Format(*report)
	if !strings.Contains(text, "pr #5 closed without merging") || !strings.Contains(text, "pull request was closed without merging") || !strings.Contains(text, "ci=none") {
		t.Fatal(text)
	}
}

func TestOpenPullRequestStaysNotMerged(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "still open")
	gitCmd(t, dir, "remote", "add", "origin", "https://github.com/example/demo.git")
	session := sess("ses_open", dir, 1_700_000_000, 1_700_000_005, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: sha, Repo: dir, Branch: "scenario-d"}}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	lookup := &scriptedLookup{pulls: map[string][]summary.PullRequest{
		sha: {{Number: 4, URL: "https://github.com/example/demo/pull/4", State: "open"}},
	}, checks: "pending"}
	Attach(report, Options{Lookup: lookup})
	got := report.Sessions[0].Outcome
	if got.Accepted == nil || *got.Accepted || got.PullRequest == nil || got.PullRequest.Number != 4 {
		t.Fatalf("%+v", got)
	}
	if got.AcceptedReason != "pull request is not merged" || got.CI != "pending" {
		t.Fatalf("%+v", got)
	}
}

func TestNoPullRequestStaysUnaccepted(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "no pr")
	gitCmd(t, dir, "remote", "add", "origin", "https://github.com/example/demo.git")
	session := sess("ses_none", dir, 1_700_000_000, 1_700_000_005, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: sha, Repo: dir, Branch: "scenario-f"}}
	script := &githubScript{
		commitPulls: "[]",
		branchPulls: "[]",
		status:      `{"state":"pending","statuses":[],"total_count":0}`,
		runs:        `{"check_runs":[]}`,
		wantHead:    "example:scenario-f",
	}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{Lookup: script.server(t)})
	got := report.Sessions[0].Outcome
	if !script.sawBranch || got.Accepted == nil || *got.Accepted || got.PullRequest != nil {
		t.Fatalf("%+v branch %v", got, script.sawBranch)
	}
	if got.AcceptedReason != "no pull request contains the linked commit" || got.CI != "none" {
		t.Fatalf("%+v", got)
	}
}

func TestBranchFallbackIgnoresADifferentHead(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "moved")
	gitCmd(t, dir, "remote", "add", "origin", "https://github.com/example/demo.git")
	session := sess("ses_moved", dir, 1_700_000_000, 1_700_000_005, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: sha, Repo: dir, Branch: "scenario-e"}}
	script := &githubScript{
		commitPulls: "[]",
		branchPulls: `[{"number":9,"html_url":"https://github.com/example/demo/pull/9","state":"closed","merged_at":null,"head":{"sha":"` + strings.Repeat("cd", 20) + `"}}]`,
		status:      `{"state":"success","total_count":1,"statuses":[{}]}`,
		runs:        `{"check_runs":[]}`,
		wantHead:    "example:scenario-e",
	}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{Lookup: script.server(t)})
	got := report.Sessions[0].Outcome
	if got.Accepted == nil || *got.Accepted || got.PullRequest != nil || got.AcceptedReason != "no pull request contains the linked commit" {
		t.Fatalf("%+v", got)
	}
}

func TestSeveralBranchPullsStayUnknown(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "two prs")
	gitCmd(t, dir, "remote", "add", "origin", "https://github.com/example/demo.git")
	session := sess("ses_two", dir, 1_700_000_000, 1_700_000_005, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: sha, Repo: dir, Branch: "scenario-e"}}
	row := `"state":"closed","merged_at":null,"head":{"sha":"` + sha + `"}`
	script := &githubScript{
		commitPulls: "[]",
		branchPulls: `[{"number":5,"html_url":"https://github.com/example/demo/pull/5",` + row + `},{"number":6,"html_url":"https://github.com/example/demo/pull/6",` + row + `}]`,
		status:      `{"state":"pending","total_count":0,"statuses":[]}`,
		runs:        `{"check_runs":[]}`,
		wantHead:    "example:scenario-e",
	}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{Lookup: script.server(t)})
	got := report.Sessions[0].Outcome
	if got.Accepted != nil || got.PullRequest != nil || !strings.Contains(got.AcceptedReason, "more than one pull request") {
		t.Fatalf("%+v", got)
	}
}

func TestBranchQueryFailureStaysUnknown(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "query failed")
	gitCmd(t, dir, "remote", "add", "origin", "https://github.com/example/demo.git")
	session := sess("ses_err", dir, 1_700_000_000, 1_700_000_005, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: sha, Repo: dir, Branch: "scenario-e"}}
	script := &githubScript{
		commitPulls:  "[]",
		status:       `{"state":"pending","total_count":0,"statuses":[]}`,
		runs:         `{"check_runs":[]}`,
		wantHead:     "example:scenario-e",
		branchStatus: http.StatusInternalServerError,
	}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{Lookup: script.server(t)})
	got := report.Sessions[0].Outcome
	if got.Accepted != nil || got.PullRequest != nil || !strings.Contains(got.AcceptedReason, "github:") {
		t.Fatalf("%+v", got)
	}
}

func TestCommitPullsSkipTheBranchQuery(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "merged")
	gitCmd(t, dir, "remote", "add", "origin", "https://github.com/example/demo.git")
	session := sess("ses_merged", dir, 1_700_000_000, 1_700_000_005, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: sha, Repo: dir, Branch: "scenario-a"}}
	script := &githubScript{
		commitPulls: `[{"number":1,"html_url":"https://github.com/example/demo/pull/1","state":"closed","merged_at":"2026-11-11T00:00:00Z","head":{"sha":"` + sha + `"}}]`,
		status:      `{"state":"success","total_count":1,"statuses":[{"state":"success"}]}`,
		runs:        `{"check_runs":[]}`,
		wantHead:    "example:scenario-a",
	}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{Lookup: script.server(t)})
	got := report.Sessions[0].Outcome
	if script.sawBranch {
		t.Fatal("branch list was queried after the commit list returned a pull request")
	}
	if got.Accepted == nil || !*got.Accepted || got.PullRequest == nil || got.PullRequest.Number != 1 || got.CI != "success" {
		t.Fatalf("%+v", got)
	}
}

func TestNoBranchDoesNotListPullsByHead(t *testing.T) {
	dir := gitRepo(t)
	sha := commitAt(t, dir, 1_700_000_000, "detached")
	gitCmd(t, dir, "remote", "add", "origin", "https://github.com/example/demo.git")
	session := sess("ses_detached", dir, 1_700_000_000, 1_700_000_005, "1")
	session.Repository.HookCommits = []summary.HookCommit{{SHA: sha, Repo: dir}}
	script := &githubScript{
		commitPulls: "[]",
		status:      `{"state":"pending","total_count":0,"statuses":[]}`,
		runs:        `{"check_runs":[]}`,
	}
	report := &summary.Report{Sessions: []summary.SessionSummary{session}}
	Attach(report, Options{Lookup: script.server(t)})
	got := report.Sessions[0].Outcome
	if script.sawBranch || got.PullRequest != nil || got.AcceptedReason != "no pull request contains the linked commit" {
		t.Fatalf("%+v branch %v", got, script.sawBranch)
	}
}

func TestDecodeChecks(t *testing.T) {
	cases := []struct {
		name   string
		status string
		runs   string
		want   string
	}{
		{"no statuses", `{"state":"pending","statuses":[],"total_count":0}`, `{"check_runs":[]}`, "none"},
		{"pending status", `{"state":"pending","total_count":1,"statuses":[{"state":"pending"}]}`, `{"check_runs":[]}`, "pending"},
		{"success check and empty status", `{"state":"pending","total_count":0,"statuses":[]}`, `{"check_runs":[{"status":"completed","conclusion":"success"}]}`, "success"},
		{"pending check", `{"state":"pending","total_count":0}`, `{"check_runs":[{"status":"in_progress"}]}`, "pending"},
		{"failure wins", `{"state":"success","total_count":1,"statuses":[{}]}`, `{"check_runs":[{"status":"completed","conclusion":"failure"}]}`, "failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeChecks([]byte(tc.status), []byte(tc.runs))
			if err != nil || got != tc.want {
				t.Fatalf("got %q err %v", got, err)
			}
		})
	}
}

func TestGitHubPathAllowsTheBranchFallbackOnly(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	if err := validateGitHubPath("repos/example/demo/commits/" + sha + "/pulls"); err != nil {
		t.Fatal(err)
	}
	if err := validateGitHubPath("repos/example/demo/pulls?head=example%3Ascenario-e&per_page=100&state=all"); err != nil {
		t.Fatal(err)
	}
	if err := validateGitHubPath("repos/example/demo/pulls?head=example%3Afeature%2Fscenario-e&per_page=100&state=all"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"repos/example/demo/pulls?state=open&head=example:scenario-e&per_page=100",
		"repos/example/demo/pulls?state=all&head=other:scenario-e&per_page=100",
		"repos/example/demo/pulls?state=all&head=example:HEAD&per_page=100",
		"search/issues?q=repo:example/demo",
	} {
		if err := validateGitHubPath(path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}

func TestMissingWorkDirStaysUnlinked(t *testing.T) {
	// The Pi sample exports pi.cwd=/tmp/sample. A directory that is not on
	// this machine is left unlinked rather than matched to some other repo.
	start := int64(1)
	end := int64(2)
	report := &summary.Report{Sessions: []summary.SessionSummary{{
		SessionID: "pi",
		Repository: summary.RepositoryFacts{
			WorkDirs:      []string{"/no/such/sdf-session"},
			CommitSHAs:    []string{},
			StartUnixNano: &start,
			EndUnixNano:   &end,
		},
	}}}
	Attach(report, Options{})
	got := report.Sessions[0].Outcome
	if got.Status != "unlinked" || !strings.Contains(got.Reason, "/no/such/sdf-session") {
		t.Fatalf("%+v", got)
	}
}

type scriptedLookup struct {
	pulls  map[string][]summary.PullRequest
	checks string
	calls  int
}

func (s *scriptedLookup) Pulls(_ context.Context, _, _, sha, _ string) ([]summary.PullRequest, error) {
	s.calls++
	return s.pulls[sha], nil
}

func (s *scriptedLookup) Checks(context.Context, string, string, string) (string, error) {
	s.calls++
	return s.checks, nil
}

type githubScript struct {
	commitPulls    string
	branchPulls    string
	status         string
	runs           string
	wantHead       string
	branchStatus   int
	sawCommitPulls bool
	sawBranch      bool
}

func (s *githubScript) server(t *testing.T) *httpGitHub {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" || strings.Contains(r.URL.String(), "test-token") {
			t.Errorf("authorization %q url %s", r.Header.Get("Authorization"), r.URL.String())
		}
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/status"):
			writeJSON(w, s.status)
		case strings.HasSuffix(path, "/check-runs"):
			writeJSON(w, s.runs)
		case strings.Contains(path, "/commits/") && strings.HasSuffix(path, "/pulls"):
			s.sawCommitPulls = true
			writeJSON(w, s.commitPulls)
		case strings.HasSuffix(path, "/pulls"):
			s.sawBranch = true
			if r.URL.Query().Get("state") != "all" || r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("head") != s.wantHead {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			if s.branchStatus != 0 {
				w.WriteHeader(s.branchStatus)
				return
			}
			writeJSON(w, s.branchPulls)
		default:
			t.Errorf("unexpected %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &httpGitHub{token: "test-token", client: srv.Client(), base: srv.URL}
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func sess(id, dir string, start, end int64, usd string) summary.SessionSummary {
	startN := start * 1_000_000_000
	endN := end * 1_000_000_000
	item := summary.SessionSummary{
		SessionID: id,
		Repository: summary.RepositoryFacts{
			WorkDirs:      []string{},
			CommitSHAs:    []string{},
			StartUnixNano: &startN,
			EndUnixNano:   &endN,
		},
		ComputedCost: &pricing.Cost{Status: "known", USD: usd},
	}
	if dir != "" {
		item.Repository.WorkDirs = []string{dir}
	}
	return item
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.email", "sdf-test@example.com")
	gitCmd(t, dir, "config", "user.name", "sdf test")
	gitCmd(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func commitAt(t *testing.T, dir string, unix int64, subject string) string {
	t.Helper()
	return commitMessage(t, dir, unix, subject)
}

func commitMessage(t *testing.T, dir string, unix int64, message string) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "msg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(message); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-F", file.Name())
	stamp := strconv.FormatInt(unix, 10) + " +0000"
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v %s", args, err, out)
	}
}

func TestParseGitHubRemoteDropsCredentials(t *testing.T) {
	owner, repo, ok := parseGitHubRemote("https://user:abc@github.com/octo/demo.git")
	if !ok || owner != "octo" || repo != "demo" || strings.Contains(owner+repo, "abc") {
		t.Fatalf("%s %s %v", owner, repo, ok)
	}
	owner, repo, ok = parseGitHubRemote("git@github.com:octo/demo.git")
	if !ok || owner != "octo" || repo != "demo" {
		t.Fatalf("ssh %s %s", owner, repo)
	}
	if _, _, ok := parseGitHubRemote("https://gitlab.com/octo/demo.git"); ok {
		t.Fatal("gitlab matched")
	}
	if combineCI([]string{"success", "pending", "none"}) != "pending" {
		t.Fatal(combineCI([]string{"success", "pending", "none"}))
	}
	if combineCI([]string{"success", "failure"}) != "failure" {
		t.Fatal("failure")
	}
}

func TestCostReasonsUseSingularForOneSession(t *testing.T) {
	yes := true
	no := false
	unknown := summary.SessionSummary{SessionID: "only", Outcome: &summary.SessionOutcome{Status: "unlinked"}}
	got := CostPerAccepted([]summary.SessionSummary{unknown})
	if got.Reason != "1 session has no known outcome" {
		t.Fatal(got.Reason)
	}
	got = CostPerAccepted([]summary.SessionSummary{unknown, {SessionID: "other", Outcome: &summary.SessionOutcome{Status: "unlinked"}}})
	if got.Reason != "2 sessions have no known outcome" {
		t.Fatal(got.Reason)
	}
	landed := summary.SessionSummary{
		SessionID:    "landed",
		ComputedCost: &pricing.Cost{Status: "unknown", Reason: "no price"},
		Outcome:      &summary.SessionOutcome{Status: "linked", Accepted: &yes, Commits: []summary.LinkedCommit{{SHA: "abc"}}},
	}
	got = CostPerAccepted([]summary.SessionSummary{landed})
	if got.Reason != "1 accepted session has unknown computed cost" {
		t.Fatal(got.Reason)
	}
	got = CostPerAccepted([]summary.SessionSummary{landed, {
		SessionID:    "landed-2",
		ComputedCost: &pricing.Cost{Status: "unknown"},
		Outcome:      &summary.SessionOutcome{Status: "linked", Accepted: &yes, Commits: []summary.LinkedCommit{{SHA: "def"}}},
	}})
	if got.Reason != "2 accepted sessions have unknown computed cost" {
		t.Fatal(got.Reason)
	}
	known := summary.SessionSummary{
		SessionID:    "ok",
		ComputedCost: &pricing.Cost{Status: "known", USD: "2"},
		Outcome:      &summary.SessionOutcome{Status: "linked", Accepted: &yes, Commits: []summary.LinkedCommit{{SHA: strings.Repeat("b", 40)}}},
	}
	idle := summary.SessionSummary{
		SessionID:    "idle",
		ComputedCost: &pricing.Cost{Status: "unknown"},
		Outcome:      &summary.SessionOutcome{Status: "unlinked", Accepted: &no},
	}
	got = CostPerAccepted([]summary.SessionSummary{known, idle})
	if got.Status != "known" || got.Note != "1 session has unknown computed cost and is not accepted" {
		t.Fatalf("%+v", got)
	}
	got = CostPerAccepted([]summary.SessionSummary{known, idle, {
		SessionID:    "idle-2",
		ComputedCost: &pricing.Cost{Status: "unknown"},
		Outcome:      &summary.SessionOutcome{Status: "unlinked", Accepted: &no},
	}})
	if got.Status != "known" || !strings.Contains(got.Note, "2 sessions have unknown computed cost and are not accepted") {
		t.Fatalf("%+v", got)
	}
}
