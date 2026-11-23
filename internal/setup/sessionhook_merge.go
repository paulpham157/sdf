package setup

import (
	"fmt"
	neturl "net/url"
	"os"
	"sort"
	"strings"
)

// mergeSessionHooks adds sdf's session hook to each event array under
// root["hooks"] without touching the user's own entries. commands maps an
// event name (SessionStart, SessionEnd) to the command sdf wants there.
//
// An existing sdf hook, including one written by an older sdf with another
// host, port, or command text, is updated in place, so its group, matcher,
// and position stay where they are. Further sdf copies of the same event are
// removed, and a group left with no hooks by that removal is dropped. When the
// event has no sdf hook, one matcher-less group is appended after the user's
// groups. A second run with the same commands changes nothing.
func mergeSessionHooks(root map[string]any, tool string, commands map[string]string) (bool, error) {
	raw, present := root["hooks"]
	hooks, ok := raw.(map[string]any)
	if present && !ok {
		return false, fmt.Errorf("hooks is not an object (%s); it was not changed", jsonKind(raw))
	}
	if hooks == nil {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	events := make([]string, 0, len(commands))
	for event := range commands {
		events = append(events, event)
	}
	sort.Strings(events)
	changed := false
	for _, event := range events {
		raw, present := hooks[event]
		groups, ok := raw.([]any)
		if present && !ok {
			return false, fmt.Errorf("hooks.%s is not an array (%s); it was not changed", event, jsonKind(raw))
		}
		merged, eventChanged := mergeEventHooks(groups, tool, commands[event])
		if eventChanged {
			hooks[event] = merged
			changed = true
		}
	}
	return changed, nil
}

func mergeEventHooks(groups []any, tool, command string) ([]any, bool) {
	found, changed := false, false
	out := make([]any, 0, len(groups)+1)
	for _, g := range groups {
		group, ok := g.(map[string]any)
		list, listOK := group["hooks"].([]any)
		if !ok || !listOK {
			out = append(out, g)
			continue
		}
		kept := make([]any, 0, len(list))
		for _, h := range list {
			hook, ok := h.(map[string]any)
			if !ok || !isSdfSessionHook(hook, tool) {
				kept = append(kept, h)
				continue
			}
			if found {
				changed = true
				continue
			}
			found = true
			if hook["command"] != command {
				hook["command"] = command
				changed = true
			}
			kept = append(kept, hook)
		}
		if len(kept) == 0 && len(list) > 0 {
			continue
		}
		if len(kept) != len(list) {
			group["hooks"] = kept
		}
		out = append(out, group)
	}
	if !found {
		out = append(out, hookGroup(command))
		changed = true
	}
	return out, changed
}

// jsonKind names a decoded JSON value's type for an error message.
func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	default:
		return "a number"
	}
}

// isSdfSessionHook recognizes a command hook sdf wrote for tool. Only the
// exact command text an sdf build has generated matches: the current
// sessionHookCommand, or one of the earlier forms in sdfHookPosts, for either
// edge, with any receiver URL (an earlier init may have used another host or
// port). sdf has no separate marker in the hook, so the whole command is the
// marker. A user's hook that merely curls /v1/sessions, or names the tool in
// a payload, or wraps sdf's command in more shell, is never matched, so it is
// never rewritten or removed. testdata/sdf_session_hooks.tsv holds every form
// sdf has written; a test checks each one still matches.
func isSdfSessionHook(hook map[string]any, tool string) bool {
	if t, _ := hook["type"].(string); t != "command" {
		return false
	}
	command, _ := hook["command"].(string)
	if tool == "claude-code" {
		command = strings.TrimPrefix(command, claudeGuard)
	}
	for _, edge := range []string{"open", "close"} {
		rest, ok := strings.CutPrefix(command, sdfHookHead(tool, edge))
		if !ok {
			continue
		}
		for _, post := range sdfHookPosts {
			if url, ok := strings.CutPrefix(rest, post); ok {
				if url, ok := strings.CutSuffix(url, sdfHookEnd); ok && sdfHookURL(url) {
					return true
				}
			}
		}
	}
	return false
}

