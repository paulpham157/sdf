// Package record is one stored span, metric point, or log record.
package record

// Record is a flattened OTLP span, metric point, or log record.
// Optional fields are nil when the telemetry did not carry them.
// Attribute and resource values are strings, bools, int64s, float64s,
// []any, or map[string]any, matching the Python decoder's JSON types.
type Record struct {
	ID                int64
	Signal            string
	ServiceName       *string
	SessionID         *string
	Name              *string
	StartUnixNano     *int64
	EndUnixNano       *int64
	TimeUnixNano      *int64
	Value             *float64
	MetricUnit        *string
	MetricTemporality *string
	MetricKind        *string
	Attributes        map[string]any
	Resource          map[string]any
	Body              *string
	TraceID           *string
	SpanID            *string
	ParentSpanID      *string
}

// WithSession returns a copy tagged with sessionID. Maps are shared.
func (r Record) WithSession(sessionID string) Record {
	r.SessionID = &sessionID
	return r
}

// Attr returns an attribute value.
func (r Record) Attr(key string) (any, bool) {
	if r.Attributes == nil {
		return nil, false
	}
	v, ok := r.Attributes[key]
	return v, ok
}
