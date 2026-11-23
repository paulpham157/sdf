package setup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Grok CLI (xai-org/grok-build) reads its external OpenTelemetry settings
// from the environment and from the [telemetry] table of
// $GROK_HOME/config.toml (default ~/.grok/config.toml). Environment wins over
// the file. Checked against grok-build main 2bdd1d6 (SOURCE_REV 559751fd,
// 2026-09-29) on 2026-11-23:
//
//   - crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md
//     ("Config file": otel_enabled, otel_metrics_exporter, otel_logs_exporter,
//     otel_endpoint, otel_protocol; "There is deliberately no headers key").
//   - crates/codegen/xai-grok-telemetry/src/config.rs (TelemetryConfig
//     fields with those names).
//   - crates/codegen/xai-grok-telemetry/src/external/config.rs
//     parse_header_list splits OTEL_EXPORTER_OTLP_HEADERS on "," and "=" and
//     does not percent-decode, so the bearer header needs a literal space.
//   - crates/codegen/xai-dirs/src/lib.rs (GROK_HOME, else ~/.grok).
//
// Hooks: Grok loads every $GROK_HOME/hooks/*.json as a global, always
// trusted hook file with Claude Code's {"hooks": {"SessionStart": [...]}}
// shape (user-guide/10-hooks.md, "Hook Locations"). The stdin envelope
// carries sessionId plus a snake_case session_id alias, source on
// SessionStart, reason on SessionEnd, and subagentType on a child session's
// SessionEnd (crates/codegen/xai-grok-hooks/src/event.rs,
// HookEventEnvelope, SNAKE_CASE_ALIASES, HookPayload). SessionStart does not
// fire for a subagent's own session.

// grokHome is $GROK_HOME or ~/.grok. A relative GROK_HOME resolves against
// the working directory.
func grokHome(req Request) string {
	if req.GrokHome != "" {
		return req.GrokHome
	}
	if dir := strings.TrimSpace(os.Getenv("GROK_HOME")); dir != "" {
		if !filepath.IsAbs(dir) {
			if wd, err := os.Getwd(); err == nil {
				dir = filepath.Join(wd, dir)
			}
		}
		return dir
	}
	return filepath.Join(req.Home, ".grok")
}

// grokEnvOrder is the export order printed for a shell. The values are the
// ones the Quick start in 24-monitoring-usage.md names.
var grokEnvOrder = []string{
	"GROK_EXTERNAL_OTEL",
	"OTEL_METRICS_EXPORTER",
	"OTEL_LOGS_EXPORTER",
	"OTEL_EXPORTER_OTLP_PROTOCOL",
	"OTEL_EXPORTER_OTLP_ENDPOINT",
}

func grokEnv(ep Endpoint) map[string]string {
	return map[string]string{
		"GROK_EXTERNAL_OTEL":          "1",
		"OTEL_METRICS_EXPORTER":       "otlp",
		"OTEL_LOGS_EXPORTER":          "otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT": ep.base(),
	}
}

// grokKeyOrder is the order sdf writes the [telemetry] keys in.
var grokKeyOrder = []string{
	"otel_enabled",
	"otel_metrics_exporter",
	"otel_logs_exporter",
	"otel_endpoint",
	"otel_protocol",
}

func grokKeys(ep Endpoint) map[string]string {
	return map[string]string{
		"otel_enabled":          "true",
		"otel_metrics_exporter": `"otlp"`,
		"otel_logs_exporter":    `"otlp"`,
		"otel_endpoint":         `"` + ep.base() + `"`,
		"otel_protocol":         `"http/protobuf"`,
	}
}

func grokKeyLines(ep Endpoint) string {
	keys := grokKeys(ep)
	var b strings.Builder
	for _, key := range grokKeyOrder {
		fmt.Fprintf(&b, "%s = %s\n", key, keys[key])
	}
	return b.String()
}

func grokBlock(ep Endpoint) string {
	return "[telemetry]\n" + grokKeyLines(ep)
}

