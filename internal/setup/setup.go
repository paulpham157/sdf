// Package setup prints or writes the local OTLP configuration for one agent.
//
// Claude Code variables and the user settings file are documented at
// https://code.claude.com/docs/en/monitoring-usage
// A repository .claude/settings.json cannot turn the exporters on.
//
// Codex reads [otel] from $CODEX_HOME/config.toml (default ~/.codex/config.toml),
// not from a project .codex/config.toml:
// https://developers.openai.com/codex/config-reference
// https://github.com/openai/codex/blob/main/codex-rs/otel/README.md
//
// OpenCode v2 (@opencode/cli) exports traces and logs when
// OTEL_EXPORTER_OTLP_ENDPOINT is set. It does not export metrics. Checked
// against @opencode/cli 2.2.6 on 2026-11-10. The v1 experimental.openTelemetry
// flag is not the v2 switch.
//
// GitHub Copilot CLI exports traces and metrics over OTLP/HTTP (http/json by
// default) when OTEL_EXPORTER_OTLP_ENDPOINT is set. It has no OTLP log
// signal and no settings-file key for the endpoint, so setup is environment
// only. Content capture is off unless OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT
// asks for it. An http:// endpoint is dropped when an OTLP certificate or
// client key variable is set. Checked against `copilot help monitoring` in
// Copilot CLI 1.0.114 on 2026-11-23.
// https://docs.github.com/en/copilot/reference/enterprise-administrators/opentelemetry-reference
//
// Pi has no built-in OTLP exporter. pi-otel 0.3.0 reads .pi/settings.json in
// the project and ~/.pi/agent/settings.json, then OTEL_* and PI_OTEL_* env.
// Its default protocol is grpc at 127.0.0.1:4317. This setup uses http/json
// on the receiver's HTTP port. grpc on 4317 also reaches sdf serve.
// https://github.com/NikiforovAll/pi-otel
//
// Gemini CLI reads the telemetry block of ~/.gemini/settings.json. A
// workspace .gemini/settings.json (trusted folders only) overrides it, and the
// system file /etc/gemini-cli/settings.json overrides both. GEMINI_TELEMETRY_*
// env, then OTEL_EXPORTER_OTLP_ENDPOINT for the endpoint, override every file. Its default protocol is grpc at localhost:4317. This
// setup selects http on the receiver's HTTP port, and sets logPrompts to false
// only when the file does not already choose (Gemini's default is true).
// https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/telemetry.md
//
// Grok CLI exports metrics and logs (no traces) when GROK_EXTERNAL_OTEL=1
// and an OTLP exporter is set. The user config is $GROK_HOME/config.toml
// (default ~/.grok/config.toml). A project .grok/config.toml cannot set
// otel_*. Env vars win over that file. Checked against the xAI monitoring
// guide and grok 1.0.92 on 2026-11-23.
// https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md
package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Endpoint is the local sdf serve address the agent should use.
type Endpoint struct {
	Host     string
	HTTPPort int
	GRPCPort int
}

// Request is one init invocation.
type Request struct {
	Agent     string
	Home      string
	CodexHome string
	// GrokHome is $GROK_HOME, or ~/.grok when that variable is unset.
	GrokHome   string
	ProjectDir string
	Endpoint   Endpoint
	Write      bool
	Install    bool
	// Global writes Pi's ~/.pi/agent/settings.json instead of .pi/settings.json.
	// It applies only to pi. Project settings stay untouched.
	Global bool
	// RunInstall replaces `pi install` in tests. Nil runs pi on PATH.
	RunInstall func(ctx context.Context) (string, error)
}

// Result is the text to print. Wrote is true when a file changed.
type Result struct {
	Text  string
	Wrote bool
}

// knownAgentsHint is the parenthetical list on unknown-agent errors.
// Keep it in the same order as CLI help (initAgents).
const knownAgentsHint = "claude-code, codex, opencode, pi, qwen, grok, gemini, copilot"

// Run prints the agent setup. It writes files only when Request.Write is set,
// and it copies an existing file to a sibling .sdf-bak before replacing bytes.
func Run(req Request) (Result, error) {
	if err := normalize(&req); err != nil {
		return Result{}, err
	}
	switch req.Agent {
	case "claude-code":
		return claude(req)
	case "copilot", "copilot-cli":
		return copilot(req)
	case "codex":
		return codex(req)
	case "opencode":
		return opencode(req)
	case "qwen", "qwen-code":
		return qwen(req)
	case "pi":
		return pi(req)
	case "grok", "grok-cli":
		return grok(req)
	case "gemini", "gemini-cli":
		return gemini(req)
	default:
		return Result{}, fmt.Errorf("unknown agent %q (%s)", req.Agent, knownAgentsHint)
	}
}

func normalize(req *Request) error {
	if req.Install && req.Agent != "pi" && req.Agent != "" {
		return errors.New("--install applies to pi")
	}
	if req.Global && req.Agent != "pi" {
		return errors.New("--global applies to pi")
	}
	if req.Endpoint.Host == "" {
		req.Endpoint.Host = "127.0.0.1"
	}
	if req.Endpoint.HTTPPort == 0 {
		req.Endpoint.HTTPPort = 4318
	}
	if req.Endpoint.GRPCPort == 0 {
		req.Endpoint.GRPCPort = 4317
	}
	if err := checkEndpoint(&req.Endpoint); err != nil {
		return err
	}
	if req.Home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		req.Home = home
	}
	if req.CodexHome == "" {
		if env := os.Getenv("CODEX_HOME"); env != "" {
			req.CodexHome = env
		} else {
			req.CodexHome = filepath.Join(req.Home, ".codex")
		}
	}
	if req.GrokHome == "" {
		if env := os.Getenv("GROK_HOME"); env != "" {
			req.GrokHome = env
		} else {
			req.GrokHome = filepath.Join(req.Home, ".grok")
		}
	}
	if req.ProjectDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		req.ProjectDir = wd
	}
	return nil
}

