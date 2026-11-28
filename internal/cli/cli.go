// Package cli is the sdf command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/paulpham157/sdf/internal/check"
	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decisiongraph"
	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/setup"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
	"github.com/paulpham157/sdf/internal/ui"
	"github.com/paulpham157/sdf/internal/version"
)

// Main runs sdf with args, which do not include the program name.
// It returns the process exit code.
func Main(args []string) int {
	return mainWith(args, os.Stdout, os.Stderr)
}

func mainWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usageLine)
		fmt.Fprintln(stderr, "Run sdf --help for what each command does. New here? Start with sdf serve, then sdf init <agent> --write.")
		return 2
	}
	switch args[0] {
	case "serve":
		return cmdServe(args[1:], stdout, stderr)
	case "summary":
		return cmdSummary(args[1:], stdout, stderr)
	case "prices":
		return cmdPrices(args[1:], stdout, stderr)
	case "init":
		return cmdInit(args[1:], stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	case "decision":
		return cmdDecision(args[1:], stdout, stderr)
	case "task":
		return cmdTask(args[1:], stdout, stderr)
	case "verify":
		return cmdVerify(args[1:], stdout, stderr)
	case "audit":
		return cmdAudit(args[1:], stdout, stderr)
	case "redact":
		return cmdRedact(args[1:], stdout, stderr)
	case "hook":
		return cmdHook(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		return cmdVersion(args[1:], stdout, stderr)
	case "-h", "--help":
		writeTopHelp(stdout)
		return 0
	case "help":
		return cmdHelp(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unrecognized command %q; run sdf --help for the list\n", args[0])
		return 2
	}
}

const usageLine = "usage: sdf <serve|summary|check|init|prices|decision|audit|task|verify|redact|hook|version> [flags]"

func cmdVersion(args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		switch arg {
		case "--help", "-h":
			fmt.Fprintln(stdout, "usage: sdf version")
			fmt.Fprintln(stdout, "Prints the version, commit, build date, Go version, and platform. sdf --version and sdf -v are the same.")
			return 0
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	fmt.Fprint(stdout, version.Get().String())
	return 0
}

func cmdServe(args []string, stdout, stderr io.Writer) int {
	host := "127.0.0.1"
	port := 4318
	grpcPort := 4317
	dbPath := "sdf-telemetry.sqlite"
	uiOn := true
	uiAddr := ui.DefaultAddr
	pricesSpec := ""
	repoPath := ""
	github := false
	budgetPath := ""
	tokenFlag := ""
	tokenFlagSet := false
	tokenFileFlag := ""
	tokenFileFlagSet := false
	contentFlag := ""
	contentFlagSet := false
	var limits limitFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if name, val, ok, err := takeLimitFlag(arg, &i, args); ok {
			if err != nil {
				fmt.Fprintln(stderr, "sdf serve: "+err.Error())
				return 2
			}
			limits.set(name, val)
			continue
		}
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf serve [--host HOST] [--port PORT] [--grpc-port PORT] [--db PATH] [--otlp-token TOKEN | --otlp-token-file PATH] [--no-ui] [--ui-addr HOST:PORT] [--prices bundled|PATH] [--repo PATH] [--github] [--budget PATH] [--store-content[=AGENTS]]")
			fmt.Fprintln(stdout, "       sdf serve [--max-conns N] [--max-conns-per-ip N] [--max-conns-per-token N] [--max-body-bytes N] [--max-inflight-bytes N] [--read-header-timeout DUR] [--read-timeout DUR] [--idle-timeout DUR]")
			fmt.Fprintln(stdout, "UI default: "+ui.DefaultAddr+" (localhost only; --host does not change it)")
			fmt.Fprintln(stdout, "Prompt, response, tool, and error text is dropped before storing, for every agent. --store-content (or SDF_STORE_CONTENT=1) keeps it; --store-content=qwen,claude-code (or SDF_STORE_CONTENT=qwen,claude-code) keeps it for those agents only.")
			fmt.Fprintln(stdout, "Optional OTLP token: --otlp-token TOKEN, --otlp-token-file PATH, SDF_OTLP_TOKEN, or SDF_OTLP_TOKEN_FILE. Unset leaves the receiver open.")
			fmt.Fprintln(stdout, limitsHelp())
			fmt.Fprintln(stdout, "")
			fmt.Fprintln(stdout, serveHelp)
			return 0
		case arg == "--host":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf serve: --host requires a value")
				return 2
			}
			host = v
		case strings.HasPrefix(arg, "--host="):
			host = strings.TrimPrefix(arg, "--host=")
		case arg == "--port":
			v, ok := take(&i, args)
			n, err := parsePort(v, ok)
			if err != nil {
				fmt.Fprintln(stderr, "sdf serve: --port requires a port number (plain decimal, like 4318)")
				return 2
			}
			port = n
		case strings.HasPrefix(arg, "--port="):
			n, err := parsePort(strings.TrimPrefix(arg, "--port="), true)
			if err != nil {
				fmt.Fprintln(stderr, "sdf serve: --port requires a port number (plain decimal, like 4318)")
				return 2
			}
			port = n
		case arg == "--grpc-port":
			v, ok := take(&i, args)
			n, err := parsePort(v, ok)
			if err != nil {
				fmt.Fprintln(stderr, "sdf serve: --grpc-port requires a port number (plain decimal, like 4318)")
				return 2
			}
			grpcPort = n
		case strings.HasPrefix(arg, "--grpc-port="):
			n, err := parsePort(strings.TrimPrefix(arg, "--grpc-port="), true)
			if err != nil {
				fmt.Fprintln(stderr, "sdf serve: --grpc-port requires a port number (plain decimal, like 4318)")
				return 2
			}
			grpcPort = n
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf serve: --db requires a value")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--no-ui":
			uiOn = false
		case arg == "--ui-addr":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf serve: --ui-addr requires HOST:PORT")
				return 2
			}
			parsed, err := parseUIAddr(v)
			if err != nil {
				fmt.Fprintln(stderr, "sdf serve: --ui-addr requires HOST:PORT")
				return 2
			}
			uiAddr = parsed
		case strings.HasPrefix(arg, "--ui-addr="):
			parsed, err := parseUIAddr(strings.TrimPrefix(arg, "--ui-addr="))
			if err != nil {
				fmt.Fprintln(stderr, "sdf serve: --ui-addr requires HOST:PORT")
				return 2
			}
			uiAddr = parsed
		case arg == "--prices":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf serve: --prices requires bundled or a path")
				return 2
			}
			pricesSpec = v
		case strings.HasPrefix(arg, "--prices="):
			pricesSpec = strings.TrimPrefix(arg, "--prices=")
		case arg == "--repo":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf serve: --repo requires a path")
				return 2
			}
			repoPath = v
		case strings.HasPrefix(arg, "--repo="):
			repoPath = strings.TrimPrefix(arg, "--repo=")
		case arg == "--github":
			github = true
		case arg == "--budget":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf serve: --budget requires a path")
				return 2
			}
			budgetPath = v
		case strings.HasPrefix(arg, "--budget="):
			budgetPath = strings.TrimPrefix(arg, "--budget=")
		case arg == "--otlp-token":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf serve: --otlp-token requires a value")
				return 2
			}
			tokenFlag = v
			tokenFlagSet = true
		case strings.HasPrefix(arg, "--otlp-token="):
			tokenFlag = strings.TrimPrefix(arg, "--otlp-token=")
			tokenFlagSet = true
		case arg == "--otlp-token-file":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf serve: --otlp-token-file requires a path")
				return 2
			}
			tokenFileFlag = v
			tokenFileFlagSet = true
		case strings.HasPrefix(arg, "--otlp-token-file="):
			tokenFileFlag = strings.TrimPrefix(arg, "--otlp-token-file=")
			tokenFileFlagSet = true
		case arg == "--store-content":
			contentFlag, contentFlagSet = "all", true
		case strings.HasPrefix(arg, "--store-content="):
			contentFlag, contentFlagSet = strings.TrimPrefix(arg, "--store-content="), true
			if strings.TrimSpace(contentFlag) == "" {
				fmt.Fprintln(stderr, "sdf serve: --store-content= needs all or a list of agents")
				return 2
			}
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	token, code := resolveServeOTLPToken(tokenFlag, tokenFlagSet, tokenFileFlag, tokenFileFlagSet, os.Getenv, stderr)
	if code != 0 {
		return code
	}
	lim, err := limits.resolve(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "sdf serve: %s\n", err.Error())
		return 2
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		if token == "" {
			fmt.Fprintln(stderr, "warning: this receiver has no authentication; binding beyond localhost exposes telemetry that can include prompts and paths. The UI does not follow --host.")
		} else {
			fmt.Fprintln(stderr, "warning: a shared bearer token is not a substitute for a trusted network; binding beyond localhost still exposes telemetry that can include prompts and paths. The UI does not follow --host.")
		}
	}
	contentSource := "--store-content"
	if !contentFlagSet {
		contentFlag, contentSource = os.Getenv("SDF_STORE_CONTENT"), "SDF_STORE_CONTENT"
	}
	content, err := parseStoreContent(contentFlag)
	if err != nil {
		fmt.Fprintf(stderr, "sdf serve: %s: %s\n", contentSource, displayErr(err))
		return 2
	}
	if _, err := summary.LoadBudget(budgetPath); err != nil {
		fmt.Fprintf(stderr, "sdf serve: %s\n", displayErr(err))
		return 1
	}
	if uiOn {
		if _, err := pricing.ResolveCatalog(pricesSpec); err != nil {
			fmt.Fprintf(stderr, "sdf serve: %s\n", displayErr(err))
			return 1
		}
	}
	// Held shared until serve exits, so sdf redact refuses to rewrite the
	// file under it, and serve refuses to start while redact runs.
	lock, err := store.LockForServe(dbPath)
	if errors.Is(err, store.ErrLocked) {
		fmt.Fprintf(stderr, "sdf serve: sdf redact%s is rewriting %s; start sdf serve again when it finishes\n", store.LockHolder(dbPath), displayPath(dbPath))
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: cannot take the lock file %s (%s); sdf redact will not see this serve\n", displayPath(store.LockPath(dbPath)), displayErr(err))
	}
	defer lock.Release()
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf serve: %s\n", displayErr(err))
		return 1
	}
	defer db.Close()
	warnUnscrubbed(db, dbPath, stderr)
	if content.on() {
		db.KeepContent(content.keep())
		fmt.Fprintf(stderr, "sdf serve: storing prompt, response, tool, and error text for %s (%s); rows are marked sdf.content=kept\n", content.describe(), contentSource)
	}
	srv := &server.Server{
		Host:     host,
		HTTPPort: port,
		GRPCPort: grpcPort,
		Store:    db,
		Token:    token,
		Limits:   lim,
	}
	// Notify before listen so a SIGTERM that arrives while Start is still
	// binding is buffered and stops the process, instead of using the
	// default kill. Tests rely on that when /health does not come up.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	if err := srv.Start(); err != nil {
		fmt.Fprintln(stderr, serveListenError(err, port, grpcPort, uiAddr))
		return 1
	}
	defer srv.Shutdown()
	var uiSrv *ui.Server
	if uiOn {
		uiSrv, err = ui.Start(ui.Config{
			Addr:              uiAddr,
			Store:             db,
			Prices:            pricesSpec,
			Repo:              repoPath,
			GitHub:            github,
			Budget:            budgetPath,
			MaxConns:          lim.MaxConns,
			MaxConnsPerIP:     lim.MaxConnsPerIP,
			ReadHeaderTimeout: lim.ReadHeaderTimeout,
			ReadTimeout:       lim.ReadTimeout,
			IdleTimeout:       lim.IdleTimeout,
		})
		if err != nil {
			fmt.Fprintln(stderr, serveListenError(err, port, grpcPort, uiAddr))
			return 1
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = uiSrv.Shutdown(ctx)
		}()
		if warning := uiSrv.BindWarning(); warning != "" {
			fmt.Fprintln(stderr, "warning: "+warning)
		}
	}
	select {
	case <-sig:
		fmt.Fprintln(stderr, "\nstopped")
		return 0
	default:
	}
	fmt.Fprintf(stdout, "OTLP/HTTP listening on http://%s db=%s\n", srv.HTTPAddress(), store.AbsPath(dbPath))
	fmt.Fprintf(stdout, "OTLP/gRPC listening on %s\n", srv.GRPCAddress())
	if uiSrv != nil {
		fmt.Fprintf(stdout, "UI listening on %s\n", uiSrv.URL())
	}
	<-sig
	fmt.Fprintln(stderr, "\nstopped")
	return 0
}

