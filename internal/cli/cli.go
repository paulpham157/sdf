// Package cli is the sdf command line.
package cli

import (
	"context"
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
	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/setup"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
	"github.com/paulpham157/sdf/internal/ui"
)

// Main runs sdf with args, which do not include the program name.
// It returns the process exit code.
func Main(args []string) int {
	return mainWith(args, os.Stdout, os.Stderr)
}

func mainWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: sdf <serve|summary|prices|init|check|decision> [flags]")
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
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "usage: sdf <serve|summary|prices|init|check|decision> [flags]")
		return 0
	default:
		fmt.Fprintf(stderr, "unrecognized command %q\n", args[0])
		return 2
	}
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
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf serve [--host HOST] [--port PORT] [--grpc-port PORT] [--db PATH] [--no-ui] [--ui-addr HOST:PORT] [--prices bundled|PATH] [--repo PATH] [--github]")
			fmt.Fprintln(stdout, "UI default: "+ui.DefaultAddr+" (localhost only; --host does not change it)")
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
				fmt.Fprintln(stderr, "sdf serve: --port requires an integer")
				return 2
			}
			port = n
		case strings.HasPrefix(arg, "--port="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--port="))
			if err != nil {
				fmt.Fprintln(stderr, "sdf serve: --port requires an integer")
				return 2
			}
			port = n
		case arg == "--grpc-port":
			v, ok := take(&i, args)
			n, err := parsePort(v, ok)
			if err != nil {
				fmt.Fprintln(stderr, "sdf serve: --grpc-port requires an integer")
				return 2
			}
			grpcPort = n
		case strings.HasPrefix(arg, "--grpc-port="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--grpc-port="))
			if err != nil {
				fmt.Fprintln(stderr, "sdf serve: --grpc-port requires an integer")
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
		default:
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		fmt.Fprintln(stderr, "warning: this receiver has no authentication; binding beyond localhost exposes telemetry that can include prompts and paths. The UI does not follow --host.")
	}
	if uiOn {
		if _, err := pricing.ResolveCatalog(pricesSpec); err != nil {
			fmt.Fprintf(stderr, "sdf serve: %v\n", err)
			return 1
		}
	}
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf serve: %v\n", err)
		return 1
	}
	defer db.Close()
	srv := &server.Server{
		Host:     host,
		HTTPPort: port,
		GRPCPort: grpcPort,
		Store:    db,
	}
	if err := srv.Start(); err != nil {
		fmt.Fprintf(stderr, "sdf serve: %v\n", err)
		return 1
	}
	defer srv.Shutdown()
	var uiSrv *ui.Server
	if uiOn {
		uiSrv, err = ui.Start(ui.Config{
			Addr:   uiAddr,
			Store:  db,
			Prices: pricesSpec,
			Repo:   repoPath,
			GitHub: github,
		})
		if err != nil {
			fmt.Fprintf(stderr, "sdf serve: %v\n", err)
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
	fmt.Fprintf(stdout, "OTLP/HTTP listening on http://%s db=%s\n", srv.HTTPAddress(), dbPath)
	fmt.Fprintf(stdout, "OTLP/gRPC listening on %s\n", srv.GRPCAddress())
	if uiSrv != nil {
		fmt.Fprintf(stdout, "UI listening on %s\n", uiSrv.URL())
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
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
	var session *string
	asJSON := false
	allSpans := false
	pricesSpec := ""
	repoPath := ""
	github := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf summary [--db PATH] [--session ID] [--json] [--all-spans] [--prices bundled|PATH] [--repo PATH] [--github]")
			return 0
		case arg == "--db":
			v, ok := take(&i, args)
			if !ok {
				fmt.Fprintln(stderr, "sdf summary: --db requires a value")
				return 2
			}
			dbPath = v
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
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
		default:
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}
	prices, err := pricing.ResolveCatalog(pricesSpec)
	if err != nil {
		fmt.Fprintf(stderr, "sdf summary: %v\n", err)
		return 1
	}
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf summary: %v\n", err)
		return 1
	}
	records, err := db.Load()
	_ = db.Close()
	if err != nil {
		fmt.Fprintf(stderr, "sdf summary: %v\n", err)
		return 1
	}
	report := summary.SummarizeWith(records, allSpans, prices)
	gitOpt := outcome.Options{Repo: repoPath}
	if github {
		lookup, err := outcome.NewGitHub()
		if err != nil {
			gitOpt.GitHubNote = err.Error()
			fmt.Fprintf(stderr, "sdf summary: %v\n", err)
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
	if asJSON {
		if err := summary.WriteJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "sdf summary: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprint(stdout, summary.Format(report))
	return 0
}

const decisionUsage = "usage: sdf decision <add|link> [flags]"

func cmdDecision(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, decisionUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return cmdDecisionAdd(args[1:], stdout, stderr)
	case "link":
		return cmdDecisionLink(args[1:], stdout, stderr)
	case "--help", "-h":
		fmt.Fprintln(stdout, decisionUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "unrecognized arguments: %s\n", args[0])
		return 2
	}
}

func cmdDecisionAdd(args []string, stdout, stderr io.Writer) int {
	dbPath := "sdf-telemetry.sqlite"
	id := ""
	statement := ""
	status := ""
	source := ""
	idSet, statementSet, statusSet := false, false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf decision add --id ID --statement TEXT --status hypothesis|decision [--source TEXT] [--db PATH]")
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
		default:
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}
	if !idSet || !statementSet || !statusSet {
		fmt.Fprintln(stderr, "usage: sdf decision add --id ID --statement TEXT --status hypothesis|decision [--source TEXT] [--db PATH]")
		return 2
	}
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision add: %v\n", err)
		return 1
	}
	defer db.Close()
	if _, err := decision.Add(db, decision.RecordInput{
		ID:        id,
		Statement: statement,
		Status:    status,
		Source:    source,
	}); err != nil {
		fmt.Fprintf(stderr, "sdf decision add: %v\n", err)
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
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}
	if !relationSet || !fromSet || !toSet {
		fmt.Fprintln(stderr, "usage: sdf decision link --relation REL --from KIND:REF --to KIND:REF [--db PATH]")
		return 2
	}
	fromEnd, err := parseEndpoint(from)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision link: --from %v\n", err)
		return 2
	}
	toEnd, err := parseEndpoint(to)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision link: --to %v\n", err)
		return 2
	}
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "sdf decision link: %v\n", err)
		return 1
	}
	defer db.Close()
	if _, err := decision.Link(db, decision.EdgeInput{
		Relation: relation,
		From:     fromEnd,
		To:       toEnd,
	}); err != nil {
		fmt.Fprintf(stderr, "sdf decision link: %v\n", err)
		return 1
	}
	return 0
}

