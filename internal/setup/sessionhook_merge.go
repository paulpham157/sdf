package setup

import (
	"fmt"
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
	hooks, ok := root["hooks"].(map[string]any)
	if root["hooks"] != nil && !ok {
		return false, fmt.Errorf("hooks is not an object; it was not changed")
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
		groups, ok := hooks[event].([]any)
		if hooks[event] != nil && !ok {
			return false, fmt.Errorf("hooks.%s is not an array; it was not changed", event)
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

// isSdfSessionHook recognizes a command hook sdf wrote for tool: it posts with
// curl to /v1/sessions and names the tool in its payload, or uses the
// <TOOL>_HOOK_EDGE variable earlier sdf commands set. A user's own hooks do
// neither, so they are never rewritten.
func isSdfSessionHook(hook map[string]any, tool string) bool {
	command, _ := hook["command"].(string)
	if t, _ := hook["type"].(string); t != "command" || !strings.Contains(command, "/v1/sessions") || !strings.Contains(command, "curl") {
		return false
	}
	prefix := strings.ToUpper(strings.SplitN(tool, "-", 2)[0])
	return strings.Contains(command, `"tool":"`+tool+`"`) || strings.Contains(command, `\"tool\":\"`+tool+`\"`) ||
		strings.Contains(command, prefix+"_HOOK_EDGE")
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
