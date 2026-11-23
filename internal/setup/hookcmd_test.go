package setup

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSessionHookCommandPostsTheEdge(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = string(body)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cmd := sessionHookCommand("claude-code", "open", srv.URL)
	in := `{"session_id":"abc123","source":"startup","hook_event_name":"SessionStart"}`
	out := runHook(t, cmd, in)
	if out != "" && !strings.Contains(out, "ok") {
		t.Log(out)
	}
	if !strings.Contains(got, `"tool":"claude-code"`) || !strings.Contains(got, `"session_id":"abc123"`) || !strings.Contains(got, `"edge":"open"`) || !strings.Contains(got, `"reason":"startup"`) {
		t.Fatalf("posted %q", got)
	}
}

func TestGrokHookPostsSessionIDFromTheEnvironment(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = string(body)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cmd := grokSessionHookCommand("close", srv.URL)
	command := exec.Command("sh", "-c", cmd)
	command.Stdin = strings.NewReader(`{"sessionId":"from-json","reason":"exit"}`)
	command.Env = append(envWithout("GROK_SESSION_ID"), "GROK_SESSION_ID=from-env")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("hook exited: %v\n%s", err, out)
	}
	if !strings.Contains(got, `"tool":"grok-cli"`) || !strings.Contains(got, `"session_id":"from-env"`) || !strings.Contains(got, `"edge":"close"`) || !strings.Contains(got, `"reason":"exit"`) {
		t.Fatalf("posted %q", got)
	}
	got = ""
	command = exec.Command("sh", "-c", cmd)
	command.Stdin = strings.NewReader(`{"sessionId":"from-json","source":"startup"}`)
	command.Env = envWithout("GROK_SESSION_ID")
	out, err = command.CombinedOutput()
	if err != nil {
		t.Fatalf("hook exited: %v\n%s", err, out)
	}
	if !strings.Contains(got, `"session_id":"from-json"`) || !strings.Contains(got, `"reason":"startup"`) {
		t.Fatalf("posted %q", got)
	}
}

func TestCodexHookSkipsADescendantThread(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cmd := insertGuard(sessionHookCommand("codex", "open", srv.URL),
		`tid=$(printf '%s' "$json" | sed -n 's/.*"thread_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p'); [ -z "$tid" ] || [ "$tid" = "$sid" ] || exit 0; `)
	runHook(t, cmd, `{"session_id":"root","thread_id":"child"}`)
	if posts != 0 {
		t.Fatalf("a descendant posted %d times", posts)
	}
	runHook(t, cmd, `{"session_id":"root","thread_id":"root","source":"startup"}`)
	if posts != 1 {
		t.Fatalf("the root thread posted %d times", posts)
	}
}

func envWithout(key string) []string {
	prefix := key + "="
	var out []string
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return out
}

func runHook(t *testing.T, command, in string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = strings.NewReader(in)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hook exited: %v\n%s", err, out)
	}
	return string(out)
}
