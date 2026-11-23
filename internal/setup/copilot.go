package setup

import (
	"bytes"
	"errors"
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

func captureEnabled(key, value string) bool {
	switch key {
	case copilotCaptureKey:
		return value != "" && !strings.EqualFold(value, copilotNoContent)
	case copilotLegacyKey:
		return strings.EqualFold(value, "true") || value == "1"
	}
	return false
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
			out = append(out, "export "+key+"="+target)
			continue
		}
		if key == copilotCaptureKey {
			hasCapture = true
		}
		if captureEnabled(key, value) {
			warnings = append(warnings, fmt.Sprintf("content capture is already on in this file (%s=%s); kept. Set %s=%s to turn it off", key, value, copilotCaptureKey, copilotNoContent))
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
			out = append(out, "export "+key+"="+want[key])
		}
	}
	if !hasCapture {
		out = append(out, "export "+copilotCaptureKey+"="+copilotNoContent)
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
	for _, key := range []string{copilotCaptureKey, copilotLegacyKey} {
		if v := os.Getenv(key); captureEnabled(key, v) {
			out = append(out, fmt.Sprintf("content capture is on in this environment (%s=%s)", key, v))
		}
	}
	return out
}

func copilot(req Request) (Result, error) {
	path := copilotEnvPath(req.Home)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Result{}, err
	}
	merged, warnings := mergeCopilotEnv(string(existing), req.Endpoint)
	warnings = append(warnings, copilotEnvWarnings(req.Endpoint)...)
	var b strings.Builder
	fmt.Fprintf(&b, "GitHub Copilot CLI exports traces and metrics over OTLP/HTTP when %s is set.\n", copilotEndpointKey)
	fmt.Fprintf(&b, "It has no OTLP log signal and no settings-file key for the endpoint, so this is environment only.\n")
	fmt.Fprintf(&b, "Content capture stays off (%s). gRPC is not supported; this uses the HTTP receiver.\n", copilotNoContent)
	fmt.Fprintf(&b, "Per-signal variables (OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, ..._METRICS_ENDPOINT) and organization or device managed settings override these.\n")
	fmt.Fprintf(&b, "Docs: https://docs.github.com/en/copilot/reference/enterprise-administrators/opentelemetry-reference\n\n")
	fmt.Fprintf(&b, "%s:\n%s", path, merged)
	for _, w := range warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	fmt.Fprintf(&b, "\nCopilot CLI does not load %s by itself; source it in the shell that starts copilot.\n", path)
	if bytes.Equal(existing, []byte(merged)) {
		b.WriteString(path + " already has these values\n")
		return Result{Text: b.String()}, nil
	}
	if !req.Write {
		b.WriteString("not written (pass --write)\n")
		return Result{Text: b.String()}, nil
	}
	status, err := writeFileAtomic(path, []byte(merged))
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

// copilotRename is replaced in tests to simulate a failed write.
var copilotRename = os.Rename

// writeFileAtomic writes through a symlink to its target, backs up an
// existing file to a .sdf-bak sibling (a different existing backup is never
// overwritten; .sdf-bak.1, .2, ... are used), writes a temp file in the
// same directory, fsyncs, keeps the file mode, and renames it into place.
// A backup made by a failed write is removed.
func writeFileAtomic(path string, body []byte) (string, error) {
	target := path
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("%s is a symlink whose target cannot be resolved: %w", path, err)
		}
		target = resolved
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	mode := fileMode(target, 0o600)
	old, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	backup := ""
	created := false
	if err == nil {
		backup, created, err = backupAside(target, old, mode)
		if err != nil {
			return "", err
		}
	}
	fail := func(err error) (string, error) {
		if created {
			_ = os.Remove(backup)
		}
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".sdf-tmp-*")
	if err != nil {
		return fail(err)
	}
	tmpName := tmp.Name()
	cleanup := func(err error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fail(err)
	}
	if _, err := tmp.Write(body); err != nil {
		return cleanup(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return cleanup(err)
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		return cleanup(err)
	}
	if err := copilotRename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return fail(err)
	}
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	status := "wrote " + path + "\n"
	if target != path {
		status = "wrote " + target + " (symlink " + path + ")\n"
	}
	if backup != "" {
		status += "backup " + backup + "\n"
	}
	return status, nil
}

// backupAside copies old to target.sdf-bak. An existing backup with the
// same bytes is reused; a different one is kept and the next free
// .sdf-bak.N is used.
func backupAside(target string, old []byte, mode os.FileMode) (string, bool, error) {
	for n := 0; n < 1000; n++ {
		name := target + ".sdf-bak"
		if n > 0 {
			name = fmt.Sprintf("%s.%d", name, n)
		}
		existing, err := os.ReadFile(name)
		if err == nil {
			if bytes.Equal(existing, old) {
				return name, false, nil
			}
			continue
		}
		if !os.IsNotExist(err) {
			return "", false, err
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if _, err := f.Write(old); err != nil {
			_ = f.Close()
			_ = os.Remove(name)
			return "", false, err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(name)
			return "", false, err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(name)
			return "", false, err
		}
		return name, true, nil
	}
	return "", false, fmt.Errorf("too many backups of %s", target)
}
