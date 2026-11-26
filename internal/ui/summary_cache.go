package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"slices"
	"sync"

	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
)

// summaryCache keeps one summary for one records snapshot and one price
// catalog. The next snapshot, or a different catalog, replaces it. The
// stored report is the result of summary.SummarizeWith, before outcome
// linking and before a budget is applied. One slot, replaced rather than
// accumulated, so the cache does not grow with the number of requests.
type summaryCache struct {
	mu      sync.Mutex
	valid   bool
	snap    store.RecordSnapshot
	priceID string
	report  summary.Report
	cat     *pricing.Catalog
}

func (c *summaryCache) lookup(snap store.RecordSnapshot, priceID string) (summary.Report, *pricing.Catalog, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid || c.snap != snap || c.priceID != priceID {
		return summary.Report{}, nil, false
	}
	return c.report, c.cat, true
}

func (c *summaryCache) store(snap store.RecordSnapshot, priceID string, report summary.Report, cat *pricing.Catalog) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// A slower build of an older snapshot must not replace a newer one.
	if c.valid && c.priceID == priceID && newerSnapshot(c.snap, snap) {
		return
	}
	c.valid = true
	c.snap = snap
	c.priceID = priceID
	c.report = report
	c.cat = cat
}

func newerSnapshot(a, b store.RecordSnapshot) bool {
	if a.Generation != b.Generation {
		return a.Generation > b.Generation
	}
	return a.MaxID > b.MaxID
}

// snapshotCovers reports whether got is the floor snapshot or a later one.
// A request that observed floor must not be answered with an older build.
func snapshotCovers(got, floor store.RecordSnapshot) bool {
	if got.Generation != floor.Generation {
		return got.Generation > floor.Generation
	}
	return got.MaxID >= floor.MaxID
}

// summarizeCached returns the summary for floor when that snapshot is
// already cached. Otherwise it reads the records and summarizes them.
// The returned snapshot is the one the report was computed from. haveSnap
// is false when the file has no generation row, so the caller does not
// treat the report as named by a snapshot.
func (h *handler) summarizeCached(floor store.RecordSnapshot, floorOK bool) (summary.Report, *pricing.Catalog, store.RecordSnapshot, bool, error) {
	priceID, err := h.priceID()
	if err != nil {
		return summary.Report{}, nil, store.RecordSnapshot{}, false, err
	}
	if floorOK {
		if base, cat, ok := h.cache.lookup(floor, priceID); ok {
			report, cat := h.finish(base, cat)
			return report, cat, floor, true, nil
		}
	}
	if h.hooks != nil && h.hooks.beforeBuild != nil {
		h.hooks.beforeBuild()
	}
	if h.hooks != nil && h.hooks.loadedRecords != nil {
		h.hooks.loadedRecords()
	}
	records, snap, cacheable, err := h.readRecords()
	if err != nil {
		return summary.Report{}, nil, store.RecordSnapshot{}, false, err
	}
	if h.hooks != nil && h.hooks.afterLoad != nil {
		h.hooks.afterLoad()
	}
	cat, err := pricing.ResolveCatalog(h.prices)
	if err != nil {
		return summary.Report{}, nil, store.RecordSnapshot{}, false, err
	}
	base := summary.SummarizeWith(records, false, cat)
	// Cache only when the catalog identity still names the file we just
	// resolved. A rewrite between the two reads is not cached; the next
	// request computes it again.
	if cacheable {
		if idNow, idErr := h.priceID(); idErr == nil && idNow == priceID {
			h.cache.store(snap, priceID, base, cat)
		}
	}
	report, cat := h.finish(base, cat)
	return report, cat, snap, cacheable, nil
}

// finish copies the session list and attaches outcomes. The cached report
// stays without outcomes, so a later request links git and GitHub again.
func (h *handler) finish(base summary.Report, cat *pricing.Catalog) (summary.Report, *pricing.Catalog) {
	report := base
	report.Sessions = slices.Clone(base.Sessions)
	report.Unattributed = slices.Clone(base.Unattributed)
	report.Budget = nil
	report.CostPerAccepted = nil
	for i := range report.Sessions {
		report.Sessions[i].Outcome = nil
	}
	opt := outcome.Options{Repo: h.repo}
	if h.github {
		lookup, gerr := outcome.NewGitHub()
		if gerr != nil {
			opt.GitHubNote = gerr.Error()
		} else {
			opt.Lookup = lookup
		}
	}
	outcome.Attach(&report, opt)
	return report, cat
}

// readRecords reads every record and the snapshot of that read. A file
// store is read through the long-lived read-only connection. An in-memory
// store has no file another connection can open, so it is read through
// the writer, as before.
func (h *handler) readRecords() ([]record.Record, store.RecordSnapshot, bool, error) {
	st, err := h.reader()
	if err != nil {
		return nil, store.RecordSnapshot{}, false, err
	}
	return st.LoadSnapshot()
}

// probeSnapshot is the cheap generation and max-id read used as the cache
// key. It does not load the records table.
func (h *handler) probeSnapshot() (store.RecordSnapshot, bool, error) {
	st, err := h.reader()
	if err != nil {
		return store.RecordSnapshot{}, false, err
	}
	return st.RecordSnapshot()
}

// reader is the store a summary reads. The file connection is opened once
// and reused so a cache hit does not pay for a new SQLite open. It is not
// the writer's connection, so the read does not hold the writer's mutex.
func (h *handler) reader() (*store.Store, error) {
	if h.store.InMemory() {
		return h.store, nil
	}
	h.roMu.Lock()
	defer h.roMu.Unlock()
	if h.ro != nil {
		return h.ro, nil
	}
	ro, err := store.OpenReadOnly(h.store.Path())
	if err != nil {
		return nil, err
	}
	h.ro = ro
	return ro, nil
}

func (h *handler) closeReadOnly() {
	h.roMu.Lock()
	defer h.roMu.Unlock()
	if h.ro != nil {
		_ = h.ro.Close()
		h.ro = nil
	}
}

// priceID names the catalog ResolveCatalog would return for h.prices.
// The bundled table and a missing price-cache file share one id. A file
// catalog's id is a hash of its bytes, so a rewrite is a cache miss even
// when the records snapshot is unchanged.
func (h *handler) priceID() (string, error) {
	id, err := priceIdentity(h.prices)
	if err == nil {
		return id, nil
	}
	if _, rerr := pricing.ResolveCatalog(h.prices); rerr != nil {
		return "", rerr
	}
	return "", err
}

func priceIdentity(spec string) (string, error) {
	switch spec {
	case "bundled":
		return "bundled", nil
	case "":
		path, err := pricing.CachePath()
		if err != nil {
			return "bundled-warning:" + err.Error(), nil
		}
		sum, err := hashFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return "bundled", nil
			}
			return "", err
		}
		return "cache:" + path + ":" + sum, nil
	default:
		sum, err := hashFile(spec)
		if err != nil {
			return "", err
		}
		return "file:" + spec + ":" + sum, nil
	}
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
