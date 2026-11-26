package ui_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
	"github.com/paulpham157/sdf/internal/ui"
)

// Randomized inserts, including cumulative series that drop and restart,
// must match a full recompute after every batch. A step with no insert
// checks the cache hit.
func TestSummaryCacheMatchesFullRecompute(t *testing.T) {
	for _, mem := range []bool{false, true} {
		name := "file"
		if mem {
			name = "memory"
		}
		t.Run(name, func(t *testing.T) {
			var db *store.Store
			if mem {
				var err error
				db, err = store.Open(":memory:")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				db = openDB(t)
			}
			if mem {
				t.Cleanup(func() { _ = db.Close() })
			}
			db.KeepContent(func(record.Record) bool { return true })
			srv := startUI(t, db)
			for _, batch := range cumulativeBatches() {
				if _, err := db.Insert(batch); err != nil {
					t.Fatal(err)
				}
				assertSummaryMatches(t, srv, db, "bundled")
			}
			for _, seed := range []int64{273, 1, 99} {
				rng := rand.New(rand.NewSource(seed))
				for step := 0; step < 20; step++ {
					if rng.Intn(5) != 0 {
						if _, err := db.Insert(randomRecords(rng, seed, step)); err != nil {
							t.Fatal(err)
						}
					}
					if step%7 == 6 {
						if _, err := db.Scrub(false); err != nil {
							t.Fatal(err)
						}
					}
					assertSummaryMatches(t, srv, db, "bundled")
				}
			}
			if status, _, body := get(t, srv.URL()+"api/snapshot"); status != http.StatusOK || !json.Valid([]byte(body)) {
				t.Fatalf("snapshot %d %s", status, body)
			}
		})
	}
}

func TestSummaryCacheConcurrentInsertsMatchAfterQuiesce(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 30 * time.Second}
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, err := client.Get(srv.URL() + "api/summary")
				if err != nil {
					errs <- err
					return
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK || !json.Valid(body) {
					errs <- fmt.Errorf("summary %d %s", resp.StatusCode, body)
					return
				}
			}
		}()
	}
	rng := rand.New(rand.NewSource(273))
	for i := 0; i < 30; i++ {
		if _, err := db.Insert(randomRecords(rng, 273, i)); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	assertSummaryMatches(t, srv, db, "bundled")
}

func TestSummaryCacheInvalidatesWhenThePriceCatalogChanges(t *testing.T) {
	db := openDB(t)
	if _, err := db.Insert([]record.Record{pricedLog()}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "prices.json")
	writePrices(t, path, "0.000001")
	srv := startUIWithPrices(t, db, path)
	assertSummaryMatches(t, srv, db, path)
	first := apiSummary(t, srv)
	writePrices(t, path, "0.000009")
	second := summarizeStore(t, db, path)
	if first == second {
		t.Fatal("rewriting the catalog did not change the full summary")
	}
	assertSummaryMatches(t, srv, db, path)
}

func TestSummaryCacheInvalidatesOnScrub(t *testing.T) {
	db := openDB(t)
	db.KeepContent(func(record.Record) bool { return true })
	msg := "hello-secret-status"
	if _, err := db.Insert([]record.Record{{
		Signal:        "traces",
		ServiceName:   record.Str("claude-code"),
		SessionID:     record.Str("s"),
		Name:          record.Str("claude_code.llm_request"),
		StartUnixNano: record.Int64(1),
		EndUnixNano:   record.Int64(2_000_000),
		TraceID:       record.Str("abcd"),
		SpanID:        record.Str("ef01"),
		StatusCode:    "ERROR",
		StatusMessage: &msg,
		Attributes:    map[string]any{"session.id": "s"},
		Resource:      map[string]any{"service.name": "claude-code"},
	}}); err != nil {
		t.Fatal(err)
	}
	srv := startUI(t, db)
	assertSummaryMatches(t, srv, db, "bundled")
	before := apiSummary(t, srv)
	if !bytes.Contains([]byte(before), []byte("hello-secret-status")) {
		t.Fatalf("summary did not contain the stored status message:\n%s", before)
	}
	if _, err := db.Scrub(false); err != nil {
		t.Fatal(err)
	}
	after := apiSummary(t, srv)
	if bytes.Contains([]byte(after), []byte("hello-secret-status")) {
		t.Fatalf("summary kept the scrubbed status message:\n%s", after)
	}
	assertSummaryMatches(t, srv, db, "bundled")
}

func assertSummaryMatches(t *testing.T, srv *ui.Server, db *store.Store, prices string) {
	t.Helper()
	got := apiSummary(t, srv)
	want := summarizeStore(t, db, prices)
	if got != want {
		t.Fatalf("summary\n%s\nwant\n%s", got, want)
	}
}

