package outcome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/summary"
)

// Lookup is a GitHub read of pull requests and checks for one commit.
// Implementations must not put a token on the command line.
type Lookup interface {
	// Pulls lists pull requests that contain sha. When that list is empty
	// and branch is the head branch the hook recorded, it also lists pull
	// requests opened from that branch and keeps those whose head SHA is sha.
	Pulls(ctx context.Context, owner, repo, sha, branch string) ([]summary.PullRequest, error)
	Checks(ctx context.Context, owner, repo, sha string) (string, error)
}

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	shaPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// NewGitHub uses the gh CLI when it is on PATH, and otherwise
// GH_TOKEN or GITHUB_TOKEN against api.github.com. The token is sent as a
// header, not as an argument.
func NewGitHub() (Lookup, error) {
	if path, err := exec.LookPath("gh"); err == nil {
		return ghClient{path: path}, nil
	}
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		return nil, errors.New("--github needs gh on PATH or GH_TOKEN or GITHUB_TOKEN")
	}
	return &httpGitHub{token: token, client: &http.Client{Timeout: 15 * time.Second}, base: "https://api.github.com"}, nil
}

func enrich(out *summary.SessionOutcome, facts summary.RepositoryFacts, lookup Lookup) {
	owner, repo, reason := githubRepo(out.Repo, facts)
	if reason != "" {
		out.AcceptedReason = reason
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var (
		chosen  *summary.PullRequest
		missing bool
		states  []string
	)
	for _, commit := range out.Commits {
		pulls, err := lookup.Pulls(ctx, owner, repo, commit.SHA, branchFor(facts, commit.SHA))
		if err != nil {
			out.AcceptedReason = "github: " + redact(err.Error())
			return
		}
		state, err := lookup.Checks(ctx, owner, repo, commit.SHA)
		if err != nil {
			out.AcceptedReason = "github checks: " + redact(err.Error())
			return
		}
		states = append(states, state)
		switch len(pulls) {
		case 0:
			missing = true
		case 1:
			if chosen == nil {
				copy := pulls[0]
				chosen = &copy
				continue
			}
			if chosen.Number != pulls[0].Number {
				out.CI = combineCI(states)
				out.AcceptedReason = "commits are in more than one pull request"
				return
			}
		default:
			out.CI = combineCI(states)
			out.AcceptedReason = "commit " + commit.SHA + " is in more than one pull request"
			return
		}
	}
	out.CI = combineCI(states)
	if chosen == nil {
		no := false
		out.Accepted = &no
		out.AcceptedReason = "no pull request contains the linked commit"
		return
	}
	if missing {
		out.AcceptedReason = "not every linked commit is in the pull request"
		return
	}
	out.PullRequest = chosen
	merged := chosen.Merged
	out.Accepted = &merged
	if !merged {
		if strings.EqualFold(chosen.State, "closed") {
			out.AcceptedReason = "pull request was closed without merging"
		} else {
			out.AcceptedReason = "pull request is not merged"
		}
	}
}

// branchFor is the hook branch recorded for sha. Two different branches
// for the same SHA are not a guess.
func branchFor(facts summary.RepositoryFacts, sha string) string {
	branch := ""
	for _, commit := range facts.HookCommits {
		if !strings.EqualFold(commit.SHA, sha) || commit.Branch == "" {
			continue
		}
		if branch != "" && !strings.EqualFold(branch, commit.Branch) {
			return ""
		}
		branch = commit.Branch
	}
	return branch
}

func githubRepo(toplevel string, facts summary.RepositoryFacts) (string, string, string) {
	remoteOwner, remoteRepo := "", ""
	remoteReason := ""
	if toplevel != "" {
		raw, err := git(toplevel, "remote", "get-url", "origin")
		if err != nil {
			remoteReason = "origin remote is not set"
		} else if owner, repo, ok := parseGitHubRemote(raw); ok {
			remoteOwner, remoteRepo = owner, repo
		} else {
			remoteReason = "origin remote is not a github.com repository"
		}
	}
	factOwner, factRepo := facts.Owner, facts.RepositoryName
	if facts.Provider != "" && !strings.EqualFold(facts.Provider, "github") && factOwner != "" {
		return "", "", "repository provider is " + facts.Provider + ", not GitHub"
	}
	if remoteOwner != "" && factOwner != "" && (remoteOwner != factOwner || remoteRepo != factRepo) {
		return "", "", "exported repository " + factOwner + "/" + factRepo + " does not match origin"
	}
	if remoteOwner != "" {
		return remoteOwner, remoteRepo, ""
	}
	if factOwner != "" && factRepo != "" && (facts.Provider == "" || strings.EqualFold(facts.Provider, "github")) {
		return factOwner, factRepo, ""
	}
	if remoteReason != "" {
		return "", "", remoteReason
	}
	return "", "", "no GitHub repository for this session"
}

func parseGitHubRemote(raw string) (string, string, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ".git")
	if rest, ok := strings.CutPrefix(raw, "git@github.com:"); ok {
		return splitOwnerRepo(rest)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return "", "", false
	}
	return splitOwnerRepo(strings.TrimPrefix(parsed.EscapedPath(), "/"))
}