func parseUIAddr(v string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(v))
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return "", strconv.ErrSyntax
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}

func cmdSummary(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	dbPathSet := false
	var session *string
	asJSON := false
	allSpans := false
	pricesSpec := ""
	repoPath := ""
	github := false
	budgetPath := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf summary [--db PATH] [--session ID] [--json] [--all-spans] [--prices bundled|PATH] [--repo PATH] [--github] [--budget PATH]")
			fmt.Fprintln(stdout, "")
			fmt.Fprintln(stdout, summaryHelp)
			return 0
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf summary: --db requires a value")
				return 2
			}
			dbPath, dbPathSet = v, true
		case strings.HasPrefix(arg, "--db="):
			dbPath, dbPathSet = strings.TrimPrefix(arg, "--db="), true
		case arg == "--session":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf summary: --session requires a value")
				return 2
			}
			session = &v
		case strings.HasPrefix(arg, "--session="):
			v := strings.TrimPrefix(arg, "--session=")
			session = &v
		case arg == "--json":
			asJSON = true
		case arg == "--all-spans":
			allSpans = true
		case arg == "--prices":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf summary: --prices requires bundled or a path")
				return 2
			}
			pricesSpec = v
		case strings.HasPrefix(arg, "--prices="):
			pricesSpec = strings.TrimPrefix(arg, "--prices=")
		case arg == "--repo":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf summary: --repo requires a path")
				return 2
			}
			repoPath = v
		case strings.HasPrefix(arg, "--repo="):
			repoPath = strings.TrimPrefix(arg, "--repo=")
		case arg == "--github":
			github = true
		case arg == "--budget":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf summary: --budget requires a path")
				return 2
			}
			budgetPath = v
		case strings.HasPrefix(arg, "--budget="):
			budgetPath = strings.TrimPrefix(arg, "--budget=")
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	budget, err := summary.LoadBudget(budgetPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf summary: %s\n", displayErr(err))
		return 1
	}
	prices, err := pricing.ResolveCatalog(pricesSpec)
	if err != nil {
		fmt.Fprintf(stderr, "sdf summary: %s\n", displayErr(err))
		return 1
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf summary: %s\n", displayErr(err))
		return 1
	}
	summaryMissingHint(dbPath, dbPathSet, stderr)
	records, err := db.Load()
	_ = db.Close()
	if err != nil {
		fmt.Fprintf(stderr, "sdf summary: %s\n", displayErr(err))
		return 1
	}
	report := summary.SummarizeWith(records, allSpans, prices)
	gitOpt := outcome.Options{Repo: repoPath}
	if github {
		lookup, err := outcome.NewGitHub()
		if err != nil {
			gitOpt.GitHubNote = err.Error()
			fmt.Fprintf(stderr, "sdf summary: %s\n", displayErr(err))
		} else {
			gitOpt.Lookup = lookup
		}
	}
	outcome.Attach(&report, gitOpt)
	if session != nil {
		filtered := report.Sessions[:0:0]
		for _, item := range report.Sessions {
			if item.SessionID == *session {
				filtered = append(filtered, item)
			}
		}
		if filtered == nil {
			filtered = []summary.SessionSummary{}
		}
		rolled := outcome.CostPerAccepted(filtered)
		report = summary.Report{Sessions: filtered, Unattributed: []summary.Unattributed{}, CostPerAccepted: &rolled}
	}
	summary.ApplyBudget(&report, budget)
	if asJSON {
		if err := summary.WriteJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "sdf summary: %s\n", displayErr(err))
			return 1
		}
		return 0
	}
	fmt.Fprint(stdout, summary.Format(report))
	return 0
}

