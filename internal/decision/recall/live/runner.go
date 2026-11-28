package live

import (
	"bytes"
	"context"
	"encoding/json"
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
	if len(opt.Merge) > 0 {
		return mergeReportFiles(opt.Merge)
	}
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
	if opt.Parallel < 1 || opt.Parallel > 32 {
		return Report{}, errors.New("parallel must be from 1 to 32")
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
	}

	type ready struct {
		a   agent
		bin string
		ar  AgentReport
		run bool
	}
	prepared := make([]ready, len(agents))
	for i, a := range agents {
		sup := a.Support()
		ar := AgentReport{ID: a.ID(), Name: a.Name(), Protocol: string(sup.Protocol)}
		if !sup.OK {
			ar.Status = statusUnsupported
			ar.Reason = sup.Reason
			prepared[i] = ready{a: a, ar: ar}
			continue
		}
		bin, lookErr := opt.look(a.Binary())
		if lookErr != nil && opt.Install && len(a.InstallCommands()) > 0 {
			_ = installCLI(ctx, a, opt.Key)
			bin, lookErr = opt.look(a.Binary())
		}
		if lookErr != nil {
			ar.Status = statusUnavailable
			ar.Reason = a.Binary() + " is not on PATH"
			if hint := a.InstallHint(); hint != "" {
				ar.Reason += ". " + hint
			}
			prepared[i] = ready{a: a, ar: ar}
			continue
		}
		ar.Status = statusMeasured
		prepared[i] = ready{a: a, bin: bin, ar: ar, run: true}
	}

	type job struct {
		ai, fi, n int
	}
	var jobs []job
	acc := map[string]*scenarioAcc{}
	for i, p := range prepared {
		if !p.run {
			continue
		}
		for fi, fx := range fixtures {
			key := p.a.ID() + "\x00" + fx.Name
			acc[key] = &scenarioAcc{}
			for n := 0; n < opt.K; n++ {
				jobs = append(jobs, job{ai: i, fi: fi, n: n})
			}
		}
	}
	sem := make(chan struct{}, opt.Parallel)
	var wg sync.WaitGroup
	for ji, j := range jobs {
		if ctx.Err() != nil {
			for _, rest := range jobs[ji:] {
				key := prepared[rest.ai].a.ID() + "\x00" + fixtures[rest.fi].Name
				acc[key].fail(redact("canceled", opt.Key))
			}
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			p := prepared[j.ai]
			fx := fixtures[j.fi]
			key := p.a.ID() + "\x00" + fx.Name
			if ctx.Err() != nil {
				acc[key].fail("canceled")
				return
			}
			result, runErr := runOnce(ctx, opt, p.a, p.bin, fx, sdfBin, skill, j.n)
			if runErr != nil {
				acc[key].fail(redact(runErr.Error(), opt.Key))
				return
			}
			acc[key].add(result.scored, result.capped)
		}(j)
	}
	wg.Wait()

	var grand counts
	measured := 0
	for _, p := range prepared {
		ar := p.ar
		if p.run {
			var agentCounts counts
			for _, fx := range fixtures {
				key := p.a.ID() + "\x00" + fx.Name
				sc := acc[key].snapshot(fx.Name)
				ar.Scenarios = append(ar.Scenarios, sc)
				agentCounts.merge(countsFromScenario(sc))
			}
			pair := agentCounts.pair()
			ar.Overall = &pair
		}
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

type scenarioAcc struct {
	mu   sync.Mutex
	c    counts
	errs []string
}

func (s *scenarioAcc) fail(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c.fail()
	if len(s.errs) < 8 && msg != "" {
		s.errs = append(s.errs, msg)
	}
}

func (s *scenarioAcc) add(sc recall.ScenarioResult, capped bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c.add(sc, capped)
}

func (s *scenarioAcc) snapshot(name string) ScenarioReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	errs := append([]string(nil), s.errs...)
	return s.c.scenario(name, errs)
}

type onceResult struct {
	scored recall.ScenarioResult
	capped bool
}

func runOnce(ctx context.Context, opt Options, a agent, bin string, fx recall.Fixture, sdfBin, skill string, runIndex int) (onceResult, error) {
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
	px := newProxy(opt)
	openaiBase, anthropicBase, stopProxy, err := px.start()
	if err != nil {
		return onceResult{}, err
	}
	defer stopProxy()
	opt.proxyOpenAI = openaiBase
	opt.proxyAnthropic = anthropicBase
	sp := spec{
		Repo:          repo,
		Home:          home,
		OpenAIBase:    openaiBase,
		AnthropicBase: anthropicBase,
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
	var stderrTail tailWriter
	stderrTail.max = 64 << 10
	logPath, logFile := openRunLog(opt, a.ID(), fx.Name, runIndex)
	stderrSink := io.Writer(&stderrTail)
	if logFile != nil {
		defer logFile.Close()
		stderrSink = io.MultiWriter(&stderrTail, logFile)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = stderrSink
	runErr := cmd.Run()
	if logFile != nil {
		_ = logFile.Flush()
	}
	capped := px.cappedRun()
	stopServe()
	if runErr != nil {
		detail := noiseTail(redact(stderrTail.String(), opt.Key), 4096)
		if detail == "" {
			detail = redact(runErr.Error(), opt.Key)
		}
		if logPath != "" {
			detail += " log: " + logPath
		}
		return onceResult{}, fmt.Errorf("%s: %s", a.ID(), detail)
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

func mergeReportFiles(paths []string) (Report, error) {
	var parts []Report
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return Report{}, err
		}
		var rep Report
		if err := json.Unmarshal(b, &rep); err != nil {
			return Report{}, fmt.Errorf("%s: %w", path, err)
		}
		parts = append(parts, rep)
	}
	return mergeReports(parts)
}

func openRunLog(opt Options, agent, scenario string, runIndex int) (string, *runLog) {
	if opt.OutDir == "" {
		return "", nil
	}
	path := filepath.Join(opt.OutDir, agent, scenario, fmt.Sprintf("run-%d.log", runIndex+1))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", nil
	}
	fmt.Fprintf(f, "agent=%s scenario=%s run=%d\n", agent, scenario, runIndex+1)
	l := &runLog{f: f}
	l.rw.w = f
	l.rw.key = opt.Key
	return path, l
}

type runLog struct {
	f  *os.File
	rw redactWriter
}

func (l *runLog) Write(p []byte) (int, error) { return l.rw.Write(p) }

func (l *runLog) Flush() error { return l.rw.Flush() }

func (l *runLog) Close() error {
	err := l.rw.Flush()
	err2 := l.f.Close()
	if err != nil {
		return err
	}
	return err2
}

type redactWriter struct {
	w    io.Writer
	key  string
	hold []byte
}

func (r *redactWriter) Write(p []byte) (int, error) {
	if r.key == "" {
		_, err := r.w.Write(p)
		return len(p), err
	}
	data := append(append([]byte{}, r.hold...), p...)
	r.hold = nil
	s := string(data)
	keep := len(r.key) - 1
	if keep < 0 {
		keep = 0
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], r.key) {
			b.WriteString("[redacted]")
			i += len(r.key)
			continue
		}
		rest := s[i:]
		if len(rest) <= keep && strings.HasPrefix(r.key, rest) {
			r.hold = []byte(rest)
			break
		}
		b.WriteByte(s[i])
		i++
	}
	if b.Len() == 0 {
		return len(p), nil
	}
	_, err := r.w.Write([]byte(b.String()))
	return len(p), err
}

func (r *redactWriter) Flush() error {
	if len(r.hold) == 0 {
		return nil
	}
	s := strings.ReplaceAll(string(r.hold), r.key, "[redacted]")
	r.hold = nil
	_, err := r.w.Write([]byte(s))
	return err
}

type tailWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.max <= 0 {
		w.max = 64 << 10
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = append([]byte{}, w.buf[len(w.buf)-w.max:]...)
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

func noiseTail(s string, n int) string {
	filtered := filterNoise(s)
	if strings.TrimSpace(filtered) == "" {
		filtered = s
	}
	return strings.TrimSpace(tailText(filtered, n))
}

func filterNoise(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || isNoise(t) {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func isNoise(t string) bool {
	if strings.HasPrefix(t, "(node:") && strings.Contains(t, "Warning:") {
		return true
	}
	if strings.HasPrefix(t, "(Use `node --trace-") {
		return true
	}
	if strings.Contains(t, "ExperimentalWarning") || strings.Contains(t, "DeprecationWarning") {
		return true
	}
	return strings.HasPrefix(t, "npm warn") || strings.HasPrefix(t, "npm WARN")
}

func tailText(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[len(s)-n:]
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
