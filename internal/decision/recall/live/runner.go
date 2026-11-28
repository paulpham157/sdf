package live

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decision/recall"
	"github.com/paulpham157/sdf/internal/store"
)

// Run measures the selected agents. An empty key returns a skipped report
// and does not listen or spawn a CLI. k must be from 1 to 100 once defaults
// are applied. Per-agent failures stay in the report; they do not abort the
// other agents.
func Run(ctx context.Context, opt Options) (Report, error) {
	opt = opt.withDefaults()
	rep := Report{
		Schema:        schemaLive,
		Model:         opt.Model,
		K:             opt.K,
		OpenAIBase:    opt.OpenAIBase,
		AnthropicBase: opt.AnthropicBase,
	}
	if opt.Key == "" {
		rep.Skipped = true
		return rep, nil
	}
	if opt.K < 1 || opt.K > 100 {
		return Report{}, errors.New("k must be from 1 to 100")
	}
	agents, err := selectAgents(opt.Agents)
	if err != nil {
		return Report{}, err
	}
	fixtures, err := recall.Fixtures()
	if err != nil {
		return Report{}, err
	}
	sortFixtures(fixtures)
	if len(opt.FixtureNames) > 0 {
		fixtures, err = filterFixtures(fixtures, opt.FixtureNames)
		if err != nil {
			return Report{}, err
		}
	}

	needRun := false
	for _, a := range agents {
		if a.Support().OK {
			needRun = true
			break
		}
	}
	var sdfBin, skill string
	var px *proxy
	if needRun {
		var owned bool
		sdfBin, owned, err = resolveSDF(ctx, opt)
		if err != nil {
			return Report{}, err
		}
		if owned {
			defer os.RemoveAll(filepath.Dir(sdfBin))
		}
		skill, err = findSkill(opt.SkillDir)
		if err != nil {
			return Report{}, err
		}
		px = newProxy(opt)
		var stop func()
		opt.proxyOpenAI, opt.proxyAnthropic, stop, err = px.start()
		if err != nil {
			return Report{}, err
		}
		defer stop()
	}

	var grand counts
	measured := 0
	for _, a := range agents {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		ar := runAgent(ctx, opt, a, fixtures, sdfBin, skill, px)
		rep.Agents = append(rep.Agents, ar)
		if ar.Status == statusMeasured && ar.Overall != nil {
			measured++
			grand.merge(countsFrom(ar))
		}
	}
	if measured > 0 {
		pair := grand.pair()
		rep.Overall = &pair
	}
	return rep, nil
}

func runAgent(ctx context.Context, opt Options, a agent, fixtures []recall.Fixture, sdfBin, skill string, px *proxy) AgentReport {
	sup := a.Support()
	ar := AgentReport{ID: a.ID(), Name: a.Name(), Protocol: string(sup.Protocol)}
	if !sup.OK {
		ar.Status = statusUnsupported
		ar.Reason = sup.Reason
		return ar
	}
	bin, err := opt.look(a.Binary())
	if err != nil && opt.Install && len(a.InstallCommands()) > 0 {
		_ = installCLI(ctx, a, opt.Key)
		bin, err = opt.look(a.Binary())
	}
	if err != nil {
		ar.Status = statusUnavailable
		ar.Reason = a.Binary() + " is not on PATH"
		if hint := a.InstallHint(); hint != "" {
			ar.Reason += ". " + hint
		}
		return ar
	}
	ar.Status = statusMeasured
	var agentCounts counts
	for _, fx := range fixtures {
		var sc counts
		var errs []string
		for range opt.K {
			if ctx.Err() != nil {
				sc.fail()
				if len(errs) < 8 {
					errs = append(errs, "canceled")
				}
				continue
			}
			result, runErr := runOnce(ctx, opt, a, bin, fx, sdfBin, skill, px)
			if runErr != nil {
				sc.fail()
				if len(errs) < 8 {
					errs = append(errs, truncate(redact(runErr.Error(), opt.Key), 400))
				}
				continue
			}
			sc.add(result.scored, result.capped)
		}
		ar.Scenarios = append(ar.Scenarios, sc.scenario(fx.Name, errs))
		agentCounts.merge(sc)
	}
	pair := agentCounts.pair()
	ar.Overall = &pair
	return ar
}

type onceResult struct {
	scored recall.ScenarioResult
	capped bool
}