// decisionCommands is every sdf decision subcommand, in the order help lists them.
// cmdDecision and the top-level sdf --help line both use this table, so a new
// subcommand cannot be added to the dispatch without appearing in that help.
var decisionCommands = []struct {
	name string
	run  func(args []string, stdout, stderr io.Writer) int
}{
	{"add", cmdDecisionAdd},
	{"link", cmdDecisionLink},
	{"list", cmdDecisionList},
	{"graph", cmdDecisionGraph},
	{"brief", cmdDecisionBrief},
	{"check", cmdDecisionCheck},
	{"retract", cmdDecisionRetract},
	{"audit", cmdDecisionAudit},
	{"recall", cmdDecisionRecall},
	{"scope", cmdDecisionScope},
	{"touches", cmdDecisionTouches},
	{"pre-push", cmdDecisionPrePush},
}

func decisionSubcommands() []string {
	names := make([]string, len(decisionCommands))
	for i, c := range decisionCommands {
		names[i] = c.name
	}
	return names
}

func decisionUsage() string {
	return "usage: sdf decision <" + strings.Join(decisionSubcommands(), "|") + "> [flags]"
}

func cmdDecision(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, decisionUsage())
		return 2
	}
	for _, c := range decisionCommands {
		if c.name == args[0] {
			return c.run(args[1:], stdout, stderr)
		}
	}
	switch args[0] {
	case "--help", "-h":
		fmt.Fprintln(stdout, decisionUsage())
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, decisionHelp)
		return 0
	default:
		unrecognizedArgs(stderr, args[0])
		return 2
	}
}

