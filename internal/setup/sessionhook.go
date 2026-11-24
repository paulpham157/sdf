package setup

import (
	"fmt"
	"net"
	neturl "net/url"
	"strings"
)

// fieldExpr pulls the string value of one JSON key. The payload is one flat
// object, so a single match is enough and a missing key yields an empty string.
func fieldExpr(name string) string {
	return `sed -n 's/.*"` + name + `"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p'`
}

// hookTokenSh sets sdf_auth to an Authorization header line when a receiver
// token is available to the hook process, and to an empty string otherwise.
// It reads the same variables, the same way, as sdf serve
// (resolveServeOTLPToken): SDF_OTLP_TOKEN, else the file named by
// SDF_OTLP_TOKEN_FILE, with whitespace trimmed from the ends only (blank
// lines and a CR are dropped; spaces inside the token are kept). When both
// variables are set, sdf serve refuses to start, so the hook sends no token
// and says why on stderr. The token is never written into a hook file. The
// header reaches curl on stdin (-H @-), so it is not in curl's argument
// list; an empty line adds no header, so a receiver without a token works
// as before. Every name is sdf_-prefixed and assigned in the command, or
// uses the ${NAME:-} form, so Grok's load-time $VAR substitution leaves it
// alone. sdf_line is read with the shell's read builtin, whose default IFS
// trims leading and trailing blanks.
const hookTokenSh = `sdf_tok=; sdf_line=; sdf_auth=; ` +
	`sdf_tok=$(printf '%s\n' "${SDF_OTLP_TOKEN:-}" | tr -d '\r' | ` + hookTrimSh + `); ` +
	`if [ -n "$sdf_tok" ] && [ -n "${SDF_OTLP_TOKEN_FILE:-}" ]; then ` +
	`echo 'sdf: SDF_OTLP_TOKEN and SDF_OTLP_TOKEN_FILE are both set (sdf serve refuses that too); not sending a token' >&2; sdf_tok=; ` +
	`elif [ -n "${SDF_OTLP_TOKEN_FILE:-}" ]; then ` +
	`sdf_tok=$(tr -d '\r' 2>/dev/null < "${SDF_OTLP_TOKEN_FILE:-}" | ` + hookTrimSh + `); fi; ` +
	`[ -z "$sdf_tok" ] || sdf_auth="Authorization: Bearer $sdf_tok"; `

// hookTrimSh prints the first non-blank line of its input with leading and
// trailing blanks removed, which is strings.TrimSpace for a one-line token.
// A last line without a newline is still read.
const hookTrimSh = `{ while read -r sdf_line && [ -z "$sdf_line" ]; do :; done; printf '%s' "$sdf_line"; }`

// hookTokenShFor is hookTokenSh when u is a loopback receiver, and an empty
// sdf_auth otherwise: the token is only ever sent to localhost, 127.0.0.0/8,
// or ::1, never over plain HTTP to another host. sdf serve refuses a
// non-loopback Host anyway, so nothing that could work is lost.
func hookTokenShFor(u string) string {
	if loopbackURL(u) {
		return hookTokenSh
	}
	return `sdf_auth=; `
}

