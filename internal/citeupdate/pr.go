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
	"time"
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
		SHA string `json:"sha"`
	} `json:"base"`
}

const citationWorkflow = "citation-gitleaks.yml"

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

type workflowRun struct {
	ID         int64  `json:"id"`
	HeadSHA    string `json:"head_sha"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	CreatedAt  string `json:"created_at"`
}

// ensureScan dispatches the citation gitleaks workflow on head and waits
// until a run for headSHA completes successfully. A run that already
// succeeded is reused. The workflow's own job check is not the ruleset
// check: workflow_dispatch jobs are not evaluated for a ruleset.
func ensureScan(ctx context.Context, client *http.Client, cfg Config, owner, repo, head, headSHA, baseSHA string) (workflowRun, error) {
	if err := validateCommit(headSHA); err != nil {
		return workflowRun{}, err
	}
	if err := validateCommit(baseSHA); err != nil {
		return workflowRun{}, err
	}
	runs, err := listScanRuns(ctx, client, cfg, owner, repo, headSHA)
	if err != nil {
		return workflowRun{}, err
	}
	// minID is the newest run already known. inclusive means that run is
	// still in progress and its own completion counts. A new dispatch only
	// counts a run with a greater id, so an older failure cannot pass.
	minID := latestScanID(runs)
	inclusive := false
	if newest, ok := newestScan(runs); ok && newest.Status == "completed" && newest.Conclusion == "success" {
		return newest, nil
	} else if ok && newest.Status != "completed" {
		inclusive = true
	} else if err := dispatchScan(ctx, client, cfg, owner, repo, head, headSHA, baseSHA); err != nil {
		return workflowRun{}, err
	}
	attempts := cfg.PollAttempts
	if attempts <= 0 {
		attempts = 90
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if err := waitPoll(ctx, cfg); err != nil {
				return workflowRun{}, err
			}
		}
		runs, err = listScanRuns(ctx, client, cfg, owner, repo, headSHA)
		if err != nil {
			return workflowRun{}, err
		}
		newest, ok := newestScan(runs)
		if !ok || newest.Status != "completed" {
			continue
		}
		if inclusive && newest.ID < minID {
			continue
		}
		if !inclusive && newest.ID <= minID {
			continue
		}
		if newest.Conclusion == "success" {
			return newest, nil
		}
		return workflowRun{}, fmt.Errorf("citation gitleaks run %d finished with %s", newest.ID, newest.Conclusion)
	}
	return workflowRun{}, errors.New("citation gitleaks did not pass before the wait ended")
}

func waitPoll(ctx context.Context, cfg Config) error {
	d := cfg.PollInterval
	if d <= 0 {
		d = 10 * time.Second
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func dispatchScan(ctx context.Context, client *http.Client, cfg Config, owner, repo, head, headSHA, baseSHA string) error {
	raw, err := apiURL(cfg.APIBase, owner, repo, "actions", "workflows", citationWorkflow, "dispatches")
	if err != nil {
		return err
	}
	body := struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs"`
	}{
		Ref: head,
		Inputs: map[string]string{
			"base_sha": baseSHA,
			"head_sha": headSHA,
		},
	}
	status, data, err := doJSON(ctx, client, http.MethodPost, raw, cfg.Token, body)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("dispatch citation gitleaks: %s", apiMessage(status, data))
	}
	return nil
}

