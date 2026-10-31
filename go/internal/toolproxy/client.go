package toolproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to the existing HTTP Tool Proxy: POST /attempts/{id}/actions.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	Token      string // optional Bearer token (principal)
}

// NewClient builds an HTTP Tool Proxy client. httpClient may be nil.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTPClient: httpClient}
}

type httpPayload struct {
	Actor    string         `json:"actor,omitempty"`
	Tool     string         `json:"tool"`
	Action   string         `json:"action"`
	Resource string         `json:"resource"`
	Context  map[string]any `json:"context"`
}

type httpResponse struct {
	AttemptID string `json:"attempt_id"`
	ActionID  string `json:"action_id"`
	Actor     string `json:"actor"`
	Principal string `json:"principal"`
	Status    string `json:"status"`
	Effect    string `json:"effect"`
	Reason    string `json:"reason"`
	Value     any    `json:"value"`
	Error     string `json:"error"`
}

// Execute posts one structured action to the Python/API Tool Proxy endpoint.
func (c *Client) Execute(ctx context.Context, attemptID string, req ActionRequest, claimedActor string) (Result, error) {
	body := httpPayload{
		Actor:    claimedActor,
		Tool:     req.Tool,
		Action:   req.Action,
		Resource: req.Resource,
		Context:  req.Context,
	}
	if body.Context == nil {
		body.Context = map[string]any{}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}
	url := fmt.Sprintf("%s/attempts/%s/actions", c.BaseURL, attemptID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusForbidden {
		var detail struct {
			Detail struct {
				Error  string `json:"error"`
				Reason string `json:"reason"`
				Actor  string `json:"actor"`
			} `json:"detail"`
		}
		_ = json.Unmarshal(data, &detail)
		pol := PolicyActorBinding
		if detail.Detail.Error == "attempt_not_owned" {
			pol = PolicyAttemptBinding
		}
		return Result{
			AttemptID: attemptID,
			Status:    StatusDenied,
			Effect:    "deny",
			Reason:    detail.Detail.Reason,
			Policy:    pol,
			Actor:     detail.Detail.Actor,
		}, ErrDenied
	}
	if resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("toolproxy: HTTP %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	var hr httpResponse
	if err := json.Unmarshal(data, &hr); err != nil {
		return Result{}, err
	}
	res := Result{
		AttemptID: hr.AttemptID,
		ActionID:  hr.ActionID,
		Actor:     hr.Actor,
		Status:    hr.Status,
		Effect:    hr.Effect,
		Reason:    hr.Reason,
		Value:     hr.Value,
		Error:     hr.Error,
	}
	switch hr.Status {
	case StatusDenied:
		return res, ErrDenied
	case StatusFailed:
		return res, ErrFailed
	default:
		return res, nil
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
