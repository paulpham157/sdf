package setup

import (
	"fmt"
	"net/http"
	"time"

	"github.com/paulpham157/sdf/internal/httphost"
)

// AuthCheck is the result of ProbeAuth.
type AuthCheck struct {
	// Text is one line for sdf init --check, or "" when the receiver did
	// not answer (Probe already reports that).
	Text string
	// Failed is true for any answer other than 415: a 401 means OTLP
	// writes and the sdf hooks would be refused with the token given, and
	// any other status (a redirect, a 404 or 5xx from something that is not
	// sdf serve) means the check could not confirm writes would be stored.
	Failed bool
}

// ProbeAuth asks whether an OTLP write to ep would pass the receiver's
// bearer-token check with token ("" sends none). It posts an empty body
// with no Content-Type to /v1/logs. The receiver checks the token before
// the media type, so a write that passes gets 415 and nothing is stored.
// source names where token came from, for the message. The token is sent
// only to a loopback host, as the sdf hooks do. Redirects are not followed:
// OTLP exporters post to this URL, so a 3xx is reported as the answer.
func ProbeAuth(ep Endpoint, token, source string) AuthCheck {
	if ep.Host == "" {
		ep.Host = "127.0.0.1"
	}
	if ep.HTTPPort == 0 {
		ep.HTTPPort = 4318
	}
	url := ep.base() + "/v1/logs"
	// Like the hooks, never send the token anywhere but loopback: it would
	// cross the network in clear text.
	if probeToken(ep.Host, token) == "" {
		token, source = "", ""
	}
	req, err := http.NewRequest(http.MethodPost, url, http.NoBody)
	if err != nil {
		return AuthCheck{}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{
		Timeout: 2 * time.Second,
		// Never follow a redirect: the token would go to wherever it
		// points, and the answer would not be this receiver's.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return AuthCheck{}
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized && token == "":
		if !httphost.Loopback(ep.Host) {
			return AuthCheck{Failed: true, Text: fmt.Sprintf("%s: token required (HTTP 401). %s is not loopback, so the sdf hooks and this check never send the token there.\n", url, ep.Host)}
		}
		return AuthCheck{Failed: true, Text: fmt.Sprintf("%s: token required (HTTP 401). This receiver was started with a bearer token and none is set here, so agent exporters and the sdf hooks would get 401. Set SDF_OTLP_TOKEN or SDF_OTLP_TOKEN_FILE where the agent and git run, and send the header from the agent (docs/agents.md).\n", url)}
	case resp.StatusCode == http.StatusUnauthorized:
		return AuthCheck{Failed: true, Text: fmt.Sprintf("%s: token refused (HTTP 401). The token from %s does not match the one sdf serve was started with, so the sdf hooks would get 401.\n", url, source)}
	case resp.StatusCode == http.StatusUnsupportedMediaType && token == "":
		return AuthCheck{Text: fmt.Sprintf("%s: writes accepted without a token\n", url)}
	case resp.StatusCode == http.StatusUnsupportedMediaType:
		return AuthCheck{Text: fmt.Sprintf("%s: writes accepted with the token from %s\n", url, source)}
	default:
		return AuthCheck{Failed: true, Text: fmt.Sprintf("%s: unexpected answer to the token check (HTTP %d); expected 415 from sdf serve, so OTLP writes may not be stored. Check that --host and --port point at sdf serve.\n", url, resp.StatusCode)}
	}
}

// probeToken is token when host is loopback, and "" otherwise.
func probeToken(host, token string) string {
	if !httphost.Loopback(host) {
		return ""
	}
	return token
}
