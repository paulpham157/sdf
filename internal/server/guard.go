package server

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/tap"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/paulpham157/sdf/internal/netlimit"
)

const (
	msgTooLarge      = "payload too large"
	msgInFlight      = "inflight limit"
	msgTooManyToken  = "too many requests"
	msgTooManyUnread = "too many streams waiting for a message"
	msgReadTimeout   = "read timeout"
	msgStopping      = "server stopping"

	// maxConcurrentStreams bounds HTTP/2 streams on one gRPC connection.
	// OTLP exports are unary; a local agent uses a few at a time. The
	// in-flight byte budget is the memory cap. This stops one connection
	// from opening an unbounded number of streams.
	maxConcurrentStreams uint32 = 32
	// maxConnectionAge closes a gRPC connection even if it stays busy, so
	// a client cannot hold one forever. Clients reconnect.
	maxConnectionAge = 30 * time.Minute
	// maxConnectionAgeGrace is how long an aged-out connection may finish
	// the RPC it is already handling.
	maxConnectionAgeGrace = 10 * time.Second
	// keepaliveTime is how long a gRPC connection may be silent before
	// the server pings. Idle connections are closed sooner, by the idle
	// timeout.
	keepaliveTime = 2 * time.Minute
	// keepaliveTimeout is how long the server waits for that ping to be
	// answered.
	keepaliveTimeout = 20 * time.Second
	// minPingInterval rejects a client that pings more often than this.
	// It is below the gRPC default of 5 minutes so an exporter that pings
	// to notice a dead socket is not disconnected, and it still stops a
	// ping flood. Pings with no active RPC are allowed.
	minPingInterval = 10 * time.Second
	// maxHeaderListSize matches the HTTP receiver's header cap.
	maxHeaderListSize uint32 = 1 << 20
)

var errStopping = errors.New("server stopping")

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// beforeInsert runs after a body has been accepted and before the SQLite
// commit. A shutdown that has already started does not begin a new
// commit. The optional test hook can hold the request open; it must not
// be set in production.
func (s *Server) beforeInsert() error {
	// A nil context means the server has not been started. Shutdown is
	// a cancelled context, not a nil one. The direct ingest tests and
	// the benchmark call in without Start.
	if s.ctx != nil && s.ctx.Err() != nil {
		return errStopping
	}
	if s.blockInsert != nil {
		if err := s.blockInsert(s.ctx); err != nil {
			return errStopping
		}
	}
	if s.ctx != nil && s.ctx.Err() != nil {
		return errStopping
	}
	return nil
}

