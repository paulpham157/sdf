package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/task"
)

// B-3 (atk_race3): a tmux server that was already running outside the
// namespace starts a daemon on request, outside the namespace, which then
// races the next check. Its socket is under /tmp, which an isolated check
// does not see.
func TestB3OutsideServerIsUnreachable(t *testing.T) {
	requireIsolation(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	out, err := os.MkdirTemp("/tmp", "sdf-outside-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(out)
	sock, flag := filepath.Join(out, "s"), filepath.Join(out, "pwned")
	if b, err := exec.Command("tmux", "-S", sock, "new-session", "-d", "sleep 600").CombinedOutput(); err != nil {
		t.Skipf("tmux: %v %s", err, b)
	}
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()
	f := newFixture(t, task.CriterionInput{Name: "a", Argv: []string{"sh", "-c", "tmux -S " + sock + " run-shell 'touch " + flag + "'; exit 0"}, Designated: []string{"tests/*.sh"}})
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	o := f.opts(sha)
	o.AllowUnisolated = false
	mustRun(t, o)
	if _, err := os.Stat(flag); err == nil {
		t.Fatal("a check reached a tmux server outside its namespace")
	}
	// Sanity: from outside, the server does run commands.
	if b, err := exec.Command("tmux", "-S", sock, "run-shell", "touch "+flag).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	if _, err := os.Stat(flag); err != nil {
		t.Fatal("tmux server did not run the command from outside")
	}
}

// An isolated check sees an empty /tmp and real HOME, and only loopback;
// its own directories are there.
func TestIsolatedCheckSeesNoHostTmpHomeOrNetwork(t *testing.T) {
	requireIsolation(t)
	out, err := os.MkdirTemp("/tmp", "sdf-outside-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(out)
	wd, _ := os.Getwd()
	home, err := os.MkdirTemp(wd, "realhome-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	for _, d := range []string{out, home} {
		if err := os.WriteFile(filepath.Join(d, "secret"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	script := "test ! -e " + out + "/secret && test ! -e " + home + "/secret && " +
		`test "$(grep -c : /proc/net/dev)" = 1 && grep -q 'lo:' /proc/net/dev && ` +
		`test -d "$TMPDIR" && test -d "$HOME" && test -f src/feature.txt`
	f := newFixture(t, task.CriterionInput{Name: "look", Argv: []string{"sh", "-c", script}, Designated: []string{"tests/*.sh"}})
	sha := f.agentCommit(t, map[string]string{"src/feature.txt": "done\n"})
	o := f.opts(sha)
	o.AllowUnisolated = false
	res := mustRun(t, o)
	if res.Status != StatusPass {
		t.Fatalf("%s %+v", res.Status, res.Reasons)
	}
	crit := res.Evidence[len(res.Evidence)-1].Value
	if crit["network"] != "loopback" || !strings.Contains(strings.Join(crit["masked"].([]string), " "), "/tmp") {
		t.Fatalf("network %v masked %v", crit["network"], crit["masked"])
	}
	// --allow-network NAME keeps the host network for that criterion only.
	o.AllowNetwork = []string{"look"}
	res = mustRun(t, o)
	crit = res.Evidence[len(res.Evidence)-1].Value
	if crit["network"] != "host" {
		t.Fatalf("network %v", crit["network"])
	}
}
