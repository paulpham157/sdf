package setup

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPrintDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	res, err := Run(Request{Agent: "claude-code", Home: home, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Wrote {
		t.Fatal("wrote")
	}
	if !strings.Contains(res.Text, "CLAUDE_CODE_ENABLE_TELEMETRY=1") || !strings.Contains(res.Text, "monitoring-usage") {
		t.Fatal(res.Text)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("settings appeared")
	}
}

func TestClaudeWriteMergesAndBacksUp(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\"theme\":\"dark\",\"env\":{\"OTHER\":\"keep\"}}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(Request{Agent: "claude-code", Home: home, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	if !strings.Contains(res.Text, "backup ") {
		t.Fatal(res.Text)
	}
	backup, err := os.ReadFile(path + ".sdf-bak")
	if err != nil || string(backup) != string(original) {
		t.Fatalf("backup %s %v", backup, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"OTHER": "keep"`) || !strings.Contains(text, `"theme": "dark"`) || !strings.Contains(text, `http://127.0.0.1:4318/v1/traces`) {
		t.Fatal(text)
	}
	again, err := Run(Request{Agent: "claude-code", Home: home, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || again.Wrote || !strings.Contains(again.Text, "already has these values") {
		t.Fatal(err, again.Text)
	}
	if _, err := os.Stat(path + ".sdf-bak." + "extra"); err == nil {
		t.Fatal("second backup")
	}
}

func TestInvalidJSONIsLeftAlone(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{not json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(Request{Agent: "claude-code", Home: home, Write: true})
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatal(err)
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil || string(body) != string(original) {
		t.Fatalf("%s %v", body, readErr)
	}
	if _, statErr := os.Stat(path + ".sdf-bak"); !os.IsNotExist(statErr) {
		t.Fatal("backup of refused file")
	}
}

func TestCodexWriteRefusesAnExistingTable(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	path := filepath.Join(codexHome, "config.toml")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("model = \"keep\"\n\n[otel]\nexporter = \"none\"\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(Request{Agent: "codex", Home: home, CodexHome: codexHome, Write: true})
	if err != nil || res.Wrote || !strings.Contains(res.Text, "already has an [otel] table") {
		t.Fatal(err, res.Text)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != string(original) {
		t.Fatalf("%s", body)
	}
	fresh := filepath.Join(home, "fresh")
	created, err := Run(Request{Agent: "codex", Home: home, CodexHome: fresh, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !created.Wrote {
		t.Fatal(err, created.Text)
	}
	toml, err := os.ReadFile(filepath.Join(fresh, "config.toml"))
	if err != nil || !strings.Contains(string(toml), "http://127.0.0.1:4318/v1/logs") || !strings.Contains(string(toml), "config-reference") {
		t.Fatal(string(toml))
	}
}

func TestOpenCodeWriteIsASnippet(t *testing.T) {
	home := t.TempDir()
	res, err := Run(Request{Agent: "opencode", Home: home, Write: true, Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318}})
	if err != nil || !res.Wrote {
		t.Fatal(err, res.Text)
	}
	if !strings.Contains(res.Text, "does not load that file") || !strings.Contains(res.Text, "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318") {
		t.Fatal(res.Text)
	}
	body, err := os.ReadFile(filepath.Join(home, ".config", "sdf", "opencode.env"))
	if err != nil || !strings.Contains(string(body), "export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318\n") {
		t.Fatal(string(body), err)
	}
}

func TestPiWriteMergesProjectSettingsAndInstallIsOptIn(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	path := filepath.Join(project, ".pi", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"theme\":\"dark\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ran := false
	res, err := Run(Request{
		Agent: "pi", Home: home, ProjectDir: project, Write: true,
		Endpoint: Endpoint{Host: "127.0.0.1", HTTPPort: 4318, GRPCPort: 4317},
		RunInstall: func(context.Context) (string, error) {
			ran = true
			return "", nil
		},
	})
	if err != nil || !res.Wrote || ran {
		t.Fatal(err, res.Wrote, ran, res.Text)
	}
	if !strings.Contains(res.Text, "pi install npm:pi-otel") || !strings.Contains(res.Text, "http/json") || !strings.Contains(res.Text, "pass --install") {
		t.Fatal(res.Text)
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), `"theme": "dark"`) || !strings.Contains(string(body), `"protocol": "http/json"`) || !strings.Contains(string(body), `"spanNaming": "genai"`) {
		t.Fatal(string(body))
	}
	if _, err := os.Stat(path + ".sdf-bak"); err != nil {
		t.Fatal(err)
	}
	res, err = Run(Request{
		Agent: "pi", Home: home, ProjectDir: project, Write: true, Install: true,
		RunInstall: func(context.Context) (string, error) {
			ran = true
			return "installed\n", nil
		},
	})
	if err != nil || !ran || !strings.Contains(res.Text, "installed") {
		t.Fatal(err, res.Text)
	}
}

func TestProbeReportsHTTPAndLeavesGRPCClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	got := Probe(Endpoint{Host: host, HTTPPort: port, GRPCPort: 1})
	if !got.Reachable || !strings.Contains(got.HTTPStatus, "reachable") || !strings.Contains(got.Text, `"status":"ok"`) {
		t.Fatal(got)
	}
	if !strings.Contains(got.GRPCStatus, "not reachable") {
		t.Fatal(got.GRPCStatus)
	}
}

func TestInstallFlagIsPiOnly(t *testing.T) {
	_, err := Run(Request{Agent: "codex", Install: true, Home: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "--install") {
		t.Fatal(err)
	}
}
