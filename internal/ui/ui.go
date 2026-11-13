// Package ui is the local read-only session browser.
//
// It listens on its own address, 127.0.0.1:4320 by default, and does not
// follow the OTLP bind address. sdf serve --host 0.0.0.0 therefore exposes
// the receivers without exposing this page. The HTML, CSS, and script are
// embedded in the binary. Nothing on the page is loaded from the network.
//
// GET /api/summary is the sdf summary JSON document: summary.SummarizeWith
// with the default span view, then outcome.Attach. GET /api/snapshot is that
// same report arranged for the page. GET /api/decisions reads the append-only
// decision records; it is empty when none have been recorded, and it is not
// part of the summary. Computed cost the summary marks unknown is returned
// without a USD value. A budget file, when one is configured, adds a
// comparison of computed session cost with one local ceiling. The comparison
// is reported only. There is no route that writes the store.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
)

// DefaultAddr is the UI listen address. It is loopback, and it is not the
// OTLP port.
const DefaultAddr = "127.0.0.1:4320"

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'none'; font-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

//go:embed assets/index.html assets/app.css assets/app.js
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
	h := &handler{
		store:  cfg.Store,
		prices: cfg.Prices,
		repo:   cfg.Repo,
		github: cfg.GitHub,
		budget: cfg.Budget,
		mux:    http.NewServeMux(),
	}
	h.routes()
	httpSrv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
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
	store  *store.Store
	prices string
	repo   string
	github bool
	budget string
	mux    *http.ServeMux
}

func (h *handler) routes() {
	h.mux.HandleFunc("GET /{$}", h.page)
	h.mux.HandleFunc("GET /app.css", h.asset("assets/app.css", "text/css; charset=utf-8"))
	h.mux.HandleFunc("GET /app.js", h.asset("assets/app.js", "text/javascript; charset=utf-8"))
	h.mux.HandleFunc("GET /api/summary", h.summary)
	h.mux.HandleFunc("GET /api/snapshot", h.snapshot)
	h.mux.HandleFunc("GET /api/decisions", h.decisions)
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
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
	if err := summary.WriteJSON(w, report); err != nil {
		return
	}
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
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(snap)
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
	if err := decision.WriteJSON(w, graph); err != nil {
		return
	}
}

func (h *handler) load() (summary.Report, *pricing.Catalog, error) {
	cat, err := pricing.ResolveCatalog(h.prices)
	if err != nil {
		return summary.Report{}, nil, err
	}
	records, err := h.store.Load()
	if err != nil {
		return summary.Report{}, nil, err
	}
	report := summary.SummarizeWith(records, false, cat)
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
	budget, err := summary.LoadBudget(h.budget)
	if err != nil {
		return summary.Report{}, nil, err
	}
	summary.ApplyBudget(&report, budget)
	return report, cat, nil
}

func writeText(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg)
}
