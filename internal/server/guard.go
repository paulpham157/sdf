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
// payload. An admitted RPC reserves the per-request maximum, from the
// gRPC share of the budget, until the message has arrived or the header
// timeout fires. The timer releases the bytes and does not cancel the
// RPC. The handler then keeps only the encoded size, and the SQLite
// commit is not on that timer.
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
	// The pessimistic reservation lasts until the message arrives or the
	// header timeout fires. The timer releases the bytes. It does not
	// cancel the RPC, so a body that is still arriving can finish, and
	// the SQLite commit is not on this timer. The handler then keeps
	// only the encoded size.
	wait := s.lim.ReadHeaderTimeout
	if wait <= 0 {
		wait = netlimit.DefaultReadHeaderTimeout
	}
	rh := &rpcHold{budget: s.budget, hold: hold}
	rh.timer = time.AfterFunc(wait, rh.expire)
	context.AfterFunc(ctx, func() {
		rh.release()
		if releaseToken != nil {
			releaseToken()
		}
	})
	return context.WithValue(ctx, rpcHoldKey{}, rh), nil
}

// rpcHold is the pessimistic gRPC reservation for one RPC.
type rpcHold struct {
	mu      sync.Mutex
	budget  *netlimit.Budget
	hold    *netlimit.Hold
	timer   *time.Timer
	settled bool
}

type rpcHoldKey struct{}

// expire drops the pessimistic reservation when the message has not
// arrived. A later finish reserves the encoded size instead.
func (rh *rpcHold) expire() {
	rh.mu.Lock()
	if rh.settled {
		rh.mu.Unlock()
		return
	}
	h := rh.hold
	rh.hold = nil
	rh.mu.Unlock()
	if h != nil {
		h.Release()
	}
}

// release drops whatever this RPC still holds. It runs when the RPC
// ends and makes a later finish a no-op, so a reservation cannot be
// taken after the handler has returned.
func (rh *rpcHold) release() {
	rh.mu.Lock()
	rh.settled = true
	if rh.timer != nil {
		rh.timer.Stop()
	}
	h := rh.hold
	rh.hold = nil
	rh.mu.Unlock()
	if h != nil {
		h.Release()
	}
}

// finish keeps n bytes reserved for the rest of the RPC. n is the
// encoded size, or 0 when the message will not be stored. A missing
// hold means the header timer already released the pessimistic
// reservation; the encoded size is reserved then. false means that
// reservation did not fit.
func (rh *rpcHold) finish(n int64) bool {
	rh.mu.Lock()
	defer rh.mu.Unlock()
	if rh.settled {
		return true
	}
	rh.settled = true
	if rh.timer != nil {
		rh.timer.Stop()
	}
	if rh.hold != nil {
		rh.hold.Shrink(n)
		return true
	}
	if n == 0 {
		return true
	}
	h, ok := rh.budget.ReserveGrpc(n)
	if !ok {
		return false
	}
	rh.hold = h
	return true
}

// noteGRPCBody stops the read timer and shrinks the reservation to n
// bytes. A missing hold (ingest called without the tap) does nothing.
func (s *Server) noteGRPCBody(ctx context.Context, n int64) error {
	rh, _ := ctx.Value(rpcHoldKey{}).(*rpcHold)
	if rh == nil {
		return nil
	}
	if !rh.finish(n) {
		return retryExhausted(msgInFlight)
	}
	return nil
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
