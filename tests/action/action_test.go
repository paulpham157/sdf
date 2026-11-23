// Package action tests the scripts of .github/actions/sdf-verify the way a
// runner calls them: GITHUB_ACTION_PATH, RUNNER_TEMP, GITHUB_OUTPUT, and
// GITHUB_STEP_SUMMARY set, inputs as SDF_* variables, and a subject
// repository cloned from a "remote" so origin/<base> exists.
package action

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func actionDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", ".github", "actions", "sdf-verify")
}

func need(t *testing.T, tools ...string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the action scripts are bash")
	}
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
}

func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeCommit(t *testing.T, dir string, files map[string]string, msg string) string {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", msg)
	return git(t, dir, "rev-parse", "HEAD")
}

type env struct {
	action, runnerTemp, sdf, subject string
	good, tampered, trailered        string
	isolated                         bool
}

// passed checks a case that passes where checks can be isolated; elsewhere
// the action must refuse to run them unisolated: ERROR, exit 3,
// ISOLATION_UNAVAILABLE.
func (e *env) passed(o outcome) bool {
	if e.isolated {
		return o.outputs["status"] == "PASS"
	}
	return o.outputs["status"] == "ERROR" && strings.Contains(o.summary, "ISOLATION_UNAVAILABLE")
}

// isolated reports whether sdf verify can isolate checks here: user, pid,
// mount and network namespaces in which a tmpfs can be mounted. The tests
// never change the host to get there (GitHub's ubuntu-24.04 runners need
// runner-setup.sh, which the action runs, not these tests).
func isolated() bool {
	return exec.Command("unshare", "-Urnpfm", "--mount-proc", "sh", "-c", "mount -t tmpfs none /tmp").Run() == nil
}

// setup builds sdf with build.sh and makes a remote whose main has a
// declared task "feat", and three pull request heads.
func setup(t *testing.T) *env {
	t.Helper()
	need(t, "bash", "jq", "git", "go")
	e := &env{action: actionDir(t), runnerTemp: t.TempDir(), isolated: isolated()}

	out := filepath.Join(e.runnerTemp, "build-output")
	cmd := exec.Command("bash", filepath.Join(e.action, "build.sh"))
	cmd.Env = append(os.Environ(), "GITHUB_ACTION_PATH="+e.action, "RUNNER_TEMP="+e.runnerTemp, "GITHUB_OUTPUT="+out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build.sh: %v\n%s", err, b)
	}
	b, _ := os.ReadFile(out)
	e.sdf = strings.TrimPrefix(strings.TrimSpace(string(b)), "sdf=")

	remote := t.TempDir()
	git(t, remote, "init", "-q", "-b", "main")
	base := writeCommit(t, remote, map[string]string{
		"src/feature.txt": "todo\n",
		"tests/run.sh":    "#!/bin/sh\ngrep -q done src/feature.txt\n",
	}, "base")
	decl := exec.Command(e.sdf, "task", "declare", "--repo", remote, "--write", "--no-data-dir",
		"--id", "feat", "--title", "Feature", "--check", "unit=sh tests/run.sh", "--designated", "unit=tests/*.sh")
	if b, err := decl.CombinedOutput(); err != nil {
		t.Fatalf("declare: %v\n%s", err, b)
	}
	writeCommit(t, remote, nil, "declare task")
	git(t, remote, "checkout", "-q", "-b", "pr", base)
	e.good = writeCommit(t, remote, map[string]string{"src/feature.txt": "done\n"}, "work")
	e.tampered = writeCommit(t, remote, map[string]string{"tests/run.sh": "exit 0\n"}, "weaken")
	git(t, remote, "checkout", "-q", "-b", "pr2", e.good)
	e.trailered = writeCommit(t, remote, map[string]string{"notes.txt": "n\n"}, "more\n\nSDF-Task: feat")
	git(t, remote, "checkout", "-q", "main")

	e.subject = filepath.Join(t.TempDir(), "subject")
	clone := exec.Command("git", "clone", "-q", remote, e.subject)
	clone.Env = gitEnv()
	if b, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, b)
	}
	return e
}