// grokContentGates are the [telemetry] keys and environment variables that
// put prompt text, responses, or tool bodies on the external stream. All are
// off by default (24-monitoring-usage.md, "Environment variables").
var grokContentGates = []struct{ key, env string }{
	{"otel_log_user_prompts", "OTEL_LOG_USER_PROMPTS"},
	{"otel_log_assistant_responses", "OTEL_LOG_ASSISTANT_RESPONSES"},
	{"otel_log_tool_content", "OTEL_LOG_TOOL_CONTENT"},
}

func grok(req Request) (Result, error) {
	home := grokHome(req)
	path := filepath.Join(home, "config.toml")
	hookPath := filepath.Join(home, "hooks", "sdf-session.json")
	env := grokEnv(req.Endpoint)
	var b strings.Builder
	fmt.Fprintf(&b, "Grok CLI exports metrics and logs, not traces, once GROK_EXTERNAL_OTEL=1 and an exporter are set.\n")
	fmt.Fprintf(&b, "Set these in the shell that launches grok, or put the [telemetry] keys below in %s.\n", path)
	fmt.Fprintf(&b, "Environment variables win over the file.\n")
	fmt.Fprintf(&b, "Docs: https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md\n\n")
	for _, key := range grokEnvOrder {
		fmt.Fprintf(&b, "export %s=%s\n", key, shQuote(env[key]))
	}
	b.WriteString("\nIf sdf serve has a token, also export this (Grok does not percent-decode it, so keep the space):\n")
	b.WriteString(`export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $SDF_OTLP_TOKEN"` + "\n")
	b.WriteString("config.toml has no headers key; the token stays in the environment.\n\n")
	b.WriteString(grokBlock(req.Endpoint))
	for _, gate := range grokContentGates {
		if v := strings.TrimSpace(os.Getenv(gate.env)); v != "" && v != "0" && !strings.EqualFold(v, "false") {
			fmt.Fprintf(&b, "\nwarning: %s=%s in this shell exports content to the collector; unset it to keep prompts and bodies out of sdf\n", gate.env, v)
		}
	}
	if !req.Write {
		b.WriteString("\nnot written (pass --write to add the [telemetry] keys and the session hook file " + hookPath + ")\n")
		return Result{Text: b.String()}, nil
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Result{}, err
	}
	body, changed, warnings, err := grokMerge(existing, req.Endpoint)
	b.WriteString("\n")
	if err != nil {
		return Result{Text: b.String()}, fmt.Errorf("%s %v; it was not changed, and the session hook was not written", path, err)
	}
	for _, w := range warnings {
		b.WriteString("warning: " + w + "\n")
	}
	wroteAny := false
	switch {
	case !changed:
		b.WriteString(path + " already has these values\n")
	default:
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return Result{}, err
		}
		backup, err := copyAside(path)
		if err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(path, body, fileMode(path, 0o600)); err != nil {
			return Result{}, err
		}
		b.WriteString(wrote(path, backup))
		wroteAny = true
	}
	hookStatus, err := grokHookFile(hookPath, req.Endpoint.base()+"/v1/sessions")
	if err != nil {
		return Result{}, err
	}
	b.WriteString(hookStatus)
	if strings.HasPrefix(hookStatus, "wrote ") {
		wroteAny = true
	}
	return Result{Text: b.String(), Wrote: wroteAny}, nil
}

var (
	tomlHeader   = regexp.MustCompile(`^\s*\[\[?\s*([^\]]*?)\s*\]\]?\s*(#.*)?$`)
	tomlKeyValue = regexp.MustCompile(`^\s*([A-Za-z0-9_.\-"']+)\s*=\s*(.*?)\s*$`)
)

