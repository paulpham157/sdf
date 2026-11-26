package server

import (
	"context"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/paulpham157/sdf/internal/otlp"
)

type traceService struct {
	coltracepb.UnimplementedTraceServiceServer
	srv *Server
}

func (t traceService) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	if err := ingest(ctx, t.srv, "traces", req); err != nil {
		return nil, err
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

type metricsService struct {
	colmetricspb.UnimplementedMetricsServiceServer
	srv *Server
}

func (m metricsService) Export(ctx context.Context, req *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	if err := ingest(ctx, m.srv, "metrics", req); err != nil {
		return nil, err
	}
	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}

type logsService struct {
	collogspb.UnimplementedLogsServiceServer
	srv *Server
}

func (l logsService) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	if err := ingest(ctx, l.srv, "logs", req); err != nil {
		return nil, err
	}
	return &collogspb.ExportLogsServiceResponse{}, nil
}

func ingest(ctx context.Context, srv *Server, signal string, msg proto.Message) error {
	// The gRPC server has already decoded the request. Size is the encoded
	// length. Zero means the server was not started and the package default
	// applies, which is what a direct call and the benchmark use.
	limit := srv.lim.MaxBodyBytes
	if limit < 1 {
		limit = otlp.MaxBodyBytes
	}
	n := proto.Size(msg)
	if n > limit {
		// Drop the pessimistic reservation. No RetryInfo: the same
		// payload will not fit on a retry.
		_ = srv.noteGRPCBody(ctx, 0)
		return status.Error(codes.ResourceExhausted, msgTooLarge)
	}
	// Shrink to the encoded size and stop the read timer before the
	// commit, so --read-timeout does not cover SQLite and a small
	// message does not keep holding the per-request maximum.
	if err := srv.noteGRPCBody(ctx, int64(n)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	// Do not commit if shutdown has started. A successful return is a full
	// success: partial_success stays unset.
	if err := srv.beforeInsert(); err != nil {
		return retryExhausted(msgStopping)
	}
	records, err := otlp.DecodeRequest(signal, msg)
	if err != nil {
		return status.Error(codes.InvalidArgument, "could not parse OTLP payload")
	}
	if err := srv.insertRecords(records); err != nil {
		return status.Error(codes.Internal, "store error")
	}
	return nil
}
