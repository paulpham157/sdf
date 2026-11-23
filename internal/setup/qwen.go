package setup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

// Qwen Code (QwenLM/qwen-code) reads telemetry from the "telemetry" object
// of its settings files. Checked against v0.28.4 (e8424c88) on 2026-11-23:
// docs/developers/development/telemetry.md, packages/core/src/telemetry/
// config.ts (resolveTelemetrySettings), packages/cli/src/config/settings.ts
// (mergeSettings), and storage-paths-lite.ts.
//
// Precedence, highest first: CLI flags (--telemetry, --telemetry-otlp-*,
// --telemetry-log-prompts, --telemetry-outfile), then environment
// (QWEN_TELEMETRY_*, and OTEL_EXPORTER_OTLP_ENDPOINT for the endpoint), then
// the merged settings: system (/etc/qwen-code/settings.json or
// QWEN_CODE_SYSTEM_SETTINGS_PATH) over workspace (<cwd>/.qwen/settings.json,
// only when the folder is trusted) over user ($QWEN_HOME or ~/.qwen) over
// system defaults (system-defaults.json beside the system file, or
// QWEN_CODE_SYSTEM_DEFAULTS_PATH). logPrompts defaults to true.

const (
	qwenEnabledEnv  = "QWEN_TELEMETRY_ENABLED"
	qwenEndpointEnv = "QWEN_TELEMETRY_OTLP_ENDPOINT"
	qwenProtocolEnv = "QWEN_TELEMETRY_OTLP_PROTOCOL"
	qwenPromptsEnv  = "QWEN_TELEMETRY_LOG_PROMPTS"
	qwenOutfileEnv  = "QWEN_TELEMETRY_OUTFILE"
	qwenSensitive   = "includeSensitiveSpanAttributes"
	qwenSensEnv     = "QWEN_TELEMETRY_INCLUDE_SENSITIVE_SPAN_ATTRIBUTES"
	qwenOTELEnv     = "OTEL_EXPORTER_OTLP_ENDPOINT"
)

// qwenSignalEnv override the endpoint for one signal (QWEN_ wins over OTEL_).
var qwenSignalEnv = []string{
	"QWEN_TELEMETRY_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
	"QWEN_TELEMETRY_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
	"QWEN_TELEMETRY_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
}

var qwenSignalKeys = []string{"otlpTracesEndpoint", "otlpLogsEndpoint", "otlpMetricsEndpoint"}

func qwenHome(req Request) string {
	if req.Home != "" {
		return req.Home
	}
	home, _ := os.UserHomeDir()
	return home
}

// qwenUserSettingsPath is $QWEN_HOME/settings.json (~ expands, a relative
// path resolves against the working directory) or ~/.qwen/settings.json.
func qwenUserSettingsPath(req Request) string {
	home := qwenHome(req)
	if dir := strings.TrimSpace(os.Getenv("QWEN_HOME")); dir != "" {
		if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
			dir = filepath.Join(home, dir[1:])
		}
		if !filepath.IsAbs(dir) {
			if wd, err := os.Getwd(); err == nil {
				dir = filepath.Join(wd, dir)
			}
		}
		return filepath.Join(dir, "settings.json")
	}
	return filepath.Join(home, ".qwen", "settings.json")
}

func qwenSystemSettingsPath() string {
	if p := os.Getenv("QWEN_CODE_SYSTEM_SETTINGS_PATH"); p != "" {
		return p
	}
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/QwenCode/settings.json"
	case "windows":
		return `C:\ProgramData\qwen-code\settings.json`
	}
	return "/etc/qwen-code/settings.json"
}

func qwenSystemDefaultsPath() string {
	if p := os.Getenv("QWEN_CODE_SYSTEM_DEFAULTS_PATH"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(qwenSystemSettingsPath()), "system-defaults.json")
}

