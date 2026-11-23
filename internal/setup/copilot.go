package setup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GitHub Copilot CLI is configured by environment only: it has no settings
// file key for the OTLP endpoint (organization or device managed settings
// can set one, and sdf cannot read those). `sdf init copilot --write` keeps
// a snippet the user sources before starting copilot.

const (
	copilotEndpointKey = "OTEL_EXPORTER_OTLP_ENDPOINT"
	copilotProtocolKey = "OTEL_EXPORTER_OTLP_PROTOCOL"
	copilotCaptureKey  = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT"
	copilotLegacyKey   = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"
	copilotNoContent   = "NO_CONTENT"
	copilotProtocol    = "http/json"
)

// copilotTLSVars disable export to an http:// endpoint when any is set
// (copilot-cli changelog 1.0.107).
var copilotTLSVars = []string{
	"OTEL_EXPORTER_OTLP_CERTIFICATE",
	"OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE",
	"OTEL_EXPORTER_OTLP_CLIENT_KEY",
	"OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE",
	"OTEL_EXPORTER_OTLP_TRACES_CLIENT_CERTIFICATE",
	"OTEL_EXPORTER_OTLP_TRACES_CLIENT_KEY",
	"OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE",
	"OTEL_EXPORTER_OTLP_METRICS_CLIENT_CERTIFICATE",
	"OTEL_EXPORTER_OTLP_METRICS_CLIENT_KEY",
}

// copilotOverrides win over OTEL_EXPORTER_OTLP_ENDPOINT / _PROTOCOL for one
// signal, or send export to a file instead.
var copilotOverrides = []string{
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
	"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
	"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
	"COPILOT_OTEL_EXPORTER_TYPE",
	"COPILOT_OTEL_FILE_EXPORTER_PATH",
}

func copilotEnvPath(home string) string {
	return filepath.Join(home, ".config", "sdf", "copilot.env")
}

// parseEnvLine reads `export KEY=value` or `KEY=value`, with optional
// single or double quotes around the value.
func parseEnvLine(line string) (string, string, bool) {
	text := strings.TrimSpace(line)
	if text == "" || strings.HasPrefix(text, "#") {
		return "", "", false
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, "export "))
	key, value, ok := strings.Cut(text, "=")
	if !ok {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	return strings.TrimSpace(key), value, true
}

// spanCapture reports whether a standard capture value puts prompt and
// response text on spans. NO_CONTENT and EVENT_ONLY do not (EVENT_ONLY
// content goes to span events, which sdf does not store), and an
// unrecognized value disables capture.
func spanCapture(value string) bool {
	return strings.EqualFold(value, "SPAN_ONLY") || strings.EqualFold(value, "SPAN_AND_EVENT")
}

func legacyCapture(value string) bool {
	return strings.EqualFold(value, "true") || value == "1"
}

// captureWarning describes the effective content capture for one set of
// values. The legacy boolean counts only when the standard variable is empty
// or unset (Copilot docs, Content capture).
func captureWarning(where, standard, legacy string) string {
	if standard != "" {
		if spanCapture(standard) {
			return fmt.Sprintf("content capture is on %s (%s=%s); kept. Set %s=%s to turn it off", where, copilotCaptureKey, standard, copilotCaptureKey, copilotNoContent)
		}
		return ""
	}
	if legacyCapture(legacy) {
		return fmt.Sprintf("content capture is on %s (%s=%s and %s unset)", where, copilotLegacyKey, legacy, copilotCaptureKey)
	}
	return ""
}

func copilotExport(key, value string) string {
	return "export " + key + "=" + shQuote(value)
}

