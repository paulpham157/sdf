package otlp

import (
	"testing"
)

func TestDecodeLogRecordEventNameField(t *testing.T) {
	body := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"grok-cli"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"1","eventName":"grok_code.session_start","attributes":[{"key":"session.id","value":{"stringValue":"s1"}}]}]}]}]}`)
	recs, err := Decode("logs", body, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	if recs[0].Name == nil || *recs[0].Name != "grok_code.session_start" {
		t.Fatalf("name=%v body=%v attrs=%v", recs[0].Name, recs[0].Body, recs[0].Attributes)
	}
	if recs[0].SessionID == nil || *recs[0].SessionID != "s1" {
		t.Fatalf("session %v", recs[0].SessionID)
	}
}

func TestDecodeShortEventNamePrefixedForGrokCLI(t *testing.T) {
	body := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"grok-cli"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"1","eventName":"session_end","attributes":[{"key":"session.id","value":{"stringValue":"s2"}},{"key":"duration_secs","value":{"intValue":"9"}}]}]}]}]}`)
	recs, err := Decode("logs", body, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].Name == nil || *recs[0].Name != "grok_code.session_end" {
		t.Fatalf("name=%v", recs[0].Name)
	}
}

func TestDecodeEmptyEventNameStaysEmpty(t *testing.T) {
	// If the exporter leaves event_name, event.name, and body empty, ingest
	// cannot invent a name (summary may still infer from attributes).
	body := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"grok-cli"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"1","attributes":[{"key":"session.id","value":{"stringValue":"s3"}},{"key":"duration_secs","value":{"intValue":"9"}}]}]}]}]}`)
	recs, err := Decode("logs", body, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].Name != nil {
		t.Fatalf("expected empty name, got %v", recs[0].Name)
	}
}
