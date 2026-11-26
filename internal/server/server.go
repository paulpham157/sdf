// Package server is the local OTLP receiver.
//
// HTTP listens for JSON and protobuf on /v1/traces, /v1/metrics, and /v1/logs.
// The media type has to be application/json or application/x-protobuf.
// Anything else, including a missing Content-Type, is 415 and is not written.
// A request that carries an Origin header is 403 and is not written. When the
// listener is bound to loopback, Host must be localhost, 127.0.0.1, or [::1]
// (port optional, or the bound port); any other host is 403. A non-loopback
// bind does not apply that Host check.
// A body may use Content-Length or Transfer-Encoding: chunked. Chunked wins
// when both are present. Content-Encoding: gzip is inflated after the body
// is read; the decompressed size shares the same limit as the raw body.
// At most one non-identity coding is accepted. Any other content encoding
// is rejected with 415.
//
// gRPC listens on a separate port for the OTLP TraceService, MetricsService,
// and LogsService Export RPCs. pi-otel's default endpoint is this port.
// The gzip compressor is registered so clients can send grpc-encoding: gzip.
//
// When Token is set, POST /v1/traces, /v1/metrics, /v1/logs, and
// /v1/sessions require Authorization: Bearer <token>, and gRPC Export
// RPCs require the same value in the authorization metadata. GET /health
// stays unauthenticated. An unset Token leaves the receiver open.
//
// Limits bound connections, header and body time, and bytes buffered at
// once. Zero fields use defaults. A request is acknowledged only after
// the SQLite commit. A cap or a timeout is a failure response, not a
// partial success, so the client retries it.
package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/keepalive"

	"github.com/paulpham157/sdf/internal/netlimit"
	"github.com/paulpham157/sdf/internal/store"
)

// Server accepts OTLP/HTTP and OTLP/gRPC on two listeners.
type Server struct {
	Host     string
	HTTPPort int
	GRPCPort int
	Store    *store.Store
	Quiet    bool
	// Token, when non-empty, requires a matching Bearer token on write
	// paths and gRPC Export RPCs. It is never written to the request log.
	Token string
	// Log receives request lines when Quiet is false. When nil, stderr is used.
	Log io.Writer
	// Limits configures the connection, timeout, and byte caps. Zero
	// fields use defaults. It is read once in Start; later changes are
	// ignored.
	Limits netlimit.Limits

	httpLn   net.Listener
	grpcLn   net.Listener
	httpLim  *netlimit.Listener
	grpcLim  *netlimit.Listener
	grpcSrv  *grpc.Server
	log      io.Writer
	httpHost string
	httpPort string

	lim         netlimit.Limits
	budget      *netlimit.Budget
	tokens      *netlimit.Gate
	ctx         context.Context
	cancel      context.CancelFunc
	httpWG      sync.WaitGroup
	stopOnce    sync.Once
	blockInsert func(context.Context) error
	// blockAfterInsert delays the success response after the commit.
	// Tests use it to prove Shutdown does not drop that response.
	// It is not set in production.
	blockAfterInsert func(context.Context)

	phaseMu sync.Mutex
	// reading is the HTTP connections still in a read. Shutdown closes
	// these and leaves a connection that has started its commit alone,
	// so the client receives the 200 for a row that was stored.
	reading map[net.Conn]struct{}
}