func claude(req Request) (Result, error) {
	env := claudeEnv(req.Endpoint)
	path := filepath.Join(req.Home, ".claude", "settings.json")
	var b strings.Builder
	fmt.Fprintf(&b, "Claude Code telemetry is off until CLAUDE_CODE_ENABLE_TELEMETRY=1.\n")
	fmt.Fprintf(&b, "Set these in the shell that launches claude, or in %s under env.\n", path)
	fmt.Fprintf(&b, "A repository .claude/settings.json cannot turn these exporters on.\n")
	fmt.Fprintf(&b, "Docs: https://code.claude.com/docs/en/monitoring-usage\n\n")
	for _, key := range claudeOrder {
		fmt.Fprintf(&b, "export %s\n", quoteEndpointLine(key+"="+env[key]))
	}
	merge := func(root map[string]any) (bool, error) {
		envChanged, err := mergeStringMap(root, "env", env)
		if err != nil {
			return false, err
		}
		hooksChanged, err := mergeInitHooks(root, "claude-code", claudeHooks(req.Endpoint.base()+"/v1/sessions"))
		if err != nil {
			return false, err
		}
		return envChanged || hooksChanged, nil
	}
	status, err := mergeJSON(path, req.Write, merge)
	if err != nil {
		return Result{}, err
	}
	if !req.Write {
		envShape := map[string]any{}
		for key, value := range env {
			envShape[key] = value
		}
		status = previewJSON(path, merge, map[string]any{"env": envShape, "hooks": claudeHooks(req.Endpoint.base() + "/v1/sessions")})
	}
	b.WriteString("\n")
	b.WriteString(status)
	b.WriteString(hookTokenNoteFor(req.Endpoint))
	b.WriteString(decisionBriefStoreNote())
	return Result{Text: b.String(), Wrote: strings.HasPrefix(status, "wrote ")}, nil
}

var claudeOrder = []string{
	"CLAUDE_CODE_ENABLE_TELEMETRY",
	"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA",
	"OTEL_TRACES_EXPORTER",
	"OTEL_METRICS_EXPORTER",
	"OTEL_LOGS_EXPORTER",
	"OTEL_EXPORTER_OTLP_PROTOCOL",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	"OTEL_METRIC_EXPORT_INTERVAL",
	"OTEL_LOGS_EXPORT_INTERVAL",
	"OTEL_TRACES_EXPORT_INTERVAL",
}

func claudeEnv(ep Endpoint) map[string]string {
	base := ep.base()
	return map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY":        "1",
		"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "1",
		"OTEL_TRACES_EXPORTER":                "otlp",
		"OTEL_METRICS_EXPORTER":               "otlp",
		"OTEL_LOGS_EXPORTER":                  "otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL":         "http/protobuf",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":  base + "/v1/traces",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": base + "/v1/metrics",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT":    base + "/v1/logs",
		"OTEL_METRIC_EXPORT_INTERVAL":         "10000",
		"OTEL_LOGS_EXPORT_INTERVAL":           "5000",
		"OTEL_TRACES_EXPORT_INTERVAL":         "5000",
	}
}

func codex(req Request) (Result, error) {
	path := filepath.Join(req.CodexHome, "config.toml")
	block := codexBlock(req.Endpoint)
	var b strings.Builder
	fmt.Fprintf(&b, "Codex does not read OTEL_*. Put this [otel] table in %s.\n", path)
	fmt.Fprintf(&b, "A project .codex/config.toml cannot set otel.\n")
	fmt.Fprintf(&b, "Docs: https://developers.openai.com/codex/config-reference\n")
	fmt.Fprintf(&b, "https://github.com/openai/codex/blob/main/codex-rs/otel/README.md\n\n")
	b.WriteString(block)
	hooks := filepath.Join(req.CodexHome, "hooks.json")
	hookURL := req.Endpoint.base() + "/v1/sessions"
	if !req.Write {
		b.WriteString("\nnot written (pass --write to append this block; an existing [otel] table is left unchanged)\n")
		b.WriteString(codexHookPreview(hooks, hookURL))
		b.WriteString(decisionBriefStoreNote())
		return Result{Text: b.String()}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Result{}, err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Result{}, readErr(path, err)
	}
	// An existing [otel] table is left alone, but the session hook is still
	// merged: a user who set up [otel] by hand would otherwise get no hook.
	if hasOtelTable(string(existing)) {
		if err := checkAllWritable(hooks); err != nil {
			return Result{}, err
		}
		b.WriteString("\nnot written: " + path + " already has an [otel] table; it was not changed\n")
		hookStatus, err := codexHookFile(hooks, hookURL)
		if err != nil {
			return Result{}, err
		}
		b.WriteString(hookStatus)
		b.WriteString(hookTokenNoteFor(req.Endpoint))
		b.WriteString(decisionBriefStoreNote())
		return Result{Text: b.String(), Wrote: strings.HasPrefix(hookStatus, "wrote ")}, nil
	}
	if err := checkAllWritable(path, hooks); err != nil {
		return Result{}, err
	}
	var body []byte
	if len(bytes.TrimSpace(existing)) == 0 {
		body = []byte(block)
	} else {
		body = existing
		if !bytes.HasSuffix(body, []byte("\n")) {
			body = append(body, '\n')
		}
		body = append(body, '\n')
		body = append(body, []byte(block)...)
		if _, err := tomlHasKey(existing, "otel"); err != nil {
			return Result{}, fmt.Errorf("%s is not valid TOML (%v); Codex cannot load it either. Fix it and rerun; it was not changed", path, err)
		}
		if _, err := tomlHasKey(body, "otel"); err != nil {
			return Result{}, fmt.Errorf("%s: appending the [otel] table would not be valid TOML (%v); add the printed keys by hand; it was not changed", path, err)
		}
	}
	// Plan the hooks.json merge before config.toml is touched, so a corrupt
	// or refused hooks.json (null, not an object) leaves both files as they
	// were instead of [otel] written and the hook missing.
	if err := codexHookCheck(hooks, hookURL); err != nil {
		return Result{}, err
	}
	status, err := replaceFile(path, body, 0o600)
	if err != nil {
		return Result{}, err
	}
	b.WriteString("\n")
	b.WriteString(status)
	hookStatus, err := codexHookFile(hooks, hookURL)
	if err != nil {
		return Result{}, err
	}
	b.WriteString(hookStatus)
	b.WriteString(hookTokenNoteFor(req.Endpoint))
	b.WriteString(decisionBriefStoreNote())
	return Result{Text: b.String(), Wrote: true}, nil
}

