package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	githubAPIVersion = "2022-11-28"
	userAgent        = "sdf-citation-update"
	contentAttempts  = 3
	maxBody          = 1 << 20
	citationPath     = "CITATION.cff"
)

// Result is what Sync or Merge did with one citation update.
type Result struct {
	Status      Status
	Version     string
	Date        string
	Commit      string
	TagObject   string
	Reason      string
	HeadBranch  string
	HeadSHA     string
	PullRequest int
}

func (r Result) String() string {
	var b strings.Builder
	b.WriteString("status=")
	b.WriteString(string(r.Status))
	if r.Version != "" {
		b.WriteString(" version=")
		b.WriteString(r.Version)
	}
	if r.Date != "" {
		b.WriteString(" date-released=")
		b.WriteString(r.Date)
	}
	if r.Commit != "" {
		b.WriteString(" commit=")
		b.WriteString(r.Commit)
	}
	if r.TagObject != "" {
		b.WriteString(" tag-object=")
		b.WriteString(r.TagObject)
	}
	if r.HeadBranch != "" {
		b.WriteString(" head=")
		b.WriteString(r.HeadBranch)
	}
	if r.HeadSHA != "" {
		b.WriteString(" head-sha=")
		b.WriteString(r.HeadSHA)
	}
	if r.PullRequest != 0 {
		fmt.Fprintf(&b, " pr=%d", r.PullRequest)
	}
	if r.Reason != "" {
		b.WriteString(" reason=")
		b.WriteString(r.Reason)
	}
	return b.String()
}

// Config is the published release to apply and the repository to write.
type Config struct {
	Token        string
	Repo         string
	APIBase      string
	Tag          string
	PublishedAt  string
	Branch       string
	Draft        bool
	Prerelease   bool
	Client       *http.Client
	PollInterval time.Duration
	PollAttempts int
}

