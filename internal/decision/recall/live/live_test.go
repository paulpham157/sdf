package live

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/decision/recall"
)

const testKey = "test-recall-key-value"

func TestMain(m *testing.M) {
	if os.Getenv("SDF_RECALL_STUB") == "1" {
		os.Exit(stubMain())
	}
	os.Exit(m.Run())
}

func TestSkipWithoutKey(t *testing.T) {
	rep, err := Run(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Skipped || rep.K != defaultK || rep.Model != defaultModel {
		t.Fatalf("%+v", rep)
	}
	if rep.OpenAIBase != defaultOpenAI || rep.AnthropicBase != defaultAnthropic {
		t.Fatalf("bases %s %s", rep.OpenAIBase, rep.AnthropicBase)
	}
	if !strings.Contains(rep.Markdown(), "Skipped") {
		t.Fatal(rep.Markdown())
	}
	body, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, testKey) || !strings.Contains(body, `"skipped": true`) {
		t.Fatal(body)
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("SDF_RECALL_API_KEY", "")
	t.Setenv("TOKENROUTER_API_KEY", testKey)
	t.Setenv("SDF_RECALL_OPENAI_BASE_URL", "")
	t.Setenv("SDF_RECALL_BASE_URL", "https://example.test/v1")
	t.Setenv("SDF_RECALL_ANTHROPIC_BASE_URL", "https://example.test")
	t.Setenv("SDF_RECALL_MODEL", "model-under-test")
	t.Setenv("SDF_RECALL_K", "2")
	t.Setenv("SDF_RECALL_MAX_TURNS", "")
	t.Setenv("SDF_RECALL_MAX_TOKENS", "")
	t.Setenv("SDF_RECALL_RUN_TIMEOUT", "")
	opt := ConfigFromEnv().withDefaults()
	if opt.Key != testKey || opt.OpenAIBase != "https://example.test/v1" || opt.AnthropicBase != "https://example.test" {
		t.Fatalf("%+v", opt.OpenAIBase)
	}
	if opt.Model != "model-under-test" || opt.K != 2 {
		t.Fatalf("model %s k %d", opt.Model, opt.K)
	}
	if opt.MaxTurns != defaultMaxTurns || opt.MaxTokens != defaultMaxTokens || opt.RunTimeout != defaultRunTimeout {
		t.Fatalf("caps turns %d tokens %d timeout %s", opt.MaxTurns, opt.MaxTokens, opt.RunTimeout)
	}
	if defaultMaxTurns < 30 || defaultMaxTokens < 1_000_000 || defaultRunTimeout < 5*time.Minute {
		t.Fatalf("defaults too small")
	}
	t.Setenv("SDF_RECALL_API_KEY", "primary-key")
	t.Setenv("SDF_RECALL_OPENAI_BASE_URL", "https://openai.test/v1")
	opt = ConfigFromEnv()
	if opt.Key != "primary-key" || opt.OpenAIBase != "https://openai.test/v1" {
		t.Fatalf("override key or base")
	}
}

func TestParseArgs(t *testing.T) {
	t.Setenv("SDF_RECALL_NO_INSTALL", "")
	opt, mode, err := ParseArgs([]string{"--agents", "opencode,claude-code", "--k", "3", "--json"}, Options{Install: true})
	if err != nil {
		t.Fatal(err)
	}
	if mode != ModeJSON || opt.K != 3 || len(opt.Agents) != 2 || !opt.Install {
		t.Fatalf("%+v %v", opt, mode)
	}
	if _, _, err := ParseArgs([]string{"--agents", "nope"}, Options{}); err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatal(err)
	}
	if _, _, err := ParseArgs([]string{"--k", "0"}, Options{}); err == nil {
		t.Fatal("k 0")
	}
	if _, _, err := ParseArgs([]string{"--k", "101"}, Options{}); err == nil {
		t.Fatal("k 101")
	}
	opt, mode, err = ParseArgs([]string{"--no-install"}, Options{Install: true})
	if err != nil || opt.Install || mode != ModeBoth {
		t.Fatal(err, opt.Install, mode)
	}
	opt, _, err = ParseArgs([]string{"--scenarios", "decide-commit,approve", "--parallel", "4", "--out", "logs"}, Options{})
	if err != nil || len(opt.FixtureNames) != 2 || opt.Parallel != 4 || opt.OutDir != "logs" {
		t.Fatal(err, opt.FixtureNames, opt.Parallel, opt.OutDir)
	}
	if _, _, err := ParseArgs([]string{"--scenarios", "nope"}, Options{}); err == nil {
		t.Fatal("unknown scenario")
	}
	if _, _, err := ParseArgs([]string{"--parallel", "0"}, Options{}); err == nil {
		t.Fatal("parallel 0")
	}
	if _, _, err := ParseArgs([]string{"--merge", "a.json", "--agents", "opencode"}, Options{}); err == nil {
		t.Fatal("merge with agents")
	}
	opt, _, err = ParseArgs(nil, Options{})
	if err != nil || opt.Install {
		t.Fatal("install defaults on", err, opt.Install)
	}
	opt, _, err = ParseArgs([]string{"--install"}, Options{})
	if err != nil || !opt.Install {
		t.Fatal("install flag", err, opt.Install)
	}
}

