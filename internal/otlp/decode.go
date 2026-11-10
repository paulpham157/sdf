// Package otlp decodes OTLP/HTTP JSON and protobuf into flat records.
//
// JSON and protobuf both go through the official OTLP protobuf messages
// (go.opentelemetry.io/proto/otlp) so the two encodings share one shape.
// Trace and span ids follow the Python receiver: protobuf bytes are
// base64-encoded the way MessageToDict does, then a 16- or 32-character hex
// string is kept as hex and any other base64 string is decoded to hex.
// That preserves the ids in the hand-written JSON fixtures, which are hex
// strings of a length that is also valid base64.
package otlp

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/paulpham157/sdf/internal/record"
)

// MaxBodyBytes is the largest export body the receiver accepts.
const MaxBodyBytes = 8_000_000

// Error is a payload that is not an OTLP export request.
type Error struct {
	msg string
}

func (e *Error) Error() string { return e.msg }

func errf(msg string) error { return &Error{msg: msg} }

var jsonUnmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}

// First match wins. Pi spans set session.id together with pi.session.id and
// gen_ai.conversation.id. Pi logs set only pi.session.id. OpenCode runner
// spans set session.id, the same key Claude Code uses.
var sessionKeys = []string{
	"session.id",
	"conversation.id",
	"pi.session.id",
	"gen_ai.conversation.id",
}

// Decode parses an OTLP export body. contentType application/json selects
// JSON; every other media type, including an empty one, is protobuf.
func Decode(signal string, body []byte, contentType string) ([]record.Record, error) {
	if signal != "traces" && signal != "metrics" && signal != "logs" {
		return nil, errf("unknown signal " + signal)
	}
	if len(body) > MaxBodyBytes {
		return nil, errf("payload too large")
	}
	msg := newRequest(signal)
	media := mediaType(contentType)
	var err error
	if media == "application/json" {
		if !utf8.Valid(body) {
			return nil, errf("could not parse OTLP payload")
		}
		err = jsonUnmarshal.Unmarshal(body, msg)
	} else {
		err = proto.Unmarshal(body, msg)
	}
	if err != nil {
		return nil, errf("could not parse OTLP payload")
	}
	switch m := msg.(type) {
	case *coltracepb.ExportTraceServiceRequest:
		return traces(m), nil
	case *colmetricspb.ExportMetricsServiceRequest:
		return metrics(m), nil
	case *collogspb.ExportLogsServiceRequest:
		return logs(m), nil
	default:
		return nil, errf("unknown signal " + signal)
	}
}

func newRequest(signal string) proto.Message {
	switch signal {
	case "traces":
		return &coltracepb.ExportTraceServiceRequest{}
	case "metrics":
		return &colmetricspb.ExportMetricsServiceRequest{}
	default:
		return &collogspb.ExportLogsServiceRequest{}
	}
}

func mediaType(contentType string) string {
	media, _, _ := strings.Cut(contentType, ";")
	return strings.ToLower(strings.TrimSpace(media))
}

func traces(msg *coltracepb.ExportTraceServiceRequest) []record.Record {
	var out []record.Record
	for _, rs := range msg.GetResourceSpans() {
		resource := attributesOf(rs.GetResource().GetAttributes())
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				attrs := attributesOf(span.GetAttributes())
				code, message := spanStatus(span)
				out = append(out, makeRecord(recordParts{
					signal:        "traces",
					resource:      resource,
					attributes:    attrs,
					name:          emptyAsNil(span.GetName()),
					start:         nano(span.GetStartTimeUnixNano()),
					end:           nano(span.GetEndTimeUnixNano()),
					traceID:       otelID(span.GetTraceId()),
					spanID:        otelID(span.GetSpanId()),
					parentSpanID:  otelID(span.GetParentSpanId()),
					statusCode:    code,
					statusMessage: message,
				}))
			}
		}
	}
	return out
}

