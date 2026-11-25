package setup

import (
	"bytes"
	"encoding/json"
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
//
// exists says the file is there. An existing file that is empty (or only
// whitespace or a BOM) is refused, as every other JSON agent refuses a file
// that is not JSON (planMerge): Qwen Code itself fails to load it
// (JSON.parse("") throws), and an empty file may be a truncated one, which
// sdf should not quietly replace. A missing file is created from {}.
func qwenMerge(existing []byte, exists bool, ep Endpoint) ([]byte, *qwenObject, []string, bool, error) {
	bom := bytes.HasPrefix(existing, utf8BOM)
	body := bytes.TrimPrefix(existing, utf8BOM)
	if exists && len(bytes.TrimSpace(body)) == 0 {
		return nil, nil, nil, false, errors.New("it is empty, which Qwen Code rejects as invalid JSON; put {} in it (or delete it) and rerun")
	}
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
	stopChanged, stopWarnings := qwenInstallStop(root)
	warnings = append(warnings, stopWarnings...)
	changed = changed || stopChanged
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

// qwenInstallStop adds the Stop decision reminder to settings.json hooks.
// A hooks value that is not an object, or a Stop value that is not an
// array, is left alone and named in a warning so telemetry still writes.
func qwenInstallStop(root *qwenObject) (bool, []string) {
	cmd := stopHookCommand("qwen")
	raw, ok := root.get("hooks")
	var hooks *qwenObject
	if !ok {
		hooks = newQwenObject()
		root.set("hooks", hooks)
	} else {
		obj, is := raw.(*qwenObject)
		if !is {
			return false, []string{`"hooks" is not an object; the Stop hook was not added`}
		}
		hooks = obj
	}
	raw, ok = hooks.get("Stop")
	var groups []any
	if !ok {
		groups = []any{}
	} else {
		arr, is := raw.([]any)
		if !is {
			return false, []string{`"hooks.Stop" is not an array; the Stop hook was not added`}
		}
		groups = arr
	}
	changed := false
	found := false
	for _, g := range groups {
		group, is := g.(*qwenObject)
		if !is {
			continue
		}
		list, is := group.vals["hooks"].([]any)
		if !is {
			continue
		}
		for _, h := range list {
			hook, is := h.(*qwenObject)
			if !is {
				continue
			}
			command, _ := hook.vals["command"].(string)
			if strings.TrimPrefix(command, claudeGuard) != strings.TrimPrefix(cmd, claudeGuard) {
				continue
			}
			found = true
			if !qwenPositiveTimeout(hook.vals["timeout"]) {
				hook.set("timeout", 2)
				changed = true
			}
		}
	}
	if found {
		return changed, nil
	}
	hook := newQwenObject()
	hook.set("type", "command")
	hook.set("command", cmd)
	hook.set("timeout", 2)
	group := newQwenObject()
	group.set("hooks", []any{hook})
	groups = append(groups, group)
	hooks.set("Stop", groups)
	return true, nil
}

func qwenPositiveTimeout(v any) bool {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return err == nil && i > 0
	case int:
		return n > 0
	case int64:
		return n > 0
	case float64:
		return n > 0 && n == float64(int64(n))
	default:
		return false
	}
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
	// Run has already validated the endpoint (normalize); checking again
	// keeps qwen safe if it is ever called on its own. checkEndpoint and
	// shQuote are the helpers every agent uses (shellquote.go).
	if err := checkEndpoint(&req.Endpoint); err != nil {
		return Result{}, err
	}
	path := qwenUserSettingsPath(req)
	target, err := qwenResolveLink(path)
	if err != nil {
		return Result{}, err
	}
	mkdir, err := qwenCheckLinkTarget(path, target)
	if err != nil {
		return Result{}, err
	}
	existing, err := os.ReadFile(target)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return Result{}, readErr(path, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Qwen Code exports traces, logs, and metrics when telemetry.enabled is true and an OTLP endpoint is set.\n")
	fmt.Fprintf(&b, "otlpProtocol \"http\" sends OTLP/JSON to the HTTP receiver; logPrompts (default true) is set to false.\n")
	fmt.Fprintf(&b, "Checked against qwen-code v0.28.4. Docs: https://github.com/QwenLM/qwen-code/blob/v0.28.4/docs/developers/development/telemetry.md\n")
	fmt.Fprintf(&b, "Qwen Code always sends tool arguments (shell commands, write_file and edit content) and tool error text on qwen-code.tool_call, whatever logPrompts says; sdf discards them before storing.\n")
	fmt.Fprintf(&b, "Overrides, highest first: CLI flags, QWEN_TELEMETRY_* and OTEL_EXPORTER_OTLP_ENDPOINT, system settings %s, a trusted workspace .qwen/settings.json, then this file.\n\n", qwenSystemSettingsPath())
	merged, tel, warnings, changed, mergeErr := qwenMerge(existing, exists, req.Endpoint)
	fmt.Fprintf(&b, "export %s=%s\nexport %s=%s\nexport %s=%s\nexport %s=%s\n\n",
		qwenEnabledEnv, shQuote("true"), qwenEndpointEnv, shQuote(req.Endpoint.base()),
		qwenProtocolEnv, shQuote("http"), qwenPromptsEnv, shQuote("false"))
	if mergeErr != nil {
		fmt.Fprintf(&b, "%s was not changed: %v\n", path, mergeErr)
		fmt.Fprintf(&b, "Put these keys in its \"telemetry\" object, or use the variables above:\n  \"enabled\": true, \"otlpEndpoint\": %q, \"otlpProtocol\": \"http\", \"logPrompts\": false\n", req.Endpoint.base())
		if req.Write {
			// --write asked for a change that did not happen: fail, as
			// sdf init claude-code does for a settings file it cannot parse.
			return Result{Text: b.String()}, fmt.Errorf("%s was not changed: %w", path, mergeErr)
		}
		return Result{Text: b.String()}, nil
	}
	shown, hidden := qwenManagedView(tel)
	block, _ := qwenWriteJSON(shown, "  ")
	fmt.Fprintf(&b, "%s, the \"telemetry\" keys sdf manages after the merge (other keys, including %d other telemetry keys, are kept as they are and not shown, since they may hold secrets such as otlpHeaders):\n%s\n", path, hidden, block)
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
	backup, err := qwenWriteAtomic(path, target, merged, mkdir)
	if err != nil {
		return Result{}, err
	}
	b.WriteString("wrote " + target + "\n")
	if msg := linkEscape(path, target); msg != "" {
		b.WriteString("warning: " + msg + "; sdf wrote through it\n")
	}
	if backup != "" {
		b.WriteString("backup " + backup + "\n")
	}
	return Result{Text: b.String(), Wrote: true}, nil
}

// qwenManaged are the telemetry keys sdf init qwen sets. Only these are
// printed: other telemetry keys (otlpHeaders, endpoints with credentials)
// and the rest of settings.json (mcpServers env) may hold secrets.
var qwenManaged = []string{"enabled", "otlpEndpoint", "otlpProtocol", "logPrompts", qwenSensitive}

func qwenManagedView(tel *qwenObject) (*qwenObject, int) {
	view := newQwenObject()
	for _, key := range qwenManaged {
		if v, ok := tel.get(key); ok {
			view.set(key, v)
		}
	}
	return view, len(tel.keys) - len(view.keys)
}

// qwenCheckLinkTarget decides whether settings.json may be written at
// target, the end of the symlink chain that starts at path (they are equal
// when path is not a symlink), and whether missing parent directories may be
// created (0700).
//
//   - No symlink: the settings directory (~/.qwen or $QWEN_HOME) and its
//     parents may be created, as Qwen Code itself does.
//   - A symlink must end at a .json file. Anything else (settings.json ->
//     ../.bashrc) is refused, existing or not, so sdf never writes JSON into
//     a file of another format.
//   - An existing target is a regular file (qwenWriteAtomic checks that),
//     so a link into a dotfiles repo works.
//   - A dangling target inside the settings directory may get its parent
//     directories created. A dangling target outside it (stow-style, into a
//     dotfiles checkout) is created only when its directory already exists
//     and is a real directory: sdf does not create directories outside the
//     settings directory.
func qwenCheckLinkTarget(path, target string) (bool, error) {
	if target == path {
		return true, nil
	}
	// The extension is compared exactly: Qwen Code reads settings.json, and
	// sdf does not treat Settings.JSON or a .Json file as the same format.
	if filepath.Ext(target) != ".json" {
		return false, fmt.Errorf("%s is a symlink to %s, which is not a .json file; refusing to write settings there", path, target)
	}
	if _, err := os.Lstat(target); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	dir := filepath.Dir(path)
	if realDir, err := filepath.EvalSymlinks(dir); err == nil {
		dir = realDir
	}
	parent := filepath.Dir(target)
	if realParent, err := filepath.EvalSymlinks(parent); err == nil {
		if info, err := os.Stat(realParent); err != nil || !info.IsDir() {
			return false, fmt.Errorf("%s is a symlink to %s, whose directory is not a directory", path, target)
		}
		return false, nil
	}
	if rel, err := filepath.Rel(dir, filepath.Clean(target)); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && qwenRealAncestorInside(parent, dir) {
		return true, nil
	}
	return false, fmt.Errorf("%s is a symlink to %s, whose directory %s does not exist and is outside %s; create it first (sdf does not create directories there)", path, target, parent, filepath.Dir(path))
}

// qwenRealAncestorInside reports whether the deepest existing ancestor of
// dir resolves (through symlinks) to root or a directory under it.
func qwenRealAncestorInside(dir, root string) bool {
	for cur := dir; ; cur = filepath.Dir(cur) {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			rel, err := filepath.Rel(root, real)
			return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
		}
		if filepath.Dir(cur) == cur {
			return false
		}
	}
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
	return "", loopErr(path)
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
func qwenWriteAtomic(path, target string, body []byte, mkdir bool) (string, error) {
	// checkWritable is the precheck every other agent's write runs
	// (replaceFile): it refuses a file with no write bit (0400, 0444, 0000;
	// root could open those), one this user cannot open for writing, a
	// non-regular file, an unwritable directory, and, as root, a symlink
	// that leaves the settings directory and $HOME.
	if err := checkWritable(path); err != nil {
		return "", err
	}
	var old os.FileInfo
	if info, err := os.Stat(target); err == nil {
		old = info
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if mkdir {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", err
		}
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
