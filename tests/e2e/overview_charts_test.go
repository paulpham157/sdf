//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

// TestOverviewChartsEndpointServesRecallAndGaps starts a real sdf serve,
// hits GET /api/overview-charts on the UI port, then records a hook commit
// and a decision and checks the same unlinked counts sdf decision audit
// reports, plus a zero recall until an implements edge exists.
func TestOverviewChartsEndpointServesRecallAndGaps(t *testing.T) {
	const (
		session = "grok-charts-e2e-61a2"
		sha     = "3f2a9c1e5b7d4a6c8e0f1a2b3c4d5e6f7a8b9c0d"
	)
	srv := startServeArgs(t, "--ui-addr", "127.0.0.1:0")
	if srv.uiURL == "" {
		t.Fatal("sdf serve did not print a UI listen address")
	}
	doc := getOverviewCharts(t, srv.uiURL)
	if doc["schema"] != "sdf.overview.charts/v1" {
		t.Fatalf("schema %v", doc["schema"])
	}
	totals := asMap(t, doc["totals"])
	if totals["commits_total"] != 0.0 || totals["unlinked_commits"] != 0.0 || totals["recall_status"] != "unknown" {
		t.Fatalf("empty totals %v", totals)
	}

	at := time.Date(2026, 11, 20, 14, 30, 0, 0, time.UTC)
	payload := []byte(fmt.Sprintf(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"grok-cli"}},{"key":"sdf.git.hook","value":{"stringValue":"1"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"%d","body":{"stringValue":"sdf.git.commit"},"attributes":[{"key":"event.name","value":{"stringValue":"sdf.git.commit"}},{"key":"session.id","value":{"stringValue":%q}},{"key":"sdf.git.agent","value":{"stringValue":"grok-cli"}},{"key":"vcs.ref.head.revision","value":{"stringValue":%q}},{"key":"sdf.git.repo","value":{"stringValue":"/tmp/repo"}},{"key":"sdf.git.branch","value":{"stringValue":"main"}},{"key":"sdf.git.hook","value":{"stringValue":"1"}}]}]}]}]}`, at.UnixNano(), session, sha))
	postJSON(t, srv.httpBase+"/v1/logs", payload)

	runDB(t, srv.db, "decision", "add", "--id", "store-columns", "--status", "decision",
		"--statement", "Store hot attributes in their own columns.")

	doc = getOverviewCharts(t, srv.uiURL)
	totals = asMap(t, doc["totals"])
	if totals["commits_total"] != 1.0 || totals["commits_with_implements"] != 0.0 || totals["commits_linked"] != 0.0 {
		t.Fatalf("after hook: totals %v", totals)
	}
	if totals["unlinked_commits"] != 1.0 || totals["unlinked_decisions"] != 1.0 {
		t.Fatalf("after hook: unlinked %v", totals)
	}
	if totals["recall_status"] != "known" || totals["recall"] != "0" {
		t.Fatalf("after hook: recall %v", totals)
	}

	runDB(t, srv.db, "decision", "link", "--relation", "implements",
		"--from", "session:"+session, "--to", "decision:store-columns")
	runDB(t, srv.db, "decision", "link", "--relation", "implements",
		"--from", "commit:"+sha, "--to", "decision:store-columns")

	doc = getOverviewCharts(t, srv.uiURL)
	totals = asMap(t, doc["totals"])
	if totals["commits_with_implements"] != 1.0 || totals["commits_linked"] != 1.0 || totals["unlinked_commits"] != 0.0 || totals["unlinked_decisions"] != 0.0 {
		t.Fatalf("after link: totals %v", totals)
	}
	if totals["recall"] != "1" {
		t.Fatalf("after link: recall %v", totals)
	}
	days, _ := doc["recall_by_day"].([]any)
	if len(days) != 1 || asMap(t, days[0])["day"] != "2026-11-20" || asMap(t, days[0])["cumulative_recall"] != "1" {
		t.Fatalf("recall_by_day %v", doc["recall_by_day"])
	}
}

func getOverviewCharts(t *testing.T, uiURL string) map[string]any {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(uiURL + "api/overview-charts")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/overview-charts: %d %s", resp.StatusCode, body)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
	return doc
}

func postJSON(t *testing.T, url string, payload []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "{}" {
		t.Fatalf("POST %s: %d %s", url, resp.StatusCode, body)
	}
}

func runDB(t *testing.T, db string, args ...string) {
	t.Helper()
	cmd := exec.Command(binPath(t), append(args, "--db", db)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("want object, got %T %v", v, v)
	}
	return m
}