func metrics(msg *colmetricspb.ExportMetricsServiceRequest) []record.Record {
	var out []record.Record
	for _, rm := range msg.GetResourceMetrics() {
		resource := attributesOf(rm.GetResource().GetAttributes())
		for _, sm := range rm.GetScopeMetrics() {
			for _, metric := range sm.GetMetrics() {
				name := emptyAsNil(metric.GetName())
				unit := emptyAsNil(metric.GetUnit())
				switch data := metric.GetData().(type) {
				case *metricspb.Metric_Gauge:
					for _, point := range data.Gauge.GetDataPoints() {
						out = append(out, metricRecord(resource, name, unit, "gauge", "unspecified", point.GetAttributes(), point.GetStartTimeUnixNano(), point.GetTimeUnixNano(), numberPoint(point)))
					}
				case *metricspb.Metric_Sum:
					temp := temporality(data.Sum.GetAggregationTemporality())
					for _, point := range data.Sum.GetDataPoints() {
						out = append(out, metricRecord(resource, name, unit, "sum", temp, point.GetAttributes(), point.GetStartTimeUnixNano(), point.GetTimeUnixNano(), numberPoint(point)))
					}
				case *metricspb.Metric_Histogram:
					temp := temporality(data.Histogram.GetAggregationTemporality())
					for _, point := range data.Histogram.GetDataPoints() {
						var value *float64
						if point.Sum != nil {
							v := *point.Sum
							value = &v
						}
						out = append(out, metricRecord(resource, name, unit, "histogram", temp, point.GetAttributes(), point.GetStartTimeUnixNano(), point.GetTimeUnixNano(), value))
					}
				}
			}
		}
	}
	return out
}

func metricRecord(resource map[string]any, name, unit *string, kind, temp string, attrs []*commonpb.KeyValue, start, when uint64, value *float64) record.Record {
	return makeRecord(recordParts{
		signal:      "metrics",
		resource:    resource,
		attributes:  attributesOf(attrs),
		name:        name,
		start:       nano(start),
		when:        nano(when),
		value:       value,
		unit:        unit,
		temporality: record.Str(temp),
		kind:        record.Str(kind),
	})
}

func logs(msg *collogspb.ExportLogsServiceRequest) []record.Record {
	var out []record.Record
	for _, rl := range msg.GetResourceLogs() {
		resource := attributesOf(rl.GetResource().GetAttributes())
		for _, sl := range rl.GetScopeLogs() {
			for _, rec := range sl.GetLogRecords() {
				attrs := attributesOf(rec.GetAttributes())
				body := bodyText(rec.GetBody())
				out = append(out, makeRecord(recordParts{
					signal:     "logs",
					resource:   resource,
					attributes: attrs,
					name:       eventName(body, attrs, resource),
					when:       nano(rec.GetTimeUnixNano()),
					body:       body,
					traceID:    otelID(rec.GetTraceId()),
					spanID:     otelID(rec.GetSpanId()),
				}))
			}
		}
	}
	return out
}

type recordParts struct {
	signal        string
	resource      map[string]any
	attributes    map[string]any
	name          *string
	start         *int64
	end           *int64
	when          *int64
	body          *string
	value         *float64
	unit          *string
	temporality   *string
	kind          *string
	traceID       *string
	spanID        *string
	parentSpanID  *string
	statusCode    string
	statusMessage *string
}

func makeRecord(p recordParts) record.Record {
	if p.attributes == nil {
		p.attributes = map[string]any{}
	}
	if p.resource == nil {
		p.resource = map[string]any{}
	}
	return record.Record{
		Signal:            p.signal,
		ServiceName:       record.OTLPText(p.resource["service.name"]),
		SessionID:         sessionID(p.attributes, p.resource),
		Name:              p.name,
		StartUnixNano:     p.start,
		EndUnixNano:       p.end,
		TimeUnixNano:      p.when,
		Value:             p.value,
		MetricUnit:        p.unit,
		MetricTemporality: p.temporality,
		MetricKind:        p.kind,
		Attributes:        p.attributes,
		Resource:          p.resource,
		Body:              p.body,
		TraceID:           p.traceID,
		SpanID:            p.spanID,
		ParentSpanID:      p.parentSpanID,
		StatusCode:        p.statusCode,
		StatusMessage:     p.statusMessage,
	}
}

func spanStatus(span *tracepb.Span) (string, *string) {
	st := span.GetStatus()
	if st == nil {
		return "UNSET", nil
	}
	code := "UNSET"
	switch st.GetCode() {
	case tracepb.Status_STATUS_CODE_OK:
		code = "OK"
	case tracepb.Status_STATUS_CODE_ERROR:
		code = "ERROR"
	}
	if st.GetMessage() == "" {
		return code, nil
	}
	return code, record.Str(st.GetMessage())
}