func codexBlock(ep Endpoint) string {
	base := ep.base()
	return strings.TrimSpace(fmt.Sprintf(`
# Codex reads [otel] from $CODEX_HOME/config.toml, not from a project config.
# https://developers.openai.com/codex/config-reference
[otel]
environment = "dev"
log_user_prompt = false
exporter = { otlp-http = { endpoint = "%s/v1/logs", protocol = "binary" } }
trace_exporter = { otlp-http = { endpoint = "%s/v1/traces", protocol = "binary" } }
metrics_exporter = { otlp-http = { endpoint = "%s/v1/metrics", protocol = "binary" } }
`, base, base, base)) + "\n"
}

// hasOtelTable reports whether config.toml sets the top-level otel key in
// any TOML form ([otel], [ otel ], ["otel"], [otel.sub], inline
// otel = {...}, dotted otel.x = ...), the same parse as grokHasTelemetry.
// A file that does not parse reports false; codex refuses it before writing.
func hasOtelTable(text string) bool {
	has, err := tomlHasKey([]byte(text), "otel")
	return err == nil && has
}

func opencode(req Request) (Result, error) {
	path := filepath.Join(req.Home, ".config", "sdf", "opencode.env")
	line := "export OTEL_EXPORTER_OTLP_ENDPOINT=" + shQuote(req.Endpoint.base()) + "\n"
	var b strings.Builder
	fmt.Fprintf(&b, "OpenCode v2 (@opencode/cli) exports traces and logs when OTEL_EXPORTER_OTLP_ENDPOINT is set.\n")
	fmt.Fprintf(&b, "It does not export metrics. npm opencode-ai is v1, not this CLI.\n")
	fmt.Fprintf(&b, "Checked against @opencode/cli 2.2.6 on 2026-11-10. v1's experimental.openTelemetry flag is not the v2 switch.\n\n")
	b.WriteString(line)
	b.WriteString("\nOpenCode reads the variable from the environment of the opencode process.\n")
	fmt.Fprintf(&b, "--write saves %s. OpenCode does not load that file by itself; source it in the shell that starts opencode.\n", path)
	if !req.Write {
		b.WriteString("not written (pass --write)\n")
		return Result{Text: b.String()}, nil
	}
	plugin := filepath.Join(req.Home, ".config", "opencode", "plugins", "sdf-session.js")
	if err := checkAllWritable(path, plugin); err != nil {
		return Result{}, err
	}
	status, err := writeBytes(path, []byte("# source this file before starting opencode\n"+line), req.Write)
	if err != nil {
		return Result{}, err
	}
	b.WriteString(status)
	hookStatus, err := writeBytes(plugin, []byte(opencodePlugin(req.Endpoint.base()+"/v1/sessions")), req.Write)
	if err != nil {
		return Result{}, err
	}
	b.WriteString(hookStatus)
	b.WriteString(hookTokenNoteFor(req.Endpoint))
	return Result{Text: b.String(), Wrote: strings.HasPrefix(status, "wrote ") || strings.HasPrefix(hookStatus, "wrote ")}, nil
}