// Sync reads the release tag, peels it to a commit, and opens or updates a
// pull request that changes CITATION.cff. The pull request branch is
// citation/<tag>, cut from Branch (the default branch). The contents API is
// never pointed at Branch. A matching file does not open a pull request. The
// release tag is not updated. There is no force flag.
func Sync(ctx context.Context, cfg Config) (Result, error) {
	if cfg.Draft {
		return Result{Status: StatusSkipped, Reason: "draft release"}, nil
	}
	if cfg.Prerelease {
		return Result{Status: StatusSkipped, Reason: "prerelease"}, nil
	}
	if cfg.Tag == "" {
		return Result{}, errors.New("release tag is empty")
	}
	version, ok := stableVersion(cfg.Tag)
	if !ok {
		return Result{
			Status: StatusSkipped,
			Reason: fmt.Sprintf("tag %s is not vMAJOR.MINOR.PATCH", safeTag(cfg.Tag)),
		}, nil
	}
	date, err := releaseDate(cfg.PublishedAt)
	if err != nil {
		return Result{}, err
	}
	if err := validateConfig(cfg); err != nil {
		return Result{}, err
	}
	client := cfg.Client
	if client == nil {
		client = githubClient()
	}
	owner, repo, _ := strings.Cut(cfg.Repo, "/")
	commit, tagObject, err := peelTag(ctx, client, cfg, owner, repo)
	if err != nil {
		return Result{}, err
	}
	if tagObject != "" && commit == tagObject {
		return Result{}, fmt.Errorf("refusing to record annotated tag object %s as the commit", commit)
	}
	file, _, err := getCitation(ctx, client, cfg, owner, repo, cfg.Branch)
	if err != nil {
		return Result{}, err
	}
	updated, err := Update(file, version, date, commit)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Status:    updated.Status,
		Version:   version,
		Date:      date,
		Commit:    commit,
		TagObject: tagObject,
		Reason:    updated.Reason,
	}
	if updated.Status != StatusUpdated {
		return result, nil
	}
	head, err := citationHead(cfg.Tag)
	if err != nil {
		return Result{}, err
	}
	result.HeadBranch = head
	if err := ensureCitationBranch(ctx, client, cfg, owner, repo, head); err != nil {
		return Result{}, err
	}
	message := commitMessage(version, cfg.Tag, date, commit)
	committed := false
	for attempt := 1; attempt <= contentAttempts; attempt++ {
		branchFile, blob, err := getCitation(ctx, client, cfg, owner, repo, head)
		if err != nil {
			return Result{}, err
		}
		if bytes.Equal(branchFile, updated.Content) {
			committed = true
			break
		}
		err = putCitation(ctx, client, cfg, owner, repo, head, updated.Content, blob, message)
		if err == nil {
			committed = true
			break
		}
		var conflict *conflictError
		if !errors.As(err, &conflict) || attempt == contentAttempts {
			return Result{}, err
		}
	}
	if !committed {
		return Result{}, errors.New("CITATION.cff update did not finish")
	}
	pr, err := ensureCitationPR(ctx, client, cfg, owner, repo, head, result)
	if err != nil {
		return Result{}, err
	}
	pr, err = getPull(ctx, client, cfg, owner, repo, pr.Number)
	if err != nil {
		return Result{}, err
	}
	headSHA, err := branchCommit(ctx, client, cfg, owner, repo, head)
	if err != nil {
		return Result{}, err
	}
	if pr.Head.SHA != headSHA {
		return Result{}, fmt.Errorf("citation pull request head is %s, not %s", pr.Head.SHA, headSHA)
	}
	if pr.Base.Ref != cfg.Branch || pr.Base.SHA == "" {
		return Result{}, fmt.Errorf("citation pull request base is %s@%s", pr.Base.Ref, pr.Base.SHA)
	}
	run, err := ensureScan(ctx, client, cfg, owner, repo, head, headSHA, pr.Base.SHA)
	if err != nil {
		return Result{}, err
	}
	if err := postScanStatus(ctx, client, cfg, owner, repo, headSHA, run.HTMLURL); err != nil {
		return Result{}, err
	}
	merged, err := Merge(ctx, MergeConfig{
		Token:      cfg.Token,
		Repo:       cfg.Repo,
		APIBase:    cfg.APIBase,
		BaseBranch: cfg.Branch,
		HeadBranch: head,
		HeadSHA:    headSHA,
		Client:     client,
	})
	if err != nil {
		return Result{}, err
	}
	if merged.Status != StatusMerged {
		return Result{}, fmt.Errorf("citation pull request was not merged: %s", merged.Reason)
	}
	result.Status = StatusMerged
	result.PullRequest = pr.Number
	result.HeadSHA = headSHA
	result.Reason = merged.Reason
	return result, nil
}

func commitMessage(version, tag, date, commit string) string {
	return fmt.Sprintf("docs: update CITATION.cff for %s\n\nSet version %s, date-released %s, and commit %s from published tag %s.\nThe tag is not moved.\n", version, version, date, commit, tag)
}

func safeTag(tag string) string {
	tag = strings.ReplaceAll(tag, "\n", "")
	tag = strings.ReplaceAll(tag, "\r", "")
	if len(tag) > 80 {
		tag = tag[:80]
	}
	return tag
}

func validateConfig(cfg Config) error {
	if cfg.Token == "" || strings.ContainsAny(cfg.Token, " \t\r\n") {
		return errors.New("GITHUB_TOKEN is missing or invalid")
	}
	if !repoRE.MatchString(cfg.Repo) || strings.Contains(cfg.Repo, "..") {
		return fmt.Errorf("repository %q is not owner/name", cfg.Repo)
	}
	if !branchRE.MatchString(cfg.Branch) || strings.Contains(cfg.Branch, "..") {
		return fmt.Errorf("branch %q is not a branch name", cfg.Branch)
	}
	u, err := url.Parse(cfg.APIBase)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return errors.New("API base URL is invalid")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("API base URL must not contain a path")
	}
	if u.Scheme != "https" && cfg.Client == nil {
		return errors.New("API base URL must be https")
	}
	return nil
}

