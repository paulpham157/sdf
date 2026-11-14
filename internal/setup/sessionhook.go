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
		`curl -sf -m 2 -X POST -H 'content-type: application/json' `+
		`--data "$payload" %s >/dev/null 2>&1 || true`,
		fieldExpr("session_id"), fieldExpr("source"), fieldExpr("reason"), body, url)
}

// claudeHooks is the "hooks" object merged into ~/.claude/settings.json. A
// command hook on SessionStart and SessionEnd. Both events carry session_id,
// which is the session.id the exporter sends.
func claudeHooks(url string) map[string]any {
	return map[string]any{
		"SessionStart": []any{hookGroup(sessionHookCommand("claude-code", "open", url))},
		"SessionEnd":   []any{hookGroup(sessionHookCommand("claude-code", "close", url))},
	}
}

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
	guard := `tid=$(printf '%s' "$json" | ` + fieldExpr("thread_id") + `); ` +
		`[ -z "$tid" ] || [ "$tid" = "$sid" ] || exit 0; `
	open := sessionHookCommand("codex", "open", url)
	closeCmd := sessionHookCommand("codex", "close", url)
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
`, insertGuard(open, guard), insertGuard(closeCmd, guard))
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
