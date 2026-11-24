//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestUIReadsAlongsideIngest (issue #272) starts a real sdf serve with the
// UI on a free port, reads GET /api/summary and GET /api/snapshot in a loop
// while the samples are posted over OTLP/HTTP several times, and checks
// that every ingest and every UI read succeeds and that the last summary
// shows every sample session. The UI reads through its own read-only
// connection to the store file, not the receiver's writer connection.
func TestUIReadsAlongsideIngest(t *testing.T) {
	files := sampleFiles(t)
	uiAddr := freeLoopbackAddr(t)
	srv := startServeArgs(t, "--ui-addr", uiAddr)
	uiURL := "http://" + uiAddr + "/"

	stop := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	var readsMu sync.Mutex
	reads := 0
	for _, route := range []string{"api/summary", "api/snapshot"} {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			client := &http.Client{Timeout: 30 * time.Second}
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, err := client.Get(url)
				if err != nil {
					errs <- err
					return
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					errs <- fmt.Errorf("GET %s: %d %s", url, resp.StatusCode, body)
					return
				}
				readsMu.Lock()
				reads++
				readsMu.Unlock()
			}
		}(uiURL + route)
	}
	for i := 0; i < 5; i++ {
		postSamples(t, srv.httpBase, files)
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if reads == 0 {
		t.Fatal("no UI read completed during ingest")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(uiURL + "api/summary")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/summary: %d %s", resp.StatusCode, body)
	}
	for _, id := range []string{
		`"session_id": "sample-claude-1"`,
		`"session_id": "sample-codex-conv-1"`,
		`"session_id": "sample-copilot-1"`,
		`"session_id": "ses_sampleopencode0001"`,
		`"session_id": "sample-qwen-1"`,
		`"session_id": "2026-01-01T00-00-00-000Z_sample-pi-session"`,
		`"session_id": "sample-gemini-1"`,
	} {
		if !strings.Contains(string(body), id) {
			t.Fatalf("/api/summary missing %s\n%s", id, body)
		}
	}
	srv.stop(t)
}

// freeLoopbackAddr is a 127.0.0.1 address with a port that was free a
// moment ago, for --ui-addr.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}
