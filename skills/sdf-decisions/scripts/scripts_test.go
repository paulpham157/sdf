package scripts_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func scriptDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

func scriptPath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(scriptDir(t), name)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("%s is not executable", path)
	}
	return path
}

type result struct {
	stdout, stderr string
	code           int
}

func runCmd(t *testing.T, dir string, env []string, stdin string, name string, args ...string) result {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s %v: %v", name, args, err)
		}
		code = ee.ExitCode()
	}
	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func envWithPath(t *testing.T, binDir string, extra ...string) []string {
	t.Helper()
	env := os.Environ()
	filtered := make([]string, 0, len(env)+2+len(extra))
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") || strings.HasPrefix(item, "GROK_HOOK_EVENT=") {
			continue
		}
		filtered = append(filtered, item)
	}
	filtered = append(filtered, "PATH="+binDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	filtered = append(filtered, extra...)
	return filtered
}

func writeFakeSdf(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "sdf")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScriptsArePOSIXShell(t *testing.T) {
	for _, name := range []string{"brief", "check"} {
		body, err := os.ReadFile(scriptPath(t, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.HasPrefix(text, "#!/bin/sh\n") {
			t.Fatalf("%s shebang: %q", name, text[:min(len(text), 32)])
		}
		for _, banned := range []string{"[[", "source ", "eval "} {
			if strings.Contains(text, banned) {
				t.Fatalf("%s contains %q", name, banned)
			}
		}
	}
	brief, err := os.ReadFile(scriptPath(t, "brief"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(brief)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "GROK_HOOK_EVENT") {
			t.Fatal("scripts/brief must not include the Claude hook GROK_HOOK_EVENT guard")
		}
		if trimmed == "timeout" || strings.HasPrefix(trimmed, "timeout ") {
			t.Fatal("scripts/brief must not run a timeout command")
		}
	}
}

func TestBriefFailOpenAndCapture(t *testing.T) {
	brief := scriptPath(t, "brief")
	dir := t.TempDir()
	bin := t.TempDir()
	marker := filepath.Join(dir, "ran")

	writeFakeSdf(t, bin, "#!/bin/sh\necho 'constraint: do not push'\n")
	out := runCmd(t, dir, envWithPath(t, bin), "", brief)
	if out.code != 0 || out.stderr != "" || out.stdout != "constraint: do not push\n" {
		t.Fatalf("success: %+v", out)
	}

	const unavailable = "sdf decision brief unavailable: run sdf decision list\n"
	writeFakeSdf(t, bin, "#!/bin/sh\necho 'sdf decision brief unavailable: run sdf decision list'\n")
	out = runCmd(t, dir, envWithPath(t, bin), "", brief)
	if out.code != 0 || out.stdout != unavailable {
		t.Fatalf("unavailable: %+v", out)
	}

	writeFakeSdf(t, bin, "#!/bin/sh\necho leaked\nexit 2\n")
	out = runCmd(t, dir, envWithPath(t, bin), "", brief)
	if out.code != 0 || out.stdout != "" || out.stderr != "" {
		t.Fatalf("exit 2: %+v", out)
	}

	writeFakeSdf(t, bin, "#!/bin/sh\necho leaked\nexit 1\n")
	out = runCmd(t, dir, envWithPath(t, bin), "", brief)
	if out.code != 0 || out.stdout != "" {
		t.Fatalf("exit 1: %+v", out)
	}

	writeFakeSdf(t, bin, "#!/bin/sh\nexit 0\n")
	out = runCmd(t, dir, envWithPath(t, bin), "", brief)
	if out.code != 0 || out.stdout != "" {
		t.Fatalf("empty: %+v", out)
	}

	empty := t.TempDir()
	out = runCmd(t, dir, envWithPath(t, empty), "", brief)
	if out.code != 0 || out.stdout != "" {
		t.Fatalf("missing sdf: %+v", out)
	}

	writeFakeSdf(t, bin, "#!/bin/sh\ntouch "+marker+"\necho leaked\n")
	out = runCmd(t, dir, envWithPath(t, bin, "GROK_HOOK_EVENT=session_start"), "", brief)
	if out.code != 0 || out.stdout != "leaked\n" {
		t.Fatalf("GROK_HOOK_EVENT should not skip the skill script: %+v", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("brief did not run sdf under GROK_HOOK_EVENT")
	}

	stdinPath := filepath.Join(dir, "stdin.txt")
	writeFakeSdf(t, bin, "#!/bin/sh\ncat >"+stdinPath+"\necho ok\n")
	out = runCmd(t, dir, envWithPath(t, bin), "secret from agent stdin\n", brief)
	if out.code != 0 || out.stdout != "ok\n" {
		t.Fatalf("stdin close: %+v", out)
	}
	got, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("brief must close sdf stdin, got %q", got)
	}
}

func TestBriefForwardsArgs(t *testing.T) {
	brief := scriptPath(t, "brief")
	dir := t.TempDir()
	bin := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	writeFakeSdf(t, bin, "#!/bin/sh\nprintf '%s\\n' \"$@\" >"+argsPath+"\necho briefed\n")
	out := runCmd(t, dir, envWithPath(t, bin), "", brief, "--db", "/tmp/custom.sqlite", "--format", "text")
	if out.code != 0 || out.stdout != "briefed\n" {
		t.Fatalf("%+v", out)
	}
	got, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "decision\nbrief\n--db\n/tmp/custom.sqlite\n--format\ntext\n"
	if string(got) != want {
		t.Fatalf("args=%q want %q", got, want)
	}
}

func TestCheckPreservesExitCodesAndArgs(t *testing.T) {
	check := scriptPath(t, "check")
	dir := t.TempDir()
	bin := t.TempDir()
	argsPath := filepath.Join(dir, "args")

	writeFakeSdf(t, bin, "#!/bin/sh\nprintf '%s\\n' \"$@\" >"+argsPath+"\necho 'allowed: push'\nexit 0\n")
	out := runCmd(t, dir, envWithPath(t, bin), "", check, "push")
	if out.code != 0 || out.stdout != "allowed: push\n" || out.stderr != "" {
		t.Fatalf("exit 0: %+v", out)
	}

	for _, code := range []int{1, 2, 3} {
		writeFakeSdf(t, bin, fmt.Sprintf("#!/bin/sh\necho 'out %d'\necho 'err %d' >&2\nexit %d\n", code, code, code))
		out = runCmd(t, dir, envWithPath(t, bin), "", check, "push")
		if out.code != code || out.stdout != fmt.Sprintf("out %d\n", code) || out.stderr != fmt.Sprintf("err %d\n", code) {
			t.Fatalf("exit %d: %+v", code, out)
		}
	}

	action := "replace sqlite; echo pwned"
	writeFakeSdf(t, bin, "#!/bin/sh\nprintf '%s\\n' \"$@\" >"+argsPath+"\necho ok\n")
	out = runCmd(t, dir, envWithPath(t, bin), "", check, action, "--db", "/tmp/store.sqlite", "--json")
	if out.code != 0 || out.stdout != "ok\n" {
		t.Fatalf("forward: %+v", out)
	}
	got, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "decision\ncheck\n" + action + "\n--db\n/tmp/store.sqlite\n--json\n"
	if string(got) != want {
		t.Fatalf("args=%q want %q", got, want)
	}
	// A shell eval of ACTION would have created this file next to the args dump.
	if _, err := os.Stat(filepath.Join(dir, "pwned")); !os.IsNotExist(err) {
		t.Fatal("action text was evaluated")
	}

	empty := t.TempDir()
	out = runCmd(t, dir, envWithPath(t, empty), "", check, "push")
	if out.code == 0 {
		t.Fatalf("missing sdf must not be swallowed: %+v", out)
	}
	if out.code != 127 {
		t.Fatalf("missing sdf exit %d, want 127: %+v", out.code, out)
	}
	if out.stdout != "" || !strings.Contains(out.stderr, "sdf not found on PATH") {
		t.Fatalf("missing sdf stderr: %+v", out)
	}
}

var (
	sdfOnce sync.Once
	sdfBin  string
	sdfErr  error
)

func builtSDF(t *testing.T) string {
	t.Helper()
	sdfOnce.Do(func() {
		root := filepath.Clean(filepath.Join(scriptDir(t), "..", "..", ".."))
		tmp, err := os.MkdirTemp("", "sdf-skill-scripts-bin-")
		if err != nil {
			sdfErr = err
			return
		}
		sdfBin = filepath.Join(tmp, "sdf")
		cmd := exec.Command("go", "build", "-o", sdfBin, "./cmd/sdf")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		sdfErr = cmd.Run()
		if sdfErr != nil {
			sdfErr = fmt.Errorf("go build sdf: %w\n%s", sdfErr, stderr.String())
		}
	})
	if sdfErr != nil {
		t.Fatal(sdfErr)
	}
	return sdfBin
}

func TestBriefAndCheckAgainstATempStore(t *testing.T) {
	bin := builtSDF(t)
	binDir := t.TempDir()
	if err := os.Symlink(bin, filepath.Join(binDir, "sdf")); err != nil {
		t.Fatal(err)
	}
	env := envWithPath(t, binDir)
	dir := t.TempDir()
	brief := scriptPath(t, "brief")
	check := scriptPath(t, "check")

	empty := runCmd(t, dir, env, "", brief)
	if empty.code != 0 || empty.stdout != "" || empty.stderr != "" {
		t.Fatalf("missing store brief: %+v", empty)
	}

	allowed := runCmd(t, dir, env, "", check, "push")
	if allowed.code != 0 || allowed.stdout != "allowed: push\n" || allowed.stderr != "" {
		t.Fatalf("empty store check: %+v", allowed)
	}
	if _, err := os.Stat(filepath.Join(dir, "sdf-telemetry.sqlite")); !os.IsNotExist(err) {
		t.Fatal("check created a default store")
	}

	add := runCmd(t, dir, env, "", bin,
		"decision", "add",
		"--id", "dont-push",
		"--statement", "Do not push until the user says so.",
		"--status", "decision",
		"--decided-by", "user",
	)
	if add.code != 0 || add.stdout != "" || add.stderr != "" {
		t.Fatalf("add: %+v", add)
	}

	got := runCmd(t, dir, env, "", brief)
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("brief: %+v", got)
	}
	if !strings.Contains(got.stdout, "dont-push") || !strings.Contains(got.stdout, "Do not push until the user says so.") {
		t.Fatalf("brief stdout: %q", got.stdout)
	}

	forbid := runCmd(t, dir, env, "", check, "push")
	if forbid.code != 1 || forbid.stderr != "" || !strings.Contains(forbid.stdout, "forbidden: push\n") || !strings.Contains(forbid.stdout, "dont-push") {
		t.Fatalf("check push: %+v", forbid)
	}

	merge := runCmd(t, dir, env, "", check, "merge")
	if merge.code != 0 || merge.stdout != "allowed: merge\n" {
		t.Fatalf("check merge: %+v", merge)
	}

	other := t.TempDir()
	viaDB := runCmd(t, other, env, "", check, "--db", filepath.Join(dir, "sdf-telemetry.sqlite"), "push")
	if viaDB.code != 1 || !strings.Contains(viaDB.stdout, "forbidden: push\n") {
		t.Fatalf("check --db: %+v", viaDB)
	}

	arch := "replace sqlite; echo pwned"
	archOut := runCmd(t, dir, env, "", check, arch)
	if archOut.code != 0 || archOut.stdout != "allowed: replace sqlite; echo pwned (architecture)\n" {
		t.Fatalf("unusual action: %+v", archOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); !os.IsNotExist(err) {
		t.Fatal("unusual action text was evaluated")
	}

	usage := runCmd(t, dir, env, "", check)
	if usage.code != 2 {
		t.Fatalf("usage: %+v", usage)
	}

	missingDir := filepath.Join(dir, "no-such-dir", "telemetry.sqlite")
	storeErr := runCmd(t, dir, env, "", check, "--db", missingDir, "push")
	if storeErr.code != 3 || storeErr.stdout != "" || !strings.Contains(storeErr.stderr, "does not exist") {
		t.Fatalf("store error: %+v", storeErr)
	}

	js := runCmd(t, dir, env, "", check, "--json", "push")
	if js.code != 1 || !strings.Contains(js.stdout, `"allowed": false`) {
		t.Fatalf("json: %+v", js)
	}
}
