package otlp

import (
	"fmt"
	"testing"
)

// Live OpenCode v2.3.2 session id from the 23 Nov 2026 dogfood run.
const openCodeLiveSession = "ses_dfb792527ffeQUniA6w70OGP4U"

func TestDecodeOpenCodeFailedToDrainLogUsesCamelCaseSessionID(t *testing.T) {
	// Shape matches the live v2.3.2 "Failed to drain Session" log: camelCase
	// sessionID on the log record, no session.id, service.name=opencode.
	body := []byte(`{
		"resourceLogs": [{
			"resource": {"attributes": [
				{"key": "service.name", "value": {"stringValue": "opencode"}},
				{"key": "service.version", "value": {"stringValue": "2.3.2"}},
				{"key": "opencode.client", "value": {"stringValue": "cli"}},
				{"key": "telemetry.sdk.language", "value": {"stringValue": "nodejs"}},
				{"key": "telemetry.sdk.name", "value": {"stringValue": "effect"}},
				{"key": "sdf.fixture", "value": {"stringValue": "sample"}}
			]},
			"scopeLogs": [{"logRecords": [{
				"timeUnixNano": "1795372276854000000",
				"body": {"stringValue": "Failed to drain Session"},
				"attributes": [
					{"key": "effect.fiberId", "value": {"intValue": "842"}},
					{"key": "exception.message", "value": {"stringValue": "Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go"}},
					{"key": "exception.type", "value": {"stringValue": "AI.Error"}},
					{"key": "role", "value": {"stringValue": "server"}},
					{"key": "sessionID", "value": {"stringValue": "ses_dfb792527ffeQUniA6w70OGP4U"}}
				]
			}]}]
		}]
	}`)
	recs, err := Decode("logs", body, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	rec := recs[0]
	if rec.SessionID == nil || *rec.SessionID != openCodeLiveSession {
		t.Fatalf("session %v", rec.SessionID)
	}
	if rec.ServiceName == nil || *rec.ServiceName != "opencode" {
		t.Fatalf("service %v", rec.ServiceName)
	}
	if rec.Name == nil || *rec.Name != "Failed to drain Session" {
		t.Fatalf("name %v", rec.Name)
	}
	if rec.Attributes["sessionID"] != openCodeLiveSession {
		t.Fatalf("attributes %v", rec.Attributes)
	}
	if _, ok := rec.Attributes["session.id"]; ok {
		t.Fatalf("unexpected session.id in attributes: %v", rec.Attributes)
	}
}

func TestSessionIDAliasIsReadFromRecordScopeAndResource(t *testing.T) {
	const id = "ses_dfb792527ffeQUniA6w70OGP4U"
	cases := []struct {
		name   string
		signal string
		body   string
	}{
		{
			name:   "log record attributes",
			signal: "logs",
			body:   openCodeLogJSON("", "", `{"key":"sessionID","value":{"stringValue":"`+id+`"}}`),
		},
		{
			name:   "log scope attributes",
			signal: "logs",
			body:   openCodeLogJSON("", `{"key":"sessionID","value":{"stringValue":"`+id+`"}}`, ""),
		},
		{
			name:   "log resource attributes",
			signal: "logs",
			body:   openCodeLogJSON(`{"key":"sessionID","value":{"stringValue":"`+id+`"}}`, "", ""),
		},
		{
			name:   "span attributes",
			signal: "traces",
			body:   openCodeSpanJSON("", "", `{"key":"sessionID","value":{"stringValue":"`+id+`"}}`),
		},
		{
			name:   "span scope attributes",
			signal: "traces",
			body:   openCodeSpanJSON("", `{"key":"sessionID","value":{"stringValue":"`+id+`"}}`, ""),
		},
		{
			name:   "span resource attributes",
			signal: "traces",
			body:   openCodeSpanJSON(`{"key":"sessionID","value":{"stringValue":"`+id+`"}}`, "", ""),
		},
		{
			name:   "metric point attributes",
			signal: "metrics",
			body:   openCodeMetricJSON("", "", `{"key":"sessionID","value":{"stringValue":"`+id+`"}}`),
		},
		{
			name:   "metric scope attributes",
			signal: "metrics",
			body:   openCodeMetricJSON("", `{"key":"sessionID","value":{"stringValue":"`+id+`"}}`, ""),
		},
		{
			name:   "metric resource attributes",
			signal: "metrics",
			body:   openCodeMetricJSON(`{"key":"sessionID","value":{"stringValue":"`+id+`"}}`, "", ""),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs, err := Decode(tc.signal, []byte(tc.body), "application/json")
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 1 {
				t.Fatalf("got %d records", len(recs))
			}
			if recs[0].SessionID == nil || *recs[0].SessionID != id {
				t.Fatalf("session %v", recs[0].SessionID)
			}
		})
	}
}

