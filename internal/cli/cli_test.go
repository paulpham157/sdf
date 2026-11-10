package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/cli"
	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/samplepath"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/ui"
)

func TestSummaryOfANewDatabase(t *testing.T) {
	out := run(t, "summary", "--db", filepath.Join(t.TempDir(), "empty.sqlite"))
	if out.code != 0 || out.stdout != "No telemetry sessions in the store.\n" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
}

func TestPricesRefreshWritesACatalogWithoutCallingOpenRouter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("authorization header")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"vendor/priced","pricing":{"prompt":"0.000001","completion":"0.000002"}}]}`))
	}))
	defer server.Close()
	outPath := filepath.Join(t.TempDir(), "prices.json")
	out := run(t, "prices", "refresh", "--url", server.URL, "--out", outPath)
	if out.code != 0 {
		t.Fatal(out.stderr)
	}
	if !strings.Contains(out.stdout, "wrote 1 models") || !strings.Contains(out.stdout, pricing.SourceURL) {
		t.Fatal(out.stdout)
	}
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "0.000001") || !strings.Contains(string(body), pricing.SourceURL) {
		t.Fatal(string(body))
	}
	missing := run(t, "summary", "--db", filepath.Join(t.TempDir(), "empty.sqlite"), "--prices", filepath.Join(t.TempDir(), "nope.json"))
	if missing.code != 1 || !strings.Contains(missing.stderr, "price catalog") {
		t.Fatalf("%d %s", missing.code, missing.stderr)
	}
}

func TestInitPrintsWithoutWriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	out := run(t, "init", "claude-code")
	if out.code != 0 || !strings.Contains(out.stdout, "CLAUDE_CODE_ENABLE_TELEMETRY=1") || !strings.Contains(out.stdout, "not written") {
		t.Fatalf("%d %s %s", out.code, out.stdout, out.stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatal("claude settings were written")
	}
}

func TestInitCheckReachesTheHTTPReceiver(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	out := run(t, "init", "--check", "--host", host, "--port", port, "--grpc-port", "1")
	if out.code != 0 || !strings.Contains(out.stdout, "reachable") || !strings.Contains(out.stdout, "not reachable") {
		t.Fatalf("%d %s %s", out.code, out.stdout, out.stderr)
	}
	closed := run(t, "init", "--check", "--host", "127.0.0.1", "--port", "1", "--grpc-port", "1")
	if closed.code != 1 || !strings.Contains(closed.stdout, "not reachable") {
		t.Fatalf("%d %s", closed.code, closed.stdout)
	}
}

func TestServeHelpMentionsTheLocalUI(t *testing.T) {
	out := run(t, "serve", "--help")
	if out.code != 0 || !strings.Contains(out.stdout, "--no-ui") || !strings.Contains(out.stdout, "127.0.0.1:4320") || !strings.Contains(out.stdout, "--host does not change it") {
		t.Fatalf("%d %s", out.code, out.stdout)
	}
	bad := run(t, "serve", "--ui-addr")
	if bad.code != 2 || !strings.Contains(bad.stderr, "--ui-addr") {
		t.Fatalf("%d %s", bad.code, bad.stderr)
	}
	worse := run(t, "serve", "--ui-addr", "not-an-address")
	if worse.code != 2 {
		t.Fatalf("%d %s", worse.code, worse.stderr)
	}
}

func TestUISummaryMatchesTheSummaryCommandOnSamples(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "samples.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	loadSamples(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	text := run(t, "summary", "--db", dbPath, "--json", "--prices", "bundled")
	if text.code != 0 {
		t.Fatal(text.stderr)
	}
	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv, err := ui.Start(ui.Config{Addr: "127.0.0.1:0", Store: db, Prices: "bundled"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	summaryBody := httpGet(t, srv.URL()+"api/summary")
	if summaryBody != text.stdout {
		t.Fatalf("api and sdf summary --json differ\napi %s\nsummary %s", summaryBody, text.stdout)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(text.stdout), &report); err != nil {
		t.Fatal(err)
	}
	var snap map[string]any
	if err := json.Unmarshal([]byte(httpGet(t, srv.URL()+"api/snapshot")), &snap); err != nil {
		t.Fatal(err)
	}
	rawSessions, _ := report["sessions"].([]any)
	rawViews, _ := snap["sessions"].([]any)
	if len(rawSessions) == 0 || len(rawViews) != len(rawSessions) {
		t.Fatalf("sessions report %d snapshot %d", len(rawSessions), len(rawViews))
	}
	byID := map[string]map[string]any{}
	for _, item := range rawSessions {
		session := item.(map[string]any)
		byID[session["session_id"].(string)] = session
	}
	for _, item := range rawViews {
		view := item.(map[string]any)
		summarySession := view["summary"].(map[string]any)
		id := summarySession["session_id"].(string)
		if !reflectDeepEqual(summarySession, byID[id]) {
			t.Fatalf("session %s\napi %#v\nsummary %#v", id, summarySession, byID[id])
		}
		assertCostField(t, id, summarySession["computed_cost_usd"], view["computed_cost_usd"])
		assertAgentField(t, id, summarySession["agent_reported_cost_usd"], view["agent_reported_cost_usd"])
	}
	if !reflectDeepEqual(report["cost_per_accepted_outcome"], snap["cost_per_accepted_outcome"]) {
		t.Fatalf("cost per accepted\napi %#v\nsummary %#v", snap["cost_per_accepted_outcome"], report["cost_per_accepted_outcome"])
	}
}

func assertCostField(t *testing.T, id string, summaryCost, viewCost any) {
	t.Helper()
	view := viewCost.(map[string]any)
	raw, _ := summaryCost.(map[string]any)
	if raw == nil || raw["status"] != "known" || raw["usd"] == nil || raw["usd"] == "" {
		if view["status"] != "unknown" || view["value"] != nil {
			t.Fatalf("%s computed view %#v summary %#v", id, view, raw)
		}
		return
	}
	if view["status"] != "known" || view["value"] != raw["usd"] {
		t.Fatalf("%s computed view %#v summary %#v", id, view, raw)
	}
}

func assertAgentField(t *testing.T, id string, summaryCost, viewCost any) {
	t.Helper()
	view := viewCost.(map[string]any)
	if summaryCost == nil {
		if view["status"] != "unknown" || view["value"] != nil {
			t.Fatalf("%s agent view %#v", id, view)
		}
		return
	}
	if view["status"] != "known" || view["value"] == nil || view["value"] == "" {
		t.Fatalf("%s agent view %#v summary %#v", id, view, summaryCost)
	}
}

func reflectDeepEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

func loadSamples(t *testing.T, db *store.Store) {
	t.Helper()
	dir := samplepath.Dir()
	files := [][2]string{
		{"metrics", "claude_metrics.json"},
		{"logs", "claude_logs.json"},
		{"traces", "claude_traces.json"},
		{"metrics", "codex_metrics.json"},
		{"logs", "codex_logs.json"},
		{"logs", "opencode_logs.json"},
		{"traces", "opencode_traces.json"},
		{"logs", "pi_logs.json"},
		{"metrics", "pi_metrics.json"},
		{"traces", "pi_traces.json"},
	}
	for _, item := range files {
		body, err := os.ReadFile(filepath.Join(dir, item[1]))
		if err != nil {
			t.Fatal(err)
		}
		records, err := otlp.Decode(item[0], body, "application/json")
		if err != nil {
			t.Fatalf("%s: %v", item[1], err)
		}
		if _, err := db.Insert(records); err != nil {
			t.Fatal(err)
		}
	}
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last error
	for {
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				t.Fatalf("%s %d %s", url, resp.StatusCode, body)
			}
			return string(body)
		}
		last = err
		if time.Now().After(deadline) {
			t.Fatal(last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUnknownCommand(t *testing.T) {
	out := run(t, "nope")
	if out.code != 2 || !strings.Contains(out.stderr, "unrecognized command") {
		t.Fatalf("%d %q", out.code, out.stderr)
	}
}

type result struct {
	code           int
	stdout, stderr string
}

func run(t *testing.T, args ...string) result {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	code := cli.Main(args)
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	var stdout, stderr bytes.Buffer
	_, _ = io.Copy(&stdout, outR)
	_, _ = io.Copy(&stderr, errR)
	return result{code, stdout.String(), stderr.String()}
}