func listScanRuns(ctx context.Context, client *http.Client, cfg Config, owner, repo, headSHA string) ([]workflowRun, error) {
	raw, err := apiURL(cfg.APIBase, owner, repo, "actions", "workflows", citationWorkflow, "runs")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("head_sha", headSHA)
	q.Set("event", "workflow_dispatch")
	q.Set("per_page", "20")
	u.RawQuery = q.Encode()
	status, data, err := doJSON(ctx, client, http.MethodGet, u.String(), cfg.Token, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list citation gitleaks runs: %s", apiMessage(status, data))
	}
	var body struct {
		WorkflowRuns []workflowRun `json:"workflow_runs"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	var matched []workflowRun
	for _, run := range body.WorkflowRuns {
		if strings.EqualFold(run.HeadSHA, headSHA) && run.Event == "workflow_dispatch" {
			matched = append(matched, run)
		}
	}
	return matched, nil
}

func newestScan(runs []workflowRun) (workflowRun, bool) {
	var found workflowRun
	ok := false
	for _, run := range runs {
		if !ok || run.CreatedAt >= found.CreatedAt {
			found = run
			ok = true
		}
	}
	return found, ok
}

func latestScanID(runs []workflowRun) int64 {
	var id int64
	for _, run := range runs {
		if run.ID > id {
			id = run.ID
		}
	}
	return id
}

func postScanStatus(ctx context.Context, client *http.Client, cfg Config, owner, repo, sha, target string) error {
	if target == "" {
		return errors.New("citation gitleaks run has no URL")
	}
	raw, err := apiURL(cfg.APIBase, owner, repo, "statuses", sha)
	if err != nil {
		return err
	}
	body := struct {
		State       string `json:"state"`
		Context     string `json:"context"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	}{
		State:       "success",
		Context:     gitleaksCheck,
		Description: "gitleaks scan of the citation pull request range passed",
		TargetURL:   target,
	}
	status, data, err := doJSON(ctx, client, http.MethodPost, raw, cfg.Token, body)
	if err != nil {
		return err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return fmt.Errorf("post gitleaks status: %s", apiMessage(status, data))
	}
	return nil
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
	if pr.Head.SHA != "" && pr.Head.SHA != cfg.HeadSHA {
		return Result{}, fmt.Errorf("citation pull request head is %s, not %s", pr.Head.SHA, cfg.HeadSHA)
	}
	if err := citationPRFiles(ctx, client, syncCfg, owner, repo, pr.Number); err != nil {
		return Result{}, err
	}
	passed, err := gitleaksStatusPassed(ctx, client, syncCfg, owner, repo, cfg.HeadSHA)
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

func gitleaksStatusPassed(ctx context.Context, client *http.Client, cfg Config, owner, repo, sha string) (bool, error) {
	raw, err := apiURL(cfg.APIBase, owner, repo, "commits", sha, "statuses")
	if err != nil {
		return false, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false, err
	}
	q := u.Query()
	q.Set("per_page", "100")
	u.RawQuery = q.Encode()
	status, data, err := doJSON(ctx, client, http.MethodGet, u.String(), cfg.Token, nil)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("list commit statuses: %s", apiMessage(status, data))
	}
	var statuses []struct {
		Context   string `json:"context"`
		State     string `json:"state"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(data, &statuses); err != nil {
		return false, err
	}
	found := false
	latest := ""
	state := ""
	for _, st := range statuses {
		if st.Context != gitleaksCheck {
			continue
		}
		if !found || st.CreatedAt >= latest {
			found = true
			latest = st.CreatedAt
			state = st.State
		}
	}
	return found && state == "success", nil
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
	pr, err := getPull(ctx, client, cfg, owner, repo, number)
	if err != nil {
		return false, err
	}
	return pr.Merged, nil
}

func getPull(ctx context.Context, client *http.Client, cfg Config, owner, repo string, number int) (pullRequest, error) {
	raw, err := apiURL(cfg.APIBase, owner, repo, "pulls", fmt.Sprint(number))
	if err != nil {
		return pullRequest{}, err
	}
	status, data, err := doJSON(ctx, client, http.MethodGet, raw, cfg.Token, nil)
	if err != nil {
		return pullRequest{}, err
	}
	if status != http.StatusOK {
		return pullRequest{}, fmt.Errorf("read pull request #%d: %s", number, apiMessage(status, data))
	}
	var pr pullRequest
	if err := json.Unmarshal(data, &pr); err != nil {
		return pullRequest{}, err
	}
	if pr.Number == 0 {
		pr.Number = number
	}
	return pr, nil
}