func pi(req Request) (Result, error) {
	path := filepath.Join(req.ProjectDir, ".pi", "settings.json")
	global := filepath.Join(req.Home, ".pi", "agent", "settings.json")
	target := path
	if req.Global {
		target = global
	}
	otel := piOtel(req.Endpoint)
	var b strings.Builder
	fmt.Fprintf(&b, "Pi does not export OTLP. Install the community extension pi-otel (the setup checked here is 0.3.0):\n\n")
	fmt.Fprintf(&b, "pi install npm:pi-otel\n\n")
	fmt.Fprintf(&b, "pi-otel defaults to grpc at http://127.0.0.1:4317. sdf serve accepts that.\n")
	fmt.Fprintf(&b, "The variables below select http/json on the HTTP receiver instead.\n")
	fmt.Fprintf(&b, "Metrics and logs are off unless PI_OTEL_METRICS and PI_OTEL_LOGS are set.\n")
	fmt.Fprintf(&b, "Project config is %s. Global config is %s. pi-otel reads the project file first, then the global file. Environment overrides both.\n", path, global)
	if req.Global {
		fmt.Fprintf(&b, "--write --global updates %s and does not change the project file.\n", global)
	} else {
		fmt.Fprintf(&b, "--write updates %s. Pass --global to update %s instead.\n", path, global)
	}
	fmt.Fprintf(&b, "https://github.com/NikiforovAll/pi-otel\n\n")
	fmt.Fprintf(&b, "export OTEL_EXPORTER_OTLP_ENDPOINT=%s\n", shQuote(req.Endpoint.base()))
	fmt.Fprintf(&b, "export OTEL_EXPORTER_OTLP_PROTOCOL=http/json\n")
	fmt.Fprintf(&b, "export PI_OTEL_METRICS=1\n")
	fmt.Fprintf(&b, "export PI_OTEL_LOGS=1\n")
	fmt.Fprintf(&b, "export PI_OTEL_SPAN_NAMING=genai\n\n")
	piDir := filepath.Join(req.ProjectDir, ".pi", "extensions")
	if req.Global {
		piDir = filepath.Join(req.Home, ".pi", "agent", "extensions")
	}
	extension := filepath.Join(piDir, "sdf-session.js")
	if req.Write {
		if err := checkAllWritable(target, extension); err != nil {
			return Result{}, err
		}
	}
	merge := func(root map[string]any) (bool, error) {
		return mergeAnyMap(root, "otel", otel)
	}
	status, err := mergeJSON(target, req.Write, merge)
	if err != nil {
		return Result{}, err
	}
	if !req.Write {
		status = previewJSON(target, merge, map[string]any{"otel": piOtel(req.Endpoint)})
	}
	b.WriteString(status)
	extStatus, err := writeBytes(extension, []byte(piExtension(req.Endpoint.base()+"/v1/sessions")), req.Write)
	if err != nil {
		return Result{}, err
	}
	b.WriteString(extStatus)
	b.WriteString(hookTokenNoteFor(req.Endpoint))
	if req.Install {
		out, err := runPiInstall(req)
		b.WriteString("\n")
		b.WriteString(out)
		if err != nil {
			return Result{Text: b.String(), Wrote: strings.HasPrefix(status, "wrote ")}, err
		}
	} else {
		b.WriteString("\npi install was not run (pass --install)\n")
	}
	return Result{Text: b.String(), Wrote: strings.HasPrefix(status, "wrote ")}, nil
}

func grok(req Request) (Result, error) {
	path := filepath.Join(req.GrokHome, "config.toml")
	hookPath := filepath.Join(req.GrokHome, "hooks", "sdf-session.json")
	block := grokBlock(req.Endpoint)
	var b strings.Builder
	fmt.Fprintf(&b, "Grok CLI exports metrics and logs, not traces, when GROK_EXTERNAL_OTEL=1 and an OTLP exporter is set.\n")
	fmt.Fprintf(&b, "Set these in the shell that launches grok, or in %s under [telemetry].\n", path)
	fmt.Fprintf(&b, "A project .grok/config.toml cannot set otel_*. Environment variables win over config.toml.\n")
	fmt.Fprintf(&b, "Checked against grok 1.0.92 on 2026-11-23. The schema is grok_code.schema.version v1 (alpha).\n")
	fmt.Fprintf(&b, "https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md\n\n")
	for _, line := range grokEnv(req.Endpoint) {
		fmt.Fprintf(&b, "export %s\n", quoteEndpointLine(line))
	}
	b.WriteString("\nIf sdf serve has a token, also export this. A literal space after Bearer always works:\n")
	b.WriteString(`export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $SDF_OTLP_TOKEN"` + "\n")
	b.WriteString("config.toml has no headers key; the token stays in the environment.\n\n")
	b.WriteString(block)
	b.WriteString("\n")
	for _, gate := range grokContentGates {
		if v := os.Getenv(gate); grokEnvTrue(v) {
			b.WriteString(grokGateWarning(gate, strings.TrimSpace(v)))
		}
	}
	if !req.Write {
		fmt.Fprintf(&b, "not written (pass --write to append this block to %s; an existing [telemetry] table is left unchanged)\n", path)
		fmt.Fprintf(&b, "not written (pass --write to install %s)\n", hookPath)
		return Result{Text: b.String()}, nil
	}
	configStatus, configWrote, err := writeGrokConfig(path, hookPath, block)
	if err != nil {
		return Result{}, err
	}
	b.WriteString(configStatus)
	hookStatus, err := writeBytes(hookPath, []byte(grokHooks(req.Endpoint.base()+"/v1/sessions")), true)
	if err != nil {
		return Result{}, err
	}
	b.WriteString(hookStatus)
	b.WriteString(hookTokenNoteFor(req.Endpoint))
	return Result{Text: b.String(), Wrote: configWrote || strings.HasPrefix(hookStatus, "wrote ")}, nil
}

// hookTokenNote follows a written session hook. The hook reads the token
// when it runs; init never copies it into the file.
const hookTokenNote = "If sdf serve requires a token, the session hook sends it when SDF_OTLP_TOKEN or SDF_OTLP_TOKEN_FILE is set where the agent starts; the token is not written to the hook.\n"

// hookTokenNoteFor is hookTokenNote for a loopback endpoint. For any other
// host the hook never sends the token (hookTokenShFor), and says so.
func hookTokenNoteFor(ep Endpoint) string {
	if loopbackURL(ep.base()) {
		return hookTokenNote
	}
	return "The session hook does not send a receiver token: " + ep.Host + " is not loopback, and the token is only sent to localhost, 127.0.0.1, or ::1 over plain HTTP.\n"
}

// grokContentGates are the environment variables that put prompt text,
// responses, tool metadata, or tool bodies on Grok's external stream. All are
// off by default (24-monitoring-usage.md, "Environment variables"), and the
// [telemetry] table sdf writes pins all four off; a set variable wins over
// the file, so init warns about each one that turns a gate on.
var grokContentGates = []string{"OTEL_LOG_USER_PROMPTS", "OTEL_LOG_ASSISTANT_RESPONSES", "OTEL_LOG_TOOL_DETAILS", "OTEL_LOG_TOOL_CONTENT"}