func TestGeminiIsNotExecuted(t *testing.T) {
	called := false
	rep, err := Run(context.Background(), Options{
		Agents: []string{"gemini"},
		Key:    testKey,
		K:      1,
		LookPath: func(string) (string, error) {
			called = true
			return "", errors.New("called")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("looked up a binary")
	}
	if len(rep.Agents) != 1 || rep.Agents[0].Status != statusUnsupported {
		t.Fatalf("%+v", rep.Agents)
	}
	if !strings.Contains(rep.Agents[0].Reason, "generateContent") || !strings.Contains(rep.Agents[0].Reason, "404") {
		t.Fatal(rep.Agents[0].Reason)
	}
	text := rep.Markdown()
	body, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text+body, testKey) {
		t.Fatal("key in report")
	}
	if !strings.Contains(text, "unsupported") || !strings.Contains(body, `"status": "unsupported"`) {
		t.Fatal(text)
	}
}

func TestTranscriptOmitsLabels(t *testing.T) {
	fixtures, err := recall.Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 7 {
		t.Fatalf("fixtures %d", len(fixtures))
	}
	for _, fx := range fixtures {
		prompt, err := transcript(fx)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(prompt, fx.Notes) {
			t.Fatalf("%s prompt includes notes", fx.Name)
		}
		for _, d := range fx.Labels.Decisions {
			if strings.Contains(prompt, d.ID) {
				t.Fatalf("%s prompt includes label id %s", fx.Name, d.ID)
			}
		}
		if !strings.Contains(prompt, "SKILL.md") {
			t.Fatalf("%s prompt missing skill", fx.Name)
		}
	}
}

func TestAdapterFilesOmitKey(t *testing.T) {
	skill, err := findSkill("")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range registry() {
		if !a.Support().OK {
			if _, err := a.Prepare(context.Background(), spec{}); err == nil {
				t.Fatalf("%s prepare succeeded", a.ID())
			}
			continue
		}
		root := t.TempDir()
		repo := filepath.Join(root, "repo")
		home := filepath.Join(root, "home")
		if err := os.MkdirAll(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		sp := spec{
			Repo:          repo,
			Home:          home,
			OpenAIBase:    "http://127.0.0.1:9/v1",
			AnthropicBase: "http://127.0.0.1:9",
			Model:         defaultModel,
			OTLP:          "http://127.0.0.1:8",
			SkillSrc:      skill,
			Prompt:        "record the decision",
		}
		inv, err := a.Prepare(context.Background(), sp)
		if err != nil {
			t.Fatalf("%s: %v", a.ID(), err)
		}
		if len(inv.Argv) < 2 || inv.Argv[len(inv.Argv)-1] != sp.Prompt {
			t.Fatalf("%s argv %q", a.ID(), inv.Argv)
		}
		if _, err := os.Stat(filepath.Join(repo, "skills", "sdf-decisions", "SKILL.md")); err != nil {
			t.Fatalf("%s skill: %v", a.ID(), err)
		}
		walkNoKey(t, root)
		for _, v := range inv.Env {
			if strings.Contains(v, testKey) {
				t.Fatalf("%s env contains the key", a.ID())
			}
		}
		blob := strings.Join(inv.Argv, "\n") + "\n" + strings.Join(inv.SecretEnv, "\n")
		if !strings.Contains(readAll(t, root), "http://127.0.0.1:") && !containsValue(inv.Env, "http://127.0.0.1:") {
			t.Fatalf("%s missing endpoint\n%s", a.ID(), blob)
		}
		joined := strings.Join(inv.Argv, " ")
		if !fileHas(t, root, defaultModel) && !containsValue(inv.Env, defaultModel) && !strings.Contains(joined, defaultModel) {
			t.Fatalf("%s missing model", a.ID())
		}
		if a.ID() == "qwen" {
			if inv.Env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "" {
				t.Fatal("qwen sets the generic OTEL endpoint")
			}
			if inv.Env["QWEN_TELEMETRY_OTLP_PROTOCOL"] != "http" || inv.Env["QWEN_HOME"] == "" {
				t.Fatalf("qwen env %+v", inv.Env)
			}
			if !fileHas(t, root, "TRUST_FOLDER") || !fileHas(t, root, `"otlpProtocol": "http"`) {
				t.Fatal("qwen settings")
			}
		}
	}
}

func TestAgentProcessDoesNotSeeProviderKey(t *testing.T) {
	const sentinel = "sk-sentinel-recall-SHOULD-NOT-LEAK"
	const token = "recall-local"
	skill, err := findSkill("")
	if err != nil {
		t.Fatal(err)
	}
	encoded := []string{
		sentinel,
		base64.StdEncoding.EncodeToString([]byte(sentinel)),
		url.QueryEscape(sentinel),
	}
	for _, a := range registry() {
		if !a.Support().OK {
			continue
		}
		root := t.TempDir()
		repo := filepath.Join(root, "repo")
		home := filepath.Join(root, "home")
		if err := os.MkdirAll(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		inv, err := a.Prepare(context.Background(), spec{
			Repo:          repo,
			Home:          home,
			OpenAIBase:    "http://127.0.0.1:9/v1",
			AnthropicBase: "http://127.0.0.1:9",
			Model:         defaultModel,
			OTLP:          "http://127.0.0.1:8",
			SkillSrc:      skill,
			Prompt:        "record the decision",
			LocalToken:    token,
		})
		if err != nil {
			t.Fatalf("%s: %v", a.ID(), err)
		}
		env := agentEnv(Options{
			Key: sentinel,
			ExtraEnv: map[string]string{
				"SDF_RECALL_API_KEY": sentinel,
				"OPENAI_API_KEY":     sentinel,
			},
		}, home, "/tmp", a.Support().Protocol, inv, token)
		blob := strings.Join(env, "\n") + "\n" + readAll(t, root)
		for _, bad := range encoded {
			if bad != "" && strings.Contains(blob, bad) {
				t.Fatalf("%s contains provider key material", a.ID())
			}
		}
		if envValue(env, "SDF_RECALL_API_KEY") != "" {
			t.Fatalf("%s sets SDF_RECALL_API_KEY", a.ID())
		}
		if envValue(env, localTokenEnv) != token {
			t.Fatalf("%s local token %q", a.ID(), envValue(env, localTokenEnv))
		}
		for _, name := range inv.SecretEnv {
			if envValue(env, name) != token {
				t.Fatalf("%s %s=%q", a.ID(), name, envValue(env, name))
			}
		}
		if a.ID() == "pi" && strings.Contains(blob, "$SDF_RECALL_API_KEY") {
			t.Fatal("pi config interpolates SDF_RECALL_API_KEY")
		}
		if a.ID() == "pi" && !strings.Contains(readAll(t, root), token) {
			t.Fatal("pi config missing placeholder")
		}
	}
}

func envValue(env []string, name string) string {
	prefix := name + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}

func TestRedactCoversTokenShapes(t *testing.T) {
	const key = "provider-key/with space"
	raw := key + " " + url.QueryEscape(key) + " " + base64.StdEncoding.EncodeToString([]byte(key)) +
		" Bearer " + key + " sk-abcdefghijklmnopqrstuvwxyz"
	out := redact(raw, key)
	for _, bad := range []string{key, url.QueryEscape(key), base64.StdEncoding.EncodeToString([]byte(key)), "sk-abcdefghijklmnopqrstuvwxyz"} {
		if strings.Contains(out, bad) {
			t.Fatalf("left %q in %s", bad, out)
		}
	}
	if !strings.Contains(out, "Bearer [redacted]") || !strings.Contains(out, "[redacted]") {
		t.Fatal(out)
	}
}

func TestProxyRejectsMissingTokenAndRedactsErrors(t *testing.T) {
	const leaked = "sk-sentinel-recall-SHOULD-NOT-LEAK"
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+leaked {
			t.Errorf("upstream auth %q", r.Header.Get("Authorization"))
		}
		http.Error(w, "bad "+leaked+" "+base64.StdEncoding.EncodeToString([]byte(leaked))+" Bearer other-secret sk-abcdefghijklmnopqrstuvwxyz", http.StatusBadRequest)
	}))
	t.Cleanup(upstream.Close)
	p := newProxy(Options{
		OpenAIBase:    upstream.URL + "/v1",
		AnthropicBase: upstream.URL,
		Key:           leaked,
		Retries:       0,
		HTTPTimeout:   2 * time.Second,
		MaxTurns:      4,
		Backoff:       func(int) time.Duration { return time.Millisecond },
	})
	p.retries = 0
	base, _, stop, err := p.start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	resp, err := http.Post(base+"/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("status %d calls %d", resp.StatusCode, calls)
	}
	resp, err = proxyDo(t, p, http.MethodPost, base+"/chat/completions", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	text := string(b)
	if resp.StatusCode != http.StatusBadRequest || calls != 1 {
		t.Fatalf("status %d calls %d body %s", resp.StatusCode, calls, text)
	}
	for _, bad := range []string{leaked, base64.StdEncoding.EncodeToString([]byte(leaked)), "sk-abcdefghijklmnopqrstuvwxyz", "other-secret"} {
		if strings.Contains(text, bad) {
			t.Fatalf("error body leaked %q: %s", bad, text)
		}
	}
}

func TestProxySumsAnthropicSplitUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":7}}\n\n")
	}))
	t.Cleanup(upstream.Close)
	p := newProxy(Options{
		OpenAIBase:    upstream.URL + "/v1",
		AnthropicBase: upstream.URL,
		Key:           testKey,
		Retries:       0,
		HTTPTimeout:   2 * time.Second,
		MaxTurns:      4,
		Backoff:       func(int) time.Duration { return time.Millisecond },
	})
	p.retries = 0
	_, anth, stop, err := p.start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	resp, err := proxyDo(t, p, http.MethodPost, anth+"/v1/messages", `{"stream":true}`)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	p.mu.Lock()
	got := p.tokens
	p.mu.Unlock()
	if got != 18 {
		t.Fatalf("tokens %d", got)
	}
}

