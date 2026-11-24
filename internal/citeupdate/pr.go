package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	// citationMarker marks a pull request opened by this updater. The merge
	// path refuses a branch that does not carry it.
	citationMarker = "<!-- sdf-citation-update -->"
	// gitleaksCheck is the ruleset's required status check. It is the job
	// name in .github/workflows/pr.yml.
	gitleaksCheck = "gitleaks (full history)"
)

var citationHeadRE = regexp.MustCompile(`^citation/v[0-9]+\.[0-9]+\.[0-9]+$`)

func citationHead(tag string) (string, error) {
	if _, ok := stableVersion(tag); !ok {
		return "", fmt.Errorf("tag %s is not vMAJOR.MINOR.PATCH", safeTag(tag))
	}
	head := "citation/" + tag
	if !validBranch(head) {
		return "", fmt.Errorf("branch %q is not a branch name", head)
	}
	return head, nil
}

func validBranch(branch string) bool {
	return branchRE.MatchString(branch) && !strings.Contains(branch, "..") && !strings.HasPrefix(branch, "/") && !strings.HasSuffix(branch, "/")
}

type pullRequest struct {
	Number int    `json:"number"`
	NodeID string `json:"node_id"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	Head   struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// ensureCitationBranch creates head from the current default-branch commit
// when the branch is missing. An existing branch is left where it is.
func ensureCitationBranch(ctx context.Context, client *http.Client, cfg Config, owner, repo, head string) error {
	if _, err := branchCommit(ctx, client, cfg, owner, repo, head); err == nil {
		return nil
	} else if !errors.As(err, new(*notFoundError)) {
		return err
	}
	base, err := branchCommit(ctx, client, cfg, owner, repo, cfg.Branch)
	if err != nil {
		return err
	}
	raw, err := apiURL(cfg.APIBase, owner, repo, "git", "refs")
	if err != nil {
		return err
	}
	body := struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}{
		Ref: "refs/heads/" + head,
		SHA: base,
	}
	status, data, err := doJSON(ctx, client, http.MethodPost, raw, cfg.Token, body)
	if err != nil {
		return err
	}
	if status == http.StatusCreated || status == http.StatusOK {
		return nil
	}
	if _, err := branchCommit(ctx, client, cfg, owner, repo, head); err == nil {
		return nil
	}
	return fmt.Errorf("create branch %s: %s", head, apiMessage(status, data))
}

func branchCommit(ctx context.Context, client *http.Client, cfg Config, owner, repo, branch string) (string, error) {
	parts := []string{"git", "ref", "heads"}
	parts = append(parts, strings.Split(branch, "/")...)
	raw, err := apiURL(cfg.APIBase, owner, repo, parts...)
	if err != nil {
		return "", err
	}
	var ref struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := getJSON(ctx, client, cfg.Token, raw, &ref); err != nil {
		return "", err
	}
	sha := strings.ToLower(ref.Object.SHA)
	if strings.ToLower(ref.Object.Type) != "commit" {
		return "", fmt.Errorf("branch %s points at %s, not a commit", branch, ref.Object.Type)
	}
	if err := validateCommit(sha); err != nil {
		return "", err
	}
	return sha, nil
}

func ensureCitationPR(ctx context.Context, client *http.Client, cfg Config, owner, repo, head string, result Result) (pullRequest, error) {
	found, ok, err := findOpenCitationPR(ctx, client, cfg, owner, repo, head)
	if err != nil {
		return pullRequest{}, err
	}
	if ok {
		return found, nil
	}
	raw, err := apiURL(cfg.APIBase, owner, repo, "pulls")
	if err != nil {
		return pullRequest{}, err
	}
	body := struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
	}{
		Title: fmt.Sprintf("docs: update CITATION.cff for %s", result.Version),
		Head:  head,
		Base:  cfg.Branch,
		Body:  pullBody(result),
	}
	status, data, err := doJSON(ctx, client, http.MethodPost, raw, cfg.Token, body)
	if err != nil {
		return pullRequest{}, err
	}
	if status == http.StatusCreated || status == http.StatusOK {
		var pr pullRequest
		if err := json.Unmarshal(data, &pr); err != nil {
			return pullRequest{}, err
		}
		if pr.Number == 0 || pr.NodeID == "" {
			return pullRequest{}, errors.New("created pull request is missing a number or node id")
		}
		return pr, nil
	}
	if status == http.StatusUnprocessableEntity {
		found, ok, err = findOpenCitationPR(ctx, client, cfg, owner, repo, head)
		if err != nil {
			return pullRequest{}, err
		}
		if ok {
			return found, nil
		}
	}
	return pullRequest{}, fmt.Errorf("open citation pull request: %s", apiMessage(status, data))
}

func findOpenCitationPR(ctx context.Context, client *http.Client, cfg Config, owner, repo, head string) (pullRequest, bool, error) {
	raw, err := apiURL(cfg.APIBase, owner, repo, "pulls")
	if err != nil {
		return pullRequest{}, false, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return pullRequest{}, false, err
	}
	q := u.Query()
	q.Set("head", owner+":"+head)
	q.Set("base", cfg.Branch)
	q.Set("state", "open")
	u.RawQuery = q.Encode()
	status, data, err := doJSON(ctx, client, http.MethodGet, u.String(), cfg.Token, nil)
	if err != nil {
		return pullRequest{}, false, err
	}
	if status != http.StatusOK {
		return pullRequest{}, false, fmt.Errorf("list citation pull requests: %s", apiMessage(status, data))
	}
	var prs []pullRequest
	if err := json.Unmarshal(data, &prs); err != nil {
		return pullRequest{}, false, err
	}
	var match *pullRequest
	for i := range prs {
		pr := &prs[i]
		stateOK := pr.State == "open" || pr.State == ""
		if pr.Head.Ref != head || pr.Base.Ref != cfg.Branch || !stateOK {
			continue
		}
		if match != nil {
			return pullRequest{}, false, fmt.Errorf("more than one open pull request for %s", head)
		}
		match = pr
	}
	if match == nil {
		return pullRequest{}, false, nil
	}
	if match.Number == 0 || match.NodeID == "" {
		return pullRequest{}, false, errors.New("citation pull request is missing a number or node id")
	}
	return *match, true, nil
}

func pullBody(result Result) string {
	var b strings.Builder
	b.WriteString(citationMarker)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Set version %s, date-released %s, and commit %s in CITATION.cff from published tag %s.\n", result.Version, result.Date, result.Commit, "v"+result.Version)
	if result.TagObject != "" {
		fmt.Fprintf(&b, "The annotated tag object is %s and is not the commit above.\n", result.TagObject)
	}
	b.WriteString("The release tag is not moved. Cite this repository reads CITATION.cff on the default branch, so it stays stale until this pull request merges.\n")
	return b.String()
}

func enableAutoMerge(ctx context.Context, client *http.Client, cfg Config, nodeID string) (string, error) {
	if nodeID == "" {
		return "", errors.New("pull request node id is empty")
	}
	raw := strings.TrimRight(cfg.APIBase, "/") + "/graphql"
	query := `mutation($id:ID!){enablePullRequestAutoMerge(input:{pullRequestId:$id,mergeMethod:MERGE}){pullRequest{number}}}`
	payload := struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}{
		Query:     query,
		Variables: map[string]any{"id": nodeID},
	}
	status, data, err := doJSON(ctx, client, http.MethodPost, raw, cfg.Token, payload)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("enable auto-merge: %s", apiMessage(status, data))
	}
	var resp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", err
	}
	if len(resp.Errors) == 0 {
		return "enabled", nil
	}
	msg := resp.Errors[0].Message
	for _, e := range resp.Errors[1:] {
		msg += "; " + e.Message
	}
	if autoMergeUnavailable(msg) {
		return "unavailable", nil
	}
	if strings.Contains(strings.ToLower(msg), "already") {
		return "enabled", nil
	}
	return "", fmt.Errorf("enable auto-merge: %s", msg)
}

func autoMergeUnavailable(msg string) bool {
	m := strings.ToLower(msg)
	if !strings.Contains(m, "auto") {
		return false
	}
	return strings.Contains(m, "not enabled") || strings.Contains(m, "not allowed") || strings.Contains(m, "disabled")
}

// MergeConfig selects the citation pull request a completed pull-request
// workflow may merge.
type MergeConfig struct {
	Token      string
	Repo       string
	APIBase    string
	BaseBranch string
	HeadBranch string
	HeadSHA    string
	Client     *http.Client
}

// Merge merges the open citation pull request for HeadBranch after gitleaks
// (full history) has passed on HeadSHA. The merge method is a merge commit.
// Any other pull request is left alone. An already-merged pull request is
// reported as merged.
func Merge(ctx context.Context, cfg MergeConfig) (Result, error) {
	if !citationHeadRE.MatchString(cfg.HeadBranch) {
		return Result{Status: StatusSkipped, Reason: "not a citation branch"}, nil
	}
	tag := strings.TrimPrefix(cfg.HeadBranch, "citation/")
	version, ok := stableVersion(tag)
	if !ok {
		return Result{Status: StatusSkipped, Reason: "not a citation branch"}, nil
	}
	if err := validateCommit(cfg.HeadSHA); err != nil {
		return Result{}, err
	}
	syncCfg := Config{
		Token:   cfg.Token,
		Repo:    cfg.Repo,
		APIBase: cfg.APIBase,
		Branch:  cfg.BaseBranch,
		Client:  cfg.Client,
	}
	if err := validateConfig(syncCfg); err != nil {
		return Result{}, err
	}
	client := cfg.Client
	if client == nil {
		client = githubClient()
	}
	owner, repo, _ := strings.Cut(cfg.Repo, "/")
	pr, ok, err := findOpenCitationPR(ctx, client, syncCfg, owner, repo, cfg.HeadBranch)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Status:      StatusSkipped,
		Version:     version,
		HeadBranch:  cfg.HeadBranch,
		PullRequest: pr.Number,
	}
	if !ok {
		result.Reason = "no open citation pull request"
		return result, nil
	}
	if !strings.Contains(pr.Body, citationMarker) {
		result.Reason = "pull request was not opened by the citation updater"
		return result, nil
	}
	if err := citationPRFiles(ctx, client, syncCfg, owner, repo, pr.Number); err != nil {
		return Result{}, err
	}
	passed, err := gitleaksPassed(ctx, client, syncCfg, owner, repo, cfg.HeadSHA)
	if err != nil {
		return Result{}, err
	}
	if !passed {
		result.Reason = "gitleaks (full history) has not passed"
		return result, nil
	}
	if err := mergePullRequest(ctx, client, syncCfg, owner, repo, pr.Number, cfg.HeadSHA); err != nil {
		return Result{}, err
	}
	result.Status = StatusMerged
	result.Reason = "merged after gitleaks (full history) passed"
	return result, nil
}

// StatusMerged means the citation pull request was merged into the default branch.
const StatusMerged Status = "merged"

func citationPRFiles(ctx context.Context, client *http.Client, cfg Config, owner, repo string, number int) error {
	raw, err := apiURL(cfg.APIBase, owner, repo, "pulls", fmt.Sprint(number), "files")
	if err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("per_page", "100")
	u.RawQuery = q.Encode()
	status, data, err := doJSON(ctx, client, http.MethodGet, u.String(), cfg.Token, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("list pull request files: %s", apiMessage(status, data))
	}
	var files []struct {
		Filename string `json:"filename"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(data, &files); err != nil {
		return err
	}
	if len(files) != 1 || files[0].Filename != citationPath || files[0].Status == "removed" {
		return fmt.Errorf("citation pull request #%d does not change only CITATION.cff", number)
	}
	return nil
}

