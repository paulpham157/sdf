package server

import (
	"context"
	"path/filepath"
	"testing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/paulpham157/sdf/internal/store"
)

// BenchmarkIngest commits one gRPC-shaped logs export to a WAL file: the
// same ingest path Export uses, including the store commit. -benchtime is a
// count (for example 40x) so a faster run does not insert more rows than a
// slower one and grow the file further.
func BenchmarkIngest(b *testing.B) {
	db, err := store.Open(filepath.Join(b.TempDir(), "telemetry.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	srv := &Server{Store: db, Quiet: true}
	req := ingestBenchRequest()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := ingest(context.Background(), srv, "logs", req); err != nil {
			b.Fatal(err)
		}
	}
}

// ingestBenchRequest is one export of 32 log records with the attribute
// shapes the receiver redacts and stores. The message is built once and
// reused, matching a client that sends the same payload repeatedly.
func ingestBenchRequest() *collogspb.ExportLogsServiceRequest {
	const n = 32
	recs := make([]*logspb.LogRecord, n)
	for i := range recs {
		recs[i] = &logspb.LogRecord{
			TimeUnixNano: uint64(1_700_000_000_000_000_000 + i),
			EventName:    "claude_code.api_request",
			Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{
				StringValue: "claude_code.api_request",
			}},
			Attributes: []*commonpb.KeyValue{
				benchKV("session.id", "bench-session"),
				benchKV("event.name", "claude_code.api_request"),
				benchKV("gen_ai.request.model", "sample-model"),
				benchKV("gen_ai.usage.input_tokens", "1200"),
				benchKV("gen_ai.usage.output_tokens", "80"),
				benchKV("prompt", "user text that is dropped before the row is stored"),
				benchKV("gen_ai.prompt.0.content", "nested prompt text"),
				benchKV("tool_name", "Bash"),
				benchKV("error.type", "timeout"),
				{
					Key: "meta",
					Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_KvlistValue{KvlistValue: &commonpb.KeyValueList{
						Values: []*commonpb.KeyValue{
							benchKV("count", "2"),
							benchKV("Prompt", "secret meta"),
						},
					}}},
				},
			},
		}
	}
	return &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				benchKV("service.name", "claude-code"),
				benchKV("session.id", "bench-session"),
				benchKV("host.name", "bench"),
			}},
			ScopeLogs: []*logspb.ScopeLogs{{
				Scope:      &commonpb.InstrumentationScope{Name: "bench", Version: "0"},
				LogRecords: recs,
			}},
		}},
	}
}

func benchKV(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}