// grokEnvTrue is Grok's env_bool for the OTEL_LOG_* gates
// (xai-grok-telemetry/src/external/config.rs at grok-build 2bdd1d6): trimmed
// and lowercased, 1/true/yes/on is on, 0/false/no/off/"" is off, and any
// other value is ignored, so the config file (pinned false) applies.
func grokEnvTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// grokGateWarning is the warning for one gate env var that Grok reads as on.
func grokGateWarning(gate, value string) string {
	if gate == "OTEL_LOG_TOOL_DETAILS" {
		return fmt.Sprintf("warning: %s=%s in this shell overrides otel_log_tool_details = false: Grok exports tool metadata (a 4 KB tool_parameters preview, full file paths, verbatim MCP/skill/plugin names), not bodies. sdf does not need it for Grok (the session hook supplies the working directory); unset it for grok if you set it for Claude Code's commit linking\n", gate, value)
	}
	return fmt.Sprintf("warning: %s=%s in this shell exports content to the collector and overrides the pinned config; unset it to keep prompts and bodies out of sdf\n", gate, value)
}

// quoteEndpointLine shQuotes the value of a KEY=VALUE line whose key names
// an OTLP endpoint, the one value built from --host and --port. checkEndpoint
// already keeps the host inert; quoting the printed line as well matches the
// files sdf writes (opencode.env, copilot.env) for a line meant to be pasted
// into a shell. Constant values (1, otlp, http/json) are printed as they are.
func quoteEndpointLine(line string) string {
	key, value, ok := strings.Cut(line, "=")
	if !ok || !strings.HasSuffix(key, "_ENDPOINT") {
		return line
	}
	return key + "=" + shQuote(value)
}

func grokEnv(ep Endpoint) []string {
	return []string{
		"GROK_EXTERNAL_OTEL=1",
		"OTEL_METRICS_EXPORTER=otlp",
		"OTEL_LOGS_EXPORTER=otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT=" + ep.base(),
	}
}

func grokBlock(ep Endpoint) string {
	return strings.TrimSpace(fmt.Sprintf(`
# Grok reads [telemetry] from $GROK_HOME/config.toml (default ~/.grok/config.toml).
# A project .grok/config.toml cannot set otel_*. Env vars win over this file.
# https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md
[telemetry]
otel_enabled = true
otel_metrics_exporter = "otlp"
otel_logs_exporter = "otlp"
otel_protocol = "http/protobuf"
otel_endpoint = "%s"
# Content and detail gates, pinned off. Grok's defaults are off too, but
# otel_log_assistant_responses follows otel_log_user_prompts when unset, so
# all four are written explicitly. An OTEL_LOG_* env var still wins.
otel_log_user_prompts = false
otel_log_assistant_responses = false
otel_log_tool_details = false
otel_log_tool_content = false
`, ep.base())) + "\n"
}

// writeGrokConfig appends block to config.toml unless it already has a
// [telemetry] table. Like the other agents, every file this init writes
// (config.toml and hookPath, the session hook written right after) is
// checked first with checkAllWritable, so a read-only config, a refused
// symlink as root, or an unwritable hooks/ directory fails before anything
// changes; config.toml is then replaced atomically with replaceFile.
func writeGrokConfig(path, hookPath, block string) (string, bool, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", false, readErr(path, err)
	}
	has, err := grokHasTelemetry(existing)
	if err != nil {
		return "", false, fmt.Errorf("%s is not valid TOML (%v); Grok cannot load it either. Fix it and rerun; it was not changed", path, err)
	}
	if has {
		if err := checkAllWritable(hookPath); err != nil {
			return "", false, err
		}
		return "not written: " + path + " already has a [telemetry] table (or an inline telemetry = {...}, or dotted telemetry.* keys); it was not changed; add the printed keys to it by hand\n", false, nil
	}
	if err := checkAllWritable(path, hookPath); err != nil {
		return "", false, err
	}
	var body []byte
	if len(bytes.TrimSpace(existing)) == 0 {
		body = []byte(block)
	} else {
		body = existing
		if !bytes.HasSuffix(body, []byte("\n")) {
			body = append(body, '\n')
		}
		body = append(body, '\n')
		body = append(body, []byte(block)...)
	}
	// The appended file must still parse; anything this misses would give
	// Grok a duplicate table, so refuse instead of writing it.
	if _, err := grokHasTelemetry(body); err != nil {
		return "", false, fmt.Errorf("%s: appending the [telemetry] table would not be valid TOML (%v); add the printed keys by hand; it was not changed", path, err)
	}
	status, err := replaceFile(path, body, 0o600)
	if err != nil {
		return "", false, err
	}
	return status, true, nil
}

// grokHasTelemetry parses config.toml and reports whether it sets the
// top-level telemetry key in any TOML form: a [telemetry] or [ telemetry ]
// header, a [telemetry.sub] table, an inline telemetry = {...}, dotted
// telemetry.x = ... keys, or a quoted "telemetry" key. A line scan misses
// the last three, and appending a second [telemetry] then makes the file
// fail to parse in Grok (duplicate key). Empty input has no telemetry.
func grokHasTelemetry(body []byte) (bool, error) {
	return tomlHasKey(body, "telemetry")
}

// tomlHasKey parses body as TOML and reports whether it sets the top-level
// key in any form (table header with or without spaces or quotes, sub-table,
// inline table, dotted or quoted key). Empty input has no keys.
func tomlHasKey(body []byte, key string) (bool, error) {
	var doc map[string]any
	if _, err := toml.Decode(string(body), &doc); err != nil {
		return false, err
	}
	_, ok := doc[key]
	return ok, nil
}