// mergeCopilotEnv rewrites an existing snippet in place: line order and
// every unrelated line are kept, the endpoint and protocol lines are set to
// this receiver (a duplicate is dropped), and a capture-content line the
// user already set is kept with a warning. Missing lines are appended.
func mergeCopilotEnv(existing string, ep Endpoint) (string, []string) {
	want := map[string]string{copilotEndpointKey: ep.base(), copilotProtocolKey: copilotProtocol}
	var warnings []string
	var out []string
	seen := map[string]bool{}
	hasCapture := false
	standard, legacy := "", ""
	lines := strings.Split(strings.TrimRight(existing, "\n"), "\n")
	if existing == "" {
		lines = nil
	}
	for _, line := range lines {
		key, value, ok := parseEnvLine(line)
		if !ok {
			out = append(out, line)
			continue
		}
		if target, owned := want[key]; owned {
			if seen[key] {
				warnings = append(warnings, fmt.Sprintf("dropping a second %s line (%s)", key, value))
				continue
			}
			seen[key] = true
			if value != target {
				warnings = append(warnings, fmt.Sprintf("replacing %s=%s with %s", key, value, target))
			}
			out = append(out, copilotExport(key, target))
			continue
		}
		switch key {
		case copilotCaptureKey:
			hasCapture = true
			standard = value
		case copilotLegacyKey:
			legacy = value
		}
		for _, o := range copilotOverrides {
			if key == o {
				warnings = append(warnings, fmt.Sprintf("%s=%s in this file overrides the endpoint or exporter for Copilot CLI; kept", key, value))
			}
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		out = append(out, "# source this file before starting copilot")
	}
	for _, key := range []string{copilotEndpointKey, copilotProtocolKey} {
		if !seen[key] {
			out = append(out, copilotExport(key, want[key]))
		}
	}
	if !hasCapture {
		out = append(out, copilotExport(copilotCaptureKey, copilotNoContent))
		standard = copilotNoContent
	}
	if w := captureWarning("in this file", standard, legacy); w != "" {
		warnings = append(warnings, w)
	}
	if legacyCapture(legacy) && !spanCapture(standard) {
		warnings = append(warnings, fmt.Sprintf("%s=%s in this file is overridden by %s=%s for prompts and responses, but it still adds tool schemas (gen_ai.tool.definitions) to spans", copilotLegacyKey, legacy, copilotCaptureKey, standard))
	}
	return strings.Join(out, "\n") + "\n", warnings
}

// copilotEnvWarnings reads the current process environment.
func copilotEnvWarnings(ep Endpoint) []string {
	var out []string
	for _, key := range copilotTLSVars {
		if os.Getenv(key) != "" {
			out = append(out, key+" is set. Copilot CLI drops export to an http:// endpoint while an OTLP certificate or client key variable is set; unset it in the shell that starts copilot")
			break
		}
	}
	for _, key := range copilotOverrides {
		if v := os.Getenv(key); v != "" {
			out = append(out, fmt.Sprintf("%s=%s is set in this environment and overrides %s or the exporter for Copilot CLI", key, v, copilotEndpointKey))
		}
	}
	if v := os.Getenv(copilotEndpointKey); v != "" && strings.TrimRight(v, "/") != ep.base() {
		out = append(out, fmt.Sprintf("%s=%s is set in this environment; sourcing the snippet replaces it", copilotEndpointKey, v))
	}
	if v := os.Getenv(copilotProtocolKey); v != "" && v != copilotProtocol && v != "http/protobuf" {
		out = append(out, fmt.Sprintf("%s=%s is set; Copilot CLI supports only http/json and http/protobuf", copilotProtocolKey, v))
	}
	if w := captureWarning("in this environment", os.Getenv(copilotCaptureKey), os.Getenv(copilotLegacyKey)); w != "" {
		out = append(out, w)
	}
	return out
}

func copilot(req Request) (Result, error) {
	// Run's normalize already validated the host and ports (checkEndpoint).
	path := copilotEnvPath(req.Home)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Result{}, readErr(path, err)
	}
	merged, warnings := mergeCopilotEnv(string(existing), req.Endpoint)
	warnings = append(warnings, copilotEnvWarnings(req.Endpoint)...)
	var b strings.Builder
	fmt.Fprintf(&b, "GitHub Copilot CLI exports traces and metrics over OTLP/HTTP when %s is set.\n", copilotEndpointKey)
	fmt.Fprintf(&b, "It has no OTLP log signal and no settings-file key for the endpoint, so this is environment only.\n")
	fmt.Fprintf(&b, "Content capture stays off (%s). gRPC is not supported; this uses the HTTP receiver.\n", copilotNoContent)
	fmt.Fprintf(&b, "Per-signal variables (OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, ..._METRICS_ENDPOINT) and organization or device managed settings override these.\n")
	fmt.Fprintf(&b, "Docs: https://docs.github.com/en/copilot/reference/enterprise-administrators/opentelemetry-reference\n\n")
	fmt.Fprintf(&b, "%s, the lines sdf manages:\n%s", path, copilotManagedLines(merged))
	if others := copilotOtherLines(merged); others > 0 {
		fmt.Fprintf(&b, "(%d other line(s) in the file are kept as they are and not shown)\n", others)
	}
	for _, w := range warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	fmt.Fprintf(&b, "\nCopilot CLI does not load %s by itself. Run copilot through sh so the OTEL_* variables do not stay in your shell (this works from bash, zsh, and fish):\n  sh -c '. \"$1\" && exec copilot' sh %s\n", path, shQuote(path))
	if bytes.Equal(existing, []byte(merged)) {
		b.WriteString(path + " already has these values\n")
		return Result{Text: b.String()}, nil
	}
	if !req.Write {
		b.WriteString("not written (pass --write)\n")
		return Result{Text: b.String()}, nil
	}
	status, err := replaceFile(path, []byte(merged), 0o600)
	if err != nil {
		return Result{}, err
	}
	b.WriteString(status)
	return Result{Text: b.String(), Wrote: true}, nil
}

func copilotConfigured(req Request) string {
	want := req.Endpoint.base()
	var b strings.Builder
	hit := strings.TrimRight(os.Getenv(copilotEndpointKey), "/") == want
	fmt.Fprintf(&b, "copilot process env %s=%s: %t\n", copilotEndpointKey, want, hit)
	home := req.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	path := copilotEnvPath(home)
	if body, err := os.ReadFile(path); home != "" && err == nil {
		inFile := false
		for _, line := range strings.Split(string(body), "\n") {
			if key, value, ok := parseEnvLine(line); ok && key == copilotEndpointKey && strings.TrimRight(value, "/") == want {
				inFile = true
			}
		}
		fmt.Fprintf(&b, "copilot snippet %s %s=%s: %t (copilot reads the environment, not this file)\n", path, copilotEndpointKey, want, inFile)
	}
	for _, w := range copilotEnvWarnings(req.Endpoint) {
		fmt.Fprintf(&b, "copilot warning: %s\n", w)
	}
	b.WriteString("copilot: organization or device managed settings can also configure export; sdf cannot see them\n")
	return b.String()
}

func copilotManaged(key string) bool {
	return key == copilotEndpointKey || key == copilotProtocolKey || key == copilotCaptureKey || key == copilotLegacyKey
}

// copilotManagedLines is the part of the snippet shown in output: only the
// variables sdf sets or reads for capture, never the user's other lines
// (OTEL_EXPORTER_OTLP_HEADERS and the like can hold tokens).
func copilotManagedLines(merged string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(merged, "\n"), "\n") {
		if key, _, ok := parseEnvLine(line); ok && copilotManaged(key) {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func copilotOtherLines(merged string) int {
	n := 0
	for _, line := range strings.Split(strings.TrimRight(merged, "\n"), "\n") {
		if key, _, ok := parseEnvLine(line); ok && !copilotManaged(key) {
			n++
		}
	}
	return n
}
