package store_test

import (
	"path/filepath"
	"testing"

	"github.com/paulpham157/sdf/internal/store"
)

func TestInMemory(t *testing.T) {
	for path, want := range map[string]bool{
		":memory:": true,
		"":         true,
		filepath.Join(t.TempDir(), "telemetry.sqlite"): false,
	} {
		s, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.InMemory(); got != want {
			t.Errorf("Open(%q).InMemory() = %v, want %v", path, got, want)
		}
		_ = s.Close()
	}
}
