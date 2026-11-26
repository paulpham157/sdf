package server

import (
	"context"
	"errors"
	"net"
	"os"
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
	if s.ctx == nil || s.ctx.Err() != nil {
		return errStopping
	}
	if s.blockInsert != nil {
		if err := s.blockInsert(s.ctx); err != nil {
			return errStopping
		}
	}
	if s.ctx == nil || s.ctx.Err() != nil {
		return errStopping
	}
	return nil
}

// admitRPC runs before gRPC reads the message body. It rejects a bad
// token, a token cap, or an in-flight byte cap without buffering the
// payload. An admitted RPC reserves the per-request maximum until the
// RPC's context ends, because the size is not known until the length
// prefix arrives and the transport may buffer up to the maximum.
//
// The reservation is released when that context ends, including when the
// RPC is rejected after the tap (unknown method, oversized length prefix,
// handler error). Oversized messages are still RESOURCE_EXHAUSTED from
// the transport and carry no RetryInfo, so a client must not retry them.
// A cap rejection carries RetryInfo, so it is retryable.
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
	hold, ok := s.budget.Reserve(int64(s.lim.MaxBodyBytes))
	if !ok {
		if releaseToken != nil {
			releaseToken()
		}
		return nil, retryExhausted(msgInFlight)
	}
	timeout := s.lim.ReadTimeout
	if timeout <= 0 {
		timeout = netlimit.DefaultReadTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	context.AfterFunc(ctx, func() {
		cancel()
		if releaseToken != nil {
			releaseToken()
		}
		hold.Release()
	})
	return ctx, nil
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
