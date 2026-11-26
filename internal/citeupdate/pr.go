package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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
	// citationWorkflow is the workflow_dispatch scan. Its job check is not
	// the ruleset check.
	citationWorkflow = "citation-gitleaks.yml"
	// citationWorkflowPath is the path the Actions API reports for that
	// workflow. A run of any other file does not satisfy the status post.
	citationWorkflowPath = ".github/workflows/" + citationWorkflow
	// githubActionsBot is the GITHUB_TOKEN user that authors the contents
	// API commit. A local commit can copy the noreply address, so the
	// branch check also requires GitHub's verified signature.
	githubActionsBot   = "github-actions[bot]"
	githubActionsEmail = "41898282+github-actions[bot]@users.noreply.github.com"
	// citationCommitPrefix is the subject commitMessage writes.
	citationCommitPrefix = "docs: update CITATION.cff for "
	// maxCitationFilePages bounds the pull-request files walk. One more
	// page after this fails closed instead of treating a partial list as
	// the whole diff.
	maxCitationFilePages = 10
	// maxScanRunPages bounds the workflow-run list the same way. A scan
	// that only appears on a later page is not treated as absent.
	maxScanRunPages = 10
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

// ensureCitationBranch creates head at the current default-branch commit
// when the branch is missing. An existing branch is reused only when its
// tip is that commit, when it is strictly behind that commit (then this
// fast-forwards the ref, which is not a force-push), or when every commit
// reachable from the tip but not from the default branch was created by
// this workflow. A diverged history, a foreign commit, or a compare the
// API does not return in one page fails the job and leaves the ref where
// it is. There is no force update.
func ensureCitationBranch(ctx context.Context, client *http.Client, cfg Config, owner, repo, head string) error {
	if head == cfg.Branch || !citationHeadRE.MatchString(head) {
		return fmt.Errorf("refusing to use %s as a citation branch", head)
	}
	_, err := branchCommit(ctx, client, cfg, owner, repo, head)
	if err != nil {
		var missing *notFoundError
		if !errors.As(err, &missing) {
			return err
		}
		base, err := branchCommit(ctx, client, cfg, owner, repo, cfg.Branch)
		if err != nil {
			return err
		}
		if err := createCitationRef(ctx, client, cfg, owner, repo, head, base); err != nil {
			if _, again := branchCommit(ctx, client, cfg, owner, repo, head); again != nil {
				return err
			}
			// The create lost a race. Do not treat the branch that appeared
			// as ours until its commits pass the same check.
			return acceptCitationBranch(ctx, client, cfg, owner, repo, head)
		}
		got, err := branchCommit(ctx, client, cfg, owner, repo, head)
		if err != nil {
			return err
		}
		if got == base {
			return nil
		}
		return acceptCitationBranch(ctx, client, cfg, owner, repo, head)
	}
	return acceptCitationBranch(ctx, client, cfg, owner, repo, head)
}

func createCitationRef(ctx context.Context, client *http.Client, cfg Config, owner, repo, head, base string) error {
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
	return fmt.Errorf("create branch %s: %s", head, apiMessage(status, data))
}