const decisionAddUsage = "usage: sdf decision add --id ID --statement TEXT --status hypothesis|decision [--decided-by LOGIN] [--category architecture|policy|product|implementation|operations] [--alternatives TEXT] [--reason TEXT] [--source TEXT] [--path GLOB] [--db PATH]"

// identityRunner resolves a GitHub login when --decided-by is omitted.
// Tests replace it so a machine's gh or git config does not change rows.
var identityRunner = decision.ExecCommand

// SetIdentityRunner replaces the command runner used when --decided-by is
// omitted. The returned function restores the previous runner.
func SetIdentityRunner(run decision.CommandRunner) func() {
	prev := identityRunner
	if run == nil {
		run = func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("identity lookup disabled")
		}
	}
	identityRunner = run
	return func() { identityRunner = prev }
}

func cmdDecisionAdd(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	id := ""
	statement := ""
	status := ""
	source := ""
	decidedBy := ""
	decidedBySet := false
	category := ""
	alternatives := ""
	reason := ""
	var paths []string
	idSet, statementSet, statusSet := false, false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, decisionAddUsage)
			return 0
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision add: --db requires a value")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--id":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision add: --id requires a value")
				return 2
			}
			id, idSet = v, true
		case strings.HasPrefix(arg, "--id="):
			id, idSet = strings.TrimPrefix(arg, "--id="), true
		case arg == "--statement":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision add: --statement requires a value")
				return 2
			}
			statement, statementSet = v, true
		case strings.HasPrefix(arg, "--statement="):
			statement, statementSet = strings.TrimPrefix(arg, "--statement="), true
		case arg == "--status":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision add: --status requires a value")
				return 2
			}
			status, statusSet = v, true
		case strings.HasPrefix(arg, "--status="):
			status, statusSet = strings.TrimPrefix(arg, "--status="), true
		case arg == "--source":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision add: --source requires a value")
				return 2
			}
			source = v
		case strings.HasPrefix(arg, "--source="):
			source = strings.TrimPrefix(arg, "--source=")
		case arg == "--decided-by":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision add: --decided-by requires a value")
				return 2
			}
			decidedBy, decidedBySet = v, true
		case strings.HasPrefix(arg, "--decided-by="):
			decidedBy, decidedBySet = strings.TrimPrefix(arg, "--decided-by="), true
		case arg == "--category":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision add: --category requires a value")
				return 2
			}
			category = v
		case strings.HasPrefix(arg, "--category="):
			category = strings.TrimPrefix(arg, "--category=")
		case arg == "--alternatives":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision add: --alternatives requires a value")
				return 2
			}
			alternatives = v
		case strings.HasPrefix(arg, "--alternatives="):
			alternatives = strings.TrimPrefix(arg, "--alternatives=")
		case arg == "--reason":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision add: --reason requires a value")
				return 2
			}
			reason = v
		case strings.HasPrefix(arg, "--reason="):
			reason = strings.TrimPrefix(arg, "--reason=")
		case arg == "--path":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision add: --path requires a value")
				return 2
			}
			paths = append(paths, v)
		case strings.HasPrefix(arg, "--path="):
			paths = append(paths, strings.TrimPrefix(arg, "--path="))
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	if !idSet || !statementSet || !statusSet {
		fmt.Fprintln(stderr, decisionAddUsage)
		return 2
	}
	in := decision.RecordInput{
		ID:           id,
		Statement:    statement,
		Status:       status,
		Source:       source,
		DecidedBy:    decidedBy,
		Alternatives: alternatives,
		Reason:       reason,
		Category:     category,
		Paths:        paths,
	}
	// Validate the caller's fields before any identity lookup. An omitted
	// --decided-by is empty here, which is legal; the lookup fills it only
	// when the rest of the record is acceptable.
	if err := decision.ValidateRecord(in); err != nil {
		fmt.Fprintf(stderr, "sdf decision add: %s\n", displayErr(err))
		return 1
	}
	if decidedBySet {
		if strings.TrimSpace(decidedBy) != "" {
			in.DecidedBySource = decision.DecidedBySourceFlag
		}
	} else {
		in.DecidedBy, in.DecidedBySource = decision.ResolveDecidedBy("", false, identityRunner)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision add: %s\n", displayErr(err))
		return 1
	}
	defer db.Close()
	if _, err := decision.Add(db, in); err != nil {
		fmt.Fprintf(stderr, "sdf decision add: %s\n", displayErr(err))
		return 1
	}
	return 0
}

func cmdDecisionLink(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	relation := ""
	from := ""
	to := ""
	relationSet, fromSet, toSet := false, false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf decision link --relation REL --from KIND:REF --to KIND:REF [--db PATH]")
			return 0
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision link: --db requires a value")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--relation":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision link: --relation requires a value")
				return 2
			}
			relation, relationSet = v, true
		case strings.HasPrefix(arg, "--relation="):
			relation, relationSet = strings.TrimPrefix(arg, "--relation="), true
		case arg == "--from":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision link: --from requires KIND:REF")
				return 2
			}
			from, fromSet = v, true
		case strings.HasPrefix(arg, "--from="):
			from, fromSet = strings.TrimPrefix(arg, "--from="), true
		case arg == "--to":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision link: --to requires KIND:REF")
				return 2
			}
			to, toSet = v, true
		case strings.HasPrefix(arg, "--to="):
			to, toSet = strings.TrimPrefix(arg, "--to="), true
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	if !relationSet || !fromSet || !toSet {
		fmt.Fprintln(stderr, "usage: sdf decision link --relation REL --from KIND:REF --to KIND:REF [--db PATH]")
		return 2
	}
	fromEnd, err := parseEndpoint(from)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision link: --from %s\n", displayErr(err))
		return 2
	}
	toEnd, err := parseEndpoint(to)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision link: --to %s\n", displayErr(err))
		return 2
	}
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision link: %s\n", displayErr(err))
		return 1
	}
	defer db.Close()
	_, warnings, err := decision.LinkWithWarnings(db, decision.EdgeInput{
		Relation: relation,
		From:     fromEnd,
		To:       toEnd,
	})
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision link: %s\n", displayErr(err))
		return 1
	}
	for _, w := range warnings {
		fmt.Fprintf(stderr, "sdf decision link: warning: %s\n", w)
	}
	return 0
}