func summarizeStore(t *testing.T, db *store.Store, prices string) string {
	t.Helper()
	cat, err := pricing.ResolveCatalog(prices)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	report := summary.SummarizeWith(recs, false, cat)
	outcome.Attach(&report, outcome.Options{})
	var buf bytes.Buffer
	if err := summary.WriteJSON(&buf, report); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func startUIWithPrices(t *testing.T, db *store.Store, prices string) *ui.Server {
	t.Helper()
	srv, err := ui.Start(ui.Config{Addr: "127.0.0.1:0", Store: db, Prices: prices})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv
}

func writePrices(t *testing.T, path, prompt string) {
	t.Helper()
	body := fmt.Sprintf(`{
  "source": "https://openrouter.ai/api/v1/models",
  "retrieved_at": "2026-01-01T00:00:00Z",
  "models": [
    {"id": "vendor/priced", "pricing": {"prompt": %q, "completion": "0.000002"}}
  ]
}`, prompt)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pricedLog() record.Record {
	return record.Record{
		Signal:       "logs",
		ServiceName:  record.Str("claude-code"),
		SessionID:    record.Str("priced"),
		Name:         record.Str("claude_code.api_request"),
		TimeUnixNano: record.Int64(1_000_000),
		Attributes:   map[string]any{"session.id": "priced", "model": "vendor/priced", "input_tokens": 1000, "output_tokens": 10},
		Resource:     map[string]any{"service.name": "claude-code"},
	}
}

// cumulativeBatches are the reset shapes pinned in
// internal/summary/cumulative_reset_test.go, inserted a few points at a
// time so a stale cache would keep the previous total.
func cumulativeBatches() [][]record.Record {
	session := "cum"
	point := func(value float64, when int64, start int64) record.Record {
		rec := record.Record{
			Signal:            "metrics",
			ServiceName:       record.Str("claude-code"),
			SessionID:         record.Str(session),
			Name:              record.Str("claude_code.token.usage"),
			TimeUnixNano:      record.Int64(when),
			Value:             record.Float(value),
			MetricTemporality: record.Str("cumulative"),
			MetricKind:        record.Str("sum"),
			Attributes:        map[string]any{"type": "input", "session.id": session},
			Resource:          map[string]any{"service.name": "claude-code"},
		}
		if start != 0 {
			rec.StartUnixNano = record.Int64(start)
		}
		return rec
	}
	return [][]record.Record{
		{point(1000, 10, 0)},
		{point(500, 20, 0)},
		{point(600, 30, 1), point(1000, 40, 1)},
		{point(200, 50, 30), point(500, 60, 30)},
		{point(5, 70, 5), point(3, 80, 5), point(4, 90, 5)},
	}
}

func randomRecords(rng *rand.Rand, seed int64, step int) []record.Record {
	n := 1 + rng.Intn(6)
	out := make([]record.Record, 0, n)
	for i := 0; i < n; i++ {
		session := fmt.Sprintf("s%d", rng.Intn(4))
		when := int64(step+1)*1_000_000_000 + int64(i)*10_000_000 + int64(rng.Intn(50))*1_000_000
		switch rng.Intn(4) {
		case 0:
			value := float64(rng.Intn(2000))
			rec := record.Record{
				Signal:            "metrics",
				ServiceName:       record.Str("claude-code"),
				SessionID:         record.Str(session),
				Name:              record.Str("claude_code.token.usage"),
				TimeUnixNano:      record.Int64(when),
				Value:             record.Float(value),
				MetricTemporality: record.Str("cumulative"),
				MetricKind:        record.Str("sum"),
				Attributes:        map[string]any{"type": "input", "session.id": session},
				Resource:          map[string]any{"service.name": "claude-code"},
			}
			if rng.Intn(2) == 0 {
				rec.StartUnixNano = record.Int64(int64(rng.Intn(5) + 1))
			}
			out = append(out, rec)
		case 1:
			out = append(out, record.Record{
				Signal:            "metrics",
				ServiceName:       record.Str("claude-code"),
				SessionID:         record.Str(session),
				Name:              record.Str("claude_code.token.usage"),
				TimeUnixNano:      record.Int64(when),
				Value:             record.Float(float64(1 + rng.Intn(20))),
				MetricTemporality: record.Str("delta"),
				MetricKind:        record.Str("sum"),
				Attributes:        map[string]any{"type": "output", "session.id": session},
				Resource:          map[string]any{"service.name": "claude-code"},
			})
		case 2:
			out = append(out, record.Record{
				Signal:       "logs",
				ServiceName:  record.Str("claude-code"),
				SessionID:    record.Str(session),
				Name:         record.Str("claude_code.api_request"),
				TimeUnixNano: record.Int64(when),
				Attributes: map[string]any{
					"session.id": session, "model": "vendor/a",
					"input_tokens": 1 + rng.Intn(8), "output_tokens": rng.Intn(4),
					"request_id": fmt.Sprintf("%d-%d-%d", seed, step, i),
				},
				Resource: map[string]any{"service.name": "claude-code"},
			})
		default:
			id := fmt.Sprintf("%d-%d-%d", seed, step, i)
			rec := record.Record{
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
			if rng.Intn(2) == 0 {
				msg := fmt.Sprintf("secret-status-%s", id)
				rec.StatusCode = "ERROR"
				rec.StatusMessage = &msg
			}
			out = append(out, rec)
		}
	}
	return out
}
