// Package ui is the local read-only session browser.
//
// It listens on its own address, 127.0.0.1:4320 by default, and does not
// follow the OTLP bind address. sdf serve --host 0.0.0.0 therefore exposes
// the receivers without exposing this page. The HTML, CSS, script, and favicons are
// embedded in the binary. Nothing on the page is loaded from the network.
//
// Every route checks Host before the handler runs. localhost, 127.0.0.1,
// and [::1] are allowed, plus the host --ui-addr bound. The port may be
// omitted; a port that is present has to be the bound port. Any other host
// is 403, including static files, so a name that rebinds onto the loopback
// address cannot read the summary or trigger a GitHub lookup.
//
// GET /api/summary is the sdf summary JSON document: summary.SummarizeWith
// with the default span view, then outcome.Attach (outcome.Summarize). GET
// /api/snapshot is that same report arranged for the page. GET
// /api/decisions reads the append-only decision records; it is empty when
// none have been recorded, and it is not part of the summary. GET
// /api/decisions/graph arranges those records into chapters for the
// Decisions tab (see package decisiongraph), over the same report without
// the budget, as sdf decision graph builds it. Requests that arrive while a
// report is being built share it, so one page load makes one round of
// GitHub calls, not one per route. Computed cost the summary marks unknown
// is returned without a USD value. A budget file, when one is configured,
// adds a comparison of computed session cost with one local ceiling. The
// comparison is reported only. There is no route that writes the store.
package ui

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decisiongraph"
	"github.com/paulpham157/sdf/internal/httphost"
	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
)

// DefaultAddr is the UI listen address. It is loopback, and it is not the
// OTLP port.
const DefaultAddr = "127.0.0.1:4320"

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'none'; font-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Timeouts bound a slow or idle client. WriteTimeout includes handler time,
// and a --github refresh calls the GitHub API inside the handler, so the
// write budget is long enough for a few of those calls.
const (
	uiReadHeaderTimeout = 5 * time.Second
	uiReadTimeout       = 30 * time.Second
	uiWriteTimeout      = 3 * time.Minute
	uiIdleTimeout       = 90 * time.Second
)

//go:embed assets/index.html assets/app.css assets/app.js assets/favicon.svg assets/favicon.ico
var assetFS embed.FS

// Config is the read-only view over one store.
// Prices, Repo, and GitHub match the sdf summary flags of the same names.
// GitHub false does not query pull requests; accepted stays unknown.
// Budget is a path to one local ceiling file. Empty means no budget, and
// the report stays as it is without one.
type Config struct {
	Addr   string
	Store  *store.Store
	Prices string
	Repo   string
	GitHub bool
	Budget string
	// Log receives a line when a response cannot be encoded. Nil is
	// os.Stderr.
	Log io.Writer
}

// Server is the UI listener.
type Server struct {
	ln      net.Listener
	http    *http.Server
	warning string
}

// Start listens and serves until Shutdown. A non-loopback address returns a
// server and a warning; the caller prints the warning. The bound URL may
// differ from Addr when the port is 0.
func Start(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	addr, host, err := normalizeAddr(cfg.Addr)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	lnHost, lnPort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	logw := cfg.Log
	if logw == nil {
		logw = os.Stderr
	}
	h := &handler{
		log:    logw,
		store:  cfg.Store,
		prices: cfg.Prices,
		repo:   cfg.Repo,
		github: cfg.GitHub,
		budget: cfg.Budget,
		port:   lnPort,
		hosts:  []string{host, lnHost},
		mux:    http.NewServeMux(),
	}
	h.routes()
	httpSrv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: uiReadHeaderTimeout,
		ReadTimeout:       uiReadTimeout,
		WriteTimeout:      uiWriteTimeout,
		IdleTimeout:       uiIdleTimeout,
	}
	go func() { _ = httpSrv.Serve(ln) }()
	return &Server{ln: ln, http: httpSrv, warning: bindWarning(host)}, nil
}