// acceptCitationBranch fails closed unless head is the default branch, is
// strictly behind it, or is ahead of it by commits this workflow authored.
func acceptCitationBranch(ctx context.Context, client *http.Client, cfg Config, owner, repo, head string) error {
	tip, err := branchCommit(ctx, client, cfg, owner, repo, head)
	if err != nil {
		return err
	}
	base, err := branchCommit(ctx, client, cfg, owner, repo, cfg.Branch)
	if err != nil {
		return err
	}
	if tip == base {
		return nil
	}
	cmp, err := compareCommits(ctx, client, cfg, owner, repo, base, tip)
	if err != nil {
		return err
	}
	if cmp.incomplete() {
		return fmt.Errorf("refusing to reuse %s: the compare against %s is incomplete", head, cfg.Branch)
	}
	switch cmp.Status {
	case "identical":
		return fmt.Errorf("refusing to reuse %s: compare says identical but the tips differ", head)
	case "behind":
		// Every commit on head is already reachable from the default
		// branch. Moving the ref forward to that commit is a fast-forward.
		return fastForwardCitationBranch(ctx, client, cfg, owner, repo, head, base)
	case "diverged":
		return fmt.Errorf("refusing to reuse %s: it is not based on %s", head, cfg.Branch)
	case "ahead":
		for _, commit := range cmp.Commits {
			if err := requireWorkflowCommit(commit); err != nil {
				return fmt.Errorf("refusing to reuse %s: %w", head, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("refusing to reuse %s: compare status %q", head, cmp.Status)
	}
}

type branchCompare struct {
	Status       string        `json:"status"`
	AheadBy      int           `json:"ahead_by"`
	BehindBy     int           `json:"behind_by"`
	TotalCommits int           `json:"total_commits"`
	Commits      []citedCommit `json:"commits"`
	next         bool
}

func (c branchCompare) incomplete() bool {
	if c.next {
		return true
	}
	switch c.Status {
	case "ahead", "diverged":
		return c.AheadBy != len(c.Commits) || c.TotalCommits != len(c.Commits) || len(c.Commits) == 0
	case "behind":
		return len(c.Commits) != 0 || c.AheadBy != 0 || c.TotalCommits != 0 || c.BehindBy < 1
	case "identical":
		return len(c.Commits) != 0 || c.AheadBy != 0 || c.BehindBy != 0 || c.TotalCommits != 0
	default:
		return true
	}
}

type citedCommit struct {
	SHA     string `json:"sha"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"author"`
		Committer struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"committer"`
		Verification *struct {
			Verified bool   `json:"verified"`
			Reason   string `json:"reason"`
		} `json:"verification"`
	} `json:"commit"`
	Author    *gitHubAccount `json:"author"`
	Committer *gitHubAccount `json:"committer"`
}

type gitHubAccount struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

func compareCommits(ctx context.Context, client *http.Client, cfg Config, owner, repo, base, head string) (branchCompare, error) {
	raw, err := apiURL(cfg.APIBase, owner, repo, "compare", base+"..."+head)
	if err != nil {
		return branchCompare{}, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return branchCompare{}, err
	}
	q := u.Query()
	q.Set("per_page", "100")
	u.RawQuery = q.Encode()
	status, data, hdr, err := doJSONHeader(ctx, client, http.MethodGet, u.String(), cfg.Token, nil)
	if err != nil {
		return branchCompare{}, err
	}
	if status != http.StatusOK {
		return branchCompare{}, fmt.Errorf("compare %s...%s: %s", base, head, apiMessage(status, data))
	}
	var cmp branchCompare
	if err := json.Unmarshal(data, &cmp); err != nil {
		return branchCompare{}, err
	}
	next, err := nextPageURL(hdr)
	if err != nil {
		return branchCompare{}, fmt.Errorf("compare %s...%s: %w", base, head, err)
	}
	cmp.next = next != ""
	return cmp, nil
}

// requireWorkflowCommit accepts one commit the contents API wrote for this
// updater. Anything else — another account, an unverified signature, a
// different message, or a merge — is refused. The message is not copied
// into the error.
func requireWorkflowCommit(commit citedCommit) error {
	sha := strings.ToLower(strings.TrimSpace(commit.SHA))
	if err := validateCommit(sha); err != nil {
		return errors.New("a citation branch commit is not a sha")
	}
	if commit.Author == nil || commit.Author.Login != githubActionsBot || !strings.EqualFold(commit.Author.Type, "Bot") {
		return fmt.Errorf("commit %s was not authored by the citation workflow", sha)
	}
	if commit.Committer == nil || commit.Committer.Login != githubActionsBot || !strings.EqualFold(commit.Committer.Type, "Bot") {
		return fmt.Errorf("commit %s was not committed by the citation workflow", sha)
	}
	if commit.Commit.Author.Name != githubActionsBot || !strings.EqualFold(commit.Commit.Author.Email, githubActionsEmail) {
		return fmt.Errorf("commit %s author is not the citation workflow", sha)
	}
	if commit.Commit.Committer.Name != githubActionsBot || !strings.EqualFold(commit.Commit.Committer.Email, githubActionsEmail) {
		return fmt.Errorf("commit %s committer is not the citation workflow", sha)
	}
	if commit.Commit.Verification == nil || !commit.Commit.Verification.Verified || commit.Commit.Verification.Reason != "valid" {
		return fmt.Errorf("commit %s is not a verified GitHub commit", sha)
	}
	if !strings.HasPrefix(commit.Commit.Message, citationCommitPrefix) {
		return fmt.Errorf("commit %s was not created by the citation updater", sha)
	}
	if len(commit.Parents) != 1 || validateCommit(strings.ToLower(commit.Parents[0].SHA)) != nil {
		return fmt.Errorf("commit %s is not a single-parent citation update", sha)
	}
	return nil
}

func fastForwardCitationBranch(ctx context.Context, client *http.Client, cfg Config, owner, repo, branch, sha string) error {
	if branch == "" || branch == cfg.Branch || !strings.HasPrefix(branch, "citation/") {
		return fmt.Errorf("refusing to move %s", branch)
	}
	if err := validateCommit(sha); err != nil {
		return err
	}
	parts := []string{"git", "refs", "heads"}
	parts = append(parts, strings.Split(branch, "/")...)
	raw, err := apiURL(cfg.APIBase, owner, repo, parts...)
	if err != nil {
		return err
	}
	// sha only. Omitting force leaves GitHub's fast-forward check in place.
	payload := struct {
		SHA string `json:"sha"`
	}{SHA: sha}
	status, data, err := doJSON(ctx, client, http.MethodPatch, raw, cfg.Token, payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("refusing to move %s: %s", branch, apiMessage(status, data))
	}
	got, err := branchCommit(ctx, client, cfg, owner, repo, branch)
	if err != nil {
		return err
	}
	if got != sha {
		return fmt.Errorf("branch %s is %s after update, not %s", branch, got, sha)
	}
	return nil
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
	if status == http.StatusForbidden {
		return pullRequest{}, errors.New("GitHub refused to open the citation pull request (403). The repository setting \"Allow GitHub Actions to create and approve pull requests\" is off, so the citation branch was written and the pull request was not. Turn that setting on (Settings, Actions, General, Workflow permissions), then re-run this workflow. The citation branch is left as it is")
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
	Name       string `json:"name"`
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	CreatedAt  string `json:"created_at"`
	Path       string `json:"path"`
}

// scanRunName is the workflow run-name. The runs API does not return
// dispatch inputs, so this is how a run is tied to the base SHA that was
// scanned. The head SHA is also in the name and is checked on its own field.
func scanRunName(baseSHA, headSHA string) string {
	return "scan " + baseSHA + ".." + headSHA
}

// ensureScan always dispatches a fresh citation gitleaks run and waits for
// that run. A successful run already on the head is not reused: it may have
// scanned a different base. The new run is the one whose id is greater than
// every run listed before the dispatch, whose head SHA and branch match, and
// whose run name is scanRunName for this base and head. The list is followed
// across pages. The workflow's own job check is not the ruleset check.
func ensureScan(ctx context.Context, client *http.Client, cfg Config, owner, repo, head, headSHA, baseSHA string) (workflowRun, error) {
	if err := validateCommit(headSHA); err != nil {
		return workflowRun{}, err
	}
	if err := validateCommit(baseSHA); err != nil {
		return workflowRun{}, err
	}
	prior, err := listScanRuns(ctx, client, cfg, owner, repo, headSHA)
	if err != nil {
		return workflowRun{}, err
	}
	priorMax := latestScanID(prior)
	if err := dispatchScan(ctx, client, cfg, owner, repo, head, headSHA, baseSHA); err != nil {
		return workflowRun{}, err
	}
	attempts := cfg.PollAttempts
	if attempts <= 0 {
		attempts = 90
	}
	var bound int64
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if err := waitPoll(ctx, cfg); err != nil {
				return workflowRun{}, err
			}
		}
		runs, err := listScanRuns(ctx, client, cfg, owner, repo, headSHA)
		if err != nil {
			return workflowRun{}, err
		}
		if bound == 0 {
			if match, ok := dispatchedRun(runs, priorMax, headSHA, head, baseSHA); ok {
				bound = match.ID
			} else if rogue, ok := completedAfter(runs, priorMax, headSHA); ok {
				if err := scanIdentity(rogue, headSHA, head, baseSHA); err != nil {
					return workflowRun{}, fmt.Errorf("refusing citation gitleaks run %d: %w", rogue.ID, err)
				}
			}
		}
		if bound == 0 {
			continue
		}
		confirmed, err := loadScanRun(ctx, client, cfg, owner, repo, bound)
		if err != nil {
			return workflowRun{}, err
		}
		if confirmed.Status != "completed" {
			continue
		}
		if err := scanReady(confirmed, headSHA, head, baseSHA); err != nil {
			return workflowRun{}, err
		}
		return confirmed, nil
	}
	return workflowRun{}, errors.New("citation gitleaks did not pass before the wait ended")
}

// dispatchedRun is the newest run created after the dispatch that already
// matches this head, branch, and base. An older success is not a candidate.
func dispatchedRun(runs []workflowRun, priorMax int64, headSHA, branch, baseSHA string) (workflowRun, bool) {
	var found workflowRun
	ok := false
	for _, run := range runs {
		if run.ID <= priorMax {
			continue
		}
		if scanIdentity(run, headSHA, branch, baseSHA) != nil {
			continue
		}
		if !ok || run.ID > found.ID {
			found = run
			ok = true
		}
	}
	return found, ok
}

// completedAfter is the newest completed run for headSHA that appeared
// after the dispatch. It is used to fail closed when that run scanned a
// different base instead of waiting for a run that will not arrive.
func completedAfter(runs []workflowRun, priorMax int64, headSHA string) (workflowRun, bool) {
	var found workflowRun
	ok := false
	for _, run := range runs {
		if run.ID <= priorMax || !strings.EqualFold(run.HeadSHA, headSHA) || run.Status != "completed" {
			continue
		}
		if !ok || run.ID > found.ID {
			found = run
			ok = true
		}
	}
	return found, ok
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
	q.Set("per_page", "100")
	u.RawQuery = q.Encode()
	next := u.String()
	seen := map[string]struct{}{}
	var matched []workflowRun
	for page := 1; page <= maxScanRunPages; page++ {
		if _, ok := seen[next]; ok {
			return nil, errors.New("citation gitleaks run list repeated a page")
		}
		seen[next] = struct{}{}
		if err := sameAPIHost(cfg.APIBase, next); err != nil {
			return nil, err
		}
		status, data, hdr, err := doJSONHeader(ctx, client, http.MethodGet, next, cfg.Token, nil)
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
		for _, run := range body.WorkflowRuns {
			if strings.EqualFold(run.HeadSHA, headSHA) && run.Event == "workflow_dispatch" {
				matched = append(matched, run)
			}
		}
		linkNext, err := nextPageURL(hdr)
		if err != nil {
			return nil, err
		}
		if linkNext == "" {
			return matched, nil
		}
		next = linkNext
	}
	return nil, errors.New("citation gitleaks run list has too many pages")
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

func postScanStatus(ctx context.Context, client *http.Client, cfg Config, owner, repo, sha, branch string, listed workflowRun, baseSHA string, want []byte) error {
	if err := validateCommit(sha); err != nil {
		return err
	}
	// The list result is not enough. Read this run again and refuse to
	// post the ruleset status unless that response is a successful scan
	// of this exact head and base. Then read the branch tip again: the
	// status is posted only when that tip is still the verified citation
	// commit whose CITATION.cff bytes are want.
	confirmed, err := loadScanRun(ctx, client, cfg, owner, repo, listed.ID)
	if err != nil {
		return err
	}
	if err := scanReady(confirmed, sha, branch, baseSHA); err != nil {
		return err
	}
	tip, err := requireCitationTip(ctx, client, cfg, owner, repo, branch, want)
	if err != nil {
		return err
	}
	if tip != sha {
		return fmt.Errorf("refusing to post the gitleaks status: %s moved from %s to %s", branch, sha, tip)
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
		TargetURL:   confirmed.HTMLURL,
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

func loadScanRun(ctx context.Context, client *http.Client, cfg Config, owner, repo string, id int64) (workflowRun, error) {
	if id <= 0 {
		return workflowRun{}, errors.New("citation gitleaks run id is missing")
	}
	raw, err := apiURL(cfg.APIBase, owner, repo, "actions", "runs", strconv.FormatInt(id, 10))
	if err != nil {
		return workflowRun{}, err
	}
	var run workflowRun
	if err := getJSON(ctx, client, cfg.Token, raw, &run); err != nil {
		return workflowRun{}, err
	}
	return run, nil
}

// scanIdentity ties a run to this head, branch, and base. The base comes
// from the run name, because the runs API does not return dispatch inputs.
func scanIdentity(run workflowRun, headSHA, branch, baseSHA string) error {
	if run.ID <= 0 {
		return errors.New("citation gitleaks run id is missing")
	}
	sha := strings.ToLower(strings.TrimSpace(run.HeadSHA))
	if err := validateCommit(sha); err != nil || sha != headSHA {
		return fmt.Errorf("citation gitleaks run %d is for %s, not %s", run.ID, shortAPI(run.HeadSHA), headSHA)
	}
	if run.HeadBranch != branch {
		return fmt.Errorf("citation gitleaks run %d is on %s, not %s", run.ID, shortAPI(run.HeadBranch), branch)
	}
	if run.Event != "workflow_dispatch" {
		return fmt.Errorf("citation gitleaks run %d is a %s event", run.ID, shortAPI(run.Event))
	}
	if run.Name != scanRunName(baseSHA, headSHA) {
		return fmt.Errorf("citation gitleaks run %d is not the scan of %s..%s", run.ID, baseSHA, headSHA)
	}
	return nil
}

// scanReady is the check that must pass before statuses: write is used.
// The ruleset does not pin the check to GitHub Actions, so this is what
// stops the job from posting a success for a run it did not just confirm
// for this head and this base.
func scanReady(run workflowRun, headSHA, branch, baseSHA string) error {
	if err := scanIdentity(run, headSHA, branch, baseSHA); err != nil {
		return err
	}
	if run.Status != "completed" || run.Conclusion != "success" {
		return fmt.Errorf("citation gitleaks run %d is %s/%s", run.ID, shortAPI(run.Status), shortAPI(run.Conclusion))
	}
	if run.Path != citationWorkflowPath {
		return fmt.Errorf("citation gitleaks run %d is not %s", run.ID, citationWorkflowPath)
	}
	if err := validateRunURL(run.HTMLURL); err != nil {
		return err
	}
	return nil
}

func validateRunURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("citation gitleaks run URL is invalid")
	}
	return nil
}

func shortAPI(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// MergeConfig selects the citation pull request Sync may merge.
// ExpectedContent is the CITATION.cff bytes the updater wrote. The merge
// reads the branch tip immediately before the merge call and refuses a tip
// that is not a verified citation commit of those bytes.
type MergeConfig struct {
	Token           string
	Repo            string
	APIBase         string
	BaseBranch      string
	HeadBranch      string
	HeadSHA         string
	ExpectedContent []byte
	Client          *http.Client
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
	tip, err := requireCitationTip(ctx, client, syncCfg, owner, repo, cfg.HeadBranch, cfg.ExpectedContent)
	if err != nil {
		return Result{}, err
	}
	if tip != cfg.HeadSHA {
		return Result{}, fmt.Errorf("refusing to merge: %s moved from %s to %s", cfg.HeadBranch, cfg.HeadSHA, tip)
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

type prFile struct {
	Filename         string `json:"filename"`
	Status           string `json:"status"`
	PreviousFilename string `json:"previous_filename"`
}

func citationPRFiles(ctx context.Context, client *http.Client, cfg Config, owner, repo string, number int) error {
	files, err := listCitationPRFiles(ctx, client, cfg, owner, repo, number)
	if err != nil {
		return err
	}
	if len(files) != 1 || files[0].Filename != citationPath || files[0].Status != "modified" || files[0].PreviousFilename != "" {
		return fmt.Errorf("citation pull request #%d does not change only CITATION.cff", number)
	}
	return nil
}

// requireCitationTip fails closed unless branch's tip is one verified
// github-actions[bot] citation commit and CITATION.cff on that branch is
// want. A tip that moves between the commit read and the second ref read
// is refused. want is the bytes the updater just wrote, or the bytes it
// still expects.
func requireCitationTip(ctx context.Context, client *http.Client, cfg Config, owner, repo, branch string, want []byte) (string, error) {
	if branch == "" || branch == cfg.Branch || !citationHeadRE.MatchString(branch) {
		return "", fmt.Errorf("refusing to trust %s as a citation branch", branch)
	}
	sha, err := branchCommit(ctx, client, cfg, owner, repo, branch)
	if err != nil {
		return "", err
	}
	commit, err := getCitedCommit(ctx, client, cfg, owner, repo, sha)
	if err != nil {
		return "", err
	}
	if err := requireWorkflowCommit(commit); err != nil {
		return "", fmt.Errorf("refusing to continue: %w", err)
	}
	content, _, err := getCitation(ctx, client, cfg, owner, repo, branch)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(content, want) {
		return "", fmt.Errorf("refusing to continue: CITATION.cff on %s does not match the citation update", branch)
	}
	again, err := branchCommit(ctx, client, cfg, owner, repo, branch)
	if err != nil {
		return "", err
	}
	if again != sha {
		return "", fmt.Errorf("refusing to continue: %s moved from %s to %s", branch, sha, again)
	}
	return sha, nil
}

func getCitedCommit(ctx context.Context, client *http.Client, cfg Config, owner, repo, sha string) (citedCommit, error) {
	raw, err := apiURL(cfg.APIBase, owner, repo, "commits", sha)
	if err != nil {
		return citedCommit{}, err
	}
	var commit citedCommit
	if err := getJSON(ctx, client, cfg.Token, raw, &commit); err != nil {
		return citedCommit{}, err
	}
	if commit.SHA == "" {
		commit.SHA = sha
	}
	return commit, nil
}

// listCitationPRFiles follows Link rel=next on the same API host. A
// malformed link, a link to another host, a repeated page, or a page past
// the cap fails closed so a later page cannot hide a second file.
func listCitationPRFiles(ctx context.Context, client *http.Client, cfg Config, owner, repo string, number int) ([]prFile, error) {
	raw, err := apiURL(cfg.APIBase, owner, repo, "pulls", fmt.Sprint(number), "files")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("per_page", "100")
	u.RawQuery = q.Encode()
	next := u.String()
	seen := map[string]struct{}{}
	var all []prFile
	for page := 1; page <= maxCitationFilePages; page++ {
		if _, ok := seen[next]; ok {
			return nil, fmt.Errorf("pull request #%d files pagination repeated a page", number)
		}
		seen[next] = struct{}{}
		if err := sameAPIHost(cfg.APIBase, next); err != nil {
			return nil, err
		}
		status, data, hdr, err := doJSONHeader(ctx, client, http.MethodGet, next, cfg.Token, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("list pull request files: %s", apiMessage(status, data))
		}
		var files []prFile
		if err := json.Unmarshal(data, &files); err != nil {
			return nil, err
		}
		all = append(all, files...)
		linkNext, err := nextPageURL(hdr)
		if err != nil {
			return nil, err
		}
		if linkNext == "" {
			return all, nil
		}
		next = linkNext
	}
	return nil, fmt.Errorf("pull request #%d files response has too many pages", number)
}

func sameAPIHost(apiBase, raw string) error {
	base, err := url.Parse(apiBase)
	if err != nil || base.Host == "" || base.Scheme == "" {
		return errors.New("API base URL is invalid")
	}
	next, err := url.Parse(raw)
	if err != nil || next.Host == "" || next.Scheme == "" {
		return errors.New("pagination link is malformed")
	}
	if next.User != nil {
		return errors.New("refusing a pagination link with userinfo")
	}
	if !strings.EqualFold(next.Scheme, base.Scheme) || !strings.EqualFold(next.Host, base.Host) {
		return errors.New("refusing a pagination link on another host")
	}
	return nil
}

func nextPageURL(h http.Header) (string, error) {
	if len(h) == 0 {
		return "", nil
	}
	var next string
	saw := false
	for _, value := range h.Values("Link") {
		if strings.TrimSpace(value) == "" {
			continue
		}
		for _, part := range splitLinkParts(value) {
			raw, rel, err := parseLinkPart(part)
			if err != nil {
				return "", err
			}
			if rel != "next" {
				continue
			}
			if saw && raw != next {
				return "", errors.New("pagination link has more than one next URL")
			}
			saw = true
			next = raw
		}
	}
	return next, nil
}

func splitLinkParts(value string) []string {
	var parts []string
	var b strings.Builder
	depth := 0
	for _, r := range value {
		switch r {
		case '<':
			depth++
			b.WriteRune(r)
		case '>':
			if depth > 0 {
				depth--
			}
			b.WriteRune(r)
		case ',':
			if depth == 0 {
				parts = append(parts, b.String())
				b.Reset()
				continue
			}
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		parts = append(parts, b.String())
	}
	return parts
}

func parseLinkPart(part string) (string, string, error) {
	part = strings.TrimSpace(part)
	if part == "" {
		return "", "", errors.New("pagination link is malformed")
	}
	if !strings.HasPrefix(part, "<") {
		return "", "", errors.New("pagination link is malformed")
	}
	end := strings.IndexByte(part, '>')
	if end < 2 {
		return "", "", errors.New("pagination link is malformed")
	}
	raw := part[1:end]
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return "", "", errors.New("pagination link is malformed")
	}
	rel := ""
	for _, param := range strings.Split(part[end+1:], ";") {
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		key, val, ok := strings.Cut(param, "=")
		if !ok {
			return "", "", errors.New("pagination link is malformed")
		}
		if !strings.EqualFold(strings.TrimSpace(key), "rel") {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"`)
		if rel != "" && rel != val {
			return "", "", errors.New("pagination link is malformed")
		}
		rel = val
	}
	return raw, rel, nil
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