// loopbackURL reports whether u is an http URL whose host is localhost or a
// loopback IP address.
func loopbackURL(u string) bool {
	parsed, err := neturl.Parse(u)
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// jsTokenHeaders is the OpenCode and Pi header builder for a loopback
// receiver. jsPlainHeaders, for any other host, never reads the token.
const jsTokenHeaders = `import { readFileSync } from "node:fs"

// The receiver token, read when the hook posts from SDF_OTLP_TOKEN, else the
// file named by SDF_OTLP_TOKEN_FILE, the same variables sdf serve reads, the same
// way. It is not stored in this file. No token sends no Authorization header.
const sdfHeaders = () => {
  const headers = { "content-type": "application/json" }
  // Trimmed at the ends only, and both variables set is refused, as sdf serve does.
  let token = (process.env.SDF_OTLP_TOKEN || "").trim()
  if (token && process.env.SDF_OTLP_TOKEN_FILE) {
    console.error("sdf: SDF_OTLP_TOKEN and SDF_OTLP_TOKEN_FILE are both set (sdf serve refuses that too); not sending a token")
    return headers
  }
  if (!token && process.env.SDF_OTLP_TOKEN_FILE) {
    try { token = readFileSync(process.env.SDF_OTLP_TOKEN_FILE, "utf8").trim() } catch (e) {}
  }
  if (token && !/[\r\n]/.test(token)) headers.authorization = "Bearer " + token
  return headers
}
`

const jsPlainHeaders = `// The receiver is not on loopback, so the receiver token is never sent.
const sdfHeaders = () => ({ "content-type": "application/json" })
`

func jsHeaders(u string) string {
	if loopbackURL(u) {
		return jsTokenHeaders
	}
	return jsPlainHeaders
}

// sessionHookCommand reads the JSON an agent passes a command hook on stdin and
// posts one session edge to the collector. It exits 0 even when the collector
// is down, so a stopped sdf never blocks the agent. The two events differ only
// in the edge ("open" or "close") in the posted payload, and Codex reuses the
// command with its thread guard inserted (codexHookCommands).
func sessionHookCommand(tool, edge, url string) string {
	body := fmt.Sprintf(`{"tool":%q,"session_id":"%%s","edge":%q,"reason":"%%s"}`, tool, edge)
	return fmt.Sprintf(`export PATH="$PATH:/usr/bin:/bin"; json=$(cat); `+
		`sid=$(printf '%%s' "$json" | %s); [ -n "$sid" ] || exit 0; `+
		`reason=$(printf '%%s' "$json" | %s); `+
		`[ -n "$reason" ] || reason=$(printf '%%s' "$json" | %s); `+
		`payload=$(printf '%s' "$sid" "$reason"); %s`+
		`printf '%%s\n' "$sdf_auth" | curl -sf -m 1 -X POST -H 'content-type: application/json' -H @- `+
		`--data "$payload" %s >/dev/null 2>&1 || true`,
		fieldExpr("session_id"), fieldExpr("source"), fieldExpr("reason"), body, hookTokenShFor(url), shQuote(url))
}

// claudeHooks is the command sdf adds to each event under "hooks" in
// ~/.claude/settings.json (see mergeSessionHooks). Both events carry
// session_id, which is the session.id the exporter sends.
//
// Grok CLI also runs the hooks in ~/.claude/settings.json by default
// (grok-build user-guide/10-hooks.md, "Hook Locations") and sets
// GROK_HOOK_EVENT on every hook process (xai-grok-hooks/src/runner/command.rs).
// Without the guard a Grok session would post a claude-code open and close.
// ${GROK_HOOK_EVENT:-} is a modifier form, which Grok leaves for sh.
func claudeHooks(url string) map[string]string {
	return map[string]string{
		"SessionStart": claudeGuard + sessionHookCommand("claude-code", "open", url),
		"SessionEnd":   claudeGuard + sessionHookCommand("claude-code", "close", url),
	}
}

const claudeGuard = `[ -z "${GROK_HOOK_EVENT:-}" ] || exit 0; `

// decisionBriefTimeout is the SessionStart hook timeout in seconds. Claude
// Code defaults command hooks to 600s, which would stall a session if sdf
// hung. The agent kills the hook at this budget; the command also fail-opens.
const decisionBriefTimeout = 2

// stopHookTimeout is the Stop hook budget in seconds for agents that count
// timeout in seconds (Claude Code, Codex, Qwen, Grok). The command itself
// fails open well inside it. Gemini counts timeout in milliseconds and uses
// stopHookTimeoutMS.
const stopHookTimeout = 2

const stopHookTimeoutMS = 2000

// decisionBriefCommandV1 is the first unreleased command this branch wrote
// (no capture, with `|| exit 0`). The recognizer still matches it so re-init
// upgrades it in place.
const decisionBriefCommandV1 = `command -v sdf >/dev/null 2>&1 || exit 0; sdf decision brief 2>/dev/null || exit 0`

// decisionBriefCommandV2 is the unreleased exec form. It forwarded sdf's
// exit code, so an old binary or a crash could surface as a SessionStart
// hook error. The recognizer still matches it so re-init upgrades it.
const decisionBriefCommandV2 = `command -v sdf >/dev/null 2>&1 || exit 0; exec sdf decision brief 2>/dev/null`

// decisionBriefCommand is the SessionStart command that prints
// `sdf decision brief` on stdout for the agent to inject. It takes no flags:
// text is the default (a bad flag such as `--json` would exit 2). A missing
// store or no matches prints nothing and exits 0. When the store exists but
// cannot be read or queried, brief prints one stdout line,
// `sdf decision brief unavailable: run sdf decision list`, and still exits 0
// with empty stderr; the hook passes that line through for the agent to
// inject.
//
// Output is captured in sdf_out so a non-zero sdf (an old binary that prints
// usage and exits 2, or a crash) cannot leak text or a non-zero hook status.
// Command substitution also means an orphaned sdf writes to sh's pipe, not
// the agent's stdout. $(...) strips trailing newlines and printf adds one
// back, which matches brief's usual single trailing newline (including a
// 2048-byte cap that ends in \n). `command -v`, `|| exit 0`, and the hook
// timeout are belt-and-braces; brief's own bounds are well under 2s.
const decisionBriefCommand = `command -v sdf >/dev/null 2>&1 || exit 0; sdf_out=$(sdf decision brief </dev/null 2>/dev/null) || exit 0; [ -n "$sdf_out" ] && printf '%s\n' "$sdf_out"; exit 0`

// stopHookCommand is the Stop command sdf installs. It exits 0 when sdf is
// missing or the command fails, and it leaves stdin and stdout alone so the
// agent's payload reaches sdf and the block JSON reaches the agent.
func stopHookCommand(agent string) string {
	cmd := "command -v sdf >/dev/null 2>&1 || exit 0; sdf hook stop --agent " + agent + " || exit 0"
	if agent == "claude-code" {
		return claudeGuard + cmd
	}
	return cmd
}

func decisionBriefCommandFor(tool string) string {
	if tool == "claude-code" {
		return claudeGuard + decisionBriefCommand
	}
	return decisionBriefCommand
}

func hookGroup(command string) map[string]any {
	return map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command}},
	}
}

