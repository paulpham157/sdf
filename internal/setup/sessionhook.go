package setup

import (
	"fmt"
	"strings"
)

// sessionHookCommand reads the JSON an agent passes a command hook on stdin and
// posts one session edge to the collector. It exits 0 even when the collector
// is down, so a stopped sdf never blocks the agent. CLAUDE_HOOK_EDGE and
// CLAUDE_HOOK_TOOL are the only thing that differs between the two events, and
// Codex reuses the same command with CODEX_HOOK_EDGE.
// fieldExpr pulls the string value of one JSON key. The payload is one flat
// object, so a single match is enough and a missing key yields an empty string.
func fieldExpr(name string) string {
	return `sed -n 's/.*"` + name + `"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p'`
}

func sessionHookCommand(tool, edge, url string) string {
	body := fmt.Sprintf(`{"tool":%q,"session_id":"%%s","edge":%q,"reason":"%%s"}`, tool, edge)
	return fmt.Sprintf(`export PATH="$PATH:/usr/bin:/bin"; json=$(cat); `+
		`sid=$(printf '%%s' "$json" | %s); [ -n "$sid" ] || exit 0; `+
		`reason=$(printf '%%s' "$json" | %s); `+
		`[ -n "$reason" ] || reason=$(printf '%%s' "$json" | %s); `+
		`payload=$(printf '%s' "$sid" "$reason"); `+
		`curl -sf -m 1 -X POST -H 'content-type: application/json' `+
		`--data "$payload" %s >/dev/null 2>&1 || true`,
		fieldExpr("session_id"), fieldExpr("source"), fieldExpr("reason"), body, shQuote(url))
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

func hookGroup(command string) map[string]any {
	return map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": command}},
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
      {"hooks": [{"type": "command", "command": %q}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": %q}]}
    ]
  }
}
`, commands["SessionStart"], commands["SessionEnd"])
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

// opencodePlugin subscribes to session.created and posts the open. OpenCode has
// no event for the close: session.deleted fires when a session is deleted, and
// session.status going idle is the gap between turns. The in-process bus does
// not drop events the way the /api/event stream does.
func opencodePlugin(url string) string {
	return fmt.Sprintf(`// Reports a session opening to the local sdf collector.
export const SdfSession = async (ctx) => {
  ctx.event.subscribe({ types: ["session.created"] }).then(async (stream) => {
    for await (const ev of stream) {
      const id = ev && ev.sessionID
      if (!id) continue
      fetch(%q, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ tool: "opencode", session_id: id, edge: "open", reason: "" }),
      }).catch(() => {})
    }
  })
  return {}
}
`, url)
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
    ]
  }
}
`, grokSessionHookCommand("open", url), grokSessionHookCommand("close", url))
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
		`sdf_payload=$(printf '%s' "$sdf_sid" "$sdf_reason" "$sdf_cwd"); `+
		`curl -sf -m 1 -X POST -H 'content-type: application/json' `+
		`--data "$sdf_payload" %s >/dev/null 2>&1 || true`,
		fieldExpr("sessionId"), fieldExpr("session_id"), fieldExpr("source"), fieldExpr("reason"),
		jsonStringExpr("workspaceRoot"), jsonStringExpr("cwd"), body, shQuote(url))
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
export default function sdfSession(pi) {
  const post = (edge, reason) => {
    let id = ""
    try { id = pi.sessionManager.getSessionId() } catch (e) { return }
    if (!id) return
    fetch(%q, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ tool: "pi", session_id: id, edge, reason: reason || "" }),
    }).catch(() => {})
  }
  pi.on("session_start", (event) => post("open", event && event.reason))
  pi.on("session_shutdown", (event) => {
    const reason = event && event.reason
    if (reason === "reload" || reason === "new" || reason === "resume" || reason === "fork") return
    post("close", reason)
  })
}
`, url)
}
