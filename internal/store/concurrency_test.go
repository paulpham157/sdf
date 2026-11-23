package store_test

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

// TestOpenSetsWALAndBusyTimeout checks the pragmas every pooled connection
// of an opened store needs so that a second process (sdf summary, a second
// receiver) waits for the lock instead of failing with SQLITE_BUSY.
func TestOpenSetsWALAndBusyTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mode, timeout, err := store.Pragmas(s)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	if timeout <= 0 {
		t.Errorf("busy_timeout = %d, want > 0", timeout)
	}
}

// TestConcurrentWritersAndReaderLoseNothing reproduces the QA report: two
// stores on one file (the receiver and a second writer, each its own
// connection like a separate process) insert in parallel while a third
// store reads like sdf summary. Every insert must succeed and every record
// must be stored.
func TestConcurrentWritersAndReaderLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.sqlite")
	const writers, perWriter = 2, 40

	stores := make([]*store.Store, writers+1)
	for i := range stores {
		s, err := store.Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		defer s.Close()
		stores[i] = s
	}

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter+1000)
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := stores[writers].Load(); err != nil {
				errs <- fmt.Errorf("reader: %w", err)
				return
			}
		}
	}()
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			wg.Add(1)
			go func(w, i int) {
				defer wg.Done()
				sid := fmt.Sprintf("w%d-%d", w, i)
				if _, err := stores[w].Insert([]record.Record{{
					Signal:      "logs",
					ServiceName: record.Str("claude-code"),
					SessionID:   record.Str(sid),
					Name:        record.Str("claude_code.api_request"),
					Attributes:  map[string]any{"session.id": sid},
					Resource:    map[string]any{"service.name": "claude-code"},
				}}); err != nil {
					errs <- fmt.Errorf("writer %s: %w", sid, err)
				}
			}(w, i)
		}
	}
	wg.Wait()
	close(stop)
	<-readerDone
	close(errs)
	failed := 0
	for err := range errs {
		failed++
		if failed <= 3 {
			t.Error(err)
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d operations failed", failed, writers*perWriter)
	}
	rows, err := stores[0].Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != writers*perWriter {
		t.Fatalf("stored %d records, want %d", len(rows), writers*perWriter)
	}
}
