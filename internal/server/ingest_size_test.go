package server

import (
	"path/filepath"
	"strings"
	"testing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/store"
)

func TestIngestRejectsAnOversizedProto(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{
						StringValue: strings.Repeat("x", otlp.MaxBodyBytes),
					}},
				}},
			}},
		}},
	}
	err = ingest(&Server{Store: db, Quiet: true}, "logs", req)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code %v err %v", status.Code(err), err)
	}
	rows, err := db.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("stored %d rows", len(rows))
	}
}
