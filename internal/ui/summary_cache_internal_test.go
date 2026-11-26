package ui

import (
	"bytes"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
)

func TestSummaryCacheSkipsReloadUntilInsert(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "file", path: filepath.Join(t.TempDir(), "t.sqlite")},
		{name: "memory", path: ":memory:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			loads := 0
			h := &handler{store: db, prices: "bundled", log: io.Discard}
			h.hooks = &reportHooks{loadedRecords: func() { loads++ }}
			if _, _, err := h.report(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := h.report(); err != nil {
				t.Fatal(err)
			}
			if loads != 1 {
				t.Fatalf("loads after two reports with no rows: %d, want 1", loads)
			}
			if _, err := db.Insert([]record.Record{cacheLog("s", 1)}); err != nil {
				t.Fatal(err)
			}
			got, _, err := h.report()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := h.report(); err != nil {
				t.Fatal(err)
			}
			if loads != 2 {
				t.Fatalf("loads after one insert and two reports: %d, want 2", loads)
			}
			if len(got.Sessions) != 1 || got.Sessions[0].SessionID != "s" {
				t.Fatalf("sessions %+v", got.Sessions)
			}
		})
	}
}

// An in-flight summary that already read the table must not be served to a
// request that observed a later insert, and the slower build must not
// replace the newer cache entry.
func TestSummaryCacheDoesNotServeAStaleFlight(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Insert([]record.Record{cacheLog("batch1", 1)}); err != nil {
		t.Fatal(err)
	}
	h := &handler{store: db, prices: "bundled", log: io.Discard}
	if _, _, err := h.report(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Insert([]record.Record{cacheLog("batch2", 2)}); err != nil {
		t.Fatal(err)
	}

	loaded := make(chan struct{})
	release := make(chan struct{})
	var loadOnce sync.Once
	joined := make(chan struct{})
	var joinOnce sync.Once
	h.hooks = &reportHooks{
		afterLoad: func() {
			loadOnce.Do(func() { close(loaded) })
			<-release
		},
		joined: func() { joinOnce.Do(func() { close(joined) }) },
	}

	type result struct {
		report summary.Report
		err    error
	}
	firstDone := make(chan result, 1)
	go func() {
		report, _, err := h.report()
		firstDone <- result{report, err}
	}()
	select {
	case <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the in-flight load")
	}
	if _, err := db.Insert([]record.Record{cacheLog("batch3", 3)}); err != nil {
		t.Fatal(err)
	}
	secondDone := make(chan result, 1)
	go func() {
		report, _, err := h.report()
		secondDone <- result{report, err}
	}()
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the second request to join the flight")
	}
	close(release)

	var first, second result
	select {
	case first = <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first report")
	}
	select {
	case second = <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the second report")
	}
	if first.err != nil || second.err != nil {
		t.Fatalf("first %v second %v", first.err, second.err)
	}
	if ids := sessionIDSet(first.report); ids["batch3"] {
		t.Fatal("the in-flight load included batch3; the test did not pause before that insert")
	}
	if ids := sessionIDSet(second.report); !ids["batch3"] || !ids["batch1"] || !ids["batch2"] {
		t.Fatalf("request after the insert saw %v", ids)
	}
	recs, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := outcome.Summarize(recs, pricing.Bundled(), outcome.Options{})
	if jsonReport(t, second.report) != jsonReport(t, want) {
		t.Fatal("the request that observed the insert did not match a full recompute")
	}
}

func cacheLog(session string, when int64) record.Record {
	return record.Record{
		Signal:       "logs",
		ServiceName:  record.Str("claude-code"),
		SessionID:    record.Str(session),
		Name:         record.Str("claude_code.api_request"),
		TimeUnixNano: record.Int64(when * 1_000_000),
		Attributes:   map[string]any{"session.id": session, "model": "vendor/a", "input_tokens": 1, "output_tokens": 1},
		Resource:     map[string]any{"service.name": "claude-code"},
	}
}

func sessionIDSet(report summary.Report) map[string]bool {
	out := map[string]bool{}
	for _, session := range report.Sessions {
		out[session.SessionID] = true
	}
	return out
}

func jsonReport(t *testing.T, report summary.Report) string {
	t.Helper()
	var buf bytes.Buffer
	if err := summary.WriteJSON(&buf, report); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
