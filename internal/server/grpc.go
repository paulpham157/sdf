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

func (t traceService) Export(_ context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	if err := ingest(t.srv, "traces", req); err != nil {
		return nil, err
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

type metricsService struct {
	colmetricspb.UnimplementedMetricsServiceServer
	srv *Server
}

func (m metricsService) Export(_ context.Context, req *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	if err := ingest(m.srv, "metrics", req); err != nil {
		return nil, err
	}
	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}

type logsService struct {
	collogspb.UnimplementedLogsServiceServer
	srv *Server
}

func (l logsService) Export(_ context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	if err := ingest(l.srv, "logs", req); err != nil {
		return nil, err
	}
	return &collogspb.ExportLogsServiceResponse{}, nil
}

func ingest(srv *Server, signal string, msg proto.Message) error {
	// Do not commit if shutdown has started. A successful return is a full
	// success: partial_success stays unset.
	if err := srv.beforeInsert(); err != nil {
		return retryExhausted(msgStopping)
	}
	// The gRPC server has already decoded the request. Size is the encoded
	// length, the same limit the HTTP body uses. No RetryInfo: the same
	// payload will not fit on a retry.
	if srv.lim.MaxBodyBytes < 1 || proto.Size(msg) > srv.lim.MaxBodyBytes {
		return status.Error(codes.ResourceExhausted, msgTooLarge)
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
