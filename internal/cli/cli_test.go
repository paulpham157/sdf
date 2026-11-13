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
	"syscall"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/check"
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

func TestInitGlobalAndGitTemplateFlags(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	flags := run(t, "init", "--set-template-dir")
	if flags.code != 2 || !strings.Contains(flags.stderr, "--set-template-dir requires --git-template") {
		t.Fatalf("%d %s", flags.code, flags.stderr)
	}
	other := run(t, "init", "claude-code", "--global")
	if other.code != 2 || !strings.Contains(other.stderr, "--global applies to pi") {
		t.Fatalf("%d %s", other.code, other.stderr)
	}
	held := run(t, "init", "--git-template")
	if held.code != 1 || !strings.Contains(held.stdout, "--set-template-dir") || !strings.Contains(held.stdout, "did not change git config") {
		t.Fatalf("%d %s %s", held.code, held.stdout, held.stderr)
	}
	if strings.Contains(string(mustRead(t, cfg)), "templateDir") {
		t.Fatal(mustRead(t, cfg))
	}
	printed := run(t, "init", "pi", "--global")
	if printed.code != 0 || !strings.Contains(printed.stdout, filepath.Join(home, ".pi", "agent", "settings.json")) || !strings.Contains(printed.stdout, "not written") {
		t.Fatalf("%d %s %s", printed.code, printed.stdout, printed.stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".pi")); !os.IsNotExist(err) {
		t.Fatal("global pi settings were written")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
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

// pricedSessionUSD is the computed cost of the hand-written log in
// loadPricedSession. It is the same figure internal/check expects.
const pricedSessionUSD = "0.011988"

func TestSummaryBudgetComparesComputedCostWithTheCeiling(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "priced.sqlite")
	loadPricedSession(t, dbPath)
	budgetPath := writeBudget(t, `{"ceiling_usd":"1","window":"store"}`)

	out := run(t, "summary", "--db", dbPath, "--json", "--prices", "bundled", "--budget", budgetPath)
	if out.code != 0 {
		t.Fatalf("code %d\n%s%s", out.code, out.stdout, out.stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out.stdout), &report); err != nil {
		t.Fatal(err)
	}
	budget, _ := report["budget"].(map[string]any)
	if budget["status"] != "within" || budget["total_usd"] != pricedSessionUSD || budget["ceiling_usd"] != "1" || budget["window"] != "store" {
		t.Fatalf("%#v", budget)
	}
	sessions, _ := report["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions %d", len(sessions))
	}

	overPath := writeBudget(t, `{"ceiling_usd":"0.01","window":"store"}`)
	over := run(t, "summary", "--db", dbPath, "--json", "--prices", "bundled", "--budget", overPath)
	if over.code != 0 {
		t.Fatalf("code %d\n%s%s", over.code, over.stdout, over.stderr)
	}
	var overReport map[string]any
	if err := json.Unmarshal([]byte(over.stdout), &overReport); err != nil {
		t.Fatal(err)
	}
	overBudget, _ := overReport["budget"].(map[string]any)
	if overBudget["status"] != "over" || overBudget["total_usd"] != pricedSessionUSD || overBudget["ceiling_usd"] != "0.01" {
		t.Fatalf("%#v", overBudget)
	}

	text := run(t, "summary", "--db", dbPath, "--prices", "bundled", "--budget", budgetPath)
	if text.code != 0 || !strings.Contains(text.stdout, "budget: within (total_usd="+pricedSessionUSD+") ceiling_usd=1 window=store\n") {
		t.Fatalf("%d %s%s", text.code, text.stdout, text.stderr)
	}
}

func TestSummaryWithoutBudgetOmitsTheBudgetField(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "priced.sqlite")
	loadPricedSession(t, dbPath)

	plain := run(t, "summary", "--db", dbPath, "--json", "--prices", "bundled")
	if plain.code != 0 {
		t.Fatal(plain.stderr)
	}
	if bytes.Contains([]byte(plain.stdout), []byte(`"budget"`)) {
		t.Fatalf("budget field without a flag: %s", plain.stdout)
	}
	withFlag := run(t, "summary", "--db", dbPath, "--json", "--prices", "bundled", "--budget", writeBudget(t, `{"ceiling_usd":"1","window":"store"}`))
	if withFlag.code != 0 {
		t.Fatal(withFlag.stderr)
	}
	if plain.stdout == withFlag.stdout {
		t.Fatal("a budget file left the report unchanged")
	}
	var plainReport, flagged map[string]any
	if err := json.Unmarshal([]byte(plain.stdout), &plainReport); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(withFlag.stdout), &flagged); err != nil {
		t.Fatal(err)
	}
	delete(flagged, "budget")
	if !reflectDeepEqual(plainReport, flagged) {
		t.Fatalf("budget changed more than the budget field\nplain %s\nflagged %s", plain.stdout, withFlag.stdout)
	}
}