// sdfHookHead is the start of sdf's hook command for tool and edge, through
// the payload assignment. It has not changed since the first hook sdf wrote,
// apart from the Codex thread guard, which every Codex form has had.
func sdfHookHead(tool, edge string) string {
	const url = "http://127.0.0.1:1/v1/sessions"
	command := sessionHookCommand(tool, edge, url)
	if tool == "codex" {
		event := "SessionStart"
		if edge == "close" {
			event = "SessionEnd"
		}
		command = codexHookCommands(url)[event]
	}
	const payloadEnd = `"$sid" "$reason"); `
	i := strings.Index(command, payloadEnd)
	if i < 0 {
		return "\x00"
	}
	return command[:i+len(payloadEnd)]
}

// sdfHookPost is the post sdf hooks make since the receiver token (#206).
const sdfHookPost = `printf '%s\n' "$sdf_auth" | curl -sf -m 1 -X POST -H 'content-type: application/json' -H @- --data "$payload" `

// sdfHookPosts is the text between the payload assignment and the URL, in
// every form sdf has written, newest first.
var sdfHookPosts = []string{
	hookTokenSh + sdfHookPost,   // loopback receiver, current
	`sdf_auth=; ` + sdfHookPost, // other receiver, since #206
	`sdf_tok=$(printf '%s' "${SDF_OTLP_TOKEN:-}" | tr -d '[:space:]'); if [ -z "$sdf_tok" ] && [ -n "${SDF_OTLP_TOKEN_FILE:-}" ]; then sdf_tok=$(tr -d '[:space:]' < "${SDF_OTLP_TOKEN_FILE:-}" 2>/dev/null); fi; sdf_auth=; [ -z "$sdf_tok" ] || sdf_auth="Authorization: Bearer $sdf_tok"; ` + sdfHookPost, // loopback, #206
	`curl -sf -m 1 -X POST -H 'content-type: application/json' --data "$payload" `, // #203
	`curl -sf -m 2 -X POST -H 'content-type: application/json' --data "$payload" `, // the first hook
}

const sdfHookEnd = ` >/dev/null 2>&1 || true`

// sdfHookURL accepts the URL argument of an sdf hook: an http URL to
// /v1/sessions, single-quoted as shQuote writes it, or bare as the first
// hook wrote it.
func sdfHookURL(arg string) bool {
	if inner, ok := strings.CutPrefix(arg, "'"); ok {
		inner, ok = strings.CutSuffix(inner, "'")
		if !ok || strings.Contains(inner, "'") {
			return false
		}
		arg = inner
	} else if strings.ContainsAny(arg, " \t\n'\"\\$;&|<>()`*?[]{}~#") {
		return false
	}
	u, err := neturl.Parse(arg)
	return err == nil && u.Scheme == "http" && u.Host != "" && u.User == nil &&
		u.Path == "/v1/sessions" && u.RawQuery == "" && u.Fragment == "" && !u.ForceQuery
}

// codexHookFile writes a new hooks.json, or merges sdf's hooks into an
// existing one so the user's own hooks and other keys stay.
func codexHookFile(path, url string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return writeBytes(path, []byte(codexHooks(url)), true)
	}
	return mergeJSON(path, true, func(root map[string]any) (bool, error) {
		return mergeSessionHooks(root, "codex", codexHookCommands(url))
	})
}

// codexHookPreview is the dry-run line for the hooks.json merge --write
// does after the [otel] table (or instead of it, when the table is already
// there): what it would change, or why it would refuse.
func codexHookPreview(path, url string) string {
	_, changed, err := planJSON(path, func(root map[string]any) (bool, error) {
		return mergeSessionHooks(root, "codex", codexHookCommands(url))
	})
	switch {
	case err != nil:
		return fmt.Sprintf("hooks: --write will not change %s: %v\n", path, err)
	case !changed:
		return "hooks: " + path + " already has sdf's SessionStart and SessionEnd hooks\n"
	default:
		return "hooks: --write also merges sdf's SessionStart and SessionEnd hooks into " + path +
			" (your own hooks there are kept; an existing file is copied to a .sdf-bak sibling first)\n"
	}
}
