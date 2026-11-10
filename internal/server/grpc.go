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
	body, err := proto.Marshal(msg)
	if err != nil {
		return status.Error(codes.InvalidArgument, "could not parse OTLP payload")
	}
	if len(body) > otlp.MaxBodyBytes {
		return status.Error(codes.ResourceExhausted, "payload too large")
	}
	records, err := otlp.Decode(signal, body, "application/x-protobuf")
	if err != nil {
		return status.Error(codes.InvalidArgument, "could not parse OTLP payload")
	}
	if _, err := srv.Store.Insert(records); err != nil {
		return status.Error(codes.Internal, "store error")
	}
	return nil
}
