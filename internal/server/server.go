// Package server is the local OTLP receiver.
//
// HTTP listens for JSON and protobuf on /v1/traces, /v1/metrics, and /v1/logs.
// A body may use Content-Length or Transfer-Encoding: chunked. Chunked wins
// when both are present. Content-Encoding: gzip is inflated after the body
// is read; the decompressed size shares the same limit as the raw body.
// Any other content encoding is rejected with 415.
//
// gRPC listens on a separate port for the OTLP TraceService, MetricsService,
// and LogsService Export RPCs. pi-otel's default endpoint is this port.
// The gzip compressor is registered so clients can send grpc-encoding: gzip.
package server

import (
	"fmt"
	"io"
	"net"
	"os"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/encoding/gzip"

	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/store"
)

// Server accepts OTLP/HTTP and OTLP/gRPC on two listeners.
type Server struct {
	Host     string
	HTTPPort int
	GRPCPort int
	Store    *store.Store
	Quiet    bool

	httpLn  net.Listener
	grpcLn  net.Listener
	grpcSrv *grpc.Server
	log     io.Writer
}

// Start binds both listeners and serves until Shutdown.
func (s *Server) Start() error {
	if s.Store == nil {
		return fmt.Errorf("store is required")
	}
	if s.log == nil {
		s.log = os.Stderr
	}
	host := s.Host
	if host == "" {
		host = "127.0.0.1"
	}
	httpLn, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(s.HTTPPort)))
	if err != nil {
		return err
	}
	grpcLn, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(s.GRPCPort)))
	if err != nil {
		_ = httpLn.Close()
		return err
	}
	s.httpLn = httpLn
	s.grpcLn = grpcLn
	s.grpcSrv = grpc.NewServer(
		grpc.MaxRecvMsgSize(otlp.MaxBodyBytes),
		grpc.MaxSendMsgSize(otlp.MaxBodyBytes),
	)
	coltracepb.RegisterTraceServiceServer(s.grpcSrv, traceService{srv: s})
	colmetricspb.RegisterMetricsServiceServer(s.grpcSrv, metricsService{srv: s})
	collogspb.RegisterLogsServiceServer(s.grpcSrv, logsService{srv: s})
	go s.serveHTTP(httpLn)
	go func() {
		_ = s.grpcSrv.Serve(grpcLn)
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

// Shutdown stops both listeners.
func (s *Server) Shutdown() {
	if s.httpLn != nil {
		_ = s.httpLn.Close()
	}
	if s.grpcSrv != nil {
		s.grpcSrv.GracefulStop()
	}
	if s.grpcLn != nil {
		_ = s.grpcLn.Close()
	}
}
