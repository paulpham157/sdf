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
	// The byte reservation was taken before this message was read. Do not
	// commit if shutdown has started. A successful return is a full
	// success: partial_success stays unset.
	if err := srv.beforeInsert(); err != nil {
		return retryExhausted(msgStopping)
	}
	body, err := proto.Marshal(msg)
	if err != nil {
		return status.Error(codes.InvalidArgument, "could not parse OTLP payload")
	}
	if srv.lim.MaxBodyBytes < 1 || len(body) > srv.lim.MaxBodyBytes {
		// No RetryInfo: the same payload will not fit on a retry.
		return status.Error(codes.ResourceExhausted, msgTooLarge)
	}
	records, err := otlp.DecodeLimited(signal, body, "application/x-protobuf", srv.lim.MaxBodyBytes)
	if err != nil {
		return status.Error(codes.InvalidArgument, "could not parse OTLP payload")
	}
	if err := srv.insertRecords(records); err != nil {
		return status.Error(codes.Internal, "store error")
	}
	return nil
}
