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

// A caller that writes into the slices and maps of a returned report must
// not change the report the cache serves next.
func TestSummaryCacheReportIsNotShared(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Insert([]record.Record{cacheLog("s", 1), cacheSpan("s", 2)}); err != nil {
		t.Fatal(err)
	}
	h := &handler{store: db, prices: "bundled", log: io.Discard}
	first, _, err := h.report()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != 1 {
		t.Fatalf("sessions %d, want 1", len(first.Sessions))
	}
	session := first.Sessions[0]
	if len(session.Tokens.Vals) == 0 || len(session.Spans) == 0 || len(session.ServiceNames) == 0 || len(session.LogEvents) == 0 {
		t.Fatalf("fixture left a slice empty: tokens %d spans %d services %d logs %d", len(session.Tokens.Vals), len(session.Spans), len(session.ServiceNames), len(session.LogEvents))
	}
	before := jsonReport(t, first)
	mutateCachedReport(&first)
	second, _, err := h.report()
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonReport(t, second); got != before {
		t.Fatalf("mutating a returned report changed the cached summary\nbefore %s\nafter  %s", before, got)
	}
}

// mutateCachedReport writes every slice and map a caller can reach from a
// report: element overwrites and appends. A shared backing array or map
// shows up in the next cached report.
func mutateCachedReport(r *summary.Report) {
	r.Sessions = append(r.Sessions, summary.SessionSummary{SessionID: "mutated-session"})
	for i := range r.Sessions {
		s := &r.Sessions[i]
		if s.Tokens.Vals == nil {
			s.Tokens.Vals = map[string]float64{}
		}
		for k := range s.Tokens.Vals {
			s.Tokens.Vals[k] = -1
		}
		s.Tokens.Vals["mutated"] = -1
		s.Tokens.Keys = append(s.Tokens.Keys, "mutated")
		s.ServiceNames = append(s.ServiceNames, "mutated-service")
		if len(s.ServiceNames) > 0 {
			s.ServiceNames[0] = "mutated-service"
		}
		s.Models = append(s.Models, "mutated-model")
		if len(s.Models) > 0 {
			s.Models[0] = "mutated-model"
		}
		s.Spans = append(s.Spans, summary.SpanNode{Name: "mutated-span"})
		for j := range s.Spans {
			s.Spans[j].Name = "mutated-span"
			s.Spans[j].Children = append(s.Spans[j].Children, summary.SpanNode{Name: "mutated-child"})
			if len(s.Spans[j].Children) > 0 {
				s.Spans[j].Children[0].Name = "mutated-child"
			}
		}
		s.LogEvents = append(s.LogEvents, summary.LogLine{Name: "mutated-log"})
		if len(s.LogEvents) > 0 {
			s.LogEvents[0].Name = "mutated-log"
		}
		s.MetricLines = append(s.MetricLines, summary.MetricLine{Name: "mutated-metric"})
		if len(s.MetricLines) > 0 {
			s.MetricLines[0].Name = "mutated-metric"
		}
		s.Missing = append(s.Missing, summary.Missing{Signal: "mutated"})
		if len(s.Missing) > 0 {
			s.Missing[0].Signal = "mutated"
		}
		s.ToolCalls = append(s.ToolCalls, summary.ToolCall{Source: "mutated"})
		if len(s.ToolCalls) > 0 {
			s.ToolCalls[0].Source = "mutated"
		}
		s.Repository.WorkDirs = append(s.Repository.WorkDirs, "mutated-dir")
		s.Repository.CommitSHAs = append(s.Repository.CommitSHAs, "mutated-sha")
		s.Repository.HookCommits = append(s.Repository.HookCommits, summary.HookCommit{SHA: "mutated"})
		if s.ComputedCost != nil {
			s.ComputedCost.USD = "mutated"
			s.ComputedCost.Components = append(s.ComputedCost.Components, pricing.Component{Name: "mutated"})
			if len(s.ComputedCost.Components) > 0 {
				s.ComputedCost.Components[0].Name = "mutated"
			}
		}
		if s.Outcome != nil {
			s.Outcome.Commits = append(s.Outcome.Commits, summary.LinkedCommit{SHA: "mutated"})
			if len(s.Outcome.Commits) > 0 {
				s.Outcome.Commits[0].SHA = "mutated"
			}
		}
	}
	r.Unattributed = append(r.Unattributed, summary.Unattributed{Tool: "mutated", Names: []string{"mutated"}})
	for i := range r.Unattributed {
		r.Unattributed[i].Names = append(r.Unattributed[i].Names, "mutated-name")
		if len(r.Unattributed[i].Names) > 0 {
			r.Unattributed[i].Names[0] = "mutated-name"
		}
	}
}

func cacheSpan(session string, when int64) record.Record {
	id := session + "-span"
	return record.Record{
		Signal:        "traces",
		ServiceName:   record.Str("claude-code"),
		SessionID:     record.Str(session),
		Name:          record.Str("claude_code.llm_request"),
		StartUnixNano: record.Int64(when),
		EndUnixNano:   record.Int64(when + 1_000_000),
		TraceID:       record.Str(id),
		SpanID:        record.Str(id),
		Attributes:    map[string]any{"session.id": session},
		Resource:      map[string]any{"service.name": "claude-code"},
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