// grokMerge adds sdf's otel_* keys to the [telemetry] table of config.toml
// without a TOML library. It appends a new table when there is none, and
// otherwise inserts the keys sdf needs right after the [telemetry] header and
// rewrites only the lines of those same keys. Other keys, comments, and
// tables stay as they were. A file whose telemetry settings it cannot place
// safely (dotted telemetry.* keys, an inline telemetry table, more than one
// [telemetry] header, or multi-line strings) is left untouched with an error
// that says to add the block by hand.
func grokMerge(existing []byte, ep Endpoint) ([]byte, bool, []string, error) {
	want := grokKeys(ep)
	text := string(existing)
	if strings.Contains(text, `"""`) || strings.Contains(text, `'''`) {
		return nil, false, nil, errors.New("has a multi-line string, which sdf does not parse; add the [telemetry] keys shown above by hand")
	}
	lines := strings.Split(text, "\n")
	header := -1
	end := len(lines)
	table := ""
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if m := tomlHeader.FindStringSubmatch(line); m != nil {
			name := strings.ReplaceAll(m[1], " ", "")
			if table == "telemetry" && header >= 0 && end == len(lines) {
				end = i
			}
			table = name
			if name == "telemetry" {
				if header >= 0 {
					return nil, false, nil, errors.New("has more than one [telemetry] table")
				}
				header = i
				end = len(lines)
			} else if strings.HasPrefix(name, "telemetry.") {
				return nil, false, nil, fmt.Errorf("has a [%s] table; add the [telemetry] keys shown above by hand", name)
			}
			continue
		}
		if table == "" {
			if m := tomlKeyValue.FindStringSubmatch(line); m != nil {
				key := strings.Trim(m[1], `"'`)
				if key == "telemetry" || strings.HasPrefix(key, "telemetry.") {
					return nil, false, nil, errors.New("sets telemetry with a dotted key or an inline table; add the [telemetry] keys shown above by hand")
				}
			}
		}
	}
	var warnings []string
	if header < 0 {
		block := grokBlock(ep)
		if len(bytes.TrimSpace(existing)) == 0 {
			return []byte(block), true, nil, nil
		}
		body := append([]byte{}, existing...)
		if !bytes.HasSuffix(body, []byte("\n")) {
			body = append(body, '\n')
		}
		body = append(body, '\n')
		return append(body, block...), true, nil, nil
	}
	seen := map[string]bool{}
	changed := false
	out := make([]string, 0, len(lines)+len(grokKeyOrder))
	for i, line := range lines {
		if i > header && i < end {
			if m := tomlKeyValue.FindStringSubmatch(line); m != nil {
				key := strings.Trim(m[1], `"'`)
				value := stripTomlComment(m[2])
				if v, ok := want[key]; ok {
					if seen[key] {
						return nil, false, nil, fmt.Errorf("sets telemetry.%s more than once", key)
					}
					seen[key] = true
					if value != v {
						warnings = append(warnings, fmt.Sprintf("replacing telemetry.%s = %s with the sdf value", key, value))
						line = key + " = " + v
						changed = true
					}
				}
				for _, gate := range grokContentGates {
					if key == gate.key && value == "true" {
						warnings = append(warnings, fmt.Sprintf("telemetry.%s = true exports content to the collector; kept. Set it to false to keep it out of sdf", key))
					}
				}
			}
		}
		out = append(out, line)
		if i == header {
			for _, key := range grokKeyOrder {
				if !grokTableHas(lines[header+1:end], key) {
					out = append(out, key+" = "+want[key])
					changed = true
				}
			}
		}
	}
	if !changed {
		return existing, false, warnings, nil
	}
	return []byte(strings.Join(out, "\n")), true, warnings, nil
}

func grokTableHas(lines []string, key string) bool {
	for _, line := range lines {
		if m := tomlKeyValue.FindStringSubmatch(line); m != nil && strings.Trim(m[1], `"'`) == key {
			return true
		}
	}
	return false
}

// stripTomlComment drops a trailing # comment from a simple value. A # inside
// a basic or literal string is kept.
func stripTomlComment(value string) string {
	inBasic, inLiteral := false, false
	for i := 0; i < len(value); i++ {
		switch c := value[i]; {
		case c == '\\' && inBasic:
			i++
		case c == '"' && !inLiteral:
			inBasic = !inBasic
		case c == '\'' && !inBasic:
			inLiteral = !inLiteral
		case c == '#' && !inBasic && !inLiteral:
			return strings.TrimSpace(value[:i])
		}
	}
	return strings.TrimSpace(value)
}