func grokConfigured(req Request) string {
	path := filepath.Join(req.GrokHome, "config.toml")
	want := req.Endpoint.base()
	envHit := (os.Getenv("GROK_EXTERNAL_OTEL") == "1" || os.Getenv("GROK_EXTERNAL_OTEL") == "true") &&
		(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == want || strings.HasPrefix(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), want+"/"))
	body, err := os.ReadFile(path)
	has, parseErr := grokHasTelemetry(body)
	fileHit := err == nil && parseErr == nil && has && strings.Contains(string(body), want)
	return fmt.Sprintf("grok process env points at %s: %t; %s has [telemetry] pointing at %s: %t\n", want, envHit, path, want, fileHit)
}

func piOtel(ep Endpoint) map[string]any {
	return map[string]any{
		"endpoint":   ep.base(),
		"protocol":   "http/json",
		"spanNaming": "genai",
		"signals": map[string]any{
			"traces":  true,
			"metrics": true,
			"logs":    true,
		},
	}
}

func gemini(req Request) (Result, error) {
	path := filepath.Join(req.Home, ".gemini", "settings.json")
	var b strings.Builder
	fmt.Fprintf(&b, "Gemini CLI telemetry is off until telemetry.enabled is true.\n")
	fmt.Fprintf(&b, "It exports logs, metrics, and spans with session.id (spans carry gen_ai.conversation.id).\n")
	fmt.Fprintf(&b, "Its default is grpc at http://localhost:4317, which sdf serve also accepts. This setup selects http on %s.\n", req.Endpoint.base())
	fmt.Fprintf(&b, "logPrompts defaults to true in Gemini CLI. --write sets it to false unless %s already sets it.\n", path)
	fmt.Fprintf(&b, "Precedence, lowest first: %s, a workspace .gemini/settings.json (only in a trusted folder), then %s.\n", path, geminiSystemSettings())
	fmt.Fprintf(&b, "GEMINI_TELEMETRY_* overrides every file. OTEL_EXPORTER_OTLP_ENDPOINT overrides telemetry.otlpEndpoint when GEMINI_TELEMETRY_OTLP_ENDPOINT is unset.\n")
	fmt.Fprintf(&b, "telemetry.outfile, when set, replaces the endpoint, and nothing reaches sdf.\n")
	fmt.Fprintf(&b, "Docs: https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/telemetry.md\n\n")
	fmt.Fprintf(&b, "export GEMINI_TELEMETRY_ENABLED=true\n")
	fmt.Fprintf(&b, "export GEMINI_TELEMETRY_TARGET=local\n")
	fmt.Fprintf(&b, "export GEMINI_TELEMETRY_OTLP_ENDPOINT=%s\n", shQuote(req.Endpoint.base()))
	fmt.Fprintf(&b, "export GEMINI_TELEMETRY_OTLP_PROTOCOL=http\n")
	fmt.Fprintf(&b, "export GEMINI_TELEMETRY_LOG_PROMPTS=false\n\n")
	want := geminiTelemetry(req.Endpoint)
	merge := func(root map[string]any) (bool, error) {
		changed, err := mergeAnyMap(root, "telemetry", want)
		if err != nil {
			return false, err
		}
		telemetry := root["telemetry"].(map[string]any)
		if _, ok := telemetry["logPrompts"]; !ok {
			telemetry["logPrompts"] = false
			changed = true
		}
		stopChanged, err := mergeStopHook(root, "AfterAgent", stopHookCommand("gemini"), stopHookTimeoutMS, "*", "sdf-decision-reminder")
		if err != nil {
			return false, err
		}
		return changed || stopChanged, nil
	}
	var warnings strings.Builder
	if root, err := readJSON(path); err == nil {
		if telemetry, ok := root["telemetry"].(map[string]any); ok {
			for _, key := range []string{"target", "otlpEndpoint", "otlpProtocol"} {
				if old, ok := telemetry[key]; ok && !jsonEqual(old, want[key]) {
					fmt.Fprintf(&warnings, "warning: %s telemetry.%s %s -> %s\n", path, key, jsonText(old), jsonText(want[key]))
				}
			}
			if lp, ok := telemetry["logPrompts"].(bool); ok && lp {
				fmt.Fprintf(&warnings, "warning: %s sets telemetry.logPrompts=true, so prompt text is exported to sdf. sdf leaves an explicit choice alone; set it to false to keep prompts out.\n", path)
			}
			if outfile, ok := telemetry["outfile"].(string); ok && outfile != "" {
				fmt.Fprintf(&warnings, "warning: %s sets telemetry.outfile=%s, which replaces the endpoint. Remove it to send to sdf.\n", path, outfile)
			}
		}
	}
	var status string
	if req.Write {
		var err error
		status, err = mergeJSON(path, true, merge)
		if err != nil {
			return Result{}, err
		}
	} else {
		shape := map[string]any{"telemetry": map[string]any{"logPrompts": false}, "hooks": true}
		for key, value := range want {
			shape["telemetry"].(map[string]any)[key] = value
		}
		status = previewJSON(path, merge, shape)
	}
	b.WriteString(status)
	b.WriteString(warnings.String())
	return Result{Text: b.String(), Wrote: strings.HasPrefix(status, "wrote ")}, nil
}

func jsonText(v any) string {
	var buf bytes.Buffer
	if err := encodeLeaf(&buf, v); err != nil {
		return fmt.Sprint(v)
	}
	return buf.String()
}