func TestProxyBodyReadTimesOut(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	p := newProxy(Options{
		OpenAIBase:    upstream.URL + "/v1",
		AnthropicBase: upstream.URL,
		Key:           testKey,
		Retries:       0,
		HTTPTimeout:   2 * time.Second,
		MaxTurns:      2,
		Backoff:       func(int) time.Duration { return time.Millisecond },
	})
	p.retries = 0
	p.bodyTimeout = 150 * time.Millisecond
	base, _, stop, err := p.start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, base+"/chat/completions", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+p.gate)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	go func() {
		time.Sleep(800 * time.Millisecond)
		_, _ = pw.Write([]byte(`{}`))
		_ = pw.Close()
	}()
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Fatalf("status %d %s", resp.StatusCode, b)
		}
	}
	if calls != 0 {
		t.Fatalf("upstream calls %d", calls)
	}
}

func TestRunTimeoutIsReported(t *testing.T) {
	bin := buildSDF(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream called")
	}))
	t.Cleanup(upstream.Close)
	rep, err := Run(context.Background(), Options{
		Agents:        []string{"opencode"},
		FixtureNames:  []string{"decide-commit"},
		K:             1,
		Key:           testKey,
		OpenAIBase:    upstream.URL + "/v1",
		AnthropicBase: upstream.URL,
		Install:       false,
		SDFBin:        bin,
		LookPath:      stubLook(t),
		ExtraEnv:      map[string]string{"SDF_RECALL_STUB": "1", "SDF_RECALL_STUB_SLEEP": "1"},
		MaxTurns:      2,
		RunTimeout:    400 * time.Millisecond,
		HTTPTimeout:   2 * time.Second,
		Retries:       0,
		Backoff:       func(int) time.Duration { return time.Millisecond },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Agents) != 1 || len(rep.Agents[0].Scenarios) != 1 || rep.Agents[0].Scenarios[0].Failed != 1 {
		t.Fatalf("%+v", rep.Agents)
	}
	msg := strings.Join(rep.Agents[0].Scenarios[0].Errors, "\n")
	if !strings.Contains(msg, "timeout") || strings.Contains(msg, testKey) {
		t.Fatal(msg)
	}
}