func gitleaksPassed(ctx context.Context, client *http.Client, cfg Config, owner, repo, sha string) (bool, error) {
	raw, err := apiURL(cfg.APIBase, owner, repo, "commits", sha, "check-runs")
	if err != nil {
		return false, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false, err
	}
	q := u.Query()
	q.Set("check_name", gitleaksCheck)
	q.Set("per_page", "100")
	u.RawQuery = q.Encode()
	status, data, err := doJSON(ctx, client, http.MethodGet, u.String(), cfg.Token, nil)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("list check runs: %s", apiMessage(status, data))
	}
	var body struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			StartedAt  string `json:"started_at"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return false, err
	}
	latest := ""
	conclusion := ""
	statusText := ""
	for _, run := range body.CheckRuns {
		if run.Name != gitleaksCheck {
			continue
		}
		if latest == "" || run.StartedAt >= latest {
			latest = run.StartedAt
			conclusion = run.Conclusion
			statusText = run.Status
		}
	}
	if latest == "" && len(body.CheckRuns) > 0 {
		// A response with no started_at still counts if it is the only run.
		for _, run := range body.CheckRuns {
			if run.Name == gitleaksCheck && run.Status == "completed" && run.Conclusion == "success" {
				return true, nil
			}
		}
	}
	return statusText == "completed" && conclusion == "success", nil
}

func mergePullRequest(ctx context.Context, client *http.Client, cfg Config, owner, repo string, number int, sha string) error {
	raw, err := apiURL(cfg.APIBase, owner, repo, "pulls", fmt.Sprint(number), "merge")
	if err != nil {
		return err
	}
	body := struct {
		MergeMethod string `json:"merge_method"`
		SHA         string `json:"sha"`
	}{
		MergeMethod: "merge",
		SHA:         sha,
	}
	status, data, err := doJSON(ctx, client, http.MethodPut, raw, cfg.Token, body)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if status == http.StatusMethodNotAllowed || status == http.StatusConflict {
		merged, err := pullMerged(ctx, client, cfg, owner, repo, number)
		if err != nil {
			return err
		}
		if merged {
			return nil
		}
	}
	return fmt.Errorf("merge citation pull request #%d: %s", number, apiMessage(status, data))
}

func pullMerged(ctx context.Context, client *http.Client, cfg Config, owner, repo string, number int) (bool, error) {
	raw, err := apiURL(cfg.APIBase, owner, repo, "pulls", fmt.Sprint(number))
	if err != nil {
		return false, err
	}
	status, data, err := doJSON(ctx, client, http.MethodGet, raw, cfg.Token, nil)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("read pull request #%d: %s", number, apiMessage(status, data))
	}
	var pr pullRequest
	if err := json.Unmarshal(data, &pr); err != nil {
		return false, err
	}
	return pr.Merged, nil
}
