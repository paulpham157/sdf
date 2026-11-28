package setup

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Pi's agent_before_settle handlers chain: a returned entries list replaces
// the proposal so far, so the reminder must keep the entries earlier
// handlers proposed (docs.pi.dev extensions, agent_before_settle). A fake
// sdf on PATH returns the block; node runs the generated extension.
func TestPiStopReminderKeepsEarlierEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake sdf is a shell script")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"continue\":true,\"entries\":[{\"type\":\"custom_message\",\"customType\":\"sdf-decision-reminder\",\"content\":\"r\",\"display\":false}]}'\n"
	if err := os.WriteFile(filepath.Join(bin, "sdf"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(dir, "pi.mjs")
	if err := os.WriteFile(module, []byte(piExtension("http://127.0.0.1:1/v1/sessions")), 0o600); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(dir, "run.mjs")
	script := `globalThis.fetch = () => Promise.resolve()
const mod = await import(process.argv[2])
const handlers = {}
mod.default({ sessionManager: { getSessionId: () => "s1" }, on: (name, fn) => { handlers[name] = fn } })
const prior = { type: "custom", customType: "other-extension", data: {} }
const out = await handlers.agent_before_settle({ entries: [prior], continue: false }, {})
console.log(JSON.stringify(out))
`
	if err := os.WriteFile(driver, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, driver, module)
	cmd.Env = hookEnv("PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v %s", err, stderr.String())
	}
	got := strings.TrimSpace(string(out))
	want := `{"continue":true,"entries":[{"type":"custom","customType":"other-extension","data":{}},{"type":"custom_message","customType":"sdf-decision-reminder","content":"r","display":false}]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// Pi awaits the handler. The open and close must not return until fetch
// settles, and a fetch that never settles must abort after the short timeout
// so pi --print cannot exit before the POST or hang on a dead collector.
func TestPiSessionPostAwaitsTheRequest(t *testing.T) {
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
	script := `const mod = await import(process.argv[2])
const handlers = {}
mod.default({ sessionManager: { getSessionId: () => "s1" }, on: (name, fn) => { handlers[name] = fn } })

let release
let openSignal
globalThis.fetch = (url, opts) => new Promise((resolve) => {
  openSignal = opts.signal
  release = () => resolve({ ok: true })
})
let settled = false
const pending = Promise.resolve(handlers.session_start({ reason: "startup" })).then(() => { settled = true })
await new Promise((r) => setTimeout(r, 30))
if (settled) throw new Error("open settled before fetch")
if (!release) throw new Error("fetch was not called")
release()
await pending
if (!settled) throw new Error("open did not settle after fetch")

let aborted = false
globalThis.fetch = (_url, opts) => new Promise((_resolve, reject) => {
  opts.signal.addEventListener("abort", () => { aborted = true; reject(new DOMException("aborted", "AbortError")) })
})
const t0 = Date.now()
await handlers.session_shutdown({ reason: "quit" })
const ms = Date.now() - t0
const skipped = handlers.session_shutdown({ reason: "reload" })
console.log(JSON.stringify({ aborted, ms, skipped: skipped === undefined, hadSignal: !!openSignal }))
`
	if err := os.WriteFile(driver, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, driver, module)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v %s", err, stderr.String())
	}
	got := strings.TrimSpace(string(out))
	if !strings.Contains(got, `"aborted":true`) || !strings.Contains(got, `"skipped":true`) || !strings.Contains(got, `"hadSignal":true`) {
		t.Fatalf("%s", got)
	}
	// The abort is 1000ms. Allow slack for a slow CI runner, and reject a timeout that did not wait.
	var parsed struct {
		MS int `json:"ms"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil || parsed.MS < 800 || parsed.MS > 2500 {
		t.Fatalf("timeout %d (%v) in %s", parsed.MS, err, got)
	}
}
