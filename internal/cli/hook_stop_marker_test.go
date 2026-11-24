package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A marker past the read cap must neither be overwritten in the middle nor
// grow without bound, and the session it records must stay reminded.
func TestHookStopMarkerPastCapStaysOncePerSession(t *testing.T) {
	db := filepath.Join(t.TempDir(), "telemetry.sqlite")
	ingest(t, db, [][]byte{
		hookPayload("claude-code", "claude-code", "sess-1", stopSHA, "/tmp/repo", "main", time.Now().UTC()),
	})
	var old strings.Builder
	for i := 0; old.Len() <= 1<<20+4096; i++ {
		fmt.Fprintf(&old, "old-%07d\n", i)
	}
	marker := db + ".stop-reminded"
	if err := os.WriteFile(marker, []byte(old.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"session_id":"sess-1","stop_hook_active":false}`
	first := runHook(t, []string{"hook", "stop", "--agent", "claude-code", "--db", db}, body, nil)
	if first.code != 0 || first.stderr != "" {
		t.Fatalf("first %d %q %q", first.code, first.stdout, first.stderr)
	}
	assertStopBlock(t, "claude-code", first.stdout)
	second := runHook(t, []string{"hook", "stop", "--agent", "claude-code", "--db", db}, body, nil)
	if second.code != 0 || second.stderr != "" || second.stdout != "" {
		t.Fatalf("second %d %q %q", second.code, second.stdout, second.stderr)
	}
	info, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1<<20 {
		t.Fatalf("marker is %d bytes, want at most %d", info.Size(), 1<<20)
	}
}