// admitRPC runs before gRPC reads the message body. It rejects a bad
// token, a token cap, or an in-flight byte cap without buffering the
// payload. An admitted RPC reserves the per-request maximum from the
// gRPC share of the budget and keeps it until the message has fully
// arrived. A stream with no DATA frame is cancelled with
// DEADLINE_EXCEEDED after --read-header-timeout, which releases that
// reservation. Once a DATA frame arrives the deadline is --read-timeout.
// The handler stops that timer before the SQLite commit.
//
// The reservation is released when the returned context ends, including
// when the RPC is rejected after the tap (unknown method, oversized
// length prefix, handler error). Oversized messages are still
// RESOURCE_EXHAUSTED from the transport and carry no RetryInfo, so a
// client must not retry them. A cap rejection carries RetryInfo, so it
// is retryable.
func (s *Server) admitRPC(ctx context.Context, _ *tap.Info) (context.Context, error) {
	if err := s.checkGRPCAuth(ctx); err != nil {
		return nil, err
	}
	var releaseToken func()
	if s.Token != "" {
		rel, ok := s.tokens.Acquire()
		if !ok {
			return nil, retryExhausted(msgTooManyToken)
		}
		releaseToken = rel
	}
	w, _ := ctx.Value(watchKey{}).(*frameWatch)
	streamID, sawData, reject := w.claim()
	if reject {
		if releaseToken != nil {
			releaseToken()
		}
		return nil, retryExhausted(msgTooManyUnread)
	}
	hold, ok := s.budget.ReserveGrpc(int64(s.lim.MaxBodyBytes))
	if !ok {
		if releaseToken != nil {
			releaseToken()
		}
		return nil, retryExhausted(msgInFlight)
	}
	headerWait := s.lim.ReadHeaderTimeout
	if headerWait <= 0 {
		headerWait = netlimit.DefaultReadHeaderTimeout
	}
	readWait := s.lim.ReadTimeout
	if readWait <= 0 {
		readWait = netlimit.DefaultReadTimeout
	}
	// The reservation stays until the message arrives or a timer
	// cancels the read. Stopping the timer leaves the context alive, so
	// the commit is not under the deadline. A header-only stream uses
	// the header timeout; the first DATA byte promotes it to the read timeout.
	live := newReadCtx(ctx, headerWait, readWait, sawData)
	s.trackRead(live)
	if w != nil && !sawData && streamID != 0 {
		w.arm(streamID, live.promote)
	}
	rh := &rpcHold{hold: hold, stop: live.stop, watch: w, stream: streamID}
	context.AfterFunc(live, func() {
		live.stop()
		s.untrackRead(live)
		if w != nil {
			w.forget(streamID)
		}
		if releaseToken != nil {
			releaseToken()
		}
		hold.Release()
	})
	return context.WithValue(live, rpcHoldKey{}, rh), nil
}

// rpcHold is the pessimistic gRPC reservation for one RPC.
type rpcHold struct {
	hold       *netlimit.Hold
	stop       func()
	watch      *frameWatch
	stream     uint32
	commitOnce sync.Once
	commitDone func()
}

type rpcHoldKey struct{}

// noteGRPCBody stops the read timer and shrinks the reservation to n
// bytes, the encoded size, or 0 when the message will not be stored.
// A missing hold (ingest called without the tap) does nothing. The
// timer is stopped before the commit, so SQLite is not under the
// read deadline.
func (s *Server) noteGRPCBody(ctx context.Context, n int64) error {
	rh, _ := ctx.Value(rpcHoldKey{}).(*rpcHold)
	if rh == nil {
		return nil
	}
	if rh.stop != nil {
		rh.stop()
	}
	if rh.hold != nil {
		rh.hold.Shrink(n)
	}
	return nil
}

// enterGRPCCommit counts a handler that is about to commit. The stats
// End event drops the count after the status is written, so Shutdown can
// wait for that response before it closes the connection.
func (s *Server) enterGRPCCommit(ctx context.Context) {
	if s == nil || s.commits == nil {
		return
	}
	rh, _ := ctx.Value(rpcHoldKey{}).(*rpcHold)
	if rh == nil {
		return
	}
	s.commits.add()
	rh.commitDone = s.commits.done
	if rh.watch != nil {
		rh.watch.trackCommit(rh.stream, func() { releaseGRPCCommit(ctx) })
	}
}

func releaseGRPCCommit(ctx context.Context) {
	if ctx == nil {
		return
	}
	rh, _ := ctx.Value(rpcHoldKey{}).(*rpcHold)
	if rh == nil {
		return
	}
	rh.commitOnce.Do(func() {
		if rh.commitDone != nil {
			rh.commitDone()
		}
	})
}

// readCtx is the stream context for one admitted RPC. Its timer cancels
// the read with context.DeadlineExceeded. Until a DATA frame arrives the
// timer is the header timeout. promote moves it out to the read timeout
// without cancelling. stop disarms the timer without cancelling, which
// is how the commit stays off the deadline. The parent cancel still ends
// the context, so the reservation is released when the RPC finishes.
type readCtx struct {
	context.Context
	done     chan struct{}
	started  time.Time
	readWait time.Duration
	deadline time.Time
	mu       sync.Mutex
	err      error
	timer    *time.Timer
	gen      int
	stopped  bool
	closed   bool
}