func attributesOf(items []*commonpb.KeyValue) map[string]any {
	parsed := map[string]any{}
	for _, item := range items {
		if item == nil || item.Key == "" {
			continue
		}
		parsed[item.Key] = anyValue(item.GetValue())
	}
	return parsed
}

func anyValue(v *commonpb.AnyValue) any {
	if v == nil || v.Value == nil {
		return nil
	}
	switch t := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return t.StringValue
	case *commonpb.AnyValue_IntValue:
		return t.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return t.DoubleValue
	case *commonpb.AnyValue_BoolValue:
		return t.BoolValue
	case *commonpb.AnyValue_ArrayValue:
		values := t.ArrayValue.GetValues()
		out := make([]any, 0, len(values))
		for _, item := range values {
			out = append(out, anyValue(item))
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		return attributesOf(t.KvlistValue.GetValues())
	case *commonpb.AnyValue_BytesValue:
		if len(t.BytesValue) == 0 {
			return ""
		}
		return base64.StdEncoding.EncodeToString(t.BytesValue)
	default:
		return nil
	}
}

func bodyText(v *commonpb.AnyValue) *string {
	parsed := anyValue(v)
	if parsed == nil {
		return nil
	}
	if s, ok := parsed.(string); ok {
		return record.Str(s)
	}
	return record.Str(pythonStr(parsed))
}

func eventName(body *string, attributes, resource map[string]any) *string {
	attr := record.OTLPText(attributes["event.name"])
	if body != nil && (strings.HasPrefix(*body, "claude_code.") || strings.HasPrefix(*body, "codex.")) {
		return body
	}
	if attr != nil && (strings.HasPrefix(*attr, "claude_code.") || strings.HasPrefix(*attr, "codex.")) {
		return attr
	}
	service := ""
	if s := record.OTLPText(resource["service.name"]); s != nil {
		service = *s
	}
	if attr != nil && strings.HasPrefix(service, "claude-code") && !strings.Contains(*attr, ".") {
		return record.Str("claude_code." + *attr)
	}
	if attr != nil {
		return attr
	}
	return body
}

func sessionID(attributes, resource map[string]any) *string {
	for _, source := range []map[string]any{attributes, resource} {
		if source == nil {
			continue
		}
		for _, key := range sessionKeys {
			if text := record.OTLPText(source[key]); text != nil {
				return text
			}
		}
	}
	return nil
}

func numberPoint(point *metricspb.NumberDataPoint) *float64 {
	switch point.GetValue().(type) {
	case *metricspb.NumberDataPoint_AsInt:
		v := float64(point.GetAsInt())
		return &v
	case *metricspb.NumberDataPoint_AsDouble:
		v := point.GetAsDouble()
		return &v
	default:
		return nil
	}
}

func temporality(v metricspb.AggregationTemporality) string {
	switch v {
	case metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA:
		return "delta"
	case metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE:
		return "cumulative"
	default:
		text := strings.ToUpper(v.String())
		if strings.Contains(text, "DELTA") || text == "1" {
			return "delta"
		}
		if strings.Contains(text, "CUMULATIVE") || text == "2" {
			return "cumulative"
		}
		return "unspecified"
	}
}

// nano treats 0 as missing. Proto3 scalar timestamps have no presence bit,
// and MessageToDict omits the default, which the Python decoder reads as None.
func nano(v uint64) *int64 {
	if v == 0 {
		return nil
	}
	n := int64(v)
	return &n
}

func emptyAsNil(s string) *string {
	if s == "" {
		return nil
	}
	return record.Str(s)
}

// otelID normalizes an OTLP trace or span id to the string the Python
// receiver stores. See the package comment.
func otelID(raw []byte) *string {
	if len(raw) == 0 {
		return nil
	}
	text := base64.StdEncoding.EncodeToString(raw)
	if (len(text) == 16 || len(text) == 32) && isHex(text) {
		return record.Str(strings.ToLower(text))
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(decoded) == 0 {
		return nil
	}
	return record.Str(hex.EncodeToString(decoded))
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// pythonStr approximates Python's str() for a non-string log body.
func pythonStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = pythonRepr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = pythonRepr(k) + ": " + pythonRepr(t[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case nil:
		return "None"
	default:
		return fmt.Sprint(t)
	}
}

func pythonRepr(v any) string {
	s, ok := v.(string)
	if ok {
		return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
	}
	if v == nil {
		return "None"
	}
	return pythonStr(v)
}