func TestRejectsPublicHTTPBase(t *testing.T) {
	if err := validateBase("http://127.0.0.1:9/v1"); err != nil {
		t.Fatal(err)
	}
	if err := validateBase("http://localhost/v1"); err != nil {
		t.Fatal(err)
	}
	if err := validateBase("https://example.com/v1"); err != nil {
		t.Fatal(err)
	}
	if err := validateBase("http://example.com/v1"); err == nil {
		t.Fatal("public http accepted")
	}
	_, err := Run(context.Background(), Options{Key: testKey, OpenAIBase: "http://example.com/v1", K: 1})
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatal(err)
	}
}

func TestInstallIsOptInAndPinned(t *testing.T) {
	t.Setenv("SDF_RECALL_NO_INSTALL", "")
	opt, _, err := ParseArgs(nil, Options{})
	if err != nil || opt.Install {
		t.Fatal(err, opt.Install)
	}
	prefix := t.TempDir()
	saw := false
	for _, a := range registry() {
		for _, argv := range a.InstallCommands() {
			saw = true
			got := rewriteInstall(argv, prefix)
			joined := strings.Join(got, " ")
			if argv[0] == "npm" {
				if !strings.Contains(joined, "--prefix "+prefix) || !strings.Contains(joined, "--ignore-scripts") {
					t.Fatalf("%s: %s", a.ID(), joined)
				}
			}
			for _, arg := range argv {
				if strings.Contains(arg, "@") || strings.HasPrefix(arg, "npm:") {
					if !strings.ContainsAny(arg, "0123456789") {
						t.Fatalf("%s unpinned %s", a.ID(), arg)
					}
				}
			}
		}
	}
	if !saw {
		t.Fatal("no install commands")
	}
}

