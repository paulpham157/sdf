package ui_test

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

// Issue #272: GET /api/summary and GET /api/snapshot read the whole records
// table. They must do it through their own read-only connection, never the
// receiver's writer connection and its mutex, so a long UI read does not
// block OTLP Insert.

// TestUIReadsDoNotUseTheWriterConnection is deterministic: once the
// writer's connection is closed, anything that still reads through it
// fails with "sql: database is closed". The UI routes read the same file
// through a connection of their own, so they keep answering with the
// stored sessions.
func TestUIReadsDoNotUseTheWriterConnection(t *testing.T) {
	db := openDB(t)
	if _, err := db.Insert(uiReadRecords(0, 3)); err != nil {
		t.Fatal(err)
	}
	want := summaryJSON(t, db)
	srv := startUI(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	status, _, body := get(t, srv.URL()+"api/summary")
	if status != http.StatusOK {
		t.Fatalf("/api/summary after the writer closed: %d %s", status, body)
	}
	if body != want {
		t.Fatalf("/api/summary\n%s\nwant\n%s", body, want)
	}
	status, _, body = get(t, srv.URL()+"api/snapshot")
	if status != http.StatusOK {
		t.Fatalf("/api/snapshot after the writer closed: %d %s", status, body)
	}
	if snap := decodeSnapshot(t, body); len(snap.Sessions) != 3 {
		t.Fatalf("/api/snapshot sessions %d, want 3", len(snap.Sessions))
	}
}

// TestUIReadsDoNotBlockInsert is the concurrency check: with a store large
// enough that one full read takes a while, Insert calls made while
// /api/summary and /api/snapshot are read in a loop must not wait for
// those reads. Before the fix every Insert that arrived during a read waited
// for the rest of it on the writer's mutex.
func TestUIReadsDoNotBlockInsert(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a large store")
	}
	db := openDB(t)
	// Grow the store until one full read takes at least 150ms (fewer
	// records under -race, which slows reads down).
	const batch = 2000
	readTime := time.Duration(0)
	for n := 0; n < 40*batch && readTime < 150*time.Millisecond; n += batch {
		if _, err := db.Insert(uiReadRecords(n, batch)); err != nil {
			t.Fatal(err)
		}
		if n+batch >= 4*batch {
			readTime = timeLoad(t, db)
		}
	}
	if readTime < 50*time.Millisecond {
		t.Skipf("a full read takes only %v here; too fast to tell waiting from noise", readTime)
	}
	srv := startUI(t, db)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	reads := 0
	var readsMu sync.Mutex
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
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					errs <- fmt.Errorf("%s: %d", url, resp.StatusCode)
					return
				}
				readsMu.Lock()
				reads++
				readsMu.Unlock()
			}
		}(srv.URL() + route)
	}

	// Let the readers get going, then insert one record at a time, spaced so
	// several inserts land inside each read.
	time.Sleep(readTime / 2)
	const inserts = 24
	latencies := make([]time.Duration, 0, inserts)
	deadline := time.After(2 * time.Minute)
	for i := 0; i < inserts; i++ {
		done := make(chan error, 1)
		start := time.Now()
		go func(i int) {
			_, err := db.Insert(uiReadRecords(1_000_000+i, 1))
			done <- err
		}(i)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("Insert did not finish in time")
		}
		latencies = append(latencies, time.Since(start))
		time.Sleep(readTime / 8)
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if reads == 0 {
		t.Fatal("no UI read completed")
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	median := latencies[len(latencies)/2]
	p90 := latencies[len(latencies)*9/10]
	t.Logf("full read %v; %d UI reads; insert latency median %v p90 %v max %v",
		readTime, reads, median, p90, latencies[len(latencies)-1])
	// A blocked Insert waits out the rest of the UI read, so the median
	// moves with readTime. p90 on a loaded runner jitters above readTime/3
	// without the writer mutex being held (one oldstable run: median 6.6ms,
	// p90 75ms, full read 184ms). p90 still has to finish inside one read.
	if limit := readTime / 3; median > limit {
		t.Fatalf("Insert median latency %v while the UI was reading exceeds %v (a full read takes %v): UI reads are blocking ingest", median, limit, readTime)
	}
	if p90 >= readTime {
		t.Fatalf("Insert p90 latency %v is at least one full read (%v): UI reads are blocking ingest", p90, readTime)
	}
}

func timeLoad(t *testing.T, db *store.Store) time.Duration {
	t.Helper()
	start := time.Now()
	if _, err := db.Load(); err != nil {
		t.Fatal(err)
	}
	return time.Since(start)
}

// uiReadRecords is n API-request logs spread over a few sessions, starting
// at index from.
func uiReadRecords(from, n int) []record.Record {
	pad := strings.Repeat("x", 256)
	out := make([]record.Record, 0, n)
	for i := from; i < from+n; i++ {
		out = append(out, record.Record{
			Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str(fmt.Sprintf("s-%d", i%3)),
			Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(int64(i+1) * 1_000_000),
			Attributes: map[string]any{"model": "vendor/a", "input_tokens": 3, "output_tokens": 1, "note": pad},
			Resource:   map[string]any{"service.name": "claude-code"},
		})
	}
	return out
}

// TestUIReadsInMemoryStore: an in-memory store has no file a second
// connection could open, so the UI keeps reading it through the store
// itself and still reports what was inserted.
func TestUIReadsInMemoryStore(t *testing.T) {
	for _, path := range []string{":memory:", ""} {
		db, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if !db.InMemory() {
			t.Fatalf("Open(%q).InMemory() = false", path)
		}
		if _, err := db.Insert(uiReadRecords(0, 3)); err != nil {
			t.Fatal(err)
		}
		srv := startUI(t, db)
		if got, want := apiSummary(t, srv), summaryJSON(t, db); got != want {
			t.Fatalf("Open(%q): /api/summary\n%s\nwant\n%s", path, got, want)
		}
		if snap := decodeSnapshot(t, apiSnapshot(t, srv)); len(snap.Sessions) != 3 {
			t.Fatalf("Open(%q): /api/snapshot sessions %d, want 3", path, len(snap.Sessions))
		}
		_ = db.Close()
	}
}

// TestUIReadsSeeNewInserts: the read-only connection is opened per report,
// so a record inserted after the UI started is in the next response.
func TestUIReadsSeeNewInserts(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	if snap := decodeSnapshot(t, apiSnapshot(t, srv)); len(snap.Sessions) != 0 {
		t.Fatalf("sessions %d before any insert", len(snap.Sessions))
	}
	if _, err := db.Insert(uiReadRecords(0, 2)); err != nil {
		t.Fatal(err)
	}
	if got, want := apiSummary(t, srv), summaryJSON(t, db); got != want {
		t.Fatalf("/api/summary\n%s\nwant\n%s", got, want)
	}
	if snap := decodeSnapshot(t, apiSnapshot(t, srv)); len(snap.Sessions) != 2 {
		t.Fatalf("sessions %d after insert, want 2", len(snap.Sessions))
	}
}
