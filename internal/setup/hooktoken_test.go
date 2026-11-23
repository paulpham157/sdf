package setup

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// authServer records the Authorization header of every request.
func authServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := append([]string(nil), seen...)
		seen = nil
		return out
	}
}

// hookEnv is the environment without receiver tokens or Grok's markers.
func hookEnv(extra ...string) []string {
	var out []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "SDF_OTLP_TOKEN", "SDF_OTLP_TOKEN_FILE", "GROK_HOOK_EVENT", "GROK_SESSION_ID":
			continue
		}
		out = append(out, item)
	}
	return append(out, extra...)
}

func TestSessionHooksSendTheReceiverTokenFromTheEnvironment(t *testing.T) {
	const token = "tok-7f3a9c"
	file := filepath.Join(t.TempDir(), "otlp-token")
	if err := os.WriteFile(file, []byte("  "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, seen := authServer(t)
	url := srv.URL + "/v1/sessions"
	in := `{"session_id":"s1","sessionId":"s1","thread_id":"s1","source":"startup"}`
	for name, command := range map[string]string{
		"claude-code": claudeHooks(url)["SessionStart"],
		"codex":       codexHookCommands(url)["SessionStart"],
		"grok-cli":    grokSessionHookCommand("open", url),
	} {
		if strings.Contains(command, token) {
			t.Fatalf("%s: command contains the token", name)
		}
		for _, tc := range []struct {
			env  []string
			want string
		}{
			{hookEnv("SDF_OTLP_TOKEN=" + token), "Bearer " + token},
			{hookEnv("SDF_OTLP_TOKEN_FILE=" + file), "Bearer " + token},
			{hookEnv("SDF_OTLP_TOKEN="+token, "SDF_OTLP_TOKEN_FILE=/nonexistent"), "Bearer " + token},
			{hookEnv("SDF_OTLP_TOKEN_FILE=/nonexistent"), ""},
			{hookEnv(), ""},
		} {
			cmd := exec.Command("sh", "-c", command)
			cmd.Env = tc.env
			cmd.Stdin = strings.NewReader(in)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s: %v %s", name, err, out)
			}
			got := seen()
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("%s with %v: Authorization %q, want %q", name, tc.env[len(tc.env)-1:], got, tc.want)
			}
		}
	}
}

func TestGitHookSendsTheReceiverTokenFromTheEnvironment(t *testing.T) {
	isolateGit(t)
	t.Setenv("SDF_OTLP_TOKEN", "")
	t.Setenv("SDF_OTLP_TOKEN_FILE", "")
	const token = "git-tok-41b2"
	file := filepath.Join(t.TempDir(), "otlp-token")
	if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := testRepo(t)
	srv, seen := authServer(t)
	if _, err := InstallGitHooks(dir, endpointOf(t, srv)); err != nil {
		t.Fatal(err)
	}
	id := "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	for _, tc := range []struct {
		env  string
		want string
	}{
		{"SDF_OTLP_TOKEN=" + token, "Bearer " + token},
		{"SDF_OTLP_TOKEN_FILE=" + file, "Bearer " + token},
		{"SDF_OTLP_TOKEN=", ""},
	} {
		commitIn(t, dir, "c", "GROK_SESSION_ID="+id, tc.env)
		if got := seen(); len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%s: Authorization %q, want %q", tc.env, got, tc.want)
		}
	}
	hooks := hooksPath(t, dir)
	for _, name := range []string{"post-commit", "post-rewrite", "prepare-commit-msg"} {
		body, err := os.ReadFile(filepath.Join(hooks, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), token) {
			t.Fatalf("%s contains the token", name)
		}
	}
}

// The Pi extension and the OpenCode plugin read the token the same way. node
// runs the generated module with fetch replaced, when node is installed.
func TestJSHooksSendTheReceiverToken(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	module := filepath.Join(dir, "pi.mjs")
	if err := os.WriteFile(module, []byte(piExtension("http://127.0.0.1:1/v1/sessions")), 0o600); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(dir, "run.mjs")
	script := `const calls = []
globalThis.fetch = (url, opts) => { calls.push(opts.headers.authorization || ""); return Promise.resolve() }
const mod = await import(process.argv[2])
const handlers = {}
mod.default({ sessionManager: { getSessionId: () => "s1" }, on: (name, fn) => { handlers[name] = fn } })
handlers.session_start({ reason: "startup" })
console.log(JSON.stringify(calls))
`
	if err := os.WriteFile(driver, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "tok")
	if err := os.WriteFile(file, []byte("js-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for env, want := range map[string]string{
		"SDF_OTLP_TOKEN=js-tok":       `["Bearer js-tok"]`,
		"SDF_OTLP_TOKEN_FILE=" + file: `["Bearer js-tok"]`,
		"SDF_OTLP_TOKEN=":             `[""]`,
	} {
		cmd := exec.Command(node, driver, module)
		cmd.Env = hookEnv(env)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", env, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Fatalf("%s: got %s want %s", env, got, want)
		}
	}
}

