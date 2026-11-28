//go:build unix

package live

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAgentCancelKillsGrandchild(t *testing.T) {
	dir := t.TempDir()
	pidfile := dir + "/pid"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("sleep 60 & echo $! > %q; wait", pidfile))
	isolateAgentCommand(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(pidfile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			_ = cmd.Wait()
			t.Fatal("grandchild pid was not written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("wait hung")
	}
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	stat := strings.TrimSpace(string(out))
	if err == nil && stat != "" && !strings.HasPrefix(stat, "Z") {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("grandchild %d still running (%s)", pid, stat)
	}
}