func decisionBriefGroup(command string) map[string]any {
	return map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": command,
			"timeout": decisionBriefTimeout,
		}},
	}
}

// codexHooks is ~/.codex/hooks.json. Codex only posts when session_id equals
// the thread id, because a descendant thread reports the shared root id while
// its spans carry the thread id. A descendant never fires SessionEnd, so a
// posted event for one would open a session no span joins.
func codexHooks(url string) string {
	commands := codexHookCommands(url)
	return fmt.Sprintf(`{
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": %q}]},
      {"hooks": [{"type": "command", "command": %q, "timeout": %d}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": %q}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": %q, "timeout": %d}]}
    ]
  }
}
`, commands["SessionStart"], decisionBriefCommand, decisionBriefTimeout, commands["SessionEnd"], stopHookCommand("codex"), stopHookTimeout)
}

// codexHookCommands is the command for each event in hooks.json, used to
// write a new file and to merge into an existing one.
func codexHookCommands(url string) map[string]string {
	guard := `tid=$(printf '%s' "$json" | ` + fieldExpr("thread_id") + `); ` +
		`[ -z "$tid" ] || [ "$tid" = "$sid" ] || exit 0; `
	return map[string]string{
		"SessionStart": insertGuard(sessionHookCommand("codex", "open", url), guard),
		"SessionEnd":   insertGuard(sessionHookCommand("codex", "close", url), guard),
	}
}

// insertGuard places the thread-id check after the session id is read and
// before the post, so a descendant thread exits without posting.
func insertGuard(command, guard string) string {
	const marker = `[ -n "$sid" ] || exit 0; `
	return strings.Replace(command, marker, marker+guard, 1)
}