func geminiTelemetry(ep Endpoint) map[string]any {
	return map[string]any{
		"enabled":      true,
		"target":       "local",
		"otlpEndpoint": ep.base(),
		"otlpProtocol": "http",
	}
}

// geminiSystemSettings is the system override file Gemini CLI loads after the
// user and workspace files, so its telemetry keys win over both.
// https://github.com/google-gemini/gemini-cli/blob/main/docs/reference/configuration.md
func geminiSystemSettings() string {
	if env := os.Getenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH"); env != "" {
		return env
	}
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/GeminiCli/settings.json"
	case "windows":
		return `C:\ProgramData\gemini-cli\settings.json`
	default:
		return "/etc/gemini-cli/settings.json"
	}
}

// geminiConflicts lists telemetry keys in path that would stop records from
// reaching want.
func geminiConflicts(path, want string) []string {
	root, err := readJSON(path)
	if err != nil {
		return nil
	}
	telemetry, ok := root["telemetry"].(map[string]any)
	if !ok {
		return nil
	}
	var out []string
	if v, ok := telemetry["enabled"]; ok && v != true {
		out = append(out, "enabled="+jsonText(v))
	}
	if v, ok := telemetry["target"]; ok && v != "local" {
		out = append(out, "target="+jsonText(v))
	}
	if v, ok := telemetry["otlpEndpoint"]; ok {
		if s, _ := v.(string); !strings.Contains(s, want) {
			out = append(out, "otlpEndpoint="+jsonText(v))
		}
	}
	if v, ok := telemetry["otlpProtocol"]; ok && v != "http" {
		out = append(out, "otlpProtocol="+jsonText(v))
	}
	if v, ok := telemetry["outfile"]; ok && v != "" {
		out = append(out, "outfile="+jsonText(v))
	}
	return out
}

func geminiConfigured(req Request) string {
	path := filepath.Join(req.Home, ".gemini", "settings.json")
	want := req.Endpoint.base()
	envHit := strings.Contains(os.Getenv("GEMINI_TELEMETRY_OTLP_ENDPOINT"), want)
	fileHit := false
	if root, err := readJSON(path); err == nil {
		if telemetry, ok := root["telemetry"].(map[string]any); ok {
			endpoint, _ := telemetry["otlpEndpoint"].(string)
			enabled, _ := telemetry["enabled"].(bool)
			fileHit = enabled && strings.Contains(endpoint, want)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "gemini process env GEMINI_TELEMETRY_OTLP_ENDPOINT points at %s: %t; %s has telemetry enabled pointing at %s: %t\n", want, envHit, path, want, fileHit)
	if os.Getenv("GEMINI_TELEMETRY_OTLP_ENDPOINT") == "" {
		if otel := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); otel != "" && !strings.Contains(otel, want) {
			fmt.Fprintf(&b, "warning: OTEL_EXPORTER_OTLP_ENDPOINT=%s overrides telemetry.otlpEndpoint in every settings file, so gemini sends there, not to %s. Unset it or set GEMINI_TELEMETRY_OTLP_ENDPOINT=%s.\n", otel, want, want)
		}
	}
	system := geminiSystemSettings()
	if conflicts := geminiConflicts(system, want); len(conflicts) > 0 {
		fmt.Fprintf(&b, "warning: system settings %s override %s and set telemetry %s. GEMINI_TELEMETRY_* env still wins over it.\n", system, path, strings.Join(conflicts, ", "))
	}
	workspace := filepath.Join(req.ProjectDir, ".gemini", "settings.json")
	if workspace != path {
		if conflicts := geminiConflicts(workspace, want); len(conflicts) > 0 {
			fmt.Fprintf(&b, "warning: workspace settings %s set telemetry %s. They override %s only when this folder is trusted.\n", workspace, strings.Join(conflicts, ", "), path)
		}
	}
	return b.String()
}

func runPiInstall(req Request) (string, error) {
	if req.RunInstall != nil {
		return req.RunInstall(context.Background())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pi", "install", "npm:pi-otel")
	cmd.Dir = req.ProjectDir
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		return text, fmt.Errorf("pi install npm:pi-otel: %w", err)
	}
	return text, nil
}

func (e Endpoint) base() string {
	return "http://" + net.JoinHostPort(e.Host, fmt.Sprint(e.HTTPPort))
}

// Check reports whether the HTTP health URL and the gRPC port answer.
// Reachable is true when either one does. gRPC is a TCP connect, not an RPC.
type Check struct {
	Text       string
	Reachable  bool
	HTTPStatus string
	GRPCStatus string
}

// Probe checks the receiver. It does not configure an agent.
func Probe(ep Endpoint) Check {
	if ep.Host == "" {
		ep.Host = "127.0.0.1"
	}
	if ep.HTTPPort == 0 {
		ep.HTTPPort = 4318
	}
	if ep.GRPCPort == 0 {
		ep.GRPCPort = 4317
	}
	// The host and ports go into a URL and a dial address; probe nothing
	// that sdf init would refuse to write.
	if err := checkEndpoint(&ep); err != nil {
		return Check{Text: "not probed: " + err.Error() + "\n", HTTPStatus: "not probed", GRPCStatus: "not probed"}
	}
	health := ep.base() + "/health"
	httpStatus := probeHTTP(health)
	grpcAddr := net.JoinHostPort(ep.Host, fmt.Sprint(ep.GRPCPort))
	grpcStatus := probeTCP(grpcAddr)
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", health, httpStatus)
	fmt.Fprintf(&b, "%s: %s\n", grpcAddr, grpcStatus)
	return Check{
		Text:       b.String(),
		Reachable:  strings.HasPrefix(httpStatus, "reachable") || strings.HasPrefix(grpcStatus, "reachable"),
		HTTPStatus: httpStatus,
		GRPCStatus: grpcStatus,
	}
}