// URL is the browser address, including the trailing slash.
func (s *Server) URL() string {
	if s == nil || s.ln == nil {
		return ""
	}
	return "http://" + s.ln.Addr().String() + "/"
}

// BindWarning is set when the UI is not bound to a loopback address.
// Telemetry shown on the page can include prompts and paths.
func (s *Server) BindWarning() string {
	if s == nil {
		return ""
	}
	return s.warning
}

// Shutdown stops the listener. A nil server is a no-op.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func normalizeAddr(addr string) (string, string, error) {
	if strings.TrimSpace(addr) == "" {
		addr = DefaultAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", fmt.Errorf("ui address: %w", err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), host, nil
}

func bindWarning(host string) string {
	if loopbackHost(host) {
		return ""
	}
	return "the UI has no authentication; binding beyond localhost exposes telemetry that can include prompts and paths"
}

func loopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type handler struct {
	log    io.Writer
	store  *store.Store
	prices string
	repo   string
	github bool
	budget string
	port   string
	hosts  []string
	mux    *http.ServeMux

	flightMu sync.Mutex
	flight   *reportFlight // the report being built now, if any
	hooks    *reportHooks  // set by tests only; nil in production
}

// reportHooks lets a test see report sharing: beforeBuild runs at the start
// of a build, joined when a request waits for a build already running.
type reportHooks struct {
	beforeBuild func()
	joined      func()
}

// reportFlight is one report build that concurrent requests share.
type reportFlight struct {
	done   chan struct{}
	report summary.Report
	cat    *pricing.Catalog
	err    error
}

func (h *handler) routes() {
	h.mux.HandleFunc("GET /{$}", h.page)
	h.mux.HandleFunc("GET /app.css", h.asset("assets/app.css", "text/css; charset=utf-8"))
	h.mux.HandleFunc("GET /app.js", h.asset("assets/app.js", "text/javascript; charset=utf-8"))
	h.mux.HandleFunc("GET /favicon.ico", h.asset("assets/favicon.ico", "image/x-icon"))
	h.mux.HandleFunc("GET /favicon.svg", h.asset("assets/favicon.svg", "image/svg+xml; charset=utf-8"))
	h.mux.HandleFunc("GET /api/summary", h.summary)
	h.mux.HandleFunc("GET /api/snapshot", h.snapshot)
	h.mux.HandleFunc("GET /api/decisions", h.decisions)
	h.mux.HandleFunc("GET /api/decisions/graph", h.decisionGraph)
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	if !httphost.Allowed(r.Host, h.port, h.hosts...) {
		writeText(w, http.StatusForbidden, "forbidden")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) page(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, "assets/index.html", "text/html; charset=utf-8")
}

func (h *handler) asset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.serveFile(w, r, name, contentType)
	}
}

