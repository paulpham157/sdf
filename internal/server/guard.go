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
	msgTooLarge     = "payload too large"
	msgInFlight     = "inflight limit"
	msgTooManyToken = "too many requests"
	msgReadTimeout  = "read timeout"
	msgStopping     = "server stopping"

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
// arrived. The read timer cancels the RPC with DEADLINE_EXCEEDED after
// --read-timeout, which is what releases a header-only stream. The
// handler stops that timer before the SQLite commit.
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
	hold, ok := s.budget.ReserveGrpc(int64(s.lim.MaxBodyBytes))
	if !ok {
		if releaseToken != nil {
			releaseToken()
		}
		return nil, retryExhausted(msgInFlight)
	}
	wait := s.lim.ReadTimeout
	if wait <= 0 {
		wait = netlimit.DefaultReadTimeout
	}
	// The reservation stays until the message arrives or this timer
	// cancels the read. Stopping the timer leaves the context alive, so
	// the commit is not under the deadline.
	live := newReadCtx(ctx, wait)
	rh := &rpcHold{hold: hold, stop: live.stop}
	context.AfterFunc(live, func() {
		live.stop()
		if releaseToken != nil {
			releaseToken()
		}
		hold.Release()
	})
	return context.WithValue(live, rpcHoldKey{}, rh), nil
}

// rpcHold is the pessimistic gRPC reservation for one RPC.
type rpcHold struct {
	hold *netlimit.Hold
	stop func()
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

// readCtx is the stream context for one admitted RPC. Its timer cancels
// the read with context.DeadlineExceeded. stop disarms that timer
// without cancelling, which is how the commit stays off the deadline.
// The parent cancel still ends the context, so the reservation is
// released when the RPC finishes.
type readCtx struct {
	context.Context
	done     chan struct{}
	deadline time.Time
	mu       sync.Mutex
	err      error
	timer    *time.Timer
	stopped  bool
	closed   bool
}

func newReadCtx(parent context.Context, wait time.Duration) *readCtx {
	c := &readCtx{
		Context:  parent,
		done:     make(chan struct{}),
		deadline: time.Now().Add(wait),
	}
	c.timer = time.AfterFunc(wait, c.failRead)
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

// stop disarms the read timer. A timer that has already cancelled the
// read stays cancelled.
func (c *readCtx) stop() {
	c.mu.Lock()
	c.stopped = true
	t := c.timer
	c.timer = nil
	c.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}

func (c *readCtx) failRead() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || c.closed {
		return
	}
	c.closed = true
	c.err = context.DeadlineExceeded
	close(c.done)
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