func runOnce(ctx context.Context, opt Options, a agent, bin string, fx recall.Fixture, sdfBin, skill string, px *proxy) (onceResult, error) {
	if ctx.Err() != nil {
		return onceResult{}, ctx.Err()
	}
	root, err := os.MkdirTemp("", "sdf-recall-live-")
	if err != nil {
		return onceResult{}, err
	}
	defer os.RemoveAll(root)
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		return onceResult{}, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return onceResult{}, err
	}
	if err := gitInit(ctx, repo, opt.Key); err != nil {
		return onceResult{}, err
	}
	db := filepath.Join(repo, "sdf-telemetry.sqlite")
	if err := ingest(db, fx); err != nil {
		return onceResult{}, err
	}
	prompt, err := transcript(fx)
	if err != nil {
		return onceResult{}, err
	}
	otlpPort, err := freePort()
	if err != nil {
		return onceResult{}, err
	}
	grpcPort, err := freePort()
	if err != nil {
		return onceResult{}, err
	}
	cmdServe := exec.Command(sdfBin, "serve",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(otlpPort),
		"--grpc-port", strconv.Itoa(grpcPort),
		"--db", db,
		"--no-ui",
	)
	cmdServe.Dir = repo
	cmdServe.Env = baseEnv(home, filepath.Dir(sdfBin), opt.Key)
	cmdServe.Stdout = io.Discard
	var serveErr bytes.Buffer
	cmdServe.Stderr = &limitWriter{buf: &serveErr, n: 2048}
	if err := cmdServe.Start(); err != nil {
		return onceResult{}, err
	}
	var stopOnce sync.Once
	stopServe := func() { stopOnce.Do(func() { stopProcess(cmdServe) }) }
	defer stopServe()
	if err := waitPort(ctx, otlpPort, 8*time.Second); err != nil {
		msg := truncate(redact(serveErr.String(), opt.Key), 300)
		if msg == "" {
			msg = err.Error()
		}
		return onceResult{}, fmt.Errorf("sdf serve: %s", msg)
	}
	if px != nil {
		px.reset()
	}
	sp := spec{
		Repo:          repo,
		Home:          home,
		OpenAIBase:    opt.proxyOpenAI,
		AnthropicBase: opt.proxyAnthropic,
		Model:         opt.Model,
		OTLP:          "http://127.0.0.1:" + strconv.Itoa(otlpPort),
		SkillSrc:      skill,
		Prompt:        prompt,
	}
	inv, err := a.Prepare(ctx, sp)
	if err != nil {
		return onceResult{}, err
	}
	if len(inv.Argv) < 2 {
		return onceResult{}, fmt.Errorf("%s: empty command", a.ID())
	}
	runCtx, runCancel := context.WithTimeout(ctx, opt.RunTimeout)
	defer runCancel()
	cmd := exec.CommandContext(runCtx, bin, inv.Argv[1:]...)
	cmd.Dir = repo
	cmd.Env = agentEnv(opt, home, filepath.Dir(sdfBin), a.Support().Protocol, inv)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{buf: &stdout, n: 8192}
	cmd.Stderr = &limitWriter{buf: &stderr, n: 8192}
	runErr := cmd.Run()
	capped := px != nil && px.cappedRun()
	stopServe()
	if runErr != nil {
		detail := strings.TrimSpace(redact(stderr.String(), opt.Key))
		if detail == "" {
			detail = redact(runErr.Error(), opt.Key)
		}
		return onceResult{}, fmt.Errorf("%s: %s", a.ID(), truncate(detail, 400))
	}
	scored, err := scoreDB(db, fx)
	if err != nil {
		return onceResult{}, err
	}
	return onceResult{scored: scored, capped: capped}, nil
}

func ingest(db string, fx recall.Fixture) error {
	st, err := store.Open(db)
	if err != nil {
		return err
	}
	defer st.Close()
	recs, err := decodeFixture(fx)
	if err != nil {
		return err
	}
	if _, err := st.Insert(recs); err != nil {
		return err
	}
	return nil
}

func scoreDB(db string, fx recall.Fixture) (recall.ScenarioResult, error) {
	st, err := store.Open(db)
	if err != nil {
		return recall.ScenarioResult{}, err
	}
	defer st.Close()
	g, err := decision.Load(st)
	if err != nil {
		return recall.ScenarioResult{}, err
	}
	return recall.Score(fx, g), nil
}

func filterFixtures(all []recall.Fixture, names []string) ([]recall.Fixture, error) {
	by := map[string]recall.Fixture{}
	for _, fx := range all {
		by[fx.Name] = fx
	}
	seen := map[string]bool{}
	var out []recall.Fixture
	for _, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("duplicate fixture %q", name)
		}
		seen[name] = true
		fx, ok := by[name]
		if !ok {
			return nil, fmt.Errorf("unknown fixture %q", name)
		}
		out = append(out, fx)
	}
	if len(out) == 0 {
		return nil, errors.New("no fixtures")
	}
	return out, nil
}

func sortFixtures(fx []recall.Fixture) {
	for i := 1; i < len(fx); i++ {
		j := i
		for j > 0 && fx[j].Name < fx[j-1].Name {
			fx[j], fx[j-1] = fx[j-1], fx[j]
			j--
		}
	}
}

func resolveSDF(ctx context.Context, opt Options) (string, bool, error) {
	if opt.SDFBin != "" {
		return opt.SDFBin, false, nil
	}
	root, err := moduleRoot()
	if err != nil {
		return "", false, err
	}
	dir, err := os.MkdirTemp("", "sdf-recall-bin-")
	if err != nil {
		return "", false, err
	}
	bin := filepath.Join(dir, "sdf")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/sdf")
	cmd.Dir = root
	cmd.Env = append(filterKey(os.Environ(), opt.Key), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", false, fmt.Errorf("build sdf: %s", truncate(redact(string(out), opt.Key), 400))
	}
	return bin, true, nil
}