func TestSummaryBadBudgetFileExitsWithoutAReport(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "priced.sqlite")
	loadPricedSession(t, dbPath)
	cases := []string{
		`{"ceiling_usd":"-1","window":"store"}`,
		`{"ceiling_usd":"1","window":"week"}`,
		`{"ceiling_usd":"1","window":"day","day":"not-a-day"}`,
	}
	for _, body := range cases {
		out := run(t, "summary", "--db", dbPath, "--json", "--prices", "bundled", "--budget", writeBudget(t, body))
		if out.code == 0 {
			t.Fatalf("bad budget %s exited 0\n%s", body, out.stdout)
		}
		if strings.Contains(out.stdout, "session ") || strings.Contains(out.stdout, `"sessions"`) {
			t.Fatalf("bad budget %s produced a report: %s", body, out.stdout)
		}
		// The flag has to be recognized. An unrecognized --budget exits before
		// the file is read, which is not this failure.
		if strings.Contains(out.stderr, "unrecognized") || !strings.Contains(strings.ToLower(out.stderr), "budget") {
			t.Fatalf("bad budget %s stderr %q", body, out.stderr)
		}
	}
	missing := run(t, "summary", "--db", dbPath, "--json", "--prices", "bundled", "--budget", filepath.Join(t.TempDir(), "missing.json"))
	if missing.code == 0 || strings.Contains(missing.stderr, "unrecognized") || !strings.Contains(strings.ToLower(missing.stderr), "budget") {
		t.Fatalf("missing file code %d stderr %q", missing.code, missing.stderr)
	}
	if strings.Contains(missing.stdout, `"sessions"`) {
		t.Fatalf("missing budget file produced a report: %s", missing.stdout)
	}
}

func TestServeBudgetReachesTheSummaryAPI(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "priced.sqlite")
	loadPricedSession(t, dbPath)
	budgetPath := writeBudget(t, `{"ceiling_usd":"1","window":"store"}`)
	uiAddr := freeLoopback(t)

	// Leave stdout and stderr alone. "sdf serve" writes its listening lines and
	// then blocks on a signal, so a pipe nobody keeps reading fills and the
	// write never returns. The UI listener starts only after those writes.
	done := make(chan int, 1)
	go func() {
		done <- cli.Main([]string{"serve", "--db", dbPath, "--prices", "bundled", "--budget", budgetPath, "--ui-addr", uiAddr, "--port", "0", "--grpc-port", "0"})
	}()
	// An unrecognized --budget returns before the UI listens. Stop there,
	// instead of waiting out a server that was never going to start.
	select {
	case code := <-done:
		t.Fatalf("serve exited %d before GET /api/summary", code)
	case <-time.After(200 * time.Millisecond):
	}
	base := "http://" + uiAddr + "/"
	defer stopServe(t, done)
	waitForListener(t, base)
	body := httpGet(t, base+"api/summary")
	var report map[string]any
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatal(err)
	}
	budget, _ := report["budget"].(map[string]any)
	if budget["status"] != "within" || budget["total_usd"] != pricedSessionUSD || budget["ceiling_usd"] != "1" || budget["window"] != "store" {
		t.Fatalf("%#v\n%s", budget, body)
	}

}

func stopServe(t *testing.T, done <-chan int) {
	t.Helper()
	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Error(err)
		return
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		t.Error(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("serve exited %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Error("serve did not stop")
	}
}

func loadPricedSession(t *testing.T, dbPath string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(check.DefaultDir(), "priced-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OTLP struct {
			Signal string          `json:"signal"`
			Body   json.RawMessage `json:"body"`
		} `json:"otlp"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	records, err := otlp.Decode(doc.OTLP.Signal, doc.OTLP.Body, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Insert(records); err != nil {
		t.Fatal(err)
	}
}

func writeBudget(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budget.json")
	if err := os.WriteFile(path, []byte(body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func freeLoopback(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func waitForListener(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for {
		resp, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return
		}
		last = err
		if time.Now().After(deadline) {
			t.Fatal(last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCheckReportsAGoodCaseAndABadCase(t *testing.T) {
	dir := t.TempDir()
	committed := check.DefaultDir()
	copyCase(t, committed, dir, "priced-session.json")
	bad := filepath.Join(dir, "priced-wrong.json")
	body, err := os.ReadFile(filepath.Join(committed, "priced-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	rewritten := strings.Replace(string(body), `"expect": "0.011988"`, `"expect": "0"`, 1)
	rewritten = strings.Replace(rewritten, `"id": "priced-session"`, `"id": "priced-wrong"`, 1)
	if rewritten == string(body) {
		t.Fatal("case fixture did not contain the expected cost")
	}
	if err := os.WriteFile(bad, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}
	out := run(t, "check", "--cases", dir)
	if out.code != 1 {
		t.Fatalf("code %d\n%s%s", out.code, out.stdout, out.stderr)
	}
	if !strings.Contains(out.stdout, "priced-session pass\n") || !strings.Contains(out.stdout, "priced-wrong fail ") {
		t.Fatalf("%s", out.stdout)
	}
	if !strings.Contains(out.stdout, "1 passed, 1 failed, 0 not_checked\n") {
		t.Fatalf("%s", out.stdout)
	}
	unchecked := run(t, "check", "--cases", committed)
	if unchecked.code != 0 || !strings.Contains(unchecked.stdout, "unknown-price not_checked") || !strings.Contains(unchecked.stdout, "unlinked-outcome not_checked") {
		t.Fatalf("code %d\n%s%s", unchecked.code, unchecked.stdout, unchecked.stderr)
	}
	if strings.Contains(unchecked.stdout, " fail ") {
		t.Fatal(unchecked.stdout)
	}
}

func copyCase(t *testing.T, from, to, name string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(from, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(to, name), body, 0o644); err != nil {
		t.Fatal(err)
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
