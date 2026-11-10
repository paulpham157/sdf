package outcome

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/summary"
)

type gitCommit struct {
	SHA           string
	Subject       string
	CommitterUnix int64
}

func linkSession(session summary.SessionSummary, opt Options, slack time.Duration) *summary.SessionOutcome {
	facts := session.Repository
	if facts.AmbiguousWorkDirs {
		return unlinked("session reports more than one workspace path")
	}
	if facts.AmbiguousRepository {
		return unlinked("session reports more than one repository identity")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return unlinked("git is not installed")
	}
	toplevel, reason := resolveRepo(facts.WorkDirs, opt.Repo)
	if reason != "" && len(facts.CommitSHAs) == 0 {
		return unlinked(reason)
	}
	if len(facts.CommitSHAs) > 0 {
		return linkExact(facts, toplevel, reason)
	}
	if reason != "" {
		return unlinked(reason)
	}
	if facts.StartUnixNano == nil || facts.EndUnixNano == nil {
		return unlinked("session has no timestamps")
	}
	lo := *facts.StartUnixNano/1_000_000_000 - int64(slack/time.Second)
	hi := *facts.EndUnixNano/1_000_000_000 + int64(slack/time.Second)
	commits, err := commitsIn(toplevel, lo, hi)
	if err != nil {
		return unlinked(err.Error())
	}
	switch len(commits) {
	case 0:
		return unlinked("no commit with committer time inside the session window")
	case 1:
		return &summary.SessionOutcome{
			Status: "linked",
			Match:  "heuristic",
			Repo:   toplevel,
			Reason: "one commit whose committer time falls in the session window",
			Commits: []summary.LinkedCommit{{
				SHA: commits[0].SHA, Subject: commits[0].Subject, CommitterUnix: commits[0].CommitterUnix,
			}},
		}
	default:
		ids := make([]string, len(commits))
		for i, commit := range commits {
			ids[i] = commit.SHA[:12]
		}
		return unlinked(fmt.Sprintf("%d commits in the session window; not choosing one (%s)", len(commits), strings.Join(ids, ", ")))
	}
}

func linkExact(facts summary.RepositoryFacts, toplevel, repoReason string) *summary.SessionOutcome {
	if toplevel == "" {
		if facts.Owner == "" || facts.RepositoryName == "" {
			if repoReason == "" {
				repoReason = "commit SHA was exported but no repository was, so it was not checked"
			}
			return unlinked(repoReason)
		}
		commits := make([]summary.LinkedCommit, len(facts.CommitSHAs))
		for i, sha := range facts.CommitSHAs {
			commits[i] = summary.LinkedCommit{SHA: sha}
		}
		return &summary.SessionOutcome{
			Status:  "linked",
			Match:   "exact",
			Reason:  "commit SHA was exported by the agent and was not verified in a local checkout",
			Commits: commits,
		}
	}
	var commits []summary.LinkedCommit
	for _, sha := range facts.CommitSHAs {
		kind, err := git(toplevel, "cat-file", "-t", sha)
		if err != nil || strings.TrimSpace(kind) != "commit" {
			return unlinked("exported commit " + sha + " is not in " + toplevel)
		}
		subject, _ := git(toplevel, "log", "-1", "--format=%s", sha)
		when, _ := git(toplevel, "log", "-1", "--format=%ct", sha)
		unix, _ := strconv.ParseInt(strings.TrimSpace(when), 10, 64)
		commits = append(commits, summary.LinkedCommit{
			SHA: sha, Subject: oneLine(subject, 120), CommitterUnix: unix,
		})
	}
	return &summary.SessionOutcome{
		Status:  "linked",
		Match:   "exact",
		Repo:    toplevel,
		Reason:  "commit SHA was exported by the agent and exists in the checkout",
		Commits: commits,
	}
}

func unlinked(reason string) *summary.SessionOutcome {
	return &summary.SessionOutcome{Status: "unlinked", Reason: reason, AcceptedReason: reason}
}

func resolveRepo(workDirs []string, explicit string) (string, string) {
	var tops []string
	for _, dir := range workDirs {
		if !filepath.IsAbs(dir) {
			return "", "working directory is not absolute: " + dir
		}
		if strings.HasPrefix(dir, "-") {
			return "", "working directory is not a path"
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return "", "working directory is not on this machine: " + dir
		}
		top, err := git(dir, "rev-parse", "--show-toplevel")
		if err != nil {
			return "", "working directory is not a git repository: " + dir
		}
		tops = append(tops, filepath.Clean(strings.TrimSpace(top)))
	}
	tops = unique(tops)
	var explicitTop string
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", "repo path: " + err.Error()
		}
		top, err := git(abs, "rev-parse", "--show-toplevel")
		if err != nil {
			return "", "repo path is not a git repository: " + explicit
		}
		explicitTop = filepath.Clean(strings.TrimSpace(top))
	}
	switch {
	case len(tops) == 0 && explicitTop == "":
		return "", "no working directory attribute and no --repo"
	case len(tops) == 0:
		return explicitTop, ""
	case len(tops) > 1:
		return "", "working directories resolve to more than one git repository"
	case explicitTop != "" && explicitTop != tops[0]:
		return "", "--repo and the working directory are different repositories"
	default:
		return tops[0], ""
	}
}

func commitsIn(toplevel string, lo, hi int64) ([]gitCommit, error) {
	args := []string{"log", "--all", "--pretty=format:%H%x1f%ct%x1f%s", fmt.Sprintf("--since=@%d", lo), fmt.Sprintf("--until=@%d", hi+1)}
	if _, err := git(toplevel, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		args = append([]string{"log", "HEAD"}, args[1:]...)
	}
	text, err := git(toplevel, args...)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits") {
			return nil, nil
		}
		return nil, err
	}
	var out []gitCommit
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) < 2 {
			continue
		}
		when, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || when < lo || when > hi {
			continue
		}
		subject := ""
		if len(parts) == 3 {
			subject = oneLine(parts[2], 120)
		}
		out = append(out, gitCommit{SHA: strings.ToLower(parts[0]), Subject: subject, CommitterUnix: when})
	}
	return out, nil
}

func git(dir string, args ...string) (string, error) {
	if strings.HasPrefix(dir, "-") {
		return "", fmt.Errorf("refusing git directory %q", dir)
	}
	full := append([]string{"-c", "safe.directory=*", "-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("git: %s", msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func oneLine(text string, limit int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if len(text) <= limit {
		return text
	}
	return text[:limit]
}

func unique(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