// opencodePlugin is a V2 OpenCode plugin: a default export with an id and a
// setup function. V2 refuses any other shape ("Plugin must export a default
// definition with an id and an effect or setup function"), so a V1 named
// export never runs (#290; https://opencode.ai/docs/build/plugins/migrate-v1/).
// setup subscribes to session.created on the in-process event bus, which does
// not drop events the way the /api/event stream does, and posts the open. In
// V2 ctx.event.subscribe returns an async iterable, and the id is
// ev.data.sessionID (checked live with @opencode/cli 2.4.1 on 2026-11-23).
// The cleanup setup returns aborts the subscription when the plugin unloads.
//
// OpenCode has no event for the close: session.deleted fires when a session is
// deleted, session.idle and session.status going idle are the gap between
// turns, and the cleanup runs on unload, not when a session ends.
func opencodePlugin(url string) string {
	return fmt.Sprintf(`// Reports a session opening to the local sdf collector.
%s
export default {
  id: "sdf-session",
  setup(ctx) {
    const controller = new AbortController()
    void (async () => {
      for await (const ev of ctx.event.subscribe({ types: ["session.created"], signal: controller.signal })) {
        const id = ev && ev.data && ev.data.sessionID
        if (!id) continue
        fetch(%q, {
          method: "POST",
          headers: sdfHeaders(),
          body: JSON.stringify({ tool: "opencode", session_id: id, edge: "open", reason: "" }),
        }).catch(() => {})
      }
    })().catch(() => {})
    return () => controller.abort()
  },
}
`, jsHeaders(url), url)
}

// grokHooks is $GROK_HOME/hooks/sdf-session.json. Grok's hook JSON matches
// Claude's command-hook shape. The process receives GROK_SESSION_ID, and the
// event JSON uses sessionId rather than session_id. User hooks are trusted;
// a project .grok/hooks file is not, until the folder is trusted.
func grokHooks(url string) string {
	return fmt.Sprintf(`{
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": %q}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": %q}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": %q, "timeout": %d}]}
    ]
  }
}
`, grokSessionHookCommand("open", url), grokSessionHookCommand("close", url), stopHookCommand("grok"), stopHookTimeout)
}

// grokSessionHookCommand posts one edge. GROK_SESSION_ID is the id Grok puts
// in the hook environment (grok-build 2bdd1d6, xai-grok-hooks
// src/runner/command.rs). sessionId, and its snake_case alias session_id, is
// the same value in the JSON on stdin (src/event.rs, HookEventEnvelope,
// SNAKE_CASE_ALIASES).
//
// Grok substitutes $VAR in a hook command at load time from its own
// environment and refuses a command whose plain $VAR stays unresolved unless
// the command assigns it (docs/custom-hooks.md, "Variable Substitution";
// src/runner/command.rs find_unresolved_env_vars). The local names are
// sdf_-prefixed so a variable in Grok's environment cannot replace them, and
// PATH and GROK_SESSION_ID use the ${NAME:-} modifier form, which Grok leaves
// for sh. A SessionEnd that carries subagentType is a child session closing,
// not the session the exporter reports, so it posts nothing.
//
// The hook also sends the working directory as cwd. It reads workspaceRoot,
// which Grok sets to the git toplevel of the session directory, or that
// directory when it is not in a checkout (xai-grok-shell
// session/acp_session_impl/spawn.rs: find_git_root_from_path(session_info.cwd),
// else session_info.cwd; session/acp_session/hooks.rs make_hook_envelope).
// When workspaceRoot is missing it falls back to cwd, the session directory.
// The value is copied as the escaped JSON string body it already is, so a
// quote or backslash in the path stays escaped and cannot end the payload
// string; the server decodes it.
func grokSessionHookCommand(edge, url string) string {
	body := fmt.Sprintf(`{"tool":"grok-cli","session_id":"%%s","edge":%q,"reason":"%%s","cwd":"%%s"}`, edge)
	return fmt.Sprintf(`export PATH="${PATH:-}:/usr/bin:/bin"; sdf_json=$(cat); `+
		`case "$sdf_json" in *'"subagentType"'*) exit 0 ;; esac; `+
		`sdf_sid=${GROK_SESSION_ID:-}; `+
		`[ -n "$sdf_sid" ] || sdf_sid=$(printf '%%s' "$sdf_json" | %s); `+
		`[ -n "$sdf_sid" ] || sdf_sid=$(printf '%%s' "$sdf_json" | %s); [ -n "$sdf_sid" ] || exit 0; `+
		`sdf_reason=$(printf '%%s' "$sdf_json" | %s); `+
		`[ -n "$sdf_reason" ] || sdf_reason=$(printf '%%s' "$sdf_json" | %s); `+
		`sdf_cwd=$(printf '%%s' "$sdf_json" | %s); `+
		`[ -n "$sdf_cwd" ] || sdf_cwd=$(printf '%%s' "$sdf_json" | %s); `+
		`sdf_payload=$(printf '%s' "$sdf_sid" "$sdf_reason" "$sdf_cwd"); %s`+
		`printf '%%s\n' "$sdf_auth" | curl -sf -m 1 -X POST -H 'content-type: application/json' -H @- `+
		`--data "$sdf_payload" %s >/dev/null 2>&1 || true`,
		fieldExpr("sessionId"), fieldExpr("session_id"), fieldExpr("source"), fieldExpr("reason"),
		jsonStringExpr("workspaceRoot"), jsonStringExpr("cwd"), body, hookTokenShFor(url), shQuote(url))
}