func parseEndpoint(v string) (decision.Endpoint, error) {
	kind, ref, ok := strings.Cut(v, ":")
	if !ok || kind == "" || ref == "" {
		return decision.Endpoint{}, fmt.Errorf("requires KIND:REF")
	}
	return decision.Endpoint{Kind: kind, Ref: ref}, nil
}

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	dir := check.DefaultDir()
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf check [--cases DIR]")
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
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}
	cases, err := check.Load(dir)
	if err != nil {
		fmt.Fprintf(stderr, "sdf check: %v\n", err)
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
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, "usage: sdf prices refresh [--url URL] [--out PATH]")
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
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}
	if outPath == "" {
		path, err := pricing.CachePath()
		if err != nil {
			fmt.Fprintf(stderr, "sdf prices refresh: %v\n", err)
			return 1
		}
		outPath = path
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	snap, err := pricing.Fetch(ctx, client, pageURL)
	if err != nil {
		fmt.Fprintf(stderr, "sdf prices refresh: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "sdf prices refresh: %v\n", err)
		return 1
	}
	if err := os.WriteFile(outPath, snap.Body, 0o644); err != nil {
		fmt.Fprintf(stderr, "sdf prices refresh: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %d models from %s retrieved %s to %s\n", len(snap.Catalog.Models), snap.Catalog.Source, snap.Catalog.RetrievedAtRaw, outPath)
	return 0
}

const initUsage = "usage: sdf init [--check] [--host HOST] [--port PORT] [--grpc-port PORT] [--git-hooks | --uninstall-git-hooks] [--git-template [--set-template-dir] | --uninstall-git-template [--unset-template-dir]] [claude-code|codex|opencode|pi] [--write] [--global] [--install]"

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
	agent := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, initUsage)
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
				fmt.Fprintln(stderr, "sdf init: --port requires an integer")
				return 2
			}
			port = n
		case strings.HasPrefix(arg, "--port="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--port="))
			if err != nil {
				fmt.Fprintln(stderr, "sdf init: --port requires an integer")
				return 2
			}
			port = n
		case arg == "--grpc-port":
			v, ok := take(&i, args)
			n, err := parsePort(v, ok)
			if err != nil {
				fmt.Fprintln(stderr, "sdf init: --grpc-port requires an integer")
				return 2
			}
			grpcPort = n
		case strings.HasPrefix(arg, "--grpc-port="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--grpc-port="))
			if err != nil {
				fmt.Fprintln(stderr, "sdf init: --grpc-port requires an integer")
				return 2
			}
			grpcPort = n
		case arg == "claude-code" || arg == "codex" || arg == "opencode" || arg == "pi":
			if agent != "" {
				fmt.Fprintf(stderr, "sdf init: extra agent %s\n", arg)
				return 2
			}
			agent = arg
		default:
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
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
	if global && agent != "pi" {
		fmt.Fprintln(stderr, "sdf init: --global applies to pi")
		return 2
	}
	if agent == "" && !check && !gitHooks && !uninstallHooks && !gitTemplate && !uninstallTemplate {
		fmt.Fprintln(stderr, initUsage)
		return 2
	}
	ep := setup.Endpoint{Host: host, HTTPPort: port, GRPCPort: grpcPort}
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
		res, err := setup.InstallGitHooks("", ep)
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
		res, err := setup.InstallGitTemplate(setup.TemplateOptions{SetConfig: setTemplateDir}, ep)
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

func parsePort(v string, ok bool) (int, error) {
	if !ok {
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