var (
	repoRE   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	branchRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

func githubClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// A redirect would send the token to another host.
			return http.ErrUseLastResponse
		},
	}
}

type gitObj struct {
	Type string
	SHA  string
}

// peel walks an annotated tag until it reaches a commit. tagObject is the
// annotated tag object the ref points at, empty for a lightweight tag.
// The returned commit is never that tag object.
func peel(start gitObj, next func(tagSHA string) (gitObj, error)) (commit, tagObject string, err error) {
	const max = 8
	seen := map[string]struct{}{}
	tagObjects := map[string]struct{}{}
	obj := start
	if strings.EqualFold(start.Type, "tag") {
		tagObject = strings.ToLower(strings.TrimSpace(start.SHA))
	}
	for i := 0; i < max; i++ {
		sha := strings.ToLower(strings.TrimSpace(obj.SHA))
		typ := strings.ToLower(strings.TrimSpace(obj.Type))
		if sha == "" {
			return "", "", errors.New("git object has an empty sha")
		}
		switch typ {
		case "commit":
			if _, ok := tagObjects[sha]; ok {
				return "", "", fmt.Errorf("refusing to record annotated tag object %s as the commit", sha)
			}
			if err := validateCommit(sha); err != nil {
				return "", "", err
			}
			return sha, tagObject, nil
		case "tag":
			if _, ok := seen[sha]; ok {
				return "", "", errors.New("tag peel hit a cycle")
			}
			seen[sha] = struct{}{}
			tagObjects[sha] = struct{}{}
			obj, err = next(sha)
			if err != nil {
				return "", "", err
			}
		default:
			return "", "", fmt.Errorf("tag points at %s %s, not a commit", typ, sha)
		}
	}
	return "", "", errors.New("tag is nested too deeply to peel")
}

func peelTag(ctx context.Context, client *http.Client, cfg Config, owner, repo string) (commit, tagObject string, err error) {
	refURL, err := apiURL(cfg.APIBase, owner, repo, "git", "ref", "tags", cfg.Tag)
	if err != nil {
		return "", "", err
	}
	var ref struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := getJSON(ctx, client, cfg.Token, refURL, &ref); err != nil {
		var missing *notFoundError
		if errors.As(err, &missing) {
			return "", "", fmt.Errorf("tag %s was not found", cfg.Tag)
		}
		return "", "", err
	}
	return peel(gitObj{Type: ref.Object.Type, SHA: ref.Object.SHA}, func(tagSHA string) (gitObj, error) {
		tagURL, err := apiURL(cfg.APIBase, owner, repo, "git", "tags", tagSHA)
		if err != nil {
			return gitObj{}, err
		}
		var annotated struct {
			Object struct {
				Type string `json:"type"`
				SHA  string `json:"sha"`
			} `json:"object"`
		}
		if err := getJSON(ctx, client, cfg.Token, tagURL, &annotated); err != nil {
			return gitObj{}, err
		}
		return gitObj{Type: annotated.Object.Type, SHA: annotated.Object.SHA}, nil
	})
}