// jsonStringExpr prints the body of one JSON string value with its escapes
// left in place. ([^"\\]|\\.)* cannot stop at an escaped quote, and an
// escaped key inside another value ("x\"workspaceRoot\":...") does not match,
// because the name is followed by a backslash there, not a quote.
func jsonStringExpr(name string) string {
	return `sed -n -E 's/.*"` + name + `"[[:space:]]*:[[:space:]]*"(([^"\\]|\\.)*)".*/\1/p'`
}

// piExtension reads the real session id. pi-otel derives pi.session.id from the
// session file name, which is the timestamp plus the id, so the collector would
// not join it to the spans. session_shutdown also fires when a session is
// replaced rather than ended, and those are not a close.
func piExtension(url string) string {
	return fmt.Sprintf(`// Reports a session opening and closing to the local sdf collector.
%s
import { spawnSync } from "node:child_process"

export default function sdfSession(pi) {
  const post = (edge, reason) => {
    let id = ""
    try { id = pi.sessionManager.getSessionId() } catch (e) { return }
    if (!id) return
    fetch(%q, {
      method: "POST",
      headers: sdfHeaders(),
      body: JSON.stringify({ tool: "pi", session_id: id, edge, reason: reason || "" }),
    }).catch(() => {})
  }
  pi.on("session_start", (event) => post("open", event && event.reason))
  pi.on("session_shutdown", (event) => {
    const reason = event && event.reason
    if (reason === "reload" || reason === "new" || reason === "resume" || reason === "fork") return
    post("close", reason)
  })
  // One more turn, at most, when this session committed and did not record
  // the decision. spawnSync throws nothing on a missing sdf; a non-zero
  // status or a body that does not ask to continue lets the session settle.
  pi.on("agent_before_settle", () => {
    let id = ""
    try { id = pi.sessionManager.getSessionId() } catch (e) { return }
    if (!id) return
    let parsed
    try {
      const res = spawnSync("sh", ["-c", "sdf hook stop --agent pi"], {
        input: JSON.stringify({ session_id: id }),
        encoding: "utf8",
        timeout: 2000,
      })
      if (!res || res.status !== 0 || !res.stdout) return
      parsed = JSON.parse(res.stdout)
    } catch (e) { return }
    if (parsed && parsed.continue === true) return parsed
  })
}
`, jsHeaders(url), url)
}
