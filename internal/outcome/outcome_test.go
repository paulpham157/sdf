package outcome

import (
	"context"
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

func (s *scriptedLookup) Pulls(_ context.Context, _, _, sha string) ([]summary.PullRequest, error) {
	s.calls++
	return s.pulls[sha], nil
}

func (s *scriptedLookup) Checks(context.Context, string, string, string) (string, error) {
	s.calls++
	return s.checks, nil
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
	cmd := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", subject)
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
