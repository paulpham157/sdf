package otlp

import (
	"bytes"
	"reflect"
	"testing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestDecodeRequestMatchesProtobufDecode(t *testing.T) {
	msgs := []struct {
		signal string
		msg    proto.Message
	}{
		{"traces", &coltracepb.ExportTraceServiceRequest{
			ResourceSpans: []*tracepb.ResourceSpans{{
				Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strKV("service.name", "claude-code")}},
				ScopeSpans: []*tracepb.ScopeSpans{{
					Spans: []*tracepb.Span{{
						TraceId: bytes.Repeat([]byte{0xab}, 16),
						SpanId:  bytes.Repeat([]byte{0xcd}, 8),
						Name:    "chat",
						Attributes: []*commonpb.KeyValue{
							strKV("session.id", "s1"),
							strKV("gen_ai.request.model", "m"),
						},
					}},
				}},
			}},
		}},
		{"metrics", &colmetricspb.ExportMetricsServiceRequest{
			ResourceMetrics: []*metricspb.ResourceMetrics{{
				Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strKV("service.name", "claude-code")}},
				ScopeMetrics: []*metricspb.ScopeMetrics{{
					Metrics: []*metricspb.Metric{{
						Name: "claude_code.token.usage",
						Unit: "tokens",
						Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
							AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
							DataPoints: []*metricspb.NumberDataPoint{{
								TimeUnixNano: 10,
								Value:        &metricspb.NumberDataPoint_AsInt{AsInt: 3},
								Attributes:   []*commonpb.KeyValue{strKV("session.id", "s1")},
							}},
						}},
					}},
				}},
			}},
		}},
		{"logs", &collogspb.ExportLogsServiceRequest{
			ResourceLogs: []*logspb.ResourceLogs{{
				Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strKV("service.name", "pi")}},
				ScopeLogs: []*logspb.ScopeLogs{{
					LogRecords: []*logspb.LogRecord{{
						TimeUnixNano: 10,
						EventName:    "pi.session.start",
						Body:         &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "pi.session.start"}},
						Attributes:   []*commonpb.KeyValue{strKV("pi.session.id", "s1")},
					}},
				}},
			}},
		}},
	}
	for _, tc := range msgs {
		t.Run(tc.signal, func(t *testing.T) {
			body, err := proto.Marshal(tc.msg)
			if err != nil {
				t.Fatal(err)
			}
			fromBytes, err := Decode(tc.signal, body, "application/x-protobuf")
			if err != nil {
				t.Fatal(err)
			}
			fromMsg, err := DecodeRequest(tc.signal, tc.msg)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fromBytes, fromMsg) {
				t.Fatalf("DecodeRequest %+v\nDecode %+v", fromMsg, fromBytes)
			}
		})
	}
}

func TestDecodeRequestRejectsAMismatchedSignal(t *testing.T) {
	msg := &collogspb.ExportLogsServiceRequest{}
	if _, err := DecodeRequest("traces", msg); err == nil {
		t.Fatal("logs message accepted as traces")
	}
	if _, err := DecodeRequest("logs", nil); err == nil {
		t.Fatal("nil message accepted")
	}
}

func strKV(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}
