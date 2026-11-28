package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// proxy is a localhost forwarder. Agent CLIs talk to it. It adds the API
// key on the upstream request, retries 429 and 5xx, and stops a run that
// crosses the turn or token cap. It does not log headers or bodies.
type proxy struct {
	openai    string
	anthropic string
	key       string
	maxTurns  int
	maxTokens int
	retries   int
	timeout   time.Duration
	backoff   func(int) time.Duration
	client    *http.Client

	mu     sync.Mutex
	turns  int
	tokens int
	capped bool
}

func newProxy(opt Options) *proxy {
	opt = opt.withDefaults()
	c := opt.HTTPClient
	if c == nil {
		c = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &proxy{
		openai:    opt.OpenAIBase,
		anthropic: opt.AnthropicBase,
		key:       opt.Key,
		maxTurns:  opt.MaxTurns,
		maxTokens: opt.MaxTokens,
		retries:   opt.Retries,
		timeout:   opt.HTTPTimeout,
		backoff:   opt.Backoff,
		client:    c,
	}
}

func (p *proxy) reset() {
	p.mu.Lock()
	p.turns = 0
	p.tokens = 0
	p.capped = false
	p.mu.Unlock()
}

func (p *proxy) cappedRun() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.capped
}

func (p *proxy) start() (openaiBase, anthropicBase string, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", nil, err
	}
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	host := "http://" + ln.Addr().String()
	stop = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	return host + "/v1", host, stop, nil
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	upstream, kind, ok := p.route(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20+1))
	if err != nil || len(body) > 8<<20 {
		http.Error(w, "body", http.StatusRequestEntityTooLarge)
		return
	}
	p.mu.Lock()
	p.turns++
	over := (p.maxTurns > 0 && p.turns > p.maxTurns) || (p.maxTokens > 0 && p.tokens >= p.maxTokens)
	if over {
		p.capped = true
	}
	p.mu.Unlock()
	if over {
		writeStop(w, kind)
		return
	}
	status, respBody, err := p.forward(r.Context(), upstream, r.Header, body)
	if err != nil {
		http.Error(w, "upstream", http.StatusBadGateway)
		return
	}
	n := usageTokens(respBody)
	if n > 0 {
		p.mu.Lock()
		p.tokens += n
		if p.maxTokens > 0 && p.tokens >= p.maxTokens {
			p.capped = true
		}
		p.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}

func (p *proxy) route(path string) (upstream, kind string, ok bool) {
	switch path {
	case "/v1/chat/completions":
		return joinURL(p.openai, "chat/completions"), "chat", true
	case "/v1/responses":
		return joinURL(p.openai, "responses"), "responses", true
	case "/v1/messages":
		return joinURL(p.anthropic, "v1/messages"), "messages", true
	default:
		return "", "", false
	}
}

func (p *proxy) forward(ctx context.Context, rawURL string, hdr http.Header, body []byte) (int, []byte, error) {
	retries := p.retries
	if retries < 0 {
		retries = 0
	}
	var lastStatus int
	var lastBody []byte
	var lastRetry time.Duration
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			wait := p.backoff(attempt - 1)
			if lastRetry > wait {
				wait = lastRetry
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return 0, nil, ctx.Err()
			case <-timer.C:
			}
		}
		reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, rawURL, bytes.NewReader(body))
		if err != nil {
			cancel()
			return 0, nil, err
		}
		copySafeHeaders(req.Header, hdr)
		if p.key != "" {
			req.Header.Set("Authorization", "Bearer "+p.key)
			if strings.HasSuffix(rawURL, "/messages") {
				req.Header.Set("x-api-key", p.key)
			}
		}
		resp, err := p.client.Do(req)
		if err != nil {
			cancel()
			lastRetry = 0
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
		resp.Body.Close()
		cancel()
		lastStatus = resp.StatusCode
		lastBody = respBody
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastRetry = retryAfter(resp.Header)
			continue
		}
		return resp.StatusCode, respBody, nil
	}
	if lastStatus != 0 {
		return lastStatus, lastBody, nil
	}
	return 0, nil, errors.New("upstream")
}

func copySafeHeaders(dst, src http.Header) {
	for _, k := range []string{"Content-Type", "Accept", "Anthropic-Version"} {
		if v := src.Get(k); v != "" {
			dst.Set(k, v)
		}
	}
}

func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	sec, err := strconv.Atoi(v)
	if err != nil || sec < 0 || sec > 30 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func writeStop(w http.ResponseWriter, kind string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(stopJSON(kind))
}

func stopJSON(kind string) []byte {
	switch kind {
	case "responses":
		return []byte(`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"turn cap reached"}]}],"usage":{"total_tokens":0}}`)
	case "messages":
		return []byte(`{"content":[{"type":"text","text":"turn cap reached"}],"stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":0}}`)
	default:
		return []byte(`{"choices":[{"message":{"role":"assistant","content":"turn cap reached"},"finish_reason":"stop"}],"usage":{"total_tokens":0}}`)
	}
}

func usageTokens(body []byte) int {
	trim := bytes.TrimSpace(body)
	if bytes.Contains(trim, []byte("data:")) && !bytes.HasPrefix(trim, []byte("{")) {
		n := 0
		for _, line := range bytes.Split(body, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
				continue
			}
			if t := usageObject(line); t > n {
				n = t
			}
		}
		return n
	}
	return usageObject(body)
}

func usageObject(body []byte) int {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return 0
	}
	raw, ok := m["usage"]
	if !ok {
		return 0
	}
	var u map[string]any
	if json.Unmarshal(raw, &u) != nil {
		return 0
	}
	if n, ok := asInt(u["total_tokens"]); ok && n > 0 {
		return n
	}
	in, _ := asInt(u["input_tokens"])
	if in == 0 {
		in, _ = asInt(u["prompt_tokens"])
	}
	out, _ := asInt(u["output_tokens"])
	if out == 0 {
		out, _ = asInt(u["completion_tokens"])
	}
	if in+out < 0 {
		return 0
	}
	return in + out
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
}