const decisionListUsage = "usage: sdf decision list [--db PATH] [--json] [--session ID] [--category architecture|policy|product|implementation|operations|none]"

// cmdDecisionList prints every decision and edge. It reads through
// decision.Load, the same path GET /api/decisions uses, and --json is that
// response byte for byte. Like sdf summary, it opens the store with
// store.OpenReadOnly and never writes: a missing file in an existing
// directory lists nothing and is not created, a missing directory exits 1,
// and an older file is read without being migrated (a file without
// decision_retractions reads as having no retractions).
func cmdDecisionList(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	asJSON := false
	session := ""
	sessionSet := false
	category := ""
	categorySet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, decisionListUsage)
			return 0
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision list: --db requires a value")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--session":
			v, ok := take(&i, args)
			if !ok || v == "" {
				fmt.Fprintln(stderr, "sdf decision list: --session requires a value")
				return 2
			}
			session, sessionSet = v, true
		case strings.HasPrefix(arg, "--session="):
			session, sessionSet = strings.TrimPrefix(arg, "--session="), true
			if session == "" {
				fmt.Fprintln(stderr, "sdf decision list: --session requires a value")
				return 2
			}
		case arg == "--json":
			asJSON = true
		case arg == "--category":
			v, ok := take(&i, args)
			if !ok || v == "" {
				fmt.Fprintln(stderr, "sdf decision list: --category requires a value")
				return 2
			}
			category, categorySet = v, true
		case strings.HasPrefix(arg, "--category="):
			category, categorySet = strings.TrimPrefix(arg, "--category="), true
			if category == "" {
				fmt.Fprintln(stderr, "sdf decision list: --category requires a value")
				return 2
			}
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	if categorySet && !decision.ValidCategoryFilter(category) {
		fmt.Fprintln(stderr, "sdf decision list: --category must be architecture, policy, product, implementation, operations, or none")
		return 2
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision list: %s\n", displayErr(err))
		return 1
	}
	graph, err := decision.Load(db)
	_ = db.Close()
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision list: %s\n", displayErr(err))
		return 1
	}
	if sessionSet {
		graph = decision.FilterBySession(graph, session)
	}
	preCategory := graph
	if categorySet {
		graph = decision.FilterByCategory(graph, category)
	}
	// A category filter that drops every row is not an empty store.
	// WriteText's "No decisions in the store." is only for a graph that
	// was already empty. --json stays the empty lists.
	if categorySet && !asJSON && len(graph.Decisions) == 0 && len(graph.Edges) == 0 &&
		(len(preCategory.Decisions) > 0 || len(preCategory.Edges) > 0) {
		fmt.Fprintln(stdout, "No decisions match.")
		return 0
	}
	write := decision.WriteText
	if asJSON {
		write = decision.WriteJSON
	}
	if err := write(stdout, graph); err != nil {
		fmt.Fprintf(stderr, "sdf decision list: %s\n", displayErr(err))
		return 1
	}
	return 0
}

const decisionGraphUsage = "usage: sdf decision graph [--db PATH] [--json] [--prices bundled|PATH] [--repo PATH] [--github]"

// cmdDecisionGraph prints the Decisions tab document. With --json it is the
// same bytes as GET /api/decisions/graph on a sdf serve started with the
// same --db, --prices, --repo, and --github; without it, one headline line
// per chapter. It opens the store read-only.
func cmdDecisionGraph(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	asJSON := false
	pricesSpec := ""
	repoPath := ""
	github := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, decisionGraphUsage)
			return 0
		case arg == "--db", arg == "--prices", arg == "--repo":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintf(stderr, "sdf decision graph: %s requires a value\n", arg)
				return 2
			}
			switch arg {
			case "--db":
				dbPath = v
			case "--prices":
				pricesSpec = v
			default:
				repoPath = v
			}
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case strings.HasPrefix(arg, "--prices="):
			pricesSpec = strings.TrimPrefix(arg, "--prices=")
		case strings.HasPrefix(arg, "--repo="):
			repoPath = strings.TrimPrefix(arg, "--repo=")
		case arg == "--github":
			github = true
		case arg == "--json":
			asJSON = true
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	prices, err := pricing.ResolveCatalog(pricesSpec)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision graph: %s\n", displayErr(err))
		return 1
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision graph: %s\n", displayErr(err))
		return 1
	}
	defer func() { _ = db.Close() }()
	records, err := db.Load()
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision graph: %s\n", displayErr(err))
		return 1
	}
	gitOpt := outcome.Options{Repo: repoPath}
	if github {
		lookup, err := outcome.NewGitHub()
		if err != nil {
			gitOpt.GitHubNote = err.Error()
			fmt.Fprintf(stderr, "sdf decision graph: %s\n", displayErr(err))
		} else {
			gitOpt.Lookup = lookup
		}
	}
	// The same report GET /api/decisions/graph reads: no budget.
	report := outcome.Summarize(records, prices, gitOpt)
	view, err := decisiongraph.FromStore(db, report)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision graph: %s\n", displayErr(err))
		return 1
	}
	write := decisiongraph.WriteText
	if asJSON {
		write = decisiongraph.WriteJSON
	}
	if err := write(stdout, view); err != nil {
		fmt.Fprintf(stderr, "sdf decision graph: %s\n", displayErr(err))
		return 1
	}
	return 0
}

