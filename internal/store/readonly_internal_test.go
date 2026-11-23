package store

import "testing"

// TestReadOnlyFallbackDecision covers when OpenReadOnly retries with
// immutable=1. It needs no file permissions, so it runs as root too, where
// TestOpenReadOnlyWALInReadOnlyDirectory skips.
func TestReadOnlyFallbackDecision(t *testing.T) {
	cases := []struct {
		code      int
		walExists bool
		want      bool
	}{
		{sqliteReadonlyDirectory, false, true},
		{sqliteReadonlyCantInit, false, true},
		// Writes still in -wal are not in the main file; immutable=1 would
		// silently drop them.
		{sqliteReadonlyDirectory, true, false},
		{sqliteReadonlyCantInit, true, false},
		// Any other failure is reported, not papered over.
		{sqliteReadonly, false, false},
		{sqliteReadonlyRollback, false, false},
		{5, false, false},  // SQLITE_BUSY
		{14, false, false}, // SQLITE_CANTOPEN
		{0, false, false},
	}
	for _, c := range cases {
		if got := readOnlyFallback(c.code, c.walExists); got != c.want {
			t.Errorf("readOnlyFallback(%d, wal=%v) = %v, want %v", c.code, c.walExists, got, c.want)
		}
	}
}