// Without curl the git hook posts with wget, and the token goes through a
// temporary wgetrc that is removed afterwards.
func TestGitHookSendsTheReceiverTokenWithWget(t *testing.T) {
	if _, err := exec.LookPath("wget"); err != nil {
		t.Skip("wget is not installed")
	}
	isolateGit(t)
	t.Setenv("SDF_OTLP_TOKEN", "")
	t.Setenv("SDF_OTLP_TOKEN_FILE", "")
	bin := t.TempDir()
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.Name() == "curl" {
				continue
			}
			link := filepath.Join(bin, e.Name())
			if _, err := os.Lstat(link); err == nil {
				continue
			}
			_ = os.Symlink(filepath.Join(dir, e.Name()), link)
		}
	}
	tmp := t.TempDir()
	dir := testRepo(t)
	srv, seen := authServer(t)
	if _, err := InstallGitHooks(dir, endpointOf(t, srv)); err != nil {
		t.Fatal(err)
	}
	id := "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	for _, tc := range []struct{ env, want string }{
		{"SDF_OTLP_TOKEN=wget-tok", "Bearer wget-tok"},
		{"SDF_OTLP_TOKEN=", ""},
	} {
		commitIn(t, dir, "c", "PATH="+bin, "TMPDIR="+tmp, "GROK_SESSION_ID="+id, tc.env)
		if got := seen(); len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%s: Authorization %q, want %q", tc.env, got, tc.want)
		}
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("temporary wgetrc left behind: %v", left)
	}
}

func TestHooksNeverSendTheTokenToANonLoopbackHost(t *testing.T) {
	for u, want := range map[string]bool{
		"http://127.0.0.1:4318/v1/sessions":         true,
		"http://127.8.9.10:4318/v1/sessions":        true,
		"http://localhost:4318/v1/sessions":         true,
		"http://LOCALHOST:4318/v1/sessions":         true,
		"http://[::1]:4318/v1/sessions":             true,
		"http://10.0.0.5:4318/v1/sessions":          false,
		"http://collector.example:4318/v1/sessions": false,
		"http://localhost.example:4318/v1/sessions": false,
		"http://[::2]:4318/v1/sessions":             false,
		"https://127.0.0.1:4318/v1/sessions":        false,
		"not a url":                                 false,
	} {
		if got := loopbackURL(u); got != want {
			t.Errorf("loopbackURL(%q) = %v, want %v", u, got, want)
		}
	}
	remote := "http://collector.example:4318/v1/sessions"
	for name, text := range map[string]string{
		"claude-code": claudeHooks(remote)["SessionStart"],
		"codex":       codexHookCommands(remote)["SessionStart"],
		"grok":        grokHooks(remote),
		"opencode":    opencodePlugin(remote),
		"pi":          piExtension(remote),
	} {
		if strings.Contains(text, "SDF_OTLP_TOKEN") || strings.Contains(text, "Bearer") {
			t.Errorf("%s hook for a non-loopback host reads or sends the token:\n%s", name, text)
		}
	}
	local := "http://127.0.0.1:4318/v1/sessions"
	for name, text := range map[string]string{
		"claude-code": claudeHooks(local)["SessionStart"],
		"grok":        grokHooks(local),
		"opencode":    opencodePlugin(local),
		"pi":          piExtension(local),
	} {
		if !strings.Contains(text, "SDF_OTLP_TOKEN_FILE") {
			t.Errorf("%s hook for loopback does not read the token", name)
		}
	}
	if note := hookTokenNoteFor(Endpoint{Host: "collector.example", HTTPPort: 4318}); !strings.Contains(note, "does not send") {
		t.Errorf("note %q", note)
	}
	if note := hookTokenNoteFor(Endpoint{Host: "127.0.0.1", HTTPPort: 4318}); note != hookTokenNote {
		t.Errorf("note %q", note)
	}
}