const decisionRetractUsage = "usage: sdf decision retract --id ID [--reason TEXT] [--retracted-by user|agent] [--db PATH]"

// cmdDecisionRetract appends one retraction. Like add and link it prints
// nothing on success, exits 1 when the store refuses (unknown or already
// retracted id), and 2 on a usage error.
func cmdDecisionRetract(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	id, reason, by := "", "", ""
	idSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, decisionRetractUsage)
			return 0
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision retract: --db requires a value")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--id":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision retract: --id requires a value")
				return 2
			}
			id, idSet = v, true
		case strings.HasPrefix(arg, "--id="):
			id, idSet = strings.TrimPrefix(arg, "--id="), true
		case arg == "--reason":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision retract: --reason requires a value")
				return 2
			}
			reason = v
		case strings.HasPrefix(arg, "--reason="):
			reason = strings.TrimPrefix(arg, "--reason=")
		case arg == "--retracted-by":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision retract: --retracted-by requires a value")
				return 2
			}
			by = v
		case strings.HasPrefix(arg, "--retracted-by="):
			by = strings.TrimPrefix(arg, "--retracted-by=")
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	if !idSet {
		fmt.Fprintln(stderr, decisionRetractUsage)
		return 2
	}
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision retract: %s\n", displayErr(err))
		return 1
	}
	defer db.Close()
	if _, err := decision.Retract(db, decision.RetractInput{ID: id, Reason: reason, RetractedBy: by}); err != nil {
		fmt.Fprintf(stderr, "sdf decision retract: %s\n", displayErr(err))
		return 1
	}
	return 0
}

const decisionCheckUsage = "usage: sdf decision check <action> [--db PATH] [--json]"

const decisionCheckHelp = `Reports whether an in-force decision forbids an action. The sdf-decisions
skill requires this check before a push, merge, release, deploy, or
architecture change; a non-zero exit is what blocks those actions.

<action> is one argument: push, merge, release, or deploy, or any other
text, which is an architecture change.

Exit 0 is allowed (a line starting with "allowed"). Exit 1 is forbidden
(a line starting with "forbidden", then a row per blocking decision).
Exit 2 is a usage error. Exit 3 is a store or write error. A missing
store file in an existing directory is an empty store: exit 0, and the
file is not created. A missing directory exits 3.

The store is opened read-only with a 200ms busy timeout. --json prints
action, kind, allowed, and blocking (id, decided_by, decided_by_source
when set, decided_by_legacy, statement, match).

The sdf-decisions skill ships scripts/check, a helper that runs this
command and keeps its exit code.`

// cmdDecisionCheck reports whether an in-force decision forbids an action.
// Exit 0 is allowed, 1 is forbidden (the blocking decisions are on stdout),
// 2 is a usage error, and 3 is a store or write error. A missing store file
// in an existing directory is an empty store, which is allowed: the command
// opens the file read-only and does not create it. The action is one of
// push, merge, release, deploy, or any other text, which is an architecture
// change. Matching is decision.CheckAction.
func cmdDecisionCheck(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	asJSON := false
	var action string
	actionSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, decisionCheckUsage)
			fmt.Fprintln(stdout, "")
			fmt.Fprintln(stdout, decisionCheckHelp)
			return 0
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf decision check: --db requires a value")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--json":
			asJSON = true
		case strings.HasPrefix(arg, "-"):
			unrecognizedArgs(stderr, arg)
			return 2
		default:
			if actionSet {
				fmt.Fprintln(stderr, decisionCheckUsage)
				return 2
			}
			action, actionSet = arg, true
		}
	}
	if !actionSet || strings.TrimSpace(action) == "" {
		fmt.Fprintln(stderr, decisionCheckUsage)
		return 2
	}
	// Same default path as sdf decision brief, and the same read: a 200ms
	// busy timeout, then LoadContext. A missing file in an existing
	// directory is an empty store and is not created.
	db, err := store.OpenReadOnlyBusy(dbPath, int(briefBusyTimeout/time.Millisecond))
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision check: %v\n", err)
		return 3
	}
	ctx := context.Background()
	graph, err := decision.LoadContext(ctx, db)
	_ = db.Close()
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision check: %v\n", err)
		return 3
	}
	verdict, err := decision.CheckAction(activeForCheck(graph), action)
	if errors.Is(err, decision.ErrEmptyAction) {
		fmt.Fprintln(stderr, decisionCheckUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision check: %v\n", err)
		return 3
	}
	write := decision.WriteCheckText
	if asJSON {
		write = decision.WriteCheckJSON
	}
	if err := write(stdout, verdict); err != nil {
		fmt.Fprintf(stderr, "sdf decision check: %v\n", err)
		return 3
	}
	if !verdict.Allowed {
		return 1
	}
	return 0
}

// activeForCheck is the set sdf decision check matches. decision.Active is
// that set: every settled decision still in force, user or agent, including
// blocked and conflicted records, in insertion order. brief.Classify is
// narrower. It keeps a decision only when the user decided it or it is the
// source of a constrains edge, so an agent's "Do not push" with no
// constrains edge would disappear.
func activeForCheck(g decision.Graph) []decision.Record {
	return decision.Active(g)
}