func TestProxyRetries429AndCapsTurns(t *testing.T) {
	var n int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		if strings.Contains(r.Header.Get("Authorization"), "\n") {
			t.Error("auth header broken")
		}
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"total_tokens":4}}`))
	}))
	t.Cleanup(upstream.Close)
	p := newProxy(Options{
		OpenAIBase:    upstream.URL + "/v1",
		AnthropicBase: upstream.URL,
		Key:           testKey,
		Retries:       2,
		HTTPTimeout:   2 * time.Second,
		MaxTurns:      1,
		MaxTokens:     100,
		Backoff:       func(int) time.Duration { return time.Millisecond },
	})
	base, _, stop, err := p.start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	resp, err := proxyDo(t, p, http.MethodPost, base+"/chat/completions", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || n != 2 {
		t.Fatalf("status %d calls %d", resp.StatusCode, n)
	}
	before := n
	resp, err = proxyDo(t, p, http.MethodPost, base+"/chat/completions", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || n != before || !strings.Contains(string(body), "turn cap reached") {
		t.Fatalf("capped status %d calls %d body %s", resp.StatusCode, n, body)
	}
	if !p.cappedRun() {
		t.Fatal("not capped")
	}
}

func TestProxyStreamsAndStops(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	rel := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(rel)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"usage\":{\"total_tokens\":3}}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-release
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(upstream.Close)
	p := newProxy(Options{
		OpenAIBase:    upstream.URL + "/v1",
		AnthropicBase: upstream.URL,
		Key:           testKey,
		Model:         defaultModel,
		Retries:       0,
		HTTPTimeout:   2 * time.Second,
		MaxTurns:      2,
		MaxTokens:     100,
		Backoff:       func(int) time.Duration { return time.Millisecond },
	})
	// Retries 0 is replaced by the default. Force a single try.
	p.retries = 0
	base, anth, stop, err := p.start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+p.gate)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type %q", resp.Header.Get("Content-Type"))
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not flush")
	}
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil || !bytes.Contains(buf[:n], []byte("total_tokens")) {
		t.Fatalf("first chunk %q %v", buf[:n], err)
	}
	rel()
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rest, []byte("[DONE]")) {
		t.Fatalf("rest %s", rest)
	}

	for _, kind := range []struct {
		url, body, needle string
	}{
		{base + "/chat/completions", `{"stream":true}`, `"finish_reason":"stop"`},
		{base + "/responses", `{"stream":true}`, "response.completed"},
		{anth + "/v1/messages", `{"stream":true}`, "message_stop"},
	} {
		p.reset()
		p.mu.Lock()
		p.turns = p.maxTurns
		p.mu.Unlock()
		req, err = http.NewRequest(http.MethodPost, kind.url, strings.NewReader(kind.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+p.gate)
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("Content-Type") != "text/event-stream" || !bytes.Contains(b, []byte(kind.needle)) || !bytes.Contains(b, []byte("turn cap reached")) {
			t.Fatalf("%s type %q body %s", kind.url, resp.Header.Get("Content-Type"), b)
		}
	}
}

func TestProxyRewritesCopilotModelAndListsModels(t *testing.T) {
	var gotModel string
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(upstream.Close)
	p := newProxy(Options{
		OpenAIBase:    upstream.URL + "/v1",
		AnthropicBase: upstream.URL,
		Key:           testKey,
		Model:         defaultModel,
		MaxTurns:      4,
		HTTPTimeout:   2 * time.Second,
		Backoff:       func(int) time.Duration { return time.Millisecond },
	})
	p.retries = 0
	base, _, stop, err := p.start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	resp, err := http.Get(base + "/models")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(b, []byte(defaultModel)) || !bytes.Contains(b, []byte(copilotModelAlias)) {
		t.Fatalf("models %d %s", resp.StatusCode, b)
	}
	resp, err = proxyDo(t, p, http.MethodPost, base+"/chat/completions", `{"model":"tokenrouter/recall","stream":false}`)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotModel != defaultModel {
		t.Fatalf("upstream model %q", gotModel)
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls %d", upstreamCalls)
	}
}

func TestCodexIsNotExecuted(t *testing.T) {
	called := false
	rep, err := Run(context.Background(), Options{
		Agents: []string{"codex"},
		Key:    testKey,
		K:      1,
		LookPath: func(string) (string, error) {
			called = true
			return "", errors.New("called")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("looked up codex")
	}
	if len(rep.Agents) != 1 || rep.Agents[0].Status != statusUnsupported {
		t.Fatalf("%+v", rep.Agents)
	}
	if !strings.Contains(rep.Agents[0].Reason, "tool_choice") || !strings.Contains(rep.Agents[0].Reason, "reasoning") {
		t.Fatal(rep.Agents[0].Reason)
	}
}

func TestMergePoolsRuns(t *testing.T) {
	one := func(hits int) Report {
		recall := 1.0
		return Report{
			Schema: schemaLive,
			Model:  defaultModel,
			Agents: []AgentReport{{
				ID:       "opencode",
				Name:     "OpenCode",
				Protocol: "openai-chat",
				Status:   statusMeasured,
				Scenarios: []ScenarioReport{{
					Name:      "decide-commit",
					Runs:      1,
					Scored:    1,
					Decisions: Ratio{Labeled: 1, Recorded: 1, Hits: hits, Recall: &recall},
					Links:     Ratio{Labeled: 1, Recorded: 1, Hits: hits, Recall: &recall},
				}},
			}},
		}
	}
	rep, err := mergeReports([]Report{one(1), one(0)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.K != 2 || len(rep.Agents) != 1 || rep.Agents[0].Overall == nil {
		t.Fatalf("%+v", rep)
	}
	if rep.Agents[0].Overall.Decisions.Hits != 1 || rep.Agents[0].Overall.Decisions.Labeled != 2 {
		t.Fatalf("%+v", rep.Agents[0].Overall.Decisions)
	}
	if rep.Agents[0].Overall.Decisions.Recall == nil || *rep.Agents[0].Overall.Decisions.Recall != 0.5 {
		t.Fatalf("recall %+v", rep.Agents[0].Overall.Decisions.Recall)
	}
	dir := t.TempDir()
	body, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "a.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := mergeReportFiles([]string{path})
	if err != nil || again.Agents[0].Overall.Decisions.Hits != 1 {
		t.Fatal(err, again)
	}
	twice, err := mergeReportFiles([]string{path, path})
	if err != nil || twice.Agents[0].Overall.Decisions.Hits != 1 || twice.K != 2 {
		t.Fatalf("dedupe %+v %v", twice.Overall, err)
	}
}

func TestStderrTailDropsNodeNoise(t *testing.T) {
	bin := buildSDF(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream called: %s", r.URL.Path)
	}))
	t.Cleanup(upstream.Close)
	out := t.TempDir()
	rep, err := Run(context.Background(), Options{
		Agents:        []string{"opencode"},
		FixtureNames:  []string{"decide-commit"},
		K:             1,
		Key:           testKey,
		OpenAIBase:    upstream.URL + "/v1",
		AnthropicBase: upstream.URL,
		Install:       false,
		SDFBin:        bin,
		OutDir:        out,
		LookPath:      stubLook(t),
		ExtraEnv:      map[string]string{"SDF_RECALL_STUB": "1", "SDF_RECALL_STUB_NOISE": "1"},
		MaxTurns:      4,
		RunTimeout:    30 * time.Second,
		HTTPTimeout:   2 * time.Second,
		Retries:       1,
		Backoff:       func(int) time.Duration { return time.Millisecond },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Agents) != 1 || len(rep.Agents[0].Scenarios) != 1 || rep.Agents[0].Scenarios[0].Failed != 1 {
		t.Fatalf("%+v", rep.Agents)
	}
	msg := strings.Join(rep.Agents[0].Scenarios[0].Errors, "\n")
	if !strings.Contains(msg, "late-error-marker") || strings.Contains(msg, "ExperimentalWarning") || strings.Contains(msg, testKey) {
		t.Fatal(msg)
	}
	logPath := filepath.Join(out, "opencode", "decide-commit", "run-1.log")
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "ExperimentalWarning") || !strings.Contains(text, "late-error-marker") || strings.Contains(text, testKey) {
		t.Fatalf("log %s", text)
	}
}

func TestScriptedAgentsScoreDecideCommit(t *testing.T) {
	bin := buildSDF(t)
	var mu sync.Mutex
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		auth := r.Header.Get("Authorization")
		mu.Unlock()
		if auth != "Bearer "+testKey {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		kind := ""
		switch r.URL.Path {
		case "/v1/chat/completions":
			kind = "chat"
		case "/v1/responses":
			kind = "responses"
		case "/v1/messages":
			kind = "messages"
		default:
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		n := pathCount(paths, r.URL.Path) - 1
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(scriptBody(kind, n%3))
	}))
	t.Cleanup(upstream.Close)

	rep, err := Run(context.Background(), Options{
		Agents:        []string{"opencode", "claude-code"},
		FixtureNames:  []string{"decide-commit"},
		K:             1,
		Parallel:      2,
		Key:           testKey,
		Model:         defaultModel,
		OpenAIBase:    upstream.URL + "/v1",
		AnthropicBase: upstream.URL,
		Install:       false,
		SDFBin:        bin,
		LookPath:      stubLook(t),
		ExtraEnv:      map[string]string{"SDF_RECALL_STUB": "1"},
		MaxTurns:      8,
		RunTimeout:    45 * time.Second,
		HTTPTimeout:   5 * time.Second,
		Retries:       1,
		Backoff:       func(int) time.Duration { return time.Millisecond },
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped || len(rep.Agents) != 2 {
		t.Fatalf("%+v", rep)
	}
	for _, a := range rep.Agents {
		if a.Status != statusMeasured || a.Overall == nil {
			t.Fatalf("%s %s %s", a.ID, a.Status, a.Reason)
		}
		if a.Overall.Failed != 0 || a.Overall.Scored != 1 {
			t.Fatalf("%s scored %d failed %d errors %v", a.ID, a.Overall.Scored, a.Overall.Failed, scenarioErrors(a))
		}
		if a.Overall.Decisions.Hits != 1 || a.Overall.Decisions.Recall == nil || *a.Overall.Decisions.Recall != 1 {
			t.Fatalf("%s decisions %+v", a.ID, a.Overall.Decisions)
		}
		if a.Overall.Links.Hits != 1 || a.Overall.Links.Recall == nil || *a.Overall.Links.Recall != 1 {
			t.Fatalf("%s links %+v", a.ID, a.Overall.Links)
		}
		if a.Overall.Decisions.Wilson95 == nil || a.Overall.Decisions.Wilson95.High != 1 {
			t.Fatalf("%s wilson %+v", a.ID, a.Overall.Decisions.Wilson95)
		}
	}
	if rep.Overall == nil || rep.Overall.Decisions.Hits != 2 || rep.Overall.Links.Hits != 2 {
		t.Fatalf("overall %+v", rep.Overall)
	}
	mu.Lock()
	got := append([]string(nil), paths...)
	mu.Unlock()
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		if pathCount(got, path) < 3 {
			t.Fatalf("path %s count %d in %v", path, pathCount(got, path), got)
		}
	}
	text := rep.Markdown()
	body, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text+body, testKey) {
		t.Fatal("key in report")
	}
	if !strings.Contains(body, schemaLive) || !strings.Contains(text, "Decision recall") {
		t.Fatal("report shape")
	}
}

func TestFailedRunIsNotAMiss(t *testing.T) {
	bin := buildSDF(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream called on a failed stub: %s", r.URL.Path)
	}))
	t.Cleanup(upstream.Close)
	rep, err := Run(context.Background(), Options{
		Agents:        []string{"opencode"},
		FixtureNames:  []string{"decide-commit"},
		K:             1,
		Key:           testKey,
		OpenAIBase:    upstream.URL + "/v1",
		AnthropicBase: upstream.URL,
		Install:       false,
		SDFBin:        bin,
		LookPath:      stubLook(t),
		ExtraEnv:      map[string]string{"SDF_RECALL_STUB": "1", "SDF_RECALL_STUB_FAIL": "1"},
		MaxTurns:      4,
		RunTimeout:    30 * time.Second,
		HTTPTimeout:   2 * time.Second,
		Retries:       1,
		Backoff:       func(int) time.Duration { return time.Millisecond },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Agents) != 1 || rep.Agents[0].Status != statusMeasured {
		t.Fatalf("%+v", rep.Agents)
	}
	var decide ScenarioReport
	for _, sc := range rep.Agents[0].Scenarios {
		if sc.Name == "decide-commit" {
			decide = sc
		}
		if sc.Failed != 1 || sc.Scored != 0 {
			t.Fatalf("%s failed %d scored %d", sc.Name, sc.Failed, sc.Scored)
		}
		if sc.Decisions.Recall != nil || sc.Decisions.Labeled != 0 {
			t.Fatalf("%s counted a failure as a miss: %+v", sc.Name, sc.Decisions)
		}
	}
	if decide.Name == "" {
		t.Fatal("missing decide-commit")
	}
	body, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, testKey) || !strings.Contains(body, "stub failed") {
		t.Fatalf("json errors: %s", body)
	}
}

func scenarioErrors(a AgentReport) []string {
	var out []string
	for _, sc := range a.Scenarios {
		out = append(out, sc.Errors...)
	}
	return out
}

func pathCount(paths []string, path string) int {
	n := 0
	for _, p := range paths {
		if p == path {
			n++
		}
	}
	return n
}

func walkNoKey(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), testKey) {
			t.Errorf("%s contains the API key", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readAll(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.Write(body)
		b.WriteByte('\n')
		return nil
	})
	return b.String()
}

func fileHas(t *testing.T, root, needle string) bool {
	t.Helper()
	return strings.Contains(readAll(t, root), needle)
}

func containsValue(env map[string]string, needle string) bool {
	for _, v := range env {
		if strings.Contains(v, needle) {
			return true
		}
	}
	return false
}

var (
	sdfOnce sync.Once
	sdfBin  string
	sdfErr  error
)

func buildSDF(t *testing.T) string {
	t.Helper()
	sdfOnce.Do(func() {
		root, err := moduleRoot()
		if err != nil {
			sdfErr = err
			return
		}
		dir, err := os.MkdirTemp("", "sdf-recall-testbin-")
		if err != nil {
			sdfErr = err
			return
		}
		bin := filepath.Join(dir, "sdf")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/sdf")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			sdfErr = errors.New(string(out))
			return
		}
		sdfBin = bin
	})
	if sdfErr != nil {
		t.Fatal(sdfErr)
	}
	return sdfBin
}

func proxyDo(t *testing.T, p *proxy, method, rawURL, body string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Authorization", "Bearer "+p.gate)
	}
	return http.DefaultClient.Do(req)
}

func stubLook(t *testing.T) func(string) (string, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"opencode", "pi", "qwen", "grok", "copilot", "codex", "claude"} {
		if err := os.Symlink(exe, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return func(name string) (string, error) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			return "", err
		}
		return p, nil
	}
}

func stubMain() int {
	if os.Getenv("SDF_RECALL_STUB_SLEEP") == "1" {
		time.Sleep(5 * time.Second)
		return 0
	}
	if os.Getenv("SDF_RECALL_STUB_NOISE") == "1" {
		fmtErr("(node:1) Warning: ExperimentalWarning fake")
		fmtErr("(Use `node --trace-warnings ...` to show where the warning was created)")
		fmtErr("secret " + os.Getenv("SDF_RECALL_API_KEY"))
		fmtErr(strings.Repeat("x", 6000))
		fmtErr("late-error-marker")
		return 1
	}
	if os.Getenv("SDF_RECALL_STUB_FAIL") == "1" {
		fmtErr("stub failed")
		return 1
	}
	base := os.Getenv("SDF_RECALL_MODEL_BASE")
	kind := os.Getenv("SDF_RECALL_PROTOCOL")
	endpoint := stubEndpoint(base, kind)
	if endpoint == "" {
		fmtErr("stub missing model base")
		return 1
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for range 8 {
		cmds, err := stubRound(client, endpoint, kind)
		if err != nil {
			fmtErr(err.Error())
			return 1
		}
		if len(cmds) == 0 {
			return 0
		}
		for _, c := range cmds {
			cmd := exec.Command("sh", "-c", c)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmtErr(err.Error())
				return 1
			}
		}
	}
	fmtErr("stub turn cap")
	return 1
}

func fmtErr(msg string) {
	_, _ = os.Stderr.WriteString(msg + "\n")
}

func stubEndpoint(base, kind string) string {
	base = strings.TrimRight(base, "/")
	switch kind {
	case string(protocolChat):
		return base + "/chat/completions"
	case string(protocolResponses):
		return base + "/responses"
	case string(protocolAnthropic):
		return base + "/v1/messages"
	default:
		return ""
	}
}

func stubRound(client *http.Client, endpoint, kind string) ([]string, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := os.Getenv("SDF_RECALL_LOCAL_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.New("stub model status")
	}
	return commandsFrom(kind, raw)
}

func commandsFrom(kind string, raw json.RawMessage) ([]string, error) {
	switch kind {
	case string(protocolChat):
		var body struct {
			Choices []struct {
				Message struct {
					ToolCalls []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		var cmds []string
		for _, ch := range body.Choices {
			for _, call := range ch.Message.ToolCalls {
				cmd, err := commandArg(json.RawMessage(call.Function.Arguments))
				if err != nil {
					return nil, err
				}
				cmds = append(cmds, cmd)
			}
		}
		return cmds, nil
	case string(protocolResponses):
		var body struct {
			Output []struct {
				Type      string `json:"type"`
				Arguments string `json:"arguments"`
			} `json:"output"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		var cmds []string
		for _, item := range body.Output {
			if item.Type != "function_call" {
				continue
			}
			cmd, err := commandArg(json.RawMessage(item.Arguments))
			if err != nil {
				return nil, err
			}
			cmds = append(cmds, cmd)
		}
		return cmds, nil
	default:
		var body struct {
			Content []struct {
				Type  string `json:"type"`
				Input struct {
					Command string `json:"command"`
				} `json:"input"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		var cmds []string
		for _, block := range body.Content {
			if block.Type == "tool_use" && block.Input.Command != "" {
				cmds = append(cmds, block.Input.Command)
			}
		}
		return cmds, nil
	}
}

func commandArg(raw json.RawMessage) (string, error) {
	var wrap struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return "", err
	}
	if wrap.Command == "" {
		return "", errors.New("empty command")
	}
	return wrap.Command, nil
}

func scriptBody(kind string, step int) []byte {
	add := `sdf decision add --id pure-go --statement 'Keep the SQLite driver pure Go.' --status decision --decided-by agent`
	link := `sdf decision link --relation implements --from commit:1111111111111111111111111111111111111111 --to decision:pure-go`
	switch step {
	case 0:
		return toolBody(kind, add)
	case 1:
		return toolBody(kind, link)
	default:
		return stopBody(kind)
	}
}

func toolBody(kind, command string) []byte {
	switch kind {
	case "chat":
		args, _ := json.Marshal(map[string]string{"command": command})
		b, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"finish_reason": "tool_calls",
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []any{map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "shell",
							"arguments": string(args),
						},
					}},
				},
			}},
			"usage": map[string]int{"total_tokens": 12},
		})
		return b
	case "responses":
		args, _ := json.Marshal(map[string]string{"command": command})
		b, _ := json.Marshal(map[string]any{
			"output": []any{map[string]any{
				"type":      "function_call",
				"name":      "shell",
				"arguments": string(args),
			}},
			"usage": map[string]int{"total_tokens": 12},
		})
		return b
	default:
		b, _ := json.Marshal(map[string]any{
			"stop_reason": "tool_use",
			"content": []any{map[string]any{
				"type":  "tool_use",
				"id":    "toolu_1",
				"name":  "shell",
				"input": map[string]string{"command": command},
			}},
			"usage": map[string]int{"input_tokens": 8, "output_tokens": 4},
		})
		return b
	}
}

func stopBody(kind string) []byte {
	switch kind {
	case "responses":
		return []byte(`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"total_tokens":2}}`)
	case "messages":
		return []byte(`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	default:
		return []byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"total_tokens":2}}`)
	}
}