func splitOwnerRepo(path string) (string, string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !ownerPattern.MatchString(parts[0]) || !ownerPattern.MatchString(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// combineCI folds combined-status and check-run states into one word.
// failure wins over pending, which wins over success. An empty set is none.
func combineCI(states []string) string {
	rank := map[string]int{"": 0, "none": 0, "success": 1, "pending": 2, "failure": 3}
	best := "none"
	for _, state := range states {
		if rank[state] > rank[best] {
			best = state
		}
	}
	return best
}

type ghClient struct {
	path string
}

func (g ghClient) Pulls(ctx context.Context, owner, repo, sha, branch string) ([]summary.PullRequest, error) {
	return pullsByCommitOrBranch(ctx, g.api, owner, repo, sha, branch)
}

func (g ghClient) Checks(ctx context.Context, owner, repo, sha string) (string, error) {
	statusBody, err := g.api(ctx, fmt.Sprintf("repos/%s/%s/commits/%s/status", owner, repo, sha))
	if err != nil {
		return "", err
	}
	runsBody, err := g.api(ctx, fmt.Sprintf("repos/%s/%s/commits/%s/check-runs", owner, repo, sha))
	if err != nil {
		return "", err
	}
	return decodeChecks(statusBody, runsBody)
}

func (g ghClient) api(ctx context.Context, path string) ([]byte, error) {
	if err := validateGitHubPath(path); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, g.path, "api", "-H", "Accept: application/vnd.github+json", path)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		msg := err.Error()
		if errors.As(err, &exit) {
			msg = strings.TrimSpace(string(exit.Stderr))
		}
		return nil, errors.New(redact(msg))
	}
	return out, nil
}

type httpGitHub struct {
	token  string
	client *http.Client
	base   string
}

func (h *httpGitHub) Pulls(ctx context.Context, owner, repo, sha, branch string) ([]summary.PullRequest, error) {
	return pullsByCommitOrBranch(ctx, func(ctx context.Context, path string) ([]byte, error) {
		return h.get(ctx, "/"+path)
	}, owner, repo, sha, branch)
}

// pullsByCommitOrBranch uses the commit's pull request list, then the hook
// branch. GitHub's commit list omits a closed pull request that was not
// merged. The branch list includes it; a row counts only when its head SHA
// is the commit.
func pullsByCommitOrBranch(ctx context.Context, get func(context.Context, string) ([]byte, error), owner, repo, sha, branch string) ([]summary.PullRequest, error) {
	body, err := get(ctx, fmt.Sprintf("repos/%s/%s/commits/%s/pulls", owner, repo, sha))
	if err != nil {
		return nil, err
	}
	pulls, err := decodePulls(body)
	if err != nil || len(pulls) > 0 || !usableBranch(branch) {
		return pulls, err
	}
	q := url.Values{}
	q.Set("state", "all")
	q.Set("head", owner+":"+branch)
	q.Set("per_page", "100")
	body, err = get(ctx, fmt.Sprintf("repos/%s/%s/pulls?%s", owner, repo, q.Encode()))
	if err != nil {
		return nil, err
	}
	return decodePullsForSHA(body, sha)
}

