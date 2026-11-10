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
// Pi has no built-in OTLP exporter. pi-otel 0.3.0 reads .pi/settings.json in
// the project and ~/.pi/agent/settings.json, then OTEL_* and PI_OTEL_* env.
// Its default protocol is grpc at 127.0.0.1:4317. This setup uses http/json
// on the receiver's HTTP port. grpc on 4317 also reaches sdf serve.
// https://github.com/NikiforovAll/pi-otel
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
	"strings"
	"time"
)

// Endpoint is the local sdf serve address the agent should use.
type Endpoint struct {
	Host     string
	HTTPPort int
	GRPCPort int
}

// Request is one init invocation.
type Request struct {
	Agent      string
	Home       string
	CodexHome  string
	ProjectDir string
	Endpoint   Endpoint
	Write      bool
	Install    bool
	// RunInstall replaces `pi install` in tests. Nil runs pi on PATH.
	RunInstall func(ctx context.Context) (string, error)
}

// Result is the text to print. Wrote is true when a file changed.
type Result struct {
	Text  string
	Wrote bool
}

// Run prints the agent setup. It writes files only when Request.Write is set,
// and it copies an existing file to a sibling .sdf-bak before replacing bytes.
func Run(req Request) (Result, error) {
	if err := normalize(&req); err != nil {
		return Result{}, err
	}
	switch req.Agent {
	case "claude-code":
		return claude(req)
	case "codex":
		return codex(req)
	case "opencode":
		return opencode(req)
	case "pi":
		return pi(req)
	default:
		return Result{}, fmt.Errorf("unknown agent %q (claude-code, codex, opencode, pi)", req.Agent)
	}
}

func normalize(req *Request) error {
	if req.Install && req.Agent != "pi" && req.Agent != "" {
		return errors.New("--install applies to pi")
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
		fmt.Fprintf(&b, "export %s=%s\n", key, env[key])
	}
	status, err := mergeJSON(path, req.Write, func(root map[string]any) (bool, error) {
		return mergeStringMap(root, "env", env)
	})
	if err != nil {
		return Result{}, err
	}
	b.WriteString("\n")
	b.WriteString(status)
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
	if !req.Write {
		b.WriteString("\nnot written (pass --write to append this block; an existing [otel] table is left unchanged)\n")
		return Result{Text: b.String()}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Result{}, err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Result{}, err
	}
	if hasOtelTable(string(existing)) {
		b.WriteString("\nnot written: " + path + " already has an [otel] table; it was not changed\n")
		return Result{Text: b.String()}, nil
	}
	backup, err := copyAside(path)
	if err != nil {
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
	}
	if err := os.WriteFile(path, body, fileMode(path, 0o600)); err != nil {
		return Result{}, err
	}
	b.WriteString("\n")
	b.WriteString(wrote(path, backup))
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

func hasOtelTable(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if trim == "[otel]" || strings.HasPrefix(trim, "[otel.") {
			return true
		}
	}
	return false
}

func opencode(req Request) (Result, error) {
	path := filepath.Join(req.Home, ".config", "sdf", "opencode.env")
	line := "export OTEL_EXPORTER_OTLP_ENDPOINT=" + req.Endpoint.base() + "\n"
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
	status, err := writeBytes(path, []byte("# source this file before starting opencode\n"+line), req.Write)
	if err != nil {
		return Result{}, err
	}
	b.WriteString(status)
	return Result{Text: b.String(), Wrote: strings.HasPrefix(status, "wrote ")}, nil
}

func pi(req Request) (Result, error) {
	path := filepath.Join(req.ProjectDir, ".pi", "settings.json")
	global := filepath.Join(req.Home, ".pi", "agent", "settings.json")
	otel := piOtel(req.Endpoint)
	var b strings.Builder
	fmt.Fprintf(&b, "Pi does not export OTLP. Install the community extension pi-otel (the setup checked here is 0.3.0):\n\n")
	fmt.Fprintf(&b, "pi install npm:pi-otel\n\n")
	fmt.Fprintf(&b, "pi-otel defaults to grpc at http://127.0.0.1:4317. sdf serve accepts that.\n")
	fmt.Fprintf(&b, "The variables below select http/json on the HTTP receiver instead.\n")
	fmt.Fprintf(&b, "Metrics and logs are off unless PI_OTEL_METRICS and PI_OTEL_LOGS are set.\n")
	fmt.Fprintf(&b, "Project config is %s. Global config is %s. Environment overrides both.\n", path, global)
	fmt.Fprintf(&b, "https://github.com/NikiforovAll/pi-otel\n\n")
	fmt.Fprintf(&b, "export OTEL_EXPORTER_OTLP_ENDPOINT=%s\n", req.Endpoint.base())
	fmt.Fprintf(&b, "export OTEL_EXPORTER_OTLP_PROTOCOL=http/json\n")
	fmt.Fprintf(&b, "export PI_OTEL_METRICS=1\n")
	fmt.Fprintf(&b, "export PI_OTEL_LOGS=1\n")
	fmt.Fprintf(&b, "export PI_OTEL_SPAN_NAMING=genai\n\n")
	status, err := mergeJSON(path, req.Write, func(root map[string]any) (bool, error) {
		return mergeAnyMap(root, "otel", otel)
	})
	if err != nil {
		return Result{}, err
	}
	b.WriteString(status)
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
	case "codex":
		return codexConfigured(req), nil
	case "opencode":
		return opencodeConfigured(req), nil
	case "pi":
		return piConfigured(req), nil
	default:
		return "", fmt.Errorf("unknown agent %q", req.Agent)
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
	return fmt.Sprintf("pi process env points at %s with http/json: %t; %s: %t; %s: %t\n",
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

func mergeJSON(path string, write bool, merge func(map[string]any) (bool, error)) (string, error) {
	if !write {
		return "not written (pass --write to update " + path + "; an existing file is copied to a .sdf-bak sibling first)\n", nil
	}
	root := map[string]any{}
	if body, err := os.ReadFile(path); err == nil {
		if !json.Valid(body) {
			return "", fmt.Errorf("%s is not JSON; it was not changed", path)
		}
		if err := json.Unmarshal(body, &root); err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		if root == nil {
			root = map[string]any{}
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	changed, err := merge(root)
	if err != nil {
		return "", err
	}
	if !changed {
		return path + " already has these values\n", nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	backup, err := copyAside(path)
	if err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	encoded = append(encoded, '\n')
	mode := fileMode(path, 0o600)
	if strings.Contains(path, string(filepath.Separator)+".pi"+string(filepath.Separator)) {
		mode = fileMode(path, 0o644)
	}
	if err := os.WriteFile(path, encoded, mode); err != nil {
		return "", err
	}
	return wrote(path, backup), nil
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
			if fmt.Sprint(dst[key]) != fmt.Sprint(value) {
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
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	backup, err := copyAside(path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, body, fileMode(path, 0o600)); err != nil {
		return "", err
	}
	return wrote(path, backup), nil
}

func copyAside(path string) (string, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	dest := path + ".sdf-bak"
	if _, err := os.Stat(dest); err == nil {
		dest = path + ".sdf-bak." + time.Now().UTC().Format("20060102T150405Z")
	}
	if err := os.WriteFile(dest, body, fileMode(path, 0o600)); err != nil {
		return "", err
	}
	return dest, nil
}

func fileMode(path string, fallback os.FileMode) os.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		return fallback
	}
	return info.Mode().Perm()
}

func wrote(path, backup string) string {
	if backup == "" {
		return "wrote " + path + "\n"
	}
	return "wrote " + path + "\nbackup " + backup + "\n"
}