func installCLI(ctx context.Context, a agent, key string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var last error
	for _, argv := range a.InstallCommands() {
		if len(argv) == 0 {
			continue
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = filterKey(os.Environ(), key)
		out, err := cmd.CombinedOutput()
		if err != nil {
			last = fmt.Errorf("%s: %s", argv[0], truncate(redact(string(out), key), 200))
		}
	}
	return last
}

func gitInit(ctx context.Context, repo, key string) error {
	steps := [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "recall@example.invalid"},
		{"config", "user.name", "sdf-recall"},
		{"commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range steps {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		cmd.Env = append(filterKey(os.Environ(), key),
			"GIT_AUTHOR_NAME=sdf-recall",
			"GIT_AUTHOR_EMAIL=recall@example.invalid",
			"GIT_COMMITTER_NAME=sdf-recall",
			"GIT_COMMITTER_EMAIL=recall@example.invalid",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %s", args[0], truncate(redact(string(out), key), 200))
		}
	}
	return nil
}

// stopProcess asks sdf serve to shut down on SIGTERM and waits for it to
// exit, so SQLite closes before the store is scored. Kill follows if it is
// still running after 2 seconds. Call it once.
func stopProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	n := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		return 0, err
	}
	return n, nil
}

func waitPort(ctx context.Context, port int, d time.Duration) error {
	deadline := time.Now().Add(d)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var last error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		last = err
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if last == nil {
		last = errors.New("timed out")
	}
	return last
}

func (o Options) look(name string) (string, error) {
	if o.LookPath != nil {
		return o.LookPath(name)
	}
	return exec.LookPath(name)
}

func baseEnv(home, sdfDir, key string) []string {
	return filterKey([]string{
		"PATH=" + prependPath(sdfDir),
		"HOME=" + home,
		"TMPDIR=" + os.TempDir(),
		"LANG=C.UTF-8",
	}, key)
}

func agentEnv(opt Options, home, sdfDir string, proto protocol, inv invocation) []string {
	env := []string{
		"PATH=" + prependPath(sdfDir),
		"HOME=" + home,
		"TMPDIR=" + os.TempDir(),
		"LANG=C.UTF-8",
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"SDF_RECALL_PROTOCOL=" + string(proto),
	}
	switch proto {
	case protocolAnthropic:
		env = append(env, "SDF_RECALL_MODEL_BASE="+opt.proxyAnthropic)
	default:
		env = append(env, "SDF_RECALL_MODEL_BASE="+opt.proxyOpenAI)
	}
	for k, v := range inv.Env {
		if opt.Key != "" && strings.Contains(v, opt.Key) {
			continue
		}
		env = append(env, k+"="+v)
	}
	secrets := map[string]bool{"SDF_RECALL_API_KEY": true}
	for _, name := range inv.SecretEnv {
		if name != "" {
			secrets[name] = true
		}
	}
	for k, v := range opt.ExtraEnv {
		if k == "" || secrets[k] {
			continue
		}
		if opt.Key != "" && strings.Contains(v, opt.Key) {
			continue
		}
		env = append(env, k+"="+v)
	}
	for name := range secrets {
		env = append(env, name+"="+opt.Key)
	}
	return env
}

func prependPath(dir string) string {
	path := os.Getenv("PATH")
	if dir == "" {
		return path
	}
	if path == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + path
}

func filterKey(env []string, key string) []string {
	drop := map[string]bool{
		"SDF_RECALL_API_KEY":       true,
		"TOKENROUTER_API_KEY":      true,
		"ANTHROPIC_AUTH_TOKEN":     true,
		"OPENAI_API_KEY":           true,
		"COPILOT_PROVIDER_API_KEY": true,
	}
	var out []string
	for _, e := range env {
		name, val, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if drop[name] {
			continue
		}
		if key != "" && strings.Contains(val, key) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Redact replaces key with a placeholder. An empty key leaves text unchanged.
func Redact(text, key string) string { return redact(text, key) }

func redact(s, key string) string {
	if key == "" || s == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type limitWriter struct {
	buf *bytes.Buffer
	n   int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.buf.Len() < w.n {
		remain := w.n - w.buf.Len()
		if len(p) > remain {
			_, _ = w.buf.Write(p[:remain])
		} else {
			_, _ = w.buf.Write(p)
		}
	}
	return len(p), nil
}

func countsFrom(ar AgentReport) counts {
	if ar.Overall == nil {
		return counts{}
	}
	o := ar.Overall
	return counts{
		runs:   o.Runs,
		scored: o.Scored,
		failed: o.Failed,
		capped: o.Capped,
		decL:   o.Decisions.Labeled,
		decR:   o.Decisions.Recorded,
		decH:   o.Decisions.Hits,
		linkL:  o.Links.Labeled,
		linkR:  o.Links.Recorded,
		linkH:  o.Links.Hits,
	}
}