func TestSessionIDPrefersCanonicalKeyWhenAliasAgreesOrConflicts(t *testing.T) {
	const canonical = "ses_canonical0001"
	const alias = "ses_alias0001"
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "equal keys on the same log record keep session.id",
			body: openCodeLogJSON("", "", fmt.Sprintf(
				`{"key":"session.id","value":{"stringValue":"%s"}},{"key":"sessionID","value":{"stringValue":"%s"}}`,
				canonical, canonical,
			)),
			want: canonical,
		},
		{
			name: "conflicting keys on the same log record keep session.id",
			body: openCodeLogJSON("", "", fmt.Sprintf(
				`{"key":"session.id","value":{"stringValue":"%s"}},{"key":"sessionID","value":{"stringValue":"%s"}}`,
				canonical, alias,
			)),
			want: canonical,
		},
		{
			name: "alias on the log and session.id on the resource keep session.id",
			body: openCodeLogJSON(
				fmt.Sprintf(`{"key":"session.id","value":{"stringValue":"%s"}}`, canonical),
				"",
				fmt.Sprintf(`{"key":"sessionID","value":{"stringValue":"%s"}}`, alias),
			),
			want: canonical,
		},
		{
			name: "alias on the log and session.id on the scope keep session.id",
			body: openCodeLogJSON(
				"",
				fmt.Sprintf(`{"key":"session.id","value":{"stringValue":"%s"}}`, canonical),
				fmt.Sprintf(`{"key":"sessionID","value":{"stringValue":"%s"}}`, alias),
			),
			want: canonical,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs, err := Decode("logs", []byte(tc.body), "application/json")
			if err != nil {
				t.Fatal(err)
			}
			if recs[0].SessionID == nil || *recs[0].SessionID != tc.want {
				t.Fatalf("session %v want %s", recs[0].SessionID, tc.want)
			}
		})
	}
}

func TestEmptySessionIDAliasIsIgnored(t *testing.T) {
	body := openCodeLogJSON("", "", `{"key":"sessionID","value":{"stringValue":"   "}}`)
	recs, err := Decode("logs", []byte(body), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].SessionID != nil {
		t.Fatalf("expected no session, got %v", *recs[0].SessionID)
	}
}

// Non-string session keys go through record.OTLPText, the same helper
// canonical session.id uses: ints become decimal text, bools become
// Python-style "True" / "False", and empty strings stay absent.
func TestSessionIDNonStringValuesMatchSessionDotID(t *testing.T) {
	cases := []struct {
		name string
		attr string
		want string
	}{
		{
			name: "sessionID int is stringified",
			attr: `{"key":"sessionID","value":{"intValue":"842"}}`,
			want: "842",
		},
		{
			name: "session.id int is stringified",
			attr: `{"key":"session.id","value":{"intValue":"842"}}`,
			want: "842",
		},
		{
			name: "sessionID bool true becomes True",
			attr: `{"key":"sessionID","value":{"boolValue":true}}`,
			want: "True",
		},
		{
			name: "sessionID bool false becomes False",
			attr: `{"key":"sessionID","value":{"boolValue":false}}`,
			want: "False",
		},
		{
			name: "session.id bool true becomes True",
			attr: `{"key":"session.id","value":{"boolValue":true}}`,
			want: "True",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs, err := Decode("logs", []byte(openCodeLogJSON("", "", tc.attr)), "application/json")
			if err != nil {
				t.Fatal(err)
			}
			if recs[0].SessionID == nil || *recs[0].SessionID != tc.want {
				t.Fatalf("session %v want %s", recs[0].SessionID, tc.want)
			}
		})
	}
}

func openCodeResourceJSON(extra string) string {
	attrs := `{"key":"service.name","value":{"stringValue":"opencode"}},{"key":"sdf.fixture","value":{"stringValue":"sample"}}`
	if extra != "" {
		attrs += "," + extra
	}
	return `{"attributes":[` + attrs + `]}`
}

func openCodeLogJSON(resourceAttr, scopeAttr, recordAttr string) string {
	scope := `{}`
	if scopeAttr != "" {
		scope = `{"attributes":[` + scopeAttr + `]}`
	}
	attrs := "[]"
	if recordAttr != "" {
		attrs = `[` + recordAttr + `]`
	}
	return fmt.Sprintf(`{
		"resourceLogs": [{
			"resource": %s,
			"scopeLogs": [{
				"scope": %s,
				"logRecords": [{
					"timeUnixNano": "1",
					"body": {"stringValue": "Failed to drain Session"},
					"attributes": %s
				}]
			}]
		}]
	}`, openCodeResourceJSON(resourceAttr), scope, attrs)
}

func openCodeSpanJSON(resourceAttr, scopeAttr, spanAttr string) string {
	scope := `{}`
	if scopeAttr != "" {
		scope = `{"attributes":[` + scopeAttr + `]}`
	}
	attrs := "[]"
	if spanAttr != "" {
		attrs = `[` + spanAttr + `]`
	}
	return fmt.Sprintf(`{
		"resourceSpans": [{
			"resource": %s,
			"scopeSpans": [{
				"scope": %s,
				"spans": [{
					"name": "SessionRunner.drain",
					"traceId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					"spanId": "bbbbbbbbbbbbbbbb",
					"startTimeUnixNano": "1",
					"endTimeUnixNano": "2",
					"attributes": %s
				}]
			}]
		}]
	}`, openCodeResourceJSON(resourceAttr), scope, attrs)
}

func openCodeMetricJSON(resourceAttr, scopeAttr, pointAttr string) string {
	scope := `{}`
	if scopeAttr != "" {
		scope = `{"attributes":[` + scopeAttr + `]}`
	}
	attrs := "[]"
	if pointAttr != "" {
		attrs = `[` + pointAttr + `]`
	}
	return fmt.Sprintf(`{
		"resourceMetrics": [{
			"resource": %s,
			"scopeMetrics": [{
				"scope": %s,
				"metrics": [{
					"name": "opencode.test",
					"sum": {
						"aggregationTemporality": "AGGREGATION_TEMPORALITY_DELTA",
						"dataPoints": [{
							"timeUnixNano": "1",
							"asInt": "1",
							"attributes": %s
						}]
					}
				}]
			}]
		}]
	}`, openCodeResourceJSON(resourceAttr), scope, attrs)
}
