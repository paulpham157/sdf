package live

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	opt := ConfigFromEnv().withDefaults()
	if opt.Key != testKey || opt.OpenAIBase != "https://example.test/v1" || opt.AnthropicBase != "https://example.test" {
		t.Fatalf("%+v", opt.OpenAIBase)
	}
	if opt.Model != "model-under-test" || opt.K != 2 {
		t.Fatalf("model %s k %d", opt.Model, opt.K)
	}
	t.Setenv("SDF_RECALL_API_KEY", "primary-key")
	t.Setenv("SDF_RECALL_OPENAI_BASE_URL", "https://openai.test/v1")
	opt = ConfigFromEnv()
	if opt.Key != "primary-key" || opt.OpenAIBase != "https://openai.test/v1" {
		t.Fatalf("override key or base")
	}
}

func TestParseArgs(t *testing.T) {
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
	resp, err := http.Post(base+"/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || n != 2 {
		t.Fatalf("status %d calls %d", resp.StatusCode, n)
	}
	before := n
	resp, err = http.Post(base+"/chat/completions", "application/json", strings.NewReader(`{}`))
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
		Agents:        []string{"opencode", "codex", "claude-code"},
		FixtureNames:  []string{"decide-commit"},
		K:             1,
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
	if rep.Skipped || len(rep.Agents) != 3 {
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
	if rep.Overall == nil || rep.Overall.Decisions.Hits != 3 || rep.Overall.Links.Hits != 3 {
		t.Fatalf("overall %+v", rep.Overall)
	}
	mu.Lock()
	got := append([]string(nil), paths...)
	mu.Unlock()
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
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