func parseEndpoint(v string) (decision.Endpoint, error) {
	kind, ref, ok := strings.Cut(v, ":")
	if !ok || kind == "" || ref == "" {
		return decision.Endpoint{}, fmt.Errorf("requires KIND:REF")
	}
	return decision.Endpoint{Kind: kind, Ref: ref}, nil
}

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	dir := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf check [--cases DIR]")
			fmt.Fprintln(stdout, "")
			fmt.Fprintln(stdout, checkHelp)
			return 0
		case arg == "--cases":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf check: --cases requires a directory")
				return 2
			}
			dir = v
		case strings.HasPrefix(arg, "--cases="):
			dir = strings.TrimPrefix(arg, "--cases=")
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	var (
		cases []check.Case
		err   error
	)
	if dir == "" {
		cases, err = check.LoadDefault()
	} else {
		cases, err = check.Load(dir)
	}
	if err != nil {
		fmt.Fprintf(stderr, "sdf check: %s\n", displayErr(err))
		return 1
	}
	report := check.Run(cases)
	fmt.Fprint(stdout, check.Format(report))
	if report.Failed > 0 {
		return 1
	}
	return 0
}

func cmdPrices(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "refresh" && args[0] != "--help" && args[0] != "-h") {
		fmt.Fprintln(stderr, "usage: sdf prices refresh [--url URL] [--out PATH]")
		return 2
	}
	if args[0] == "--help" || args[0] == "-h" || (len(args) > 1 && (args[1] == "--help" || args[1] == "-h")) {
		fmt.Fprintln(stdout, "usage: sdf prices refresh [--url URL] [--out PATH]")
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, pricesHelp)
		return 0
	}
	pageURL := ""
	outPath := ""
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--url":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf prices refresh: --url requires a value")
				return 2
			}
			pageURL = v
		case strings.HasPrefix(arg, "--url="):
			pageURL = strings.TrimPrefix(arg, "--url=")
		case arg == "--out":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf prices refresh: --out requires a value")
				return 2
			}
			outPath = v
		case strings.HasPrefix(arg, "--out="):
			outPath = strings.TrimPrefix(arg, "--out=")
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	if outPath == "" {
		path, err := pricing.CachePath()
		if err != nil {
			fmt.Fprintf(stderr, "sdf prices refresh: %s\n", displayErr(err))
			return 1
		}
		outPath = path
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	snap, err := pricing.Fetch(ctx, client, pageURL)
	if err != nil {
		fmt.Fprintf(stderr, "sdf prices refresh: %s\n", displayErr(err))
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "sdf prices refresh: %s\n", displayErr(err))
		return 1
	}
	if err := os.WriteFile(outPath, snap.Body, 0o644); err != nil {
		fmt.Fprintf(stderr, "sdf prices refresh: %s\n", displayErr(err))
		return 1
	}
	fmt.Fprintf(stdout, "wrote %d models from %s retrieved %s to %s\n", len(snap.Catalog.Models), snap.Catalog.Source, snap.Catalog.RetrievedAtRaw, outPath)
	return 0
}

const initUsage = "usage: sdf init [--check] [--host HOST] [--port PORT] [--grpc-port PORT] [--git-hooks | --uninstall-git-hooks] [--db PATH] [--git-template [--set-template-dir] | --uninstall-git-template [--unset-template-dir]] [claude-code|codex|opencode|pi|qwen|grok|gemini|copilot] [--write] [--global] [--install]"