func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, name, contentType string) {
	body, err := assetFS.ReadFile(name)
	if err != nil {
		writeText(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", contentType)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(body)
}

func (h *handler) summary(w http.ResponseWriter, r *http.Request) {
	report, _, err := h.load()
	if err != nil {
		writeText(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	h.writeJSON(w, r, func(buf io.Writer) error { return summary.WriteJSON(buf, report) })
}

func (h *handler) snapshot(w http.ResponseWriter, r *http.Request) {
	report, cat, err := h.load()
	if err != nil {
		writeText(w, http.StatusInternalServerError, err.Error())
		return
	}
	table := ""
	if cat != nil {
		table = cat.Table
	}
	snap := BuildSnapshot(report, SnapshotMeta{GitHub: h.github, PriceTable: table})
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	h.writeJSON(w, r, func(buf io.Writer) error { return summary.EncodeJSON(buf, snap) })
}

func (h *handler) decisions(w http.ResponseWriter, r *http.Request) {
	graph, err := decision.Load(h.store)
	if err != nil {
		writeText(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	h.writeJSON(w, r, func(buf io.Writer) error { return decision.WriteJSON(buf, graph) })
}

// decisionGraph is the Decisions tab document: decisiongraph.Build over the
// decision records and the same summarized sessions GET /api/summary reads.
// sdf decision graph --json prints the same bytes for the same flags.
func (h *handler) decisionGraph(w http.ResponseWriter, r *http.Request) {
	// No budget: the graph does not show one, and a bad budget file must
	// not break this route. The same report sdf decision graph builds.
	report, _, err := h.report()
	if err != nil {
		writeText(w, http.StatusInternalServerError, err.Error())
		return
	}
	view, err := decisiongraph.FromStore(h.store, report)
	if err != nil {
		writeText(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	h.writeJSON(w, r, func(buf io.Writer) error { return decisiongraph.WriteJSON(buf, view) })
}

// load is the report with the budget applied, for /api/summary and
// /api/snapshot.
func (h *handler) load() (summary.Report, *pricing.Catalog, error) {
	report, cat, err := h.report()
	if err != nil {
		return summary.Report{}, nil, err
	}
	budget, err := summary.LoadBudget(h.budget)
	if err != nil {
		return summary.Report{}, nil, err
	}
	// report is shared with concurrent requests; ApplyBudget only sets
	// report.Budget, on this copy.
	summary.ApplyBudget(&report, budget)
	return report, cat, nil
}

// report is outcome.Summarize over the store, without the budget. A
// request that arrives while another is building it waits for that build
// and shares the result, so the page's parallel requests for the summary,
// the snapshot, and the graph make one round of git and GitHub lookups.
// Nothing is kept once the build finishes: the next request reads the
// store again. Callers must treat the shared report as read-only, apart
// from top-level fields of their own copy.
func (h *handler) report() (summary.Report, *pricing.Catalog, error) {
	h.flightMu.Lock()
	if f := h.flight; f != nil {
		h.flightMu.Unlock()
		if h.hooks != nil {
			h.hooks.joined()
		}
		<-f.done
		return f.report, f.cat, f.err
	}
	f := &reportFlight{done: make(chan struct{})}
	h.flight = f
	h.flightMu.Unlock()
	defer func() {
		h.flightMu.Lock()
		h.flight = nil
		h.flightMu.Unlock()
		close(f.done)
	}()
	f.report, f.cat, f.err = h.buildReport()
	return f.report, f.cat, f.err
}

func (h *handler) buildReport() (summary.Report, *pricing.Catalog, error) {
	if h.hooks != nil {
		h.hooks.beforeBuild()
	}
	cat, err := pricing.ResolveCatalog(h.prices)
	if err != nil {
		return summary.Report{}, nil, err
	}
	records, err := h.readRecords()
	if err != nil {
		return summary.Report{}, nil, err
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
	return outcome.Summarize(records, cat, opt), cat, nil
}

// readRecords reads every record for a report through a separate
// read-only connection to the same file (store.OpenReadOnly), not the
// receiver's writer connection, so a long read does not hold the writer's
// mutex and block OTLP Insert (issue #272). In WAL mode the read sees the
// last committed state and does not wait for the writer. An in-memory
// store has no file another connection could open, so it is read through
// the writer as before.
func (h *handler) readRecords() ([]record.Record, error) {
	if h.store.InMemory() {
		return h.store.Load()
	}
	ro, err := store.OpenReadOnly(h.store.Path())
	if err != nil {
		return nil, err
	}
	defer ro.Close()
	return ro.Load()
}

// writeJSON runs encode into memory first, then sends the result with
// Content-Length. An encoding error is logged and answered with 500 and a
// short JSON error, never 200 with an empty or partial body (issue #267).
func (h *handler) writeJSON(w http.ResponseWriter, r *http.Request, encode func(io.Writer) error) {
	var buf bytes.Buffer
	if err := encode(&buf); err != nil {
		fmt.Fprintf(h.log, "sdf ui: %s %s: encode response: %v\n", r.Method, r.URL.Path, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		body, _ := json.Marshal(map[string]string{"error": "could not encode the response: " + err.Error()})
		_, _ = w.Write(append(body, '\n'))
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	_, _ = w.Write(buf.Bytes())
}

func writeText(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg)
}