// grokHookCommand is sessionHookCommand for Grok. Grok substitutes $VAR in a
// hook command at load time from its own environment and refuses a command
// whose plain $VAR stays unresolved unless the command assigns it
// (docs/custom-hooks.md, "Variable Substitution";
// xai-grok-hooks/src/runner/command.rs find_unresolved_env_vars). The local
// names are sdf_-prefixed so a variable in Grok's environment cannot replace
// them, and PATH uses ${PATH:-}, which Grok leaves for sh. A SessionEnd that
// carries subagentType is a child session closing, not the session the
// exporter reports, so it posts nothing.
//
// The hook also sends the working directory as cwd. It reads workspaceRoot,
// which Grok sets to the git toplevel of the session directory, or that
// directory when it is not in a checkout (xai-grok-shell
// session/acp_session_impl/spawn.rs: find_git_root_from_path(session_info.cwd),
// else session_info.cwd; session/acp_session/hooks.rs make_hook_envelope).
// When workspaceRoot is missing it falls back to cwd, the session directory.
// Both are camelCase keys of HookEventEnvelope (xai-grok-hooks/src/event.rs).
// The value is copied as the escaped JSON string body it already is, so a
// quote or backslash in the path stays escaped and cannot end the payload
// string; the server decodes it.
func grokHookCommand(edge, url string) string {
	body := fmt.Sprintf(`{"tool":"grok-cli","session_id":"%%s","edge":%q,"reason":"%%s","cwd":"%%s"}`, edge)
	return fmt.Sprintf(`export PATH="${PATH:-}:/usr/bin:/bin"; sdf_json=$(cat); `+
		`case "$sdf_json" in *'"subagentType"'*) exit 0 ;; esac; `+
		`sdf_sid=$(printf '%%s' "$sdf_json" | %s); [ -n "$sdf_sid" ] || exit 0; `+
		`sdf_reason=$(printf '%%s' "$sdf_json" | %s); `+
		`[ -n "$sdf_reason" ] || sdf_reason=$(printf '%%s' "$sdf_json" | %s); `+
		`sdf_cwd=$(printf '%%s' "$sdf_json" | %s); `+
		`[ -n "$sdf_cwd" ] || sdf_cwd=$(printf '%%s' "$sdf_json" | %s); `+
		`sdf_payload=$(printf '%s' "$sdf_sid" "$sdf_reason" "$sdf_cwd"); `+
		`curl -sf -m 1 -X POST -H 'content-type: application/json' `+
		`--data "$sdf_payload" %s >/dev/null 2>&1 || true`,
		fieldExpr("session_id"), fieldExpr("source"), fieldExpr("reason"),
		jsonStringExpr("workspaceRoot"), jsonStringExpr("cwd"), body, shQuote(url))
}

// jsonStringExpr prints the body of one JSON string value with its escapes
// left in place. ([^"\\]|\\.)* cannot stop at an escaped quote, and an
// escaped key inside another value ("x\"workspaceRoot\":...") does not match,
// because the name is followed by a backslash there, not a quote.
func jsonStringExpr(name string) string {
	return `sed -n -E 's/.*"` + name + `"[[:space:]]*:[[:space:]]*"(([^"\\]|\\.)*)".*/\1/p'`
}

func grokHookCommands(url string) map[string]string {
	return map[string]string{
		"SessionStart": grokHookCommand("open", url),
		"SessionEnd":   grokHookCommand("close", url),
	}
}

// grokHookFile writes $GROK_HOME/hooks/sdf-session.json, or merges sdf's
// hooks into an existing file of that name so other entries stay.
func grokHookFile(path, url string) (string, error) {
	return mergeJSON(path, true, func(root map[string]any) (bool, error) {
		return mergeSessionHooks(root, "grok-cli", grokHookCommands(url))
	})
}

func grokConfigured(req Request) string {
	path := filepath.Join(grokHome(req), "config.toml")
	want := req.Endpoint.base()
	envHit := os.Getenv("GROK_EXTERNAL_OTEL") == "1" && os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == want
	body, err := os.ReadFile(path)
	fileHit := false
	if err == nil {
		_, changed, _, mergeErr := grokMerge(body, req.Endpoint)
		fileHit = mergeErr == nil && !changed
	}
	hook := filepath.Join(grokHome(req), "hooks", "sdf-session.json")
	_, hookErr := os.Stat(hook)
	return fmt.Sprintf("grok process env GROK_EXTERNAL_OTEL=1 and OTEL_EXPORTER_OTLP_ENDPOINT=%s: %t; %s [telemetry] points at %s: %t; session hook %s: %t\n",
		want, envHit, path, want, fileHit, hook, hookErr == nil)
}
