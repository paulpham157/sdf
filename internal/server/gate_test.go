package server

import (
	"runtime"
	"testing"
	"time"
)

func TestCommitGateWaitReturnsWithoutALeftoverGoroutine(t *testing.T) {
	g := newCommitGate()
	g.add()
	g.add()
	start := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		g.wait(5 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if got := runtime.NumGoroutine(); got > start+2 {
		t.Fatalf("goroutines %d after timed-out waits, started at %d", got, start)
	}
	g.done()
	g.done()
	done := make(chan struct{})
	go func() {
		g.wait(time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("wait did not return after the commits finished")
	}
}
