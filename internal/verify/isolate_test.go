package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/task"
)

// requireIsolation skips where namespaces are unavailable, except on a
// GitHub-hosted Linux runner: there it does what the Action does (lift
// Ubuntu's AppArmor restriction on unprivileged user namespaces with sudo)
// and then requires isolation, so CI shows whether the Action can work.
func requireIsolation(t *testing.T) {
	t.Helper()
	if ok, _ := isolationAvailable(); ok {
		return
	}
	if runtime.GOOS == "linux" && os.Getenv("GITHUB_ACTIONS") == "true" && os.Getenv("RUNNER_ENVIRONMENT") == "github-hosted" {
		out, err := exec.Command("sudo", "-n", "sysctl", "-w", "kernel.apparmor_restrict_unprivileged_userns=0").CombinedOutput()
		t.Logf("sudo sysctl: %v %s", err, out)
		resetIsolationProbe()
		if ok, why := isolationAvailable(); !ok {
			t.Fatalf("isolation unavailable on a GitHub-hosted runner even after the sysctl: %s", why)
		}
		return
	}
	_, why := isolationAvailable()
	t.Skipf("isolation unavailable here: %s", why)
}

func TestIsolationAvailableOnGitHubRunners(t *testing.T) {
	requireIsolation(t)
}

// B-3: a check leaves a setsid daemon that rewrites the next check's script
// between the verifier's hash and the script's read (atk_race2 passed 3 of
// 5 runs at 2c7d398). In a namespace the daemon dies with the check.
func TestB3SetsidDaemonDiesWithItsCheck(t *testing.T) {
	requireIsolation(t)
	beat := filepath.Join(t.TempDir(), "heartbeat")
	f := newFixture(t,
		task.CriterionInput{Name: "a-prep", Argv: []string{"sh", "-c", "sh src/prep.sh"}, Designated: []string{"tests/*.sh"}},
		task.CriterionInput{Name: "b-accept", Argv: []string{"sh", "tests/run.sh"}, Designated: []string{"tests/*.sh"}},
	)
	// The daemon waits for a-prep to finish and the verifier to hash, then
	// flips tests/run.sh to "exit 0" and back, over and over.
	daemon := `i=0; while [ $i -lt 400 ]; do i=$((i+1)); echo $i > ` + beat + `; sleep 0.2;
  printf 'exit 0\n' > tests/.t; mv tests/.t tests/run.sh; sleep 0.001; printf '#!/bin/sh\ngrep -q done src/feature.txt\n' > tests/.t; mv tests/.t tests/run.sh; done`
	sha := f.agentCommit(t, map[string]string{"src/prep.sh": "setsid sh -c '" + strings.ReplaceAll(daemon, "'", `'"'"'`) + "' </dev/null >/dev/null 2>&1 &\nsleep 0.1\nexit 0\n"})
	o := f.opts(sha)
	o.AllowUnisolated = false
	res := mustRun(t, o)
	if res.Verdict.Value["isolation"] != IsolationNamespace {
		t.Fatalf("isolation %v", res.Verdict.Value["isolation"])
	}
	// The feature is not done: b-accept fails honestly, on an unchanged
	// checkout.
	if res.Status != StatusFail || !hasCode(res, CodeCheckFailed) || hasCode(res, CodeCheckoutModified) {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
	b1, _ := os.ReadFile(beat)
	time.Sleep(600 * time.Millisecond)
	b2, _ := os.ReadFile(beat)
	if string(b1) != string(b2) {
		t.Fatalf("the daemon outlived its check: heartbeat %q -> %q", b1, b2)
	}
}

// Without isolation, nothing runs unless the caller allows it, and then the
// verdict says so.
func TestNoIsolationIsErrorUnlessAllowed(t *testing.T) {
	saved := isolationAvailable
	isolationAvailable = func() (bool, string) { return false, "test: no namespaces" }
	defer func() { isolationAvailable = saved }()
	f := newFixture(t)
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	o := f.opts(sha)
	o.AllowUnisolated = false
	res := mustRun(t, o)
	if res.Status != StatusError || !hasCode(res, CodeIsolationUnavailable) || strings.Contains(kinds(res), "criterion:") {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
	o.AllowUnisolated = true
	res = mustRun(t, o)
	if res.Status != StatusPass || res.Verdict.Value["isolation"] != IsolationNone || len(res.Warnings) == 0 {
		t.Fatalf("%s %+v %v %q", res.Status, res.Reasons, res.Verdict.Value["isolation"], res.Warnings)
	}
}
