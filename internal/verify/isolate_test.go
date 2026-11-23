package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/task"
)

// requireIsolation skips where namespaces are unavailable. Tests never
// change the host to get them. On this repository's CI (GitHub's
// ubuntu-24.04 runners) these tests skip: AppArmor's
// kernel.apparmor_restrict_unprivileged_userns=1 confines unprivileged user
// namespaces so they cannot mount. A workflow step before `go test`,
//
//   - run: sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
//
// would run them; it is not added here (changing .github/workflows needs
// the repository owner's approval and a token with workflow scope).
func requireIsolation(t *testing.T) {
	t.Helper()
	if ok, why := isolationAvailable(); !ok {
		t.Skipf("isolation unavailable here, so this isolation test cannot run: %s.%s "+
			"The other tests run these checks unisolated (AllowUnisolated).", why, apparmorHint())
	}
}

// apparmorHint names the AppArmor userns restriction only where it is on.
func apparmorHint() string {
	b, err := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")
	if err != nil || strings.TrimSpace(string(b)) != "1" {
		return ""
	}
	return " kernel.apparmor_restrict_unprivileged_userns is 1 here (as on GitHub's ubuntu-24.04 runners): " +
		"`sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0` before `go test` enables these tests " +
		"(the CI workflow step is not added: workflow changes need the owner's approval)."
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

// An isolated check has no capabilities and its own /proc, where pid 1 is
// the namespace init, not the verifier's processes.
func TestIsolatedCheckHasNoCapabilitiesAndItsOwnProc(t *testing.T) {
	requireIsolation(t)
	f := newFixture(t, task.CriterionInput{Name: "caps", Argv: []string{"sh", "-c",
		`grep -Eq '^CapEff:[[:space:]]+0+$' /proc/self/status && grep -Eq '^CapAmb:[[:space:]]+0+$' /proc/self/status && tr '\0' ' ' </proc/1/cmdline | grep -q ` + nsInitArg},
		Designated: []string{"tests/*.sh"}})
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	o := f.opts(sha)
	o.AllowUnisolated = false
	if res := mustRun(t, o); res.Status != StatusPass {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
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