func newReadCtx(parent context.Context, header, read time.Duration, sawData bool) *readCtx {
	initial := header
	if sawData || read < header {
		initial = read
	}
	now := time.Now()
	c := &readCtx{
		Context:  parent,
		done:     make(chan struct{}),
		started:  now,
		readWait: read,
		deadline: now.Add(initial),
		gen:      1,
	}
	c.timer = time.AfterFunc(initial, func() { c.failRead(1) })
	go func() {
		<-parent.Done()
		c.fail(parent.Err())
	}()
	return c
}

func (c *readCtx) Done() <-chan struct{} { return c.done }

func (c *readCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return c.Context.Err()
}

func (c *readCtx) Deadline() (time.Time, bool) {
	c.mu.Lock()
	stopped := c.stopped
	deadline := c.deadline
	c.mu.Unlock()
	if stopped {
		return c.Context.Deadline()
	}
	return deadline, true
}

// promote extends a header-only deadline to the read timeout. The first
// DATA payload byte is what calls it. A timer that already fired stays
// fired.
func (c *readCtx) promote() {
	c.mu.Lock()
	if c.stopped || c.closed {
		c.mu.Unlock()
		return
	}
	c.gen++
	gen := c.gen
	t := c.timer
	c.timer = nil
	deadline := c.started.Add(c.readWait)
	c.deadline = deadline
	wait := time.Until(deadline)
	c.mu.Unlock()
	if t != nil {
		t.Stop()
	}
	if wait <= 0 {
		c.failRead(gen)
		return
	}
	c.mu.Lock()
	if c.stopped || c.closed || c.gen != gen {
		c.mu.Unlock()
		return
	}
	c.timer = time.AfterFunc(wait, func() { c.failRead(gen) })
	c.mu.Unlock()
}

// stop disarms the read timer. A timer that has already cancelled the
// read stays cancelled.
func (c *readCtx) stop() {
	c.mu.Lock()
	c.stopped = true
	c.gen++
	t := c.timer
	c.timer = nil
	c.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}

func (c *readCtx) failRead(gen int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || c.closed || c.gen != gen {
		return
	}
	c.closed = true
	c.err = context.DeadlineExceeded
	close(c.done)
}

// failIfReading cancels a call that is still waiting for its message.
// A call that already stopped the timer, because the message arrived,
// is left alone so its commit can finish.
func (c *readCtx) failIfReading() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || c.closed {
		return
	}
	c.closed = true
	c.gen++
	c.err = context.DeadlineExceeded
	close(c.done)
}

func (s *Server) trackRead(c *readCtx) {
	if s == nil || c == nil {
		return
	}
	s.phaseMu.Lock()
	if s.reads == nil {
		s.reads = map[*readCtx]struct{}{}
	}
	s.reads[c] = struct{}{}
	s.phaseMu.Unlock()
}

func (s *Server) untrackRead(c *readCtx) {
	if s == nil || c == nil {
		return
	}
	s.phaseMu.Lock()
	delete(s.reads, c)
	s.phaseMu.Unlock()
}

func (s *Server) cancelReadingRPCs() {
	s.phaseMu.Lock()
	reading := make([]*readCtx, 0, len(s.reads))
	for c := range s.reads {
		reading = append(reading, c)
	}
	s.phaseMu.Unlock()
	for _, c := range reading {
		c.failIfReading()
	}
}

func (c *readCtx) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.err == nil {
		c.err = err
	}
	close(c.done)
}

// retryExhausted is RESOURCE_EXHAUSTED with RetryInfo. OTLP treats that
// code as retryable only when RetryInfo is present. A payload that is
// simply too large does not use this helper.
func retryExhausted(msg string) error {
	st, err := status.New(codes.ResourceExhausted, msg).WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(netlimit.RetryAfter),
	})
	if err != nil {
		return status.Error(codes.ResourceExhausted, msg)
	}
	return st.Err()
}