func getCitation(ctx context.Context, client *http.Client, cfg Config, owner, repo, ref string) (content []byte, blob string, err error) {
	raw, err := apiURL(cfg.APIBase, owner, repo, "contents", citationPath)
	if err != nil {
		return nil, "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", err
	}
	q := u.Query()
	q.Set("ref", ref)
	u.RawQuery = q.Encode()
	status, data, err := doJSON(ctx, client, http.MethodGet, u.String(), cfg.Token, nil)
	if err != nil {
		return nil, "", err
	}
	if status == http.StatusNotFound {
		return nil, "", fmt.Errorf("CITATION.cff is not on %s; refusing to create it", ref)
	}
	if status != http.StatusOK {
		return nil, "", fmt.Errorf("read CITATION.cff: %s", apiMessage(status, data))
	}
	if len(data) > 0 && data[0] == '[' {
		return nil, "", errors.New("CITATION.cff path is a directory")
	}
	var body struct {
		SHA      string `json:"sha"`
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Type     string `json:"type"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, "", fmt.Errorf("read CITATION.cff: %w", err)
	}
	if body.Type != "" && body.Type != "file" {
		return nil, "", fmt.Errorf("CITATION.cff is a %s, not a file", body.Type)
	}
	if body.Encoding != "" && body.Encoding != "base64" {
		return nil, "", fmt.Errorf("CITATION.cff encoding %q is not base64", body.Encoding)
	}
	blob = strings.ToLower(body.SHA)
	if err := validateCommit(blob); err != nil {
		return nil, "", fmt.Errorf("CITATION.cff blob sha: %w", err)
	}
	content, err = decodeBase64(body.Content)
	if err != nil {
		return nil, "", fmt.Errorf("CITATION.cff content: %w", err)
	}
	return content, blob, nil
}

func putCitation(ctx context.Context, client *http.Client, cfg Config, owner, repo, branch string, content []byte, blob, message string) error {
	if branch == "" || branch == cfg.Branch || !strings.HasPrefix(branch, "citation/") {
		return fmt.Errorf("refusing to write CITATION.cff on %s", branch)
	}
	raw, err := apiURL(cfg.APIBase, owner, repo, "contents", citationPath)
	if err != nil {
		return err
	}
	payload := putBody{
		Message: message,
		Content: base64.StdEncoding.EncodeToString(content),
		SHA:     blob,
		Branch:  branch,
	}
	status, data, err := doJSON(ctx, client, http.MethodPut, raw, cfg.Token, payload)
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		return &conflictError{msg: apiMessage(status, data)}
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("write CITATION.cff on %s: %s", branch, apiMessage(status, data))
	}
	return nil
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		default:
			return r
		}
	}, s)
	return base64.StdEncoding.DecodeString(s)
}

func apiURL(base, owner, repo string, elems ...string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, 2+len(elems))
	parts = append(parts, "repos", owner, repo)
	parts = append(parts, elems...)
	joined := u.JoinPath(parts...)
	return joined.String(), nil
}

func getJSON(ctx context.Context, client *http.Client, token, rawURL string, dest any) error {
	status, data, err := doJSON(ctx, client, http.MethodGet, rawURL, token, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return &notFoundError{msg: apiMessage(status, data)}
	}
	if status != http.StatusOK {
		return errors.New(apiMessage(status, data))
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return err
	}
	return nil
}

func doJSON(ctx context.Context, client *http.Client, method, rawURL, token string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(body); err != nil {
			return 0, nil, err
		}
		r = &buf
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp.StatusCode, nil, fmt.Errorf("refusing redirect %d from the GitHub API", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

func apiMessage(status int, data []byte) string {
	var parsed struct {
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(data, &parsed) == nil && parsed.Message != "" {
		msg = parsed.Message
	} else {
		msg = strings.TrimSpace(string(data))
	}
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	if len(msg) > 240 {
		msg = msg[:240]
	}
	if msg == "" {
		return fmt.Sprintf("github api status %d", status)
	}
	return fmt.Sprintf("github api status %d: %s", status, msg)
}

// putBody is the contents-API update. It has no force field: GitHub
// rejects the write when sha is not the current blob, which is a
// fast-forward conflict rather than a history rewrite.
type putBody struct {
	Message string `json:"message"`
	Content string `json:"content"`
	SHA     string `json:"sha"`
	Branch  string `json:"branch"`
}

type conflictError struct{ msg string }

func (e *conflictError) Error() string { return e.msg }

type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }
