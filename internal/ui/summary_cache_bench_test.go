package ui

import (
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

// BenchmarkSummaryRepeat compares a repeat summary against recomputing
// from every record. The cached path is the UI report used by
// /api/summary. The full path is Load plus outcome.Summarize, which is
// what that route did on every request.
func BenchmarkSummaryRepeat(b *testing.B) {
	db, err := store.Open(filepath.Join(b.TempDir(), "t.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	const n = 8000
	batch := make([]record.Record, 0, 500)
	for i := 0; i < n; i++ {
		batch = append(batch, benchRecord(i))
		if len(batch) == cap(batch) {
			if _, err := db.Insert(batch); err != nil {
				b.Fatal(err)
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if _, err := db.Insert(batch); err != nil {
			b.Fatal(err)
		}
	}
	h := &handler{store: db, prices: "bundled", log: io.Discard}
	cat := pricing.Bundled()

	b.Run("cached", func(b *testing.B) {
		if _, _, err := h.report(); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			report, _, err := h.report()
			if err != nil {
				b.Fatal(err)
			}
			benchSink += len(report.Sessions)
		}
	})
	b.Run("full", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			recs, err := db.Load()
			if err != nil {
				b.Fatal(err)
			}
			report := outcome.Summarize(recs, cat, outcome.Options{})
			benchSink += len(report.Sessions)
		}
	})
}

var benchSink int

func benchRecord(i int) record.Record {
	session := fmt.Sprintf("s%d", i%3)
	when := int64(i+1) * 1_000_000
	if i%5 == 0 {
		return record.Record{
			Signal:            "metrics",
			ServiceName:       record.Str("claude-code"),
			SessionID:         record.Str(session),
			Name:              record.Str("claude_code.token.usage"),
			TimeUnixNano:      record.Int64(when),
			Value:             record.Float(float64(i % 500)),
			MetricTemporality: record.Str("cumulative"),
			MetricKind:        record.Str("sum"),
			Attributes:        map[string]any{"type": "input", "session.id": session},
			Resource:          map[string]any{"service.name": "claude-code"},
		}
	}
	return record.Record{
		Signal:       "logs",
		ServiceName:  record.Str("claude-code"),
		SessionID:    record.Str(session),
		Name:         record.Str("claude_code.api_request"),
		TimeUnixNano: record.Int64(when),
		Attributes: map[string]any{
			"session.id": session, "model": "vendor/a",
			"input_tokens": 3, "output_tokens": 1,
			"request_id": fmt.Sprintf("r%d", i),
		},
		Resource: map[string]any{"service.name": "claude-code"},
	}
}
