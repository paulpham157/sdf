package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
)

const hookUsage = "usage: sdf hook stop --agent AGENT [--db PATH]"

// stopRemindReason is the one block a Stop hook may return, once per session.
const stopRemindReason = "phiên này có quyết định nào chưa ghi không?"

// stopHookBusyMS is the longest a Stop hook waits on the store lock. Past
// that, or on any read error, the hook lets the agent stop.
const stopHookBusyMS = 200

func cmdHook(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, hookUsage)
		return 2
	}
	switch args[0] {
	case "stop":
		return cmdHookStop(args[1:], stdout, stderr)
	case "--help", "-h":
		fmt.Fprintln(stdout, hookUsage)
		fmt.Fprintln(stdout, "stop  read the agent's Stop payload on stdin and, at most once per session, block with a reminder to record decisions")
		return 0
	default:
		unrecognizedArgs(stderr, args[0])
		return 2
	}
}

// cmdHookStop reads one agent Stop payload and, when the session committed
// without recording a decision, prints that agent's block JSON. It exits 0
// and prints nothing when the agent should be allowed to stop: the payload
// says this stop is already a continuation, this session was already
// reminded, there is no session id, the store cannot be read, or the facts
// do not warrant a reminder. A usage error exits 2.
func cmdHookStop(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	agent := ""
	agentSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, hookUsage)
			fmt.Fprintln(stdout, "Reads the Stop payload on stdin. Prints a block only when this session has an sdf.git.commit and no decision linked to it, or no implements edge from the session or those commits. Exits 0 either way, except for a usage error.")
			return 0
		case arg == "--agent":
			v, ok := take(&i, args)
			if !ok || v == "" {
				fmt.Fprintln(stderr, "sdf hook stop: --agent requires a value")
				return 2
			}
			agent, agentSet = v, true
		case strings.HasPrefix(arg, "--agent="):
			agent, agentSet = strings.TrimPrefix(arg, "--agent="), true
			if agent == "" {
				fmt.Fprintln(stderr, "sdf hook stop: --agent requires a value")
				return 2
			}
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf hook stop: --db requires a value")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	if !agentSet {
		fmt.Fprintln(stderr, hookUsage)
		return 2
	}
	canonical, ok := canonicalStopAgent(agent)
	if !ok {
		fmt.Fprintf(stderr, "sdf hook stop: unknown agent %q\n", agent)
		return 2
	}
	payload, ok := readStopPayload()
	if !ok {
		return 0
	}
	sessionID := stopSessionID(canonical, payload)
	if sessionID == "" || stopAlreadyContinuing(canonical, payload) {
		return 0
	}
	marker := dbPath + ".stop-reminded"
	if reminded, err := stopAlreadyReminded(marker, sessionID); err != nil || reminded {
		return 0
	}
	db, err := store.OpenReadOnlyBusy(dbPath, stopHookBusyMS)
	if err != nil {
		return 0
	}
	need, err := decision.UnrecordedDecision(db, sessionID)
	closeErr := db.Close()
	if err != nil || closeErr != nil || !need {
		return 0
	}
	fresh, err := markStopReminded(marker, sessionID)
	if err != nil || !fresh {
		return 0
	}
	if err := writeStopBlock(stdout, canonical); err != nil {
		return 0
	}
	return 0
}

func canonicalStopAgent(name string) (string, bool) {
	switch name {
	case "claude-code", "claude":
		return "claude-code", true
	case "codex":
		return "codex", true
	case "gemini", "gemini-cli":
		return "gemini", true
	case "qwen", "qwen-code":
		return "qwen", true
	case "grok", "grok-cli":
		return "grok", true
	case "pi":
		return "pi", true
	case "copilot", "copilot-cli":
		return "copilot", true
	case "cursor":
		return "cursor", true
	default:
		return "", false
	}
}

// readStopPayload reads the hook JSON. A terminal, an empty stdin, or a
// payload that is not an object yields an empty object. A non-empty body
// that is not JSON fails open (ok is false) so a broken pipe never blocks.
func readStopPayload() (map[string]any, bool) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return map[string]any{}, true
	}
	body, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return nil, false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}, true
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

func stopSessionID(agent string, payload map[string]any) string {
	field := func(keys ...string) string {
		for _, key := range keys {
			s, _ := payload[key].(string)
			s = strings.TrimSpace(s)
			if s != "" {
				return s
			}
		}
		return ""
	}
	env := func(key string) string { return strings.TrimSpace(os.Getenv(key)) }
	var id string
	switch agent {
	case "codex":
		if id = env("CODEX_THREAD_ID"); id == "" {
			id = field("session_id", "sessionId")
		}
	case "grok":
		if id = env("GROK_SESSION_ID"); id == "" {
			id = field("sessionId", "session_id")
		}
	case "claude-code":
		if id = field("session_id", "sessionId"); id == "" {
			id = env("CLAUDE_CODE_SESSION_ID")
		}
	case "qwen":
		if id = field("session_id", "sessionId"); id == "" {
			id = env("QWEN_CODE_SESSION_ID")
		}
	case "pi":
		if id = field("session_id", "sessionId"); id == "" {
			id = env("PI_SESSION_ID")
		}
	case "cursor":
		id = field("conversation_id", "session_id", "sessionId")
	case "copilot":
		id = field("sessionId", "session_id")
	default:
		if id = field("session_id", "sessionId"); id == "" {
			id = env("GEMINI_SESSION_ID")
		}
	}
	if !validStopSessionID(id) {
		return ""
	}
	return id
}

func validStopSessionID(id string) bool {
	return id != "" && len(id) <= 256 && !strings.ContainsAny(id, "\r\n")
}

// stopAlreadyContinuing is the agent's own "you already blocked this stop"
// signal: stop_hook_active, Grok's camelCase stopHookActive, a Grok Stop
// whose reason is not end_turn (session end is observe-only), and Cursor's
// loop_count or a non-completed status.
func stopAlreadyContinuing(agent string, payload map[string]any) bool {
	if truthy(payload["stop_hook_active"]) || truthy(payload["stopHookActive"]) {
		return true
	}
	switch agent {
	case "grok":
		reason, _ := payload["reason"].(string)
		if reason != "" && reason != "end_turn" {
			return true
		}
	case "cursor":
		status, _ := payload["status"].(string)
		if status == "aborted" || status == "error" {
			return true
		}
		if jsonNumber(payload["loop_count"]) > 0 {
			return true
		}
	}
	return false
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	default:
		return false
	}
}

func jsonNumber(v any) float64 {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0
		}
		return f
	case float64:
		return n
	case int:
		return float64(n)
	default:
		return 0
	}
}

type piStopEntry struct {
	Type       string `json:"type"`
	CustomType string `json:"customType"`
	Content    string `json:"content"`
	Display    bool   `json:"display"`
}

type piStopOutput struct {
	Continue bool          `json:"continue"`
	Entries  []piStopEntry `json:"entries"`
}

func writeStopBlock(w io.Writer, agent string) error {
	var v any
	switch agent {
	case "cursor":
		v = struct {
			Followup string `json:"followup_message"`
		}{stopRemindReason}
	case "pi":
		v = piStopOutput{
			Continue: true,
			Entries: []piStopEntry{{
				Type:       "custom_message",
				CustomType: "sdf-decision-reminder",
				Content:    stopRemindReason,
				Display:    false,
			}},
		}
	default:
		v = struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}{"block", stopRemindReason}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// stopRemindLockWait bounds how long two Stop hooks will queue on the marker.
const stopRemindLockWait = 200 * time.Millisecond
