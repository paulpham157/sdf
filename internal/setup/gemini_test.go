package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func geminiRequest(home string, write bool) Request {
	return Request{
		Agent: "gemini", Home: home, ProjectDir: home, Write: write,
		Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318, GRPCPort: 4317},
	}
}

func readGemini(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err, string(body))
	}
	return root
}

func TestGeminiPrintDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	res, err := Run(geminiRequest(home, false))
	if err != nil || res.Wrote {
		t.Fatal(err, res.Wrote)
	}
	for _, want := range []string{
		"export GEMINI_TELEMETRY_ENABLED=true",
		"export GEMINI_TELEMETRY_TARGET=local",
		"export GEMINI_TELEMETRY_OTLP_ENDPOINT=http://127.0.0.1:4318",
		"export GEMINI_TELEMETRY_OTLP_PROTOCOL=http",
		"export GEMINI_TELEMETRY_LOG_PROMPTS=false",
		"not written",
		filepath.Join(home, ".gemini", "settings.json"),
	} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("missing %q\n%s", want, res.Text)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini")); !os.IsNotExist(err) {
		t.Fatal("print wrote ~/.gemini")
	}
}

func TestGeminiWriteMergesTelemetryAndBacksUp(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"theme":"GitHub","model":{"name":"gemini-2.5-pro"},"telemetry":{"traces":true}}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(geminiRequest(home, true))
	if err != nil || !res.Wrote || !strings.Contains(res.Text, "backup ") {
		t.Fatal(err, res.Text)
	}
	root := readGemini(t, path)
	if root["theme"] != "GitHub" {
		t.Fatal(root)
	}
	if model, _ := root["model"].(map[string]any); model["name"] != "gemini-2.5-pro" {
		t.Fatal(root)
	}
	tel := root["telemetry"].(map[string]any)
	want := map[string]any{
		"enabled":      true,
		"target":       "local",
		"otlpEndpoint": "http://127.0.0.1:4318",
		"otlpProtocol": "http",
		"logPrompts":   false,
		"traces":       true,
	}
	for k, v := range want {
		if tel[k] != v {
			t.Fatalf("telemetry.%s = %#v, want %#v (%#v)", k, tel[k], v, tel)
		}
	}
	backup, err := os.ReadFile(path + ".sdf-bak")
	if err != nil || string(backup) != string(original) {
		t.Fatalf("backup %s %v", backup, err)
	}
	again, err := Run(geminiRequest(home, true))
	if err != nil || again.Wrote || !strings.Contains(again.Text, "already has these values") {
		t.Fatal(err, again.Text)
	}
	text, err := Configured(geminiRequest(home, false))
	if err != nil || !strings.Contains(text, "telemetry enabled pointing at http://127.0.0.1:4318: true") {
		t.Fatal(err, text)
	}
}

func TestGeminiWriteKeepsAnExplicitLogPromptsChoice(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"telemetry":{"logPrompts":true,"outfile":".gemini/telemetry.log"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(geminiRequest(home, true))
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	tel := readGemini(t, path)["telemetry"].(map[string]any)
	if tel["logPrompts"] != true {
		t.Fatalf("logPrompts was overwritten: %#v", tel)
	}
	if tel["outfile"] != ".gemini/telemetry.log" {
		t.Fatalf("outfile was removed: %#v", tel)
	}
	if !strings.Contains(res.Text, "warning:") || !strings.Contains(res.Text, "telemetry.outfile") {
		t.Fatal(res.Text)
	}
}

func TestGeminiWriteLeavesInvalidJSONAlone(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	broken := []byte("{ // comments are not JSON\n")
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(geminiRequest(home, true)); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != string(broken) {
		t.Fatal(string(body))
	}
	if _, err := os.Stat(path + ".sdf-bak"); !os.IsNotExist(err) {
		t.Fatal("backup written for a file that was not changed")
	}
}

func TestGeminiTelemetryThatIsNotAnObjectIsRefused(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"telemetry":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(geminiRequest(home, true)); err == nil || !strings.Contains(err.Error(), "telemetry is not an object") {
		t.Fatal(err)
	}
}

func TestGeminiCLIAliasRuns(t *testing.T) {
	req := geminiRequest(t.TempDir(), false)
	req.Agent = "gemini-cli"
	res, err := Run(req)
	if err != nil || !strings.Contains(res.Text, "GEMINI_TELEMETRY_ENABLED") {
		t.Fatal(err, res.Text)
	}
}
