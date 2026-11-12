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

func linkSession(session summary.SessionSummary, opt Options, slack time.Duration, scan *trailerScan) *summary.SessionOutcome {
	facts := session.Repository
	if _, err := exec.LookPath("git"); err != nil {
		return unlinked("git is not installed")
	}
	if len(facts.CommitSHAs) > 0 {
		if facts.AmbiguousWorkDirs {
			return unlinked("session reports more than one workspace path")
		}
		if facts.AmbiguousRepository {
			return unlinked("session reports more than one repository identity")
		}
		toplevel, reason := resolveRepo(facts.WorkDirs, opt.Repo)
		return linkExact(facts, toplevel, reason)
	}
	if len(facts.HookCommits) > 0 {
		hooked := linkHook(facts, opt)
		if hooked.Status == "linked" {
			return hooked
		}
		if trailer := scan.link(session.SessionID, trailerRepos(facts, opt)); trailer != nil {
			return trailer
		}
		return hooked
	}
	if facts.AmbiguousWorkDirs {
		return unlinked("session reports more than one workspace path")
	}
	if facts.AmbiguousRepository {
		return unlinked("session reports more than one repository identity")
	}
	toplevel, reason := resolveRepo(facts.WorkDirs, opt.Repo)
	if reason != "" {
		return unlinked(reason)
	}
	if trailer := scan.link(session.SessionID, []string{toplevel}); trailer != nil {
		return trailer
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
	claimed := scan.claimed(toplevel)
	var open []gitCommit
	for _, commit := range commits {
		if !claimed[commit.SHA] {
			open = append(open, commit)
		}
	}
	if len(open) == 0 && len(commits) > 0 {
		return unlinked("commits in the session window name another session in an SDF-Session trailer")
	}
	switch len(open) {
	case 0:
		return unlinked("no commit with committer time inside the session window")
	case 1:
		return &summary.SessionOutcome{
			Status: "linked",
			Match:  "heuristic",
			Repo:   toplevel,
			Reason: "one commit whose committer time falls in the session window",
			Commits: []summary.LinkedCommit{{
				SHA: open[0].SHA, Subject: open[0].Subject, CommitterUnix: open[0].CommitterUnix,
			}},
		}
	default:
		ids := make([]string, len(open))
		for i, commit := range open {
			ids[i] = commit.SHA[:12]
		}
		return unlinked(fmt.Sprintf("%d commits in the session window; not choosing one (%s)", len(open), strings.Join(ids, ", ")))
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

func linkHook(facts summary.RepositoryFacts, opt Options) *summary.SessionOutcome {
	kept := keptHookSHAs(facts.HookCommits)
	if len(kept) == 0 {
		return unlinked("hook commit was rewritten and the replacement is missing")
	}
	workDirs := facts.WorkDirs
	if facts.AmbiguousWorkDirs {
		workDirs = nil
	}
	// An explicit --repo is the checkout, including when the path recorded
	// by the hook still exists on disk.
	var toplevel, reason string
	if opt.Repo != "" {
		toplevel, reason = resolveRepo(workDirs, opt.Repo)
	} else {
		tops, missing := hookTops(kept)
		if len(tops) > 1 {
			return unlinked("hook events name more than one repository")
		}
		switch len(tops) {
		case 1:
			toplevel, reason = resolveRepo(workDirs, tops[0])
		default:
			toplevel, reason = resolveRepo(workDirs, "")
			if reason != "" && missing {
				return unlinked("hook repository is not on this machine")
			}
		}
	}
	if reason != "" {
		return unlinked(reason)
	}
	var commits []summary.LinkedCommit
	for _, commit := range kept {
		kind, err := git(toplevel, "cat-file", "-t", commit.SHA)
		if err != nil || strings.TrimSpace(kind) != "commit" {
			continue
		}
		subject, _ := git(toplevel, "log", "-1", "--format=%s", commit.SHA)
		when, _ := git(toplevel, "log", "-1", "--format=%ct", commit.SHA)
		unix, _ := strconv.ParseInt(strings.TrimSpace(when), 10, 64)
		commits = append(commits, summary.LinkedCommit{
			SHA: commit.SHA, Subject: oneLine(subject, 120), CommitterUnix: unix,
		})
	}
	if len(commits) == 0 {
		return unlinked("hook commit is not in " + toplevel)
	}
	return &summary.SessionOutcome{
		Status:  "linked",
		Match:   "hook",
		Repo:    toplevel,
		Reason:  "sdf git hook reported this commit for the session",
		Commits: commits,
	}
}

func keptHookSHAs(commits []summary.HookCommit) []summary.HookCommit {
	present := map[string]bool{}
	for _, commit := range commits {
		if commit.SHA != "" {
			present[commit.SHA] = true
		}
	}
	rewritten := map[string]bool{}
	for _, commit := range commits {
		if commit.RewrittenFrom != "" && present[commit.SHA] {
			rewritten[commit.RewrittenFrom] = true
		}
	}
	var out []summary.HookCommit
	seen := map[string]bool{}
	for _, commit := range commits {
		if commit.SHA == "" || rewritten[commit.SHA] || seen[commit.SHA] {
			continue
		}
		seen[commit.SHA] = true
		out = append(out, commit)
	}
	return out
}

func hookTops(commits []summary.HookCommit) (tops []string, missing bool) {
	for _, commit := range commits {
		if commit.Repo == "" {
			continue
		}
		top, err := git(commit.Repo, "rev-parse", "--show-toplevel")
		if err != nil {
			missing = true
			continue
		}
		tops = append(tops, filepath.Clean(strings.TrimSpace(top)))
	}
	return unique(tops), missing
}

func trailerRepos(facts summary.RepositoryFacts, opt Options) []string {
	if opt.Repo != "" {
		return []string{opt.Repo}
	}
	var repos []string
	for _, commit := range facts.HookCommits {
		if commit.Repo != "" {
			repos = append(repos, commit.Repo)
		}
	}
	return repos
}

// trailerScan caches SDF-Session trailers per checkout. A commit that names a
// session is not offered to the time-window heuristic.
type trailerScan struct {
	byRepo map[string]map[string][]gitCommit
}

func (s *trailerScan) link(sessionID string, repos []string) *summary.SessionOutcome {
	if sessionID == "" {
		return nil
	}
	var (
		commits []summary.LinkedCommit
		topUsed string
		seenTop = map[string]bool{}
		seenSHA = map[string]bool{}
	)
	for _, repo := range repos {
		top, err := canonicalizeRepo(repo)
		if err != nil || seenTop[top] {
			continue
		}
		seenTop[top] = true
		idx, err := s.index(top)
		if err != nil {
			continue
		}
		for _, commit := range idx[sessionID] {
			if seenSHA[commit.SHA] {
				continue
			}
			if topUsed != "" && topUsed != top {
				return unlinked("SDF-Session trailer appears in more than one repository")
			}
			seenSHA[commit.SHA] = true
			topUsed = top
			commits = append(commits, summary.LinkedCommit{
				SHA: commit.SHA, Subject: commit.Subject, CommitterUnix: commit.CommitterUnix,
			})
		}
	}
	if len(commits) == 0 {
		return nil
	}
	return &summary.SessionOutcome{
		Status:  "linked",
		Match:   "trailer",
		Repo:    topUsed,
		Reason:  "commit message trailer SDF-Session names this session",
		Commits: commits,
	}
}

func (s *trailerScan) claimed(toplevel string) map[string]bool {
	idx, err := s.index(toplevel)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, list := range idx {
		for _, commit := range list {
			out[commit.SHA] = true
		}
	}
	return out
}

func (s *trailerScan) index(toplevel string) (map[string][]gitCommit, error) {
	if s.byRepo == nil {
		s.byRepo = map[string]map[string][]gitCommit{}
	}
	if idx, ok := s.byRepo[toplevel]; ok {
		return idx, nil
	}
	idx, err := readTrailers(toplevel)
	if err != nil {
		return nil, err
	}
	s.byRepo[toplevel] = idx
	return idx, nil
}

// readTrailers indexes commits that contain an SDF-Session line.
// Git's trailer parser keeps only the last trailer block, so a squash
// message that inserts a blank line before Co-authored-by hides
// SDF-Session from %(trailers). The line scan still sees it.
func readTrailers(toplevel string) (map[string][]gitCommit, error) {
	format := "%H%x1f%ct%x1f%s%x1e%B"
	text, err := git(toplevel, "log", "--all", "--grep=SDF-Session:", "-z", "--pretty=format:"+format)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits") {
			return map[string][]gitCommit{}, nil
		}
		return nil, err
	}
	idx := map[string][]gitCommit{}
	for _, rec := range strings.Split(text, "\x00") {
		rec = strings.TrimRight(rec, "\r\n")
		if rec == "" {
			continue
		}
		header, message, ok := strings.Cut(rec, "\x1e")
		if !ok {
			continue
		}
		parts := strings.SplitN(header, "\x1f", 3)
		if len(parts) < 3 || !fullHex(parts[0]) {
			continue
		}
		when, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		commit := gitCommit{SHA: strings.ToLower(parts[0]), Subject: oneLine(parts[2], 120), CommitterUnix: when}
		seen := map[string]bool{}
		for _, id := range sessionIDs(message) {
			if seen[id] {
				continue
			}
			seen[id] = true
			idx[id] = append(idx[id], commit)
		}
	}
	return idx, nil
}

// sessionIDs returns SDF-Session values that start a line. The hook writes
// ids of letters, digits, and . _ : - and refuses anything longer than 256.
func sessionIDs(message string) []string {
	var ids []string
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimRight(line, "\r")
		rest, ok := strings.CutPrefix(line, "SDF-Session:")
		if !ok {
			continue
		}
		id := strings.TrimSpace(rest)
		if !sessionID(id) {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func sessionID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == ':' || r == '-':
		default:
			return false
		}
	}
	return true
}

func canonicalizeRepo(path string) (string, error) {
	if path == "" || strings.HasPrefix(path, "-") {
		return "", fmt.Errorf("not a path")
	}
	top, err := git(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(top)), nil
}

func fullHex(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
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