func (h *httpGitHub) Checks(ctx context.Context, owner, repo, sha string) (string, error) {
	statusBody, err := h.get(ctx, fmt.Sprintf("/repos/%s/%s/commits/%s/status", owner, repo, sha))
	if err != nil {
		return "", err
	}
	runsBody, err := h.get(ctx, fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs", owner, repo, sha))
	if err != nil {
		return "", err
	}
	return decodeChecks(statusBody, runsBody)
}

func (h *httpGitHub) get(ctx context.Context, path string) ([]byte, error) {
	if err := validateGitHubPath(strings.TrimPrefix(path, "/")); err != nil {
		return nil, err
	}
	base := h.base
	if base == "" {
		base = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "sdf")
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, errors.New(redact(err.Error()))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func validateGitHubPath(path string) error {
	rawPath, query, _ := strings.Cut(path, "?")
	parts := strings.Split(rawPath, "/")
	// repos/{owner}/{repo}/commits/{sha}/...
	if len(parts) >= 5 && parts[0] == "repos" && parts[3] == "commits" {
		if query != "" {
			return errors.New("refusing github path")
		}
		if !ownerPattern.MatchString(parts[1]) || !ownerPattern.MatchString(parts[2]) || !shaPattern.MatchString(parts[4]) {
			return errors.New("refusing github path")
		}
		return nil
	}
	// repos/{owner}/{repo}/pulls?state=all&head={owner}:{branch}&per_page=100
	if len(parts) == 4 && parts[0] == "repos" && parts[3] == "pulls" {
		if !ownerPattern.MatchString(parts[1]) || !ownerPattern.MatchString(parts[2]) {
			return errors.New("refusing github path")
		}
		return validateHeadQuery(parts[1], query)
	}
	return errors.New("refusing github path")
}

func validateHeadQuery(owner, query string) error {
	values, err := url.ParseQuery(query)
	if err != nil || values.Get("state") != "all" || values.Get("per_page") != "100" {
		return errors.New("refusing github path")
	}
	head := values.Get("head")
	prefix := owner + ":"
	if !strings.HasPrefix(head, prefix) || !usableBranch(strings.TrimPrefix(head, prefix)) {
		return errors.New("refusing github path")
	}
	for key := range values {
		switch key {
		case "state", "head", "per_page":
		default:
			return errors.New("refusing github path")
		}
	}
	return nil
}

// usableBranch is a head ref that can go in the pulls query. Detached
// HEAD and flag-like names are not pull request branches.
func usableBranch(branch string) bool {
	if branch == "" || len(branch) > 250 || strings.EqualFold(branch, "HEAD") {
		return false
	}
	if strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.Contains(branch, "@{") {
		return false
	}
	if strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") {
		return false
	}
	for i, r := range branch {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == '/' || r == '-'):
		default:
			return false
		}
	}
	return true
}

func decodePulls(body []byte) ([]summary.PullRequest, error) {
	return decodePullBodies(body, "")
}

func decodePullsForSHA(body []byte, sha string) ([]summary.PullRequest, error) {
	return decodePullBodies(body, sha)
}

func decodePullBodies(body []byte, headSHA string) ([]summary.PullRequest, error) {
	var rows []struct {
		Number   int     `json:"number"`
		HTMLURL  string  `json:"html_url"`
		State    string  `json:"state"`
		MergedAt *string `json:"merged_at"`
		Head     struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	out := make([]summary.PullRequest, 0, len(rows))
	for _, row := range rows {
		if headSHA != "" && !strings.EqualFold(row.Head.SHA, headSHA) {
			continue
		}
		out = append(out, summary.PullRequest{
			Number: row.Number,
			URL:    row.HTMLURL,
			State:  row.State,
			Merged: row.MergedAt != nil && *row.MergedAt != "",
		})
	}
	return out, nil
}

func decodeChecks(statusBody, runsBody []byte) (string, error) {
	var status struct {
		State      string            `json:"state"`
		TotalCount int               `json:"total_count"`
		Statuses   []json.RawMessage `json:"statuses"`
	}
	if len(bytesTrim(statusBody)) > 0 {
		if err := json.Unmarshal(statusBody, &status); err != nil {
			return "", err
		}
	}
	var runs struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if len(bytesTrim(runsBody)) > 0 {
		if err := json.Unmarshal(runsBody, &runs); err != nil {
			return "", err
		}
	}
	// Combined status is "pending" with total_count 0 when the commit has
	// no statuses. That is not a check waiting to finish.
	var states []string
	if !(status.State == "pending" && status.TotalCount == 0 && len(status.Statuses) == 0) {
		states = append(states, normalizeStatus(status.State))
	}
	for _, run := range runs.CheckRuns {
		states = append(states, normalizeRun(run.Status, run.Conclusion))
	}
	return combineCI(states), nil
}

func normalizeStatus(state string) string {
	switch state {
	case "success":
		return "success"
	case "pending":
		return "pending"
	case "failure", "error":
		return "failure"
	default:
		return "none"
	}
}

func normalizeRun(status, conclusion string) string {
	if status != "completed" {
		if status == "" {
			return "none"
		}
		return "pending"
	}
	switch conclusion {
	case "success":
		return "success"
	case "failure", "timed_out", "cancelled", "action_required", "startup_failure":
		return "failure"
	default:
		return "none"
	}
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func redact(msg string) string {
	for _, key := range []string{os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN")} {
		if key != "" {
			msg = strings.ReplaceAll(msg, key, "[redacted]")
		}
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}
