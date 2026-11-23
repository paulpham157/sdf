package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/otlp"
)

func TestOpenCodeCamelCaseSessionIDLogIsAttributed(t *testing.T) {
	const session = "ses_dfb792527ffeQUniA6w70OGP4U"
	span, err := otlp.Decode("traces", []byte(`{
		"resourceSpans": [{
			"resource": {"attributes": [
				{"key": "service.name", "value": {"stringValue": "opencode"}},
				{"key": "sdf.fixture", "value": {"stringValue": "sample"}}
			]},
			"scopeSpans": [{"spans": [{
				"name": "SessionRunner.drain",
				"traceId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"spanId": "bbbbbbbbbbbbbbbb",
				"startTimeUnixNano": "1",
				"endTimeUnixNano": "2",
				"status": {"code": "STATUS_CODE_ERROR", "message": "Free usage limit reached"},
				"attributes": [{"key": "session.id", "value": {"stringValue": "ses_dfb792527ffeQUniA6w70OGP4U"}}]
			}]}]
		}]
	}`), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	logs, err := otlp.Decode("logs", []byte(`{
		"resourceLogs": [{
			"resource": {"attributes": [
				{"key": "service.name", "value": {"stringValue": "opencode"}},
				{"key": "service.version", "value": {"stringValue": "2.3.2"}},
				{"key": "opencode.client", "value": {"stringValue": "cli"}},
				{"key": "sdf.fixture", "value": {"stringValue": "sample"}}
			]},
			"scopeLogs": [{"logRecords": [
				{
					"timeUnixNano": "3",
					"body": {"stringValue": "Failed to drain Session"},
					"attributes": [
						{"key": "exception.type", "value": {"stringValue": "AI.Error"}},
						{"key": "exception.message", "value": {"stringValue": "Free usage limit reached"}},
						{"key": "role", "value": {"stringValue": "server"}},
						{"key": "sessionID", "value": {"stringValue": "ses_dfb792527ffeQUniA6w70OGP4U"}}
					]
				},
				{
					"timeUnixNano": "4",
					"body": {"stringValue": "['cli starting']"},
					"attributes": [
						{"key": "role", "value": {"stringValue": "cli"}},
						{"key": "effect.fiberId", "value": {"intValue": "1"}}
					]
				}
			]}]
		}]
	}`), "application/json")
	if err != nil {
		t.Fatal(err)
	}

	report := Summarize(append(span, logs...), false)
	if len(report.Sessions) != 1 || report.Sessions[0].SessionID != session || report.Sessions[0].Tool != "opencode" {
		t.Fatalf("%+v", report.Sessions)
	}
	sessionSummary := report.Sessions[0]
	if sessionSummary.Traces != 1 || sessionSummary.Logs != 1 {
		t.Fatalf("signals traces=%d logs=%d", sessionSummary.Traces, sessionSummary.Logs)
	}
	for _, item := range sessionSummary.Missing {
		if item.Signal == "logs" {
			t.Fatalf("attributed session still missing logs: %+v", sessionSummary.Missing)
		}
	}
	if len(report.Unattributed) != 1 || report.Unattributed[0].Tool != "opencode" || report.Unattributed[0].Logs != 1 || report.Unattributed[0].Traces != 0 {
		t.Fatalf("unattributed %+v", report.Unattributed)
	}

	// Conflicting alias must not move the drain log onto a different session.
	conflict, err := otlp.Decode("logs", []byte(`{
		"resourceLogs": [{
			"resource": {"attributes": [
				{"key": "service.name", "value": {"stringValue": "opencode"}},
				{"key": "sdf.fixture", "value": {"stringValue": "sample"}}
			]},
			"scopeLogs": [{"logRecords": [{
				"timeUnixNano": "5",
				"body": {"stringValue": "Failed to drain Session"},
				"attributes": [
					{"key": "session.id", "value": {"stringValue": "ses_canonical0001"}},
					{"key": "sessionID", "value": {"stringValue": "ses_alias0001"}}
				]
			}]}]
		}]
	}`), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	conflictReport := Summarize(conflict, false)
	if len(conflictReport.Sessions) != 1 || conflictReport.Sessions[0].SessionID != "ses_canonical0001" {
		t.Fatalf("conflict sessions %+v", conflictReport.Sessions)
	}
	for _, item := range conflictReport.Sessions {
		if item.SessionID == "ses_alias0001" {
			t.Fatal("conflicting sessionID alias was merged into its own session")
		}
	}
	if conflictReport.Sessions[0].Logs != 1 {
		t.Fatalf("conflict logs %d", conflictReport.Sessions[0].Logs)
	}
	if strings.Contains(deref(conflict[0].SessionID), "alias") {
		t.Fatalf("decoded session %v", conflict[0].SessionID)
	}
}
