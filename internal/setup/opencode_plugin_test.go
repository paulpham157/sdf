package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// OpenCode V2 loads only a default export with an id and a setup function; a
// named V1-style export fails with "Plugin must export a default definition
// with an id and an effect or setup function" and never runs (#290).
func TestOpenCodePluginUsesTheV2Entrypoint(t *testing.T) {
	text := opencodePlugin("http://127.0.0.1:4318/v1/sessions")
	for _, want := range []string{"export default {", `id: "sdf-session"`, "setup(ctx)", "for await", "data.sessionID", "abort()"} {
		if !strings.Contains(text, want) {
			t.Errorf("plugin lacks %q:\n%s", want, text)
		}
	}
	// subscribe returns an AsyncIterable in V2, not a Promise.
	for _, bad := range []string{"export const", ".then("} {
		if strings.Contains(text, bad) {
			t.Errorf("plugin still has V1 %q:\n%s", bad, text)
		}
	}
}

// node runs the generated module the way OpenCode V2 does: it reads the
// default export, calls setup with a context whose event stream is an async
// iterable of V2 events, and calls the returned cleanup.
func TestOpenCodePluginPostsTheOpenFromV2Events(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	module := filepath.Join(dir, "sdf-session.mjs")
	if err := os.WriteFile(module, []byte(opencodePlugin("http://127.0.0.1:1/v1/sessions")), 0o600); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(dir, "run.mjs")
	script := `const calls = []
globalThis.fetch = (url, opts) => { calls.push([url, JSON.parse(opts.body), opts.headers.authorization || ""]); return Promise.resolve() }
const mod = await import(process.argv[2])
const def = mod.default
let opts, aborted = false
const events = [
  { type: "session.created", data: { sessionID: "ses_one" } },
  { type: "session.created", data: {} },
  { type: "session.created" },
  { type: "session.created", data: { sessionID: "ses_two" } },
]
const ctx = { event: { subscribe(o) {
  opts = o
  o.signal.addEventListener("abort", () => { aborted = true })
  return (async function* () { for (const e of events) yield e })()
} } }
const cleanup = await def.setup(ctx)
await new Promise((r) => setTimeout(r, 50))
cleanup()
console.log(JSON.stringify({ id: def.id, types: opts.types, aborted, calls }))
`
	if err := os.WriteFile(driver, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, driver, module)
	cmd.Env = hookEnv("SDF_OTLP_TOKEN=oc-tok")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	want := `{"id":"sdf-session","types":["session.created"],"aborted":true,"calls":[` +
		`["http://127.0.0.1:1/v1/sessions",{"tool":"opencode","session_id":"ses_one","edge":"open","reason":""},"Bearer oc-tok"],` +
		`["http://127.0.0.1:1/v1/sessions",{"tool":"opencode","session_id":"ses_two","edge":"open","reason":""},"Bearer oc-tok"]]}`
	if got := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