// Start binds both listeners and serves until Shutdown.
func (s *Server) Start() error {
	if s.Store == nil {
		return fmt.Errorf("store is required")
	}
	lim, err := netlimit.Resolve(s.Limits)
	if err != nil {
		return err
	}
	if s.log == nil {
		if s.Log != nil {
			s.log = s.Log
		} else {
			s.log = os.Stderr
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.lim = lim
	s.budget = netlimit.NewBudget(lim.MaxInFlightBytes)
	// gRPC may hold at most half the budget, and always enough for one
	// body. When the in-flight budget is a single body, that share is
	// the whole budget: one accepted message has to fit, and HTTP is
	// not left a separate remainder.
	grpcMax := lim.MaxInFlightBytes / 2
	if grpcMax < int64(lim.MaxBodyBytes) {
		grpcMax = int64(lim.MaxBodyBytes)
	}
	s.budget.SetGrpcMax(grpcMax)
	s.tokens = netlimit.NewGate(lim.MaxConnsPerToken)
	s.ctx = ctx
	s.cancel = cancel
	host := s.Host
	if host == "" {
		host = "127.0.0.1"
	}
	httpLn, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(s.HTTPPort)))
	if err != nil {
		cancel()
		return err
	}
	grpcLn, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(s.GRPCPort)))
	if err != nil {
		_ = httpLn.Close()
		cancel()
		return err
	}
	httpHost, httpPort, err := net.SplitHostPort(httpLn.Addr().String())
	if err != nil {
		_ = httpLn.Close()
		_ = grpcLn.Close()
		cancel()
		return err
	}
	s.httpLim = netlimit.Wrap(httpLn, lim.MaxConns, lim.MaxConnsPerIP)
	s.grpcLim = netlimit.Wrap(grpcLn, lim.MaxConns, lim.MaxConnsPerIP)
	// An over-limit gRPC connection is closed with no bytes. Writing an
	// HTTP status here would block Accept on a client that does not read.
	s.grpcLim.Silent = true
	s.httpLn = s.httpLim
	s.grpcLn = s.grpcLim
	s.httpHost = httpHost
	s.httpPort = httpPort
	// OTLP Export RPCs are unary. The stream interceptor is defense in depth
	// if a streaming method is added later. admitRPC runs before the body
	// is read.
	s.grpcSrv = grpc.NewServer(
		grpc.MaxRecvMsgSize(lim.MaxBodyBytes),
		grpc.MaxSendMsgSize(lim.MaxBodyBytes),
		grpc.MaxConcurrentStreams(maxConcurrentStreams),
		grpc.MaxHeaderListSize(maxHeaderListSize),
		grpc.ConnectionTimeout(lim.ReadHeaderTimeout),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     lim.IdleTimeout,
			MaxConnectionAge:      maxConnectionAge,
			MaxConnectionAgeGrace: maxConnectionAgeGrace,
			Time:                  keepaliveTime,
			Timeout:               keepaliveTimeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             minPingInterval,
			PermitWithoutStream: true,
		}),
		grpc.UnaryInterceptor(s.authInterceptor),
		grpc.StreamInterceptor(s.streamAuthInterceptor),
		grpc.InTapHandle(s.admitRPC),
	)
	coltracepb.RegisterTraceServiceServer(s.grpcSrv, traceService{srv: s})
	colmetricspb.RegisterMetricsServiceServer(s.grpcSrv, metricsService{srv: s})
	collogspb.RegisterLogsServiceServer(s.grpcSrv, logsService{srv: s})
	s.httpWG.Add(1)
	go s.serveHTTP(s.httpLim)
	go func() {
		_ = s.grpcSrv.Serve(s.grpcLim)
	}()
	return nil
}

// HTTPAddress is the bound HTTP host:port.
func (s *Server) HTTPAddress() string {
	if s.httpLn == nil {
		return ""
	}
	return s.httpLn.Addr().String()
}

// GRPCAddress is the bound gRPC host:port.
func (s *Server) GRPCAddress() string {
	if s.grpcLn == nil {
		return ""
	}
	return s.grpcLn.Addr().String()
}

// grpcGraceTimeout is how long Shutdown waits for gRPC handlers to
// finish before it closes their connections. A header-only stream would
// otherwise hold GracefulStop until the read timeout or the connection
// age. A commit that has already started fits in this window.
const grpcGraceTimeout = 2 * time.Second

// Shutdown stops both listeners. It cancels the server context, closes
// HTTP connections that are still reading, and leaves a connection that
// has started its commit to finish and write the 200. It then waits for
// those handlers and gracefully stops gRPC so an in-flight commit can
// finish. If that stop is still waiting after grpcGraceTimeout, the
// remaining RPCs are closed. A second call does nothing.
func (s *Server) Shutdown() {
	s.stopOnce.Do(func() {
		s.phaseMu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		reading := make([]net.Conn, 0, len(s.reading))
		for c := range s.reading {
			reading = append(reading, c)
		}
		s.phaseMu.Unlock()
		if s.httpLn != nil {
			_ = s.httpLn.Close()
		}
		for _, c := range reading {
			_ = c.Close()
		}
		s.httpWG.Wait()
		if s.grpcSrv != nil {
			done := make(chan struct{})
			go func() {
				s.grpcSrv.GracefulStop()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(grpcGraceTimeout):
				s.grpcSrv.Stop()
				<-done
			}
		}
		if s.grpcLn != nil {
			_ = s.grpcLn.Close()
		}
	})
}

// trackReading records an HTTP connection until it closes or begins a commit.
// It fails when shutdown has already been requested, so a handler cannot
// block in a read that Shutdown's snapshot already missed.
func (s *Server) trackReading(c net.Conn) error {
	s.phaseMu.Lock()
	defer s.phaseMu.Unlock()
	if s.ctx != nil && s.ctx.Err() != nil {
		return errStopping
	}
	if s.reading == nil {
		s.reading = map[net.Conn]struct{}{}
	}
	s.reading[c] = struct{}{}
	return nil
}

func (s *Server) untrack(c net.Conn) {
	s.phaseMu.Lock()
	delete(s.reading, c)
	s.phaseMu.Unlock()
}

// beginHTTPCommit takes the connection out of the reading set and then
// checks shutdown. The two happen under one lock with Shutdown, so a
// connection is not closed and also allowed to commit.
func (s *Server) beginHTTPCommit(c net.Conn) error {
	s.phaseMu.Lock()
	delete(s.reading, c)
	stopped := s.ctx != nil && s.ctx.Err() != nil
	s.phaseMu.Unlock()
	if stopped {
		return errStopping
	}
	return s.beforeInsert()
}
