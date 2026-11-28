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

// copilotModelAlias is the model id Copilot sends when --model is
// tokenrouter/recall. The proxy rewrites it to the configured wire model.
const copilotModelAlias = "tokenrouter/recall"

// proxy is a localhost forwarder. Agent CLIs talk to it. It adds the API
// key on the upstream request, retries 429 and 5xx, and stops a run that
// crosses the turn or token cap. It does not log headers or bodies.
type proxy struct {
	openai    string
	anthropic string
	key       string
	model     string
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
		model:     opt.Model,
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
	if r.Method == http.MethodGet && isModelsPath(r.URL.Path) {
		writeModels(w, p.model)
		return
	}
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
	body = rewriteModel(body, p.model)
	stream := wantsStream(r.Header, body)
	p.mu.Lock()
	p.turns++
	over := (p.maxTurns > 0 && p.turns > p.maxTurns) || (p.maxTokens > 0 && p.tokens >= p.maxTokens)
	if over {
		p.capped = true
	}
	p.mu.Unlock()
	if over {
		writeStop(w, kind, stream)
		return
	}
	resp, err := p.forward(r.Context(), upstream, r.Header, body)
	if err != nil {
		http.Error(w, "upstream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	p.writeUpstream(w, resp)
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

func (p *proxy) forward(ctx context.Context, rawURL string, hdr http.Header, body []byte) (*http.Response, error) {
	retries := p.retries
	if retries < 0 {
		retries = 0
	}
	var lastErr error
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
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		reqCtx, cancel := context.WithCancel(ctx)
		// The deadline covers waiting for headers. A stream keeps reading
		// until the client goes away or the run is canceled.
		timer := time.AfterFunc(p.timeout, func() { cancel() })
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, rawURL, bytes.NewReader(body))
		if err != nil {
			timer.Stop()
			cancel()
			return nil, err
		}
		copySafeHeaders(req.Header, hdr)
		if p.key != "" {
			req.Header.Set("Authorization", "Bearer "+p.key)
			if strings.HasSuffix(rawURL, "/messages") {
				req.Header.Set("x-api-key", p.key)
			}
		}
		resp, err := p.client.Do(req)
		timer.Stop()
		if err != nil {
			cancel()
			lastErr = err
			lastRetry = 0
			continue
		}
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < retries {
			lastRetry = retryAfter(resp.Header)
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			cancel()
			continue
		}
		resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("upstream")
	}
	return nil, lastErr
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cancel)
	return err
}

func (p *proxy) writeUpstream(w http.ResponseWriter, resp *http.Response) {
	ct := resp.Header.Get("Content-Type")
	stream := strings.Contains(strings.ToLower(ct), "text/event-stream")
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	if stream {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(resp.StatusCode)
	if !stream {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
		p.addTokens(usageTokens(buf))
		_, _ = w.Write(buf)
		return
	}
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	var line []byte
	maxUsage := 0
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			_, _ = w.Write(chunk)
			if fl != nil {
				fl.Flush()
			}
			line = append(line, chunk...)
			for {
				i := bytes.IndexByte(line, '\n')
				if i < 0 {
					break
				}
				one := line[:i]
				line = append([]byte{}, line[i+1:]...)
				if t := usageTokens(one); t > maxUsage {
					maxUsage = t
				}
			}
			if len(line) > 1<<20 {
				line = append([]byte{}, line[len(line)-4096:]...)
			}
		}
		if err != nil {
			break
		}
	}
	if t := usageTokens(line); t > maxUsage {
		maxUsage = t
	}
	p.addTokens(maxUsage)
}

func (p *proxy) addTokens(n int) {
	if n <= 0 {
		return
	}
	p.mu.Lock()
	p.tokens += n
	if p.maxTokens > 0 && p.tokens >= p.maxTokens {
		p.capped = true
	}
	p.mu.Unlock()
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

func isModelsPath(path string) bool {
	path = strings.TrimSuffix(path, "/")
	return path == "/v1/models" || path == "/models"
}

func wantsStream(h http.Header, body []byte) bool {
	if strings.Contains(strings.ToLower(h.Get("Accept")), "text/event-stream") {
		return true
	}
	var m struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &m) == nil && m.Stream
}

func rewriteModel(body []byte, model string) []byte {
	if model == "" || model == copilotModelAlias || !bytes.Contains(body, []byte(copilotModelAlias)) {
		return body
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	raw, ok := m["model"]
	if !ok {
		return body
	}
	var id string
	if json.Unmarshal(raw, &id) != nil || id != copilotModelAlias {
		return body
	}
	repl, err := json.Marshal(model)
	if err != nil {
		return body
	}
	m["model"] = repl
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

func writeModels(w http.ResponseWriter, model string) {
	var ids []string
	if model != "" {
		ids = append(ids, model)
	}
	if model != copilotModelAlias {
		ids = append(ids, copilotModelAlias)
	}
	data := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]string{
			"id":       id,
			"object":   "model",
			"owned_by": "tokenrouter",
		})
	}
	b, err := json.Marshal(map[string]any{"object": "list", "data": data})
	if err != nil {
		http.Error(w, "models", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func writeStop(w http.ResponseWriter, kind string, stream bool) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(stopJSON(kind))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(stopSSE(kind))
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}
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

func stopSSE(kind string) []byte {
	switch kind {
	case "responses":
		return []byte("event: response.completed\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_cap\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"turn cap reached\"}]}],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n")
	case "messages":
		return []byte("event: message_start\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_cap\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"cap\",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"turn cap reached\"}}\n\n" +
			"event: content_block_stop\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":0}}\n\n" +
			"event: message_stop\n" +
			"data: {\"type\":\"message_stop\"}\n\n")
	default:
		return []byte("data: {\"id\":\"chatcmpl-cap\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"turn cap reached\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl-cap\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n")
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
