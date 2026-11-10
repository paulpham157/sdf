//go:build e2e

// Package e2e runs a CGO_ENABLED=0 sdf binary against tests/samples.
// `go test ./...` does not build this file. Main CI runs `make e2e`.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/paulpham157/sdf/internal/samplepath"
)

func TestBinaryIsStatic(t *testing.T) {
	bin := binPath(t)
	info, err := exec.Command("go", "version", "-m", bin).CombinedOutput()
	if err != nil {
		t.Fatalf("go version -m: %v\n%s", err, info)
	}
	if !bytes.Contains(info, []byte("\tbuild\tCGO_ENABLED=0\n")) && !bytes.Contains(info, []byte("CGO_ENABLED=0")) {
		t.Fatalf("SDF_BIN was not built with CGO_ENABLED=0:\n%s", info)
	}
	out, err := exec.Command("ldd", bin).CombinedOutput()
	text := string(out)
	if !strings.Contains(text, "not a dynamic executable") && !strings.Contains(text, "statically linked") {
		t.Fatalf("ldd (%v) did not report a static binary:\n%s", err, text)
	}
}

func TestSamplesOverHTTPAndGRPC(t *testing.T) {
	files := sampleFiles(t)
	httpDB, httpText := ingestAndSummarize(t, files, "http")
	_, grpcText := ingestAndSummarize(t, files, "grpc")
	if httpText != grpcText {
		t.Fatalf("HTTP and gRPC summaries differ\n--- http ---\n%s\n--- grpc ---\n%s", httpText, grpcText)
	}
	for _, want := range []string{
		"session sample-claude-1\n",
		"  tool: claude-code\n",
		"  duration_ms: 2000 (traces claude_code.interaction)\n",
		"input=100",
		"output=40",
		"session sample-codex-conv-1\n",
		"  tool: codex\n",
		"  duration_ms: 2500 (metrics codex.turn.e2e_duration_ms)\n",
		"input=200",
		"output=30",
		"session ses_sampleopencode0001\n",
		"  tool: opencode\n",
		"input_tokens=5822",
		"output_tokens=306",
		"session 2026-01-01T00-00-00-000Z_sample-pi-session\n",
		"  tool: pi\n",
		"input_tokens=1245",
		"output_tokens=832",
	} {
		if !strings.Contains(httpText, want) {
			t.Fatalf("summary missing %q\n%s", want, httpText)
		}
	}

	filtered := summarize(t, httpDB, "--session", "sample-claude-1")
	if !strings.Contains(filtered, "session sample-claude-1\n") || strings.Contains(filtered, "session sample-codex-conv-1\n") {
		t.Fatalf("session filter:\n%s", filtered)
	}
	js := summarize(t, httpDB, "--json")
	for _, id := range []string{
		`"session_id": "sample-claude-1"`,
		`"session_id": "sample-codex-conv-1"`,
		`"session_id": "ses_sampleopencode0001"`,
		`"session_id": "2026-01-01T00-00-00-000Z_sample-pi-session"`,
	} {
		if !strings.Contains(js, id) {
			t.Fatalf("json summary missing %s\n%s", id, js)
		}
	}
}

func ingestAndSummarize(t *testing.T, files []string, protocol string) (string, string) {
	t.Helper()
	db := ingest(t, files, protocol)
	return db, summarize(t, db)
}

func ingest(t *testing.T, files []string, protocol string) string {
	t.Helper()
	srv := startServe(t)
	switch protocol {
	case "http":
		postSamples(t, srv.httpBase, files)
	case "grpc":
		exportSamples(t, srv.grpcAddr, files)
	default:
		t.Fatalf("protocol %s", protocol)
	}
	db := srv.db
	srv.stop(t)
	return db
}

func summarize(t *testing.T, db string, extra ...string) string {
	t.Helper()
	args := append([]string{"summary", "--db", db, "--prices", "bundled"}, extra...)
	cmd := exec.Command(binPath(t), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sdf summary %v: %v\n%s", extra, err, out)
	}
	if len(out) == 0 {
		t.Fatal("empty summary")
	}
	return string(out)
}

type serveProc struct {
	cmd      *exec.Cmd
	db       string
	httpBase string
	grpcAddr string
	stderr   *bytes.Buffer
	done     chan struct{}
	waitErr  error
	stopped  atomic.Bool
}

func startServe(t *testing.T) *serveProc {
	t.Helper()
	db := filepath.Join(t.TempDir(), "telemetry.sqlite")
	cmd := exec.Command(binPath(t), "serve", "--host", "127.0.0.1", "--port", "0", "--grpc-port", "0", "--db", db)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	srv := &serveProc{cmd: cmd, db: db, stderr: &stderr, done: make(chan struct{})}
	go func() {
		srv.waitErr = cmd.Wait()
		close(srv.done)
	}()
	t.Cleanup(func() { srv.shutdown() })

	var mu sync.Mutex
	var text strings.Builder
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			mu.Lock()
			text.WriteString(sc.Text())
			text.WriteByte('\n')
			mu.Unlock()
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-srv.done:
			t.Fatalf("sdf serve exited before listening: %v\n%s", srv.waitErr, stderr.String())
		default:
		}
		mu.Lock()
		snap := text.String()
		mu.Unlock()
		httpBase, grpcAddr, ok := parseListen(snap)
		if ok {
			srv.httpBase = httpBase
			srv.grpcAddr = grpcAddr
			waitHealth(t, httpBase)
			return srv
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	snap := text.String()
	mu.Unlock()
	srv.shutdown()
	t.Fatalf("sdf serve did not listen\nstdout:\n%s\nstderr:\n%s", snap, stderr.String())
	return nil
}

