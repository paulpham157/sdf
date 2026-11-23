package setup

import (
	"strings"
	"testing"
)

// Every printed `export ..._ENDPOINT=` line is single-quoted, for every
// agent that prints one, so pasting it into a shell is safe even for a
// bracketed IPv6 host.
func TestPrintedEndpointExportsAreQuoted(t *testing.T) {
	t.Setenv("GROK_HOME", "")
	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "GEMINI_TELEMETRY_OTLP_ENDPOINT", "QWEN_TELEMETRY_OTLP_ENDPOINT"} {
		t.Setenv(key, "")
	}
	want := map[string]string{
		"claude-code": "export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT='http://[::1]:4318/v1/logs'\n",
		"grok":        "export OTEL_EXPORTER_OTLP_ENDPOINT='http://[::1]:4318'\n",
		"gemini":      "export GEMINI_TELEMETRY_OTLP_ENDPOINT='http://[::1]:4318'\n",
		"pi":          "export OTEL_EXPORTER_OTLP_ENDPOINT='http://[::1]:4318'\n",
		"qwen":        "export QWEN_TELEMETRY_OTLP_ENDPOINT='http://[::1]:4318'\n",
	}
	for agent, line := range want {
		home := t.TempDir()
		res, err := Run(Request{Agent: agent, Home: home, ProjectDir: home, Endpoint: Endpoint{Host: "::1"}})
		if err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		if !strings.Contains(res.Text, line) {
			t.Errorf("%s: missing %q in\n%s", agent, line, res.Text)
		}
		for _, l := range strings.Split(res.Text, "\n") {
			if strings.HasPrefix(l, "export ") && strings.Contains(l, "_ENDPOINT=http") {
				t.Errorf("%s: unquoted endpoint line %q", agent, l)
			}
		}
	}
	if got := quoteEndpointLine("GROK_EXTERNAL_OTEL=1"); got != "GROK_EXTERNAL_OTEL=1" {
		t.Errorf("constant line changed: %q", got)
	}
}