type outcome struct {
	outputs map[string]string
	summary string
	log     string
}

func (e *env) verify(t *testing.T, vars map[string]string) outcome {
	t.Helper()
	dir := t.TempDir()
	outFile, summary := filepath.Join(dir, "output"), filepath.Join(dir, "summary")
	runnerTemp := filepath.Join(dir, "runner")
	if err := os.MkdirAll(runnerTemp, 0o755); err != nil {
		t.Fatal(err)
	}
	base := map[string]string{
		"SDF_BIN": e.sdf, "SDF_TASK": "", "SDF_TASK_SOURCE": "label", "SDF_LABELS": "[]",
		"SDF_SHA": e.good, "SDF_BASE_REF": "main", "SDF_PROTECTED_BRANCHES": "main", "SDF_REPO": e.subject,
	}
	for k, v := range vars {
		base[k] = v
	}
	cmd := exec.Command("bash", filepath.Join(e.action, "verify.sh"))
	cmd.Env = append(gitEnv(), "GITHUB_ACTION_PATH="+e.action, "RUNNER_TEMP="+runnerTemp,
		"GITHUB_OUTPUT="+outFile, "GITHUB_STEP_SUMMARY="+summary)
	for k, v := range base {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	log, err := cmd.CombinedOutput()
	code := 0
	if x, ok := err.(*exec.ExitError); ok {
		code = x.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	o := outcome{outputs: map[string]string{}, log: string(log)}
	b, _ := os.ReadFile(outFile)
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			o.outputs[k] = v
		}
	}
	s, _ := os.ReadFile(summary)
	o.summary = string(s)
	// The step's exit code is the verdict.
	if want := map[string]int{"PASS": 0, "FAIL": 1, "ERROR": 3}[o.outputs["status"]]; o.outputs["status"] == "" || code != want {
		t.Fatalf("verify.sh exited %d with status %q\n%s", code, o.outputs["status"], log)
	}
	return o
}