func (s *serveProc) stop(t *testing.T) {
	t.Helper()
	s.shutdown()
	if s.waitErr != nil {
		t.Fatalf("sdf serve: %v\n%s", s.waitErr, s.stderr.String())
	}
}

func (s *serveProc) shutdown() {
	if s == nil || s.cmd.Process == nil || s.stopped.Swap(true) {
		return
	}
	_ = s.cmd.Process.Signal(os.Interrupt)
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
}

func parseListen(text string) (httpBase, grpcAddr string, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if rest, found := strings.CutPrefix(line, "OTLP/HTTP listening on "); found {
			httpBase, _, _ = strings.Cut(rest, " ")
		}
		if rest, found := strings.CutPrefix(line, "OTLP/gRPC listening on "); found {
			grpcAddr = strings.TrimSpace(rest)
		}
	}
	return httpBase, grpcAddr, httpBase != "" && grpcAddr != ""
}

func waitHealth(t *testing.T, base string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/health")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && strings.TrimSpace(string(body)) == `{"status":"ok"}` {
				return
			}
			last = errStatus(resp.StatusCode, body)
		} else {
			last = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("health: %v", last)
}

type statusErr struct {
	code int
	body string
}

func (e statusErr) Error() string {
	return fmt.Sprintf("status %d: %s", e.code, e.body)
}

func errStatus(code int, body []byte) error {
	return statusErr{code: code, body: string(body)}
}

func postSamples(t *testing.T, base string, files []string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	for _, name := range files {
		signal, path := signalOf(t, name)
		body := readSample(t, path)
		req, err := http.NewRequest(http.MethodPost, base+"/v1/"+signal, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", name, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(respBody) != "{}" {
			t.Fatalf("POST %s: %d %s", name, resp.StatusCode, respBody)
		}
	}
}

func exportSamples(t *testing.T, addr string, files []string) {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	unmarshal := protojson.UnmarshalOptions{DiscardUnknown: true}
	for _, name := range files {
		signal, path := signalOf(t, name)
		body := readSample(t, path)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := exportOne(ctx, conn, unmarshal, signal, body)
		cancel()
		if err != nil {
			t.Fatalf("gRPC %s: %v", name, err)
		}
	}
}

func exportOne(ctx context.Context, conn *grpc.ClientConn, unmarshal protojson.UnmarshalOptions, signal string, body []byte) error {
	var msg proto.Message
	switch signal {
	case "traces":
		msg = &coltracepb.ExportTraceServiceRequest{}
	case "metrics":
		msg = &colmetricspb.ExportMetricsServiceRequest{}
	default:
		msg = &collogspb.ExportLogsServiceRequest{}
	}
	if err := unmarshal.Unmarshal(body, msg); err != nil {
		return err
	}
	var err error
	switch req := msg.(type) {
	case *coltracepb.ExportTraceServiceRequest:
		_, err = coltracepb.NewTraceServiceClient(conn).Export(ctx, req)
	case *colmetricspb.ExportMetricsServiceRequest:
		_, err = colmetricspb.NewMetricsServiceClient(conn).Export(ctx, req)
	case *collogspb.ExportLogsServiceRequest:
		_, err = collogspb.NewLogsServiceClient(conn).Export(ctx, req)
	}
	return err
}

func sampleFiles(t *testing.T) []string {
	t.Helper()
	dir := samplepath.Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if _, ok := signalName(name); !ok {
			t.Fatalf("sample %s is not *_traces.json, *_metrics.json, or *_logs.json", name)
		}
		names = append(names, name)
	}
	required := []string{
		"claude_logs.json", "claude_metrics.json", "claude_traces.json",
		"codex_logs.json", "codex_metrics.json",
		"opencode_logs.json", "opencode_traces.json",
		"pi_logs.json", "pi_metrics.json", "pi_traces.json",
	}
	have := map[string]bool{}
	for _, name := range names {
		have[name] = true
	}
	for _, name := range required {
		if !have[name] {
			t.Fatalf("missing sample %s", name)
		}
	}
	return names
}

func signalOf(t *testing.T, name string) (string, string) {
	t.Helper()
	signal, ok := signalName(name)
	if !ok {
		t.Fatalf("sample %s", name)
	}
	return signal, filepath.Join(samplepath.Dir(), name)
}

func signalName(name string) (string, bool) {
	switch {
	case strings.HasSuffix(name, "_traces.json"):
		return "traces", true
	case strings.HasSuffix(name, "_metrics.json"):
		return "metrics", true
	case strings.HasSuffix(name, "_logs.json"):
		return "logs", true
	default:
		return "", false
	}
}

func readSample(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func binPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("SDF_BIN")
	if path == "" {
		t.Fatal("SDF_BIN is not set; build with CGO_ENABLED=0 and point SDF_BIN at that binary")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("SDF_BIN is not a file: %s", abs)
	}
	return abs
}
