package ui

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/paulpham157/sdf/internal/store"
)

// SetBlockHandler runs fn at the start of each request, after the
// connection is no longer one Shutdown closes. Tests use it to prove a
// handler that has started still finishes.
func (s *Server) SetBlockHandler(fn func(context.Context)) {
	s.phaseMu.Lock()
	s.blockHandler = fn
	s.phaseMu.Unlock()
}

// WriteJSONForTest runs the handler's JSON writer with log as its log.
func WriteJSONForTest(log io.Writer, w http.ResponseWriter, r *http.Request, encode func(io.Writer) error) {
	(&handler{log: log}).writeJSON(w, r, encode)
}

// SharedReportForTest starts one report build, holds it until callers-1
// more requests have joined it, then lets it finish. It returns how many
// builds ran and how many sessions each caller saw.
func SharedReportForTest(st *store.Store, callers int) (builds int, sessions []int, err error) {
	var mu sync.Mutex
	started := make(chan struct{})
	joined := make(chan struct{}, callers)
	release := make(chan struct{})
	h := &handler{store: st, prices: "bundled", log: io.Discard}
	h.hooks = &reportHooks{
		beforeBuild: func() {
			mu.Lock()
			builds++
			first := builds == 1
			mu.Unlock()
			if first {
				close(started)
				<-release
			}
		},
		joined: func() { joined <- struct{}{} },
	}
	type result struct {
		n   int
		err error
	}
	results := make(chan result, callers)
	call := func() {
		r, _, err := h.report()
		results <- result{len(r.Sessions), err}
	}
	go call()
	<-started
	for i := 1; i < callers; i++ {
		go call()
	}
	for i := 1; i < callers; i++ {
		<-joined
	}
	close(release)
	for i := 0; i < callers; i++ {
		r := <-results
		if r.err != nil {
			err = r.err
		}
		sessions = append(sessions, r.n)
	}
	mu.Lock()
	defer mu.Unlock()
	return builds, sessions, err
}