func cmdInit(args []string, stdout, stderr io.Writer) int {
	host := "127.0.0.1"
	port := 4318
	grpcPort := 4317
	write := false
	install := false
	global := false
	check := false
	gitHooks := false
	uninstallHooks := false
	gitTemplate := false
	uninstallTemplate := false
	setTemplateDir := false
	unsetTemplateDir := false
	hookDB := ""
	agent := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, initUsage)
			fmt.Fprintln(stdout, "")
			fmt.Fprintln(stdout, initHelp)
			return 0
		case arg == "--write":
			write = true
		case arg == "--install":
			install = true
		case arg == "--global":
			global = true
		case arg == "--check":
			check = true
		case arg == "--git-hooks":
			gitHooks = true
		case arg == "--uninstall-git-hooks":
			uninstallHooks = true
		case arg == "--git-template":
			gitTemplate = true
		case arg == "--uninstall-git-template":
			uninstallTemplate = true
		case arg == "--set-template-dir":
			setTemplateDir = true
		case arg == "--unset-template-dir":
			unsetTemplateDir = true
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok || strings.TrimSpace(v) == "" {
				fmt.Fprintln(stderr, "sdf init: --db requires a path")
				return 2
			}
			hookDB = v
		case strings.HasPrefix(arg, "--db="):
			hookDB = strings.TrimPrefix(arg, "--db=")
			if strings.TrimSpace(hookDB) == "" {
				fmt.Fprintln(stderr, "sdf init: --db requires a path")
				return 2
			}
		case arg == "--host":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf init: --host requires a value")
				return 2
			}
			host = v
		case strings.HasPrefix(arg, "--host="):
			host = strings.TrimPrefix(arg, "--host=")
		case arg == "--port":
			v, ok := take(&i, args)
			n, err := parsePort(v, ok)
			if err != nil {
				fmt.Fprintln(stderr, "sdf init: --port requires a port number (plain decimal, like 4318)")
				return 2
			}
			port = n
		case strings.HasPrefix(arg, "--port="):
			n, err := parsePort(strings.TrimPrefix(arg, "--port="), true)
			if err != nil {
				fmt.Fprintln(stderr, "sdf init: --port requires a port number (plain decimal, like 4318)")
				return 2
			}
			port = n
		case arg == "--grpc-port":
			v, ok := take(&i, args)
			n, err := parsePort(v, ok)
			if err != nil {
				fmt.Fprintln(stderr, "sdf init: --grpc-port requires a port number (plain decimal, like 4318)")
				return 2
			}
			grpcPort = n
		case strings.HasPrefix(arg, "--grpc-port="):
			n, err := parsePort(strings.TrimPrefix(arg, "--grpc-port="), true)
			if err != nil {
				fmt.Fprintln(stderr, "sdf init: --grpc-port requires a port number (plain decimal, like 4318)")
				return 2
			}
			grpcPort = n
		case arg == "claude-code" || arg == "codex" || arg == "opencode" || arg == "pi" || arg == "qwen" || arg == "qwen-code" || arg == "grok" || arg == "grok-cli" || arg == "gemini" || arg == "gemini-cli" || arg == "copilot" || arg == "copilot-cli":
			if agent != "" {
				fmt.Fprintf(stderr, "sdf init: extra agent %s\n", arg)
				return 2
			}
			agent = arg
		case !strings.HasPrefix(arg, "-"):
			fmt.Fprintln(stderr, unknownAgentMessage(arg))
			return 2
		default:
			unrecognizedArgs(stderr, arg)
			return 2
		}
	}
	if uninstallHooks && gitHooks {
		fmt.Fprintln(stderr, "sdf init: --git-hooks and --uninstall-git-hooks cannot be combined")
		return 2
	}
	if uninstallTemplate && gitTemplate {
		fmt.Fprintln(stderr, "sdf init: --git-template and --uninstall-git-template cannot be combined")
		return 2
	}
	if setTemplateDir && !gitTemplate {
		fmt.Fprintln(stderr, "sdf init: --set-template-dir requires --git-template")
		return 2
	}
	if unsetTemplateDir && !uninstallTemplate {
		fmt.Fprintln(stderr, "sdf init: --unset-template-dir requires --uninstall-git-template")
		return 2
	}
	if hookDB != "" && !gitHooks && !gitTemplate {
		fmt.Fprintln(stderr, "sdf init: --db applies to --git-hooks and --git-template (the store the pre-push hook reads)")
		return 2
	}
	if global && agent != "pi" {
		fmt.Fprintln(stderr, "sdf init: --global applies to pi")
		return 2
	}
	if agent == "" && !check && !gitHooks && !uninstallHooks && !gitTemplate && !uninstallTemplate {
		fmt.Fprintln(stderr, initUsage)
		return 2
	}
	ep := setup.Endpoint{Host: host, HTTPPort: port, GRPCPort: grpcPort}
	// Validate --host and the ports once, before --check probes them or a
	// git hook or agent file is written. --port 0 is refused here (Run
	// would read 0 as the default).
	if err := setup.ValidateEndpoint(&ep); err != nil {
		fmt.Fprintf(stderr, "sdf init: %v\n", err)
		return 2
	}
	code := 0
	if check {
		probed := setup.Probe(ep)
		fmt.Fprint(stdout, probed.Text)
		if agent != "" {
			text, err := setup.Configured(setup.Request{Agent: agent, Endpoint: ep})
			if err != nil {
				fmt.Fprintf(stderr, "sdf init: %v\n", err)
				return 1
			}
			fmt.Fprint(stdout, text)
		}
		if !probed.Reachable {
			code = 1
		}
		token, source, note := hookToken(os.Getenv)
		if note != "" {
			fmt.Fprint(stdout, note)
		}
		if auth := setup.ProbeAuth(ep, token, source); auth.Text != "" {
			fmt.Fprint(stdout, auth.Text)
			if auth.Failed {
				code = 1
			}
		}
	}
	if agent != "" {
		res, err := setup.Run(setup.Request{Agent: agent, Endpoint: ep, Write: write, Install: install, Global: global})
		if res.Text != "" {
			fmt.Fprint(stdout, res.Text)
		}
		if err != nil {
			fmt.Fprintf(stderr, "sdf init: %v\n", err)
			code = 1
		}
	}
	if uninstallHooks {
		res, err := setup.UninstallGitHooks("")
		if res.Text != "" {
			fmt.Fprint(stdout, res.Text)
		}
		if err != nil {
			fmt.Fprintf(stderr, "sdf init: %v\n", err)
			return 1
		}
	}
	if gitHooks {
		res, err := setup.InstallGitHooksWith("", ep, hookOptions(hookDB))
		if res.Text != "" {
			fmt.Fprint(stdout, res.Text)
		}
		if err != nil {
			fmt.Fprintf(stderr, "sdf init: %v\n", err)
			return 1
		}
	}
	if uninstallTemplate {
		res, err := setup.UninstallGitTemplate(setup.TemplateOptions{UnsetConfig: unsetTemplateDir})
		if res.Text != "" {
			fmt.Fprint(stdout, res.Text)
		}
		if err != nil {
			fmt.Fprintf(stderr, "sdf init: %v\n", err)
			return 1
		}
	}
	if gitTemplate {
		res, err := setup.InstallGitTemplate(setup.TemplateOptions{SetConfig: setTemplateDir, Hook: hookOptions(hookDB)}, ep)
		if res.Text != "" {
			fmt.Fprint(stdout, res.Text)
		}
		if err != nil {
			fmt.Fprintf(stderr, "sdf init: %v\n", err)
			return 1
		}
	}
	return code
}

// hookOptions names this sdf binary for the pre-push hook, so it does not
// depend on PATH, and the store it reads. A binary path that cannot be
// found is left empty and the hook uses sdf from PATH.
func hookOptions(db string) setup.HookOptions {
	opt := setup.HookOptions{DB: db}
	if exe, err := os.Executable(); err == nil {
		opt.Bin = exe
	}
	return opt
}

// parsePort reads a port written as plain decimal: no sign, no leading
// zero, no spaces (08, +4318, and " 4318" are refused; strconv.Atoi takes
// the first two). "0" parses, so the range check can say why it is refused.
func parsePort(v string, ok bool) (int, error) {
	if !ok || v == "" || len(v) > 5 {
		return 0, strconv.ErrSyntax
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	if len(v) > 1 && v[0] == '0' {
		return 0, strconv.ErrSyntax
	}
	return strconv.Atoi(v)
}

func take(i *int, args []string) (string, bool) {
	if *i+1 >= len(args) {
		return "", false
	}
	*i++
	return args[*i], true
}