func qwenWorkspaceSettingsPath(req Request) string {
	dir := req.ProjectDir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	return filepath.Join(dir, ".qwen", "settings.json")
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// qwenMerge sets the telemetry keys sdf needs and returns the new file,
// the merged telemetry object, warnings, and whether anything changed.
func qwenMerge(existing []byte, ep Endpoint) ([]byte, *qwenObject, []string, bool, error) {
	bom := bytes.HasPrefix(existing, utf8BOM)
	body := bytes.TrimPrefix(existing, utf8BOM)
	root := newQwenObject()
	indent := "  "
	if len(bytes.TrimSpace(body)) > 0 {
		parsed, err := qwenParseJSON(body)
		if err != nil {
			if _, lenient := qwenParseJSON(qwenStripComments(body)); lenient == nil {
				return nil, nil, nil, false, errors.New("it has comments, which Qwen Code accepts but sdf would drop; add the telemetry block shown above by hand")
			}
			return nil, nil, nil, false, fmt.Errorf("it is not valid JSON (%v); left untouched", err)
		}
		obj, ok := parsed.(*qwenObject)
		if !ok {
			return nil, nil, nil, false, errors.New("its top level is not a JSON object; left untouched")
		}
		root = obj
		indent = qwenDetectIndent(body)
	}
	tel := newQwenObject()
	if v, ok := root.get("telemetry"); ok {
		obj, isObj := v.(*qwenObject)
		if !isObj {
			return nil, nil, nil, false, errors.New(`its "telemetry" value is not an object; left untouched`)
		}
		tel = obj
	}
	var warnings []string
	changed := false
	set := func(key string, want any, warn func(old any) string) {
		old, had := tel.get(key)
		if had && reflect.DeepEqual(old, want) {
			return
		}
		if had && warn != nil {
			if w := warn(old); w != "" {
				warnings = append(warnings, w)
			}
		}
		tel.set(key, want)
		changed = true
	}
	replacing := func(key string) func(any) string {
		return func(old any) string {
			return fmt.Sprintf("replacing telemetry.%s=%s with the sdf value", key, qwenShow(old))
		}
	}
	set("enabled", true, replacing("enabled"))
	set("otlpEndpoint", ep.base(), replacing("otlpEndpoint"))
	set("otlpProtocol", "http", replacing("otlpProtocol"))
	set("logPrompts", false, func(old any) string {
		if old == true {
			return "telemetry.logPrompts was true; sdf init qwen sets it to false so prompts, API request text, and response text are not exported"
		}
		return replacing("logPrompts")(old)
	})
	if v, ok := tel.get(qwenSensitive); ok && v != false {
		set(qwenSensitive, false, func(old any) string {
			return fmt.Sprintf("telemetry.%s was %s; set to false so messages, tool arguments, and tool results are not copied into spans", qwenSensitive, qwenShow(old))
		})
	}
	if v, ok := tel.get("outfile"); ok {
		warnings = append(warnings, fmt.Sprintf("telemetry.outfile=%s is set and turns OTLP export off; kept. Remove it to send to sdf", qwenShow(v)))
	}
	for _, key := range qwenSignalKeys {
		if v, ok := tel.get(key); ok {
			warnings = append(warnings, fmt.Sprintf("telemetry.%s=%s overrides otlpEndpoint for that signal; kept", key, qwenShow(v)))
		}
	}
	if _, ok := root.get("telemetry"); !ok {
		root.set("telemetry", tel)
	}
	out, err := qwenWriteJSON(root, indent)
	if err != nil {
		return nil, nil, nil, false, err
	}
	if len(body) == 0 || bytes.HasSuffix(body, []byte("\n")) {
		out = append(out, '\n')
	}
	if bom {
		out = append(append([]byte{}, utf8BOM...), out...)
	}
	return out, tel, warnings, changed, nil
}

func qwenShow(v any) string {
	b, err := qwenWriteJSON(v, "")
	if err != nil {
		return fmt.Sprint(v)
	}
	return strings.ReplaceAll(string(b), "\n", " ")
}

// qwenEnvWarnings lists environment variables that override the user file.
func qwenEnvWarnings(ep Endpoint) []string {
	var out []string
	if v, ok := os.LookupEnv(qwenEnabledEnv); ok && v != "true" && v != "1" {
		out = append(out, fmt.Sprintf("%s=%s turns telemetry off whatever the settings say", qwenEnabledEnv, v))
	}
	for _, key := range []string{qwenEndpointEnv, qwenOTELEnv} {
		if v := os.Getenv(key); v != "" && strings.TrimRight(v, "/") != ep.base() {
			out = append(out, fmt.Sprintf("%s=%s overrides telemetry.otlpEndpoint", key, v))
		}
	}
	if v := os.Getenv(qwenProtocolEnv); v != "" && v != "http" {
		out = append(out, fmt.Sprintf("%s=%s overrides telemetry.otlpProtocol", qwenProtocolEnv, v))
	}
	if v := os.Getenv(qwenPromptsEnv); v == "true" || v == "1" {
		out = append(out, fmt.Sprintf("%s=%s turns prompt logging back on", qwenPromptsEnv, v))
	}
	if v := os.Getenv(qwenSensEnv); v == "true" || v == "1" {
		out = append(out, fmt.Sprintf("%s=%s copies messages and tool data into spans", qwenSensEnv, v))
	}
	if v := os.Getenv(qwenOutfileEnv); v != "" {
		out = append(out, fmt.Sprintf("%s=%s turns OTLP export off", qwenOutfileEnv, v))
	}
	for _, key := range qwenSignalEnv {
		if v := os.Getenv(key); v != "" {
			out = append(out, fmt.Sprintf("%s=%s overrides the endpoint for one signal", key, v))
		}
	}
	return out
}

func qwen(req Request) (Result, error) {
	ep, err := qwenCheckEndpoint(req.Endpoint)
	if err != nil {
		return Result{}, err
	}
	req.Endpoint = ep
	path := qwenUserSettingsPath(req)
	target, err := qwenResolveLink(path)
	if err != nil {
		return Result{}, err
	}
	existing, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		return Result{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Qwen Code exports traces, logs, and metrics when telemetry.enabled is true and an OTLP endpoint is set.\n")
	fmt.Fprintf(&b, "otlpProtocol \"http\" sends OTLP/JSON to the HTTP receiver; logPrompts (default true) is set to false.\n")
	fmt.Fprintf(&b, "Checked against qwen-code v0.28.4. Docs: https://github.com/QwenLM/qwen-code/blob/main/docs/developers/development/telemetry.md\n")
	fmt.Fprintf(&b, "Overrides, highest first: CLI flags, QWEN_TELEMETRY_* and OTEL_EXPORTER_OTLP_ENDPOINT, system settings %s, a trusted workspace .qwen/settings.json, then this file.\n\n", qwenSystemSettingsPath())
	merged, tel, warnings, changed, mergeErr := qwenMerge(existing, req.Endpoint)
	fmt.Fprintf(&b, "export %s=%s\nexport %s=%s\nexport %s=%s\nexport %s=%s\n\n",
		qwenEnabledEnv, qwenShQuote("true"), qwenEndpointEnv, qwenShQuote(req.Endpoint.base()),
		qwenProtocolEnv, qwenShQuote("http"), qwenPromptsEnv, qwenShQuote("false"))
	if mergeErr != nil {
		fmt.Fprintf(&b, "%s was not changed: %v\n", path, mergeErr)
		fmt.Fprintf(&b, "Put these keys in its \"telemetry\" object, or use the variables above:\n  \"enabled\": true, \"otlpEndpoint\": %q, \"otlpProtocol\": \"http\", \"logPrompts\": false\n", req.Endpoint.base())
		return Result{Text: b.String()}, nil
	}
	block, _ := qwenWriteJSON(tel, "  ")
	fmt.Fprintf(&b, "%s, \"telemetry\" after the merge (other keys are kept as they are and not shown):\n%s\n", path, block)
	if target != path {
		fmt.Fprintf(&b, "%s is a symlink; the write goes to %s\n", path, target)
	}
	for _, w := range append(warnings, qwenEnvWarnings(req.Endpoint)...) {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	if !changed {
		b.WriteString(path + " already has these values\n")
		return Result{Text: b.String()}, nil
	}
	if !req.Write {
		b.WriteString("not written (pass --write)\n")
		return Result{Text: b.String()}, nil
	}
	backup, err := qwenWriteAtomic(target, merged)
	if err != nil {
		return Result{}, err
	}
	b.WriteString("wrote " + target + "\n")
	if backup != "" {
		b.WriteString("backup " + backup + "\n")
	}
	return Result{Text: b.String(), Wrote: true}, nil
}

// qwenResolveLink follows a symlink chain with Readlink, so a link to a
// file that does not exist yet (a stow-style dotfile) resolves to the path
// that will be created.
func qwenResolveLink(path string) (string, error) {
	current := path
	for range 40 {
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
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
	return "", fmt.Errorf("%s: too many levels of symlinks", path)
}

// qwenRename is replaced in tests to simulate a failed write.
var qwenRename = os.Rename

// qwenWriteAtomic writes target through a temp file in the same directory
// (fsync, then rename). It keeps the mode and, where allowed, the owner.
// The rename gives the file a new inode: a hard link to the old file keeps
// the old content, and extended attributes or ACLs are not copied. It
// refuses a target the user cannot write, instead of renaming over a
// read-only file. An existing file is copied to .sdf-bak first; a
// different existing backup is kept and .sdf-bak.N is used. A backup made
// by a failed write is removed.
func qwenWriteAtomic(target string, body []byte) (string, error) {
	var old os.FileInfo
	if info, err := os.Stat(target); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", target)
		}
		f, err := os.OpenFile(target, os.O_WRONLY, 0)
		if err != nil {
			return "", fmt.Errorf("%s is not writable, refusing to replace it: %w", target, err)
		}
		_ = f.Close()
		old = info
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	mode := os.FileMode(0o600)
	backup, created := "", false
	if old != nil {
		mode = old.Mode().Perm()
		previous, err := os.ReadFile(target)
		if err != nil {
			return "", err
		}
		backup, created, err = qwenBackup(target, previous, mode)
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
	name := tmp.Name()
	abort := func(err error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fail(err)
	}
	if _, err := tmp.Write(body); err != nil {
		return abort(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return abort(err)
	}
	qwenKeepOwner(tmp, old)
	if err := tmp.Sync(); err != nil {
		return abort(err)
	}
	if err := tmp.Close(); err != nil {
		return abort(err)
	}
	if err := qwenRename(name, target); err != nil {
		_ = os.Remove(name)
		return fail(err)
	}
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return backup, nil
}

func qwenBackup(target string, previous []byte, mode os.FileMode) (string, bool, error) {
	for n := range 1000 {
		name := target + ".sdf-bak"
		if n > 0 {
			name = fmt.Sprintf("%s.%d", name, n)
		}
		existing, err := os.ReadFile(name)
		if err == nil {
			if bytes.Equal(existing, previous) {
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
		_, werr := f.Write(previous)
		if werr == nil {
			werr = f.Sync()
		}
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(name)
			return "", false, werr
		}
		return name, true, nil
	}
	return "", false, fmt.Errorf("too many backups of %s", target)
}

// qwenLayer is one settings file's telemetry values.
type qwenLayer struct {
	name, path  string
	tel         *qwenObject
	folderTrust any
	note        string
}

func qwenReadLayer(name, path string) qwenLayer {
	layer := qwenLayer{name: name, path: path}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		layer.note = "not present"
		return layer
	}
	if err != nil {
		layer.note = err.Error()
		return layer
	}
	parsed, err := qwenParseJSON(qwenStripComments(bytes.TrimPrefix(body, utf8BOM)))
	if err != nil {
		layer.note = "not valid JSON: " + err.Error()
		return layer
	}
	root, ok := parsed.(*qwenObject)
	if !ok {
		layer.note = "top level is not an object"
		return layer
	}
	if v, ok := root.get("telemetry"); ok {
		layer.tel, _ = v.(*qwenObject)
	}
	if sec, ok := root.get("security"); ok {
		if s, ok := sec.(*qwenObject); ok {
			if ft, ok := s.get("folderTrust"); ok {
				if f, ok := ft.(*qwenObject); ok {
					layer.folderTrust, _ = f.get("enabled")
				}
			}
		}
	}
	return layer
}

func qwenConfigured(req Request) string {
	want := req.Endpoint.base()
	user := qwenUserSettingsPath(req)
	workspace := qwenWorkspaceSettingsPath(req)
	layers := []qwenLayer{
		qwenReadLayer("system defaults", qwenSystemDefaultsPath()),
		qwenReadLayer("user", user),
	}
	sameAsUser := false
	if a, err := filepath.EvalSymlinks(filepath.Dir(workspace)); err == nil {
		if b, err := filepath.EvalSymlinks(filepath.Dir(user)); err == nil && a == b {
			sameAsUser = true
		}
	}
	trustGate := false
	for _, l := range []qwenLayer{layers[0], layers[1], qwenReadLayer("system", qwenSystemSettingsPath())} {
		if l.folderTrust == true {
			trustGate = true
		}
	}
	if !sameAsUser {
		ws := qwenReadLayer("workspace", workspace)
		if trustGate {
			ws.note = strings.TrimSpace(ws.note + " (applies only if this folder is trusted: security.folderTrust.enabled is true)")
		}
		layers = append(layers, ws)
	}
	layers = append(layers, qwenReadLayer("system", qwenSystemSettingsPath()))
	var b strings.Builder
	effective := map[string]string{}
	source := map[string]string{}
	for _, l := range layers {
		note := l.note
		if note == "" && l.tel == nil {
			note = "no telemetry object"
		}
		if note != "" {
			fmt.Fprintf(&b, "qwen %s settings %s: %s\n", l.name, l.path, note)
		}
		if l.tel == nil {
			continue
		}
		var parts []string
		for _, key := range l.tel.keys {
			v := qwenShow(l.tel.vals[key])
			parts = append(parts, key+"="+v)
			effective[key] = v
			source[key] = l.name + " settings"
			if strings.Contains(v, "$") {
				parts[len(parts)-1] += " (Qwen expands $VAR)"
			}
		}
		fmt.Fprintf(&b, "qwen %s settings %s: %s\n", l.name, l.path, strings.Join(parts, " "))
	}
	envOver := func(key string, envs ...string) {
		for _, e := range envs {
			if v, ok := os.LookupEnv(e); ok {
				shown := qwenShow(v)
				if key == "enabled" || key == "logPrompts" {
					shown = fmt.Sprint(v == "true" || v == "1")
				}
				effective[key] = shown
				source[key] = e
				return
			}
		}
	}
	envOver("enabled", qwenEnabledEnv)
	envOver("otlpEndpoint", qwenEndpointEnv, qwenOTELEnv)
	envOver("otlpProtocol", qwenProtocolEnv)
	envOver("logPrompts", qwenPromptsEnv)
	envOver("outfile", qwenOutfileEnv)
	show := func(key, fallback string) string {
		if v, ok := effective[key]; ok {
			return v + " (" + source[key] + ")"
		}
		return fallback + " (default)"
	}
	fmt.Fprintf(&b, "qwen effective without CLI flags: enabled=%s otlpEndpoint=%s otlpProtocol=%s logPrompts=%s\n",
		show("enabled", "false"), show("otlpEndpoint", "http://localhost:4317"), show("otlpProtocol", `"grpc"`), show("logPrompts", "true"))
	enabled := effective["enabled"] == "true"
	_, outfile := effective["outfile"]
	endpoint := strings.Trim(effective["otlpEndpoint"], `"`)
	protocol := strings.Trim(effective["otlpProtocol"], `"`)
	hit := enabled && !outfile && strings.TrimRight(endpoint, "/") == want && protocol == "http"
	fmt.Fprintf(&b, "qwen points at %s over http: %t\n", want, hit)
	if outfile {
		fmt.Fprintf(&b, "qwen warning: outfile=%s turns OTLP export off\n", show("outfile", ""))
	}
	if v, ok := effective["logPrompts"]; !ok || v == "true" {
		b.WriteString("qwen warning: logPrompts is on, so prompts and API request/response text are exported\n")
	}
	for _, w := range qwenEnvWarnings(req.Endpoint) {
		fmt.Fprintf(&b, "qwen warning: %s\n", w)
	}
	b.WriteString("qwen: CLI flags (--telemetry-otlp-endpoint, --telemetry-log-prompts, ...) override all of the above\n")
	return b.String()
}