func TestActionScripts(t *testing.T) {
	e := setup(t)

	t.Run("pass from a label", func(t *testing.T) {
		o := e.verify(t, map[string]string{"SDF_LABELS": `["bug","sdf-task:feat"]`})
		if !e.passed(o) || o.outputs["task"] != "feat" || !strings.HasPrefix(o.outputs["verdict-hash"], "sha256:") {
			t.Fatalf("%v\n%s", o.outputs, o.log)
		}
		st := o.outputs["status"]
		if !strings.Contains(o.summary, "## sdf verify: "+st) || !strings.Contains(o.log, "verdict "+st+" "+o.outputs["verdict-hash"]) {
			t.Fatalf("summary %q log %q", o.summary, o.log)
		}
		if _, err := os.Stat(filepath.Join(o.outputs["out-dir"], "verdict.json")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tampered test fails with its code in the summary", func(t *testing.T) {
		o := e.verify(t, map[string]string{"SDF_TASK": "feat", "SDF_SHA": e.tampered})
		if o.outputs["status"] != "FAIL" || !strings.Contains(o.summary, "| DESIGNATED_FILE_MODIFIED |") {
			t.Fatalf("%v\n%s", o.outputs, o.summary)
		}
	})
	t.Run("trailer only when allowed", func(t *testing.T) {
		if o := e.verify(t, map[string]string{"SDF_SHA": e.trailered}); o.outputs["status"] != "ERROR" {
			t.Fatalf("label source read a trailer: %v", o.outputs)
		}
		o := e.verify(t, map[string]string{"SDF_SHA": e.trailered, "SDF_TASK_SOURCE": "label-or-trailer"})
		if !e.passed(o) || o.outputs["task"] != "feat" {
			t.Fatalf("%v\n%s", o.outputs, o.log)
		}
	})
	t.Run("unknown task fails closed", func(t *testing.T) {
		o := e.verify(t, map[string]string{"SDF_TASK": "nope"})
		if o.outputs["status"] != "FAIL" || !strings.Contains(o.summary, "TASK_SNAPSHOT_MISSING") {
			t.Fatalf("%v\n%s", o.outputs, o.summary)
		}
	})
	canary := filepath.Join(t.TempDir(), "pwned")
	for name, vars := range map[string]map[string]string{
		"no label":          {},
		"two labels":        {"SDF_LABELS": `["sdf-task:feat","sdf-task:other"]`},
		"injected label":    {"SDF_LABELS": `["sdf-task:$(touch ` + canary + `)"]`},
		"newline in label":  {"SDF_LABELS": `["sdf-task:feat\nx"]`},
		"short sha":         {"SDF_TASK": "feat", "SDF_SHA": "abc123"},
		"bad base ref":      {"SDF_TASK": "feat", "SDF_BASE_REF": "main..x"},
		"missing base ref":  {"SDF_TASK": "feat", "SDF_BASE_REF": "release", "SDF_PROTECTED_BRANCHES": "main release"},
		"unprotected base":  {"SDF_TASK": "feat", "SDF_BASE_REF": "release"},
		"no protected list": {"SDF_TASK": "feat", "SDF_PROTECTED_BRANCHES": ""},
		"bad task source":   {"SDF_TASK_SOURCE": "anything"},
		"verdict not there": {"SDF_TASK": "feat", "SDF_BIN": "/bin/false"},
	} {
		t.Run(name, func(t *testing.T) {
			o := e.verify(t, vars)
			if o.outputs["status"] != "ERROR" {
				t.Fatalf("%v\n%s", o.outputs, o.log)
			}
		})
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("a label name ran as shell")
	}
}

// runner-setup.sh changes nothing off GitHub-hosted runners unless asked,
// and says what it changed.
func TestRunnerSetup(t *testing.T) {
	need(t, "bash")
	action := actionDir(t)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	// Fake sudo and systemctl: record calls; docker.socket is "active".
	for name, body := range map[string]string{
		"sudo":      "echo \"sudo $*\" >>" + calls + "\nexit 0\n",
		"systemctl": "echo \"systemctl $*\" >>" + calls + "\ncase \"$*\" in *docker.socket*) exit 0;; esac\nexit 3\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func(vars ...string) (string, string, int) {
		os.Remove(calls)
		cmd := exec.Command("bash", filepath.Join(action, "runner-setup.sh"))
		cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin"}, vars...)
		out, err := cmd.CombinedOutput()
		code := 0
		if x, ok := err.(*exec.ExitError); ok {
			code = x.ExitCode()
		}
		b, _ := os.ReadFile(calls)
		return string(out), string(b), code
	}
	for _, vars := range [][]string{{}, {"RUNNER_ENVIRONMENT=self-hosted"}, {"RUNNER_ENVIRONMENT=github-hosted", "SDF_CONFIGURE_RUNNER=false"}} {
		out, c, code := run(vars...)
		if code != 0 || strings.Contains(c, "sudo") || !strings.Contains(out, "::notice") {
			t.Fatalf("%v: exit %d calls %q out %q", vars, code, c, out)
		}
	}
	if _, _, code := run("SDF_CONFIGURE_RUNNER=yes"); code != 1 {
		t.Fatalf("bad input exit %d", code)
	}
	out, c, code := run("RUNNER_ENVIRONMENT=github-hosted")
	if code != 0 || !strings.Contains(c, "sudo -n systemctl stop docker.socket") || !strings.Contains(out, "stopped or locked: docker.socket") {
		t.Fatalf("exit %d calls %q out %q", code, c, out)
	}
	if b, _ := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); strings.TrimSpace(string(b)) == "1" &&
		(!strings.Contains(c, "sudo -n sysctl -q -w kernel.apparmor_restrict_unprivileged_userns=0") || !strings.Contains(out, "set kernel.apparmor_restrict_unprivileged_userns=0")) {
		t.Fatalf("sysctl: calls %q out %q", c, out)
	}
	if _, c, _ := run("SDF_CONFIGURE_RUNNER=true"); !strings.Contains(c, "systemctl stop docker.socket") {
		t.Fatalf("true on any runner: %q", c)
	}
}
