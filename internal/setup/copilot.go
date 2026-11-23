package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
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

// copilotHostName is an RFC 1123 host name: dot-separated labels of letters,
// digits, and inner hyphens.
var copilotHostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// copilotNumericHost is a host whose dot-separated labels are all digits.
var copilotNumericHost = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*\.?$`)

// copilotEndpoint validates --host and --port before they reach a file the
// user sources: a host name, an IPv4 address, or an IPv6 address (bracketed
// or bare), and a port in 1-65535. Anything else (spaces, ;, $, quotes,
// backticks) is refused.
func copilotEndpoint(ep Endpoint) (Endpoint, error) {
	host := ep.Host
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		inner := host[1 : len(host)-1]
		if ip := net.ParseIP(inner); ip == nil || ip.To4() != nil || !strings.Contains(inner, ":") {
			return ep, fmt.Errorf("--host %q is not a valid bracketed IPv6 address", ep.Host)
		}
		host = inner
	} else if strings.Contains(host, ":") {
		if ip := net.ParseIP(host); ip == nil {
			return ep, fmt.Errorf("--host %q is not a valid IPv6 address", ep.Host)
		}
	} else if copilotNumericHost.MatchString(host) && net.ParseIP(host) == nil {
		// 999.1.1.1 or 127.1 is not a host name, and resolvers read the
		// short forms differently: an all-numeric host must be IPv4.
		return ep, fmt.Errorf("--host %q is all numbers but not a valid IPv4 address", ep.Host)
	} else if net.ParseIP(host) == nil && (len(host) > 253 || !copilotHostName.MatchString(host)) {
		return ep, fmt.Errorf("--host %q is not a host name, IPv4 address, or bracketed IPv6 address", ep.Host)
	}
	if ep.HTTPPort < 1 || ep.HTTPPort > 65535 {
		return ep, fmt.Errorf("--port %d is not in 1-65535", ep.HTTPPort)
	}
	ep.Host = host
	return ep, nil
}

// shellQuote single-quotes value for a POSIX shell. An embedded single
// quote closes the quoting, is written as a backslash-escaped quote, and
// reopens it.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func copilotExport(key, value string) string {
	return "export " + key + "=" + shellQuote(value)
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
	ep, err := copilotEndpoint(req.Endpoint)
	if err != nil {
		return Result{}, err
	}
	req.Endpoint = ep
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
	fmt.Fprintf(&b, "%s, the lines sdf manages:\n%s", path, copilotManagedLines(merged))
	if others := copilotOtherLines(merged); others > 0 {
		fmt.Fprintf(&b, "(%d other line(s) in the file are kept as they are and not shown)\n", others)
	}
	for _, w := range warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	fmt.Fprintf(&b, "\nCopilot CLI does not load %s by itself. Run copilot through sh so the OTEL_* variables do not stay in your shell (this works from bash, zsh, and fish):\n  sh -c '. \"$1\" && exec copilot' sh %s\n", path, shellQuote(path))
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

// copilotResolve follows path's symlinks by hand, relative links from the
// link's directory, so a link to a file that does not exist yet (stow-style)
// resolves to the file to create.
func copilotResolve(path string) (string, error) {
	current := path
	for range 40 {
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			return current, nil
		}
		link, err := os.Readlink(current)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(current), link)
		}
		current = link
	}
	return "", fmt.Errorf("%s: too many levels of symbolic links", path)
}

// copilotWritable refuses an existing target we may not write: one with no
// write bit (0400, 0444; this also holds for root, who could open it), or
// one a write-only open (no truncation) is denied.
func copilotWritable(path, target string) error {
	info, err := os.Stat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; it was not changed", path)
	}
	if info.Mode().Perm()&0o222 == 0 {
		return fmt.Errorf("%s is read-only (mode %04o); it was not changed", path, info.Mode().Perm())
	}
	f, err := os.OpenFile(target, os.O_WRONLY, 0)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return fmt.Errorf("%s is not writable (%v); it was not changed", path, err)
	}
	return f.Close()
}

// copilotRename is replaced in tests to simulate a failed write.
var copilotRename = os.Rename

// writeFileAtomic writes through a symlink to its target (a link to a
// missing file creates it), refuses a read-only target before doing
// anything, backs up an existing file to a .sdf-bak sibling (a different
// existing backup is never overwritten; .sdf-bak.1, .2, ... are used),
// writes a temp file in the same directory, fsyncs, keeps the file mode and,
// when allowed, the owner and group, and renames it into place. A backup
// made by a failed write is removed. The rename gives the file a new inode:
// other hard links keep the old bytes (a note is printed), and extended
// attributes and ACLs are not copied.
func writeFileAtomic(path string, body []byte) (string, error) {
	target, err := copilotResolve(path)
	if err != nil {
		return "", err
	}
	if err := copilotWritable(path, target); err != nil {
		return "", err
	}
	links := copilotHardLinks(target)
	// For a new file, including the target of a dangling (stow-style)
	// symlink, missing parent directories are created with mode 0700.
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
	copilotKeepOwner(tmp, target)
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
	if links > 1 {
		status += fmt.Sprintf("note: %s had %d hard links; the other links still hold the old contents\n", target, links)
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
