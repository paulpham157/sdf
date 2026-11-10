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
	Pulls(ctx context.Context, owner, repo, sha string) ([]summary.PullRequest, error)
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
	return &httpGitHub{token: token, client: &http.Client{Timeout: 15 * time.Second}}, nil
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
		pulls, err := lookup.Pulls(ctx, owner, repo, commit.SHA)
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
		out.AcceptedReason = "pull request is not merged"
	}
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

func (g ghClient) Pulls(ctx context.Context, owner, repo, sha string) ([]summary.PullRequest, error) {
	body, err := g.api(ctx, fmt.Sprintf("repos/%s/%s/commits/%s/pulls", owner, repo, sha))
	if err != nil {
		return nil, err
	}
	return decodePulls(body)
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
}

func (h *httpGitHub) Pulls(ctx context.Context, owner, repo, sha string) ([]summary.PullRequest, error) {
	body, err := h.get(ctx, fmt.Sprintf("/repos/%s/%s/commits/%s/pulls", owner, repo, sha))
	if err != nil {
		return nil, err
	}
	return decodePulls(body)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
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
	parts := strings.Split(path, "/")
	// repos/{owner}/{repo}/commits/{sha}/...
	if len(parts) < 5 || parts[0] != "repos" || parts[3] != "commits" {
		return errors.New("refusing github path")
	}
	if !ownerPattern.MatchString(parts[1]) || !ownerPattern.MatchString(parts[2]) || !shaPattern.MatchString(parts[4]) {
		return errors.New("refusing github path")
	}
	return nil
}

func decodePulls(body []byte) ([]summary.PullRequest, error) {
	var rows []struct {
		Number   int     `json:"number"`
		HTMLURL  string  `json:"html_url"`
		State    string  `json:"state"`
		MergedAt *string `json:"merged_at"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	out := make([]summary.PullRequest, 0, len(rows))
	for _, row := range rows {
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
		State string `json:"state"`
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
	states := []string{normalizeStatus(status.State)}
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