func probeHTTP(health string) string {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(health)
	if err != nil {
		return "not reachable (" + trimErr(err) + ")"
	}
	defer resp.Body.Close()
	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	body := strings.TrimSpace(string(buf[:n]))
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("not reachable (HTTP %d)", resp.StatusCode)
	}
	if body == "" {
		body = "HTTP 200"
	}
	return "reachable (" + body + ")"
}

func probeTCP(addr string) string {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return "not reachable (" + trimErr(err) + ")"
	}
	_ = conn.Close()
	return "reachable (TCP connect succeeded; this is not an OTLP response)"
}

func trimErr(err error) string {
	msg := err.Error()
	if len(msg) > 180 {
		msg = msg[:180]
	}
	return msg
}

// Configured reports whether the agent's local config or environment already
// points at this endpoint. It does not change files.
func Configured(req Request) (string, error) {
	if err := normalize(&req); err != nil {
		return "", err
	}
	switch req.Agent {
	case "claude-code":
		return claudeConfigured(req), nil
	case "copilot", "copilot-cli":
		return copilotConfigured(req), nil
	case "codex":
		return codexConfigured(req), nil
	case "opencode":
		return opencodeConfigured(req), nil
	case "qwen", "qwen-code":
		return qwenConfigured(req), nil
	case "pi":
		return piConfigured(req), nil
	case "grok", "grok-cli":
		return grokConfigured(req), nil
	case "gemini", "gemini-cli":
		return geminiConfigured(req), nil
	default:
		return "", fmt.Errorf("unknown agent %q (%s)", req.Agent, knownAgentsHint)
	}
}

func claudeConfigured(req Request) string {
	path := filepath.Join(req.Home, ".claude", "settings.json")
	want := req.Endpoint.base()
	envHit := strings.Contains(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), want) ||
		strings.Contains(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"), want) ||
		strings.Contains(os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"), want)
	fileHit := jsonEnvPointsAt(path, want)
	return fmt.Sprintf("claude-code process env points at %s: %t; %s points at %s: %t\n", want, envHit, path, want, fileHit)
}

func codexConfigured(req Request) string {
	path := filepath.Join(req.CodexHome, "config.toml")
	body, err := os.ReadFile(path)
	hit := err == nil && strings.Contains(string(body), req.Endpoint.base()) && hasOtelTable(string(body))
	return fmt.Sprintf("codex %s has [otel] pointing at %s: %t\n", path, req.Endpoint.base(), hit)
}

func opencodeConfigured(req Request) string {
	hit := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == req.Endpoint.base()
	return fmt.Sprintf("opencode process env OTEL_EXPORTER_OTLP_ENDPOINT=%s: %t\n", req.Endpoint.base(), hit)
}

func piConfigured(req Request) string {
	project := filepath.Join(req.ProjectDir, ".pi", "settings.json")
	global := filepath.Join(req.Home, ".pi", "agent", "settings.json")
	want := req.Endpoint.base()
	envHit := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == want && os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL") == "http/json"
	return fmt.Sprintf("pi process env points at %s with http/json: %t; %s: %t; %s: %t\nProject settings are sdf init pi --write. Global settings are sdf init pi --write --global.\n",
		want, envHit, project, jsonPointsAt(project, want), global, jsonPointsAt(global, want))
}

func jsonEnvPointsAt(path, endpoint string) bool {
	root, err := readJSON(path)
	if err != nil {
		return false
	}
	env, _ := root["env"].(map[string]any)
	if env == nil {
		return false
	}
	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"} {
		if text, _ := env[key].(string); strings.Contains(text, endpoint) {
			return true
		}
	}
	return false
}

func jsonPointsAt(path, endpoint string) bool {
	body, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(body), endpoint)
}

func readJSON(path string) (map[string]any, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

func mergeStringMap(root map[string]any, key string, values map[string]string) (bool, error) {
	current, ok := root[key].(map[string]any)
	if root[key] != nil && !ok {
		return false, fmt.Errorf("%s is not an object; it was not changed", key)
	}
	if current == nil {
		current = map[string]any{}
	}
	changed := false
	for name, value := range values {
		have, _ := current[name].(string)
		if have != value {
			current[name] = value
			changed = true
		}
	}
	root[key] = current
	return changed, nil
}

func mergeAnyMap(root map[string]any, key string, values map[string]any) (bool, error) {
	current, ok := root[key].(map[string]any)
	if root[key] != nil && !ok {
		return false, fmt.Errorf("%s is not an object; it was not changed", key)
	}
	if current == nil {
		current = map[string]any{}
		root[key] = current
	}
	return mergeInto(current, values), nil
}

func mergeInto(dst, src map[string]any) bool {
	changed := false
	for key, value := range src {
		child, ok := value.(map[string]any)
		if !ok {
			if have, ok := dst[key]; !ok || !jsonEqual(have, value) {
				dst[key] = value
				changed = true
			}
			continue
		}
		existing, ok := dst[key].(map[string]any)
		if !ok {
			dst[key] = child
			changed = true
			continue
		}
		if mergeInto(existing, child) {
			changed = true
		}
	}
	return changed
}

func writeBytes(path string, body []byte, write bool) (string, error) {
	if !write {
		return "not written (pass --write)\n", nil
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, body) {
		return path + " already has these values\n", nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", readErr(path, err)
	}
	return replaceFile(path, body, 0o600)
}

func wrote(path, backup string) string {
	if backup == "" {
		return "wrote " + path + "\n"
	}
	return "wrote " + path + "\nbackup " + backup + "\n"
}
