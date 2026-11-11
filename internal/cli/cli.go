// Package cli is the sdf command line.
package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/paulpham157/sdf/internal/outcome"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/server"
	"github.com/paulpham157/sdf/internal/setup"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
)

// Main runs sdf with args, which do not include the program name.
// It returns the process exit code.
func Main(args []string) int {
	return mainWith(args, os.Stdout, os.Stderr)
}

func mainWith(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: sdf <serve|summary|prices|init> [flags]")
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
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "usage: sdf <serve|summary|prices|init> [flags]")
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
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf serve [--host HOST] [--port PORT] [--grpc-port PORT] [--db PATH]")
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
		default:
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		fmt.Fprintln(stderr, "warning: this receiver has no authentication; binding beyond localhost exposes telemetry that can include prompts when the agent is opted in")
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
	fmt.Fprintf(stdout, "OTLP/HTTP listening on http://%s db=%s\n", srv.HTTPAddress(), dbPath)
	fmt.Fprintf(stdout, "OTLP/gRPC listening on %s\n", srv.GRPCAddress())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	<-sig
	fmt.Fprintln(stderr, "\nstopped")
	return 0
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

func cmdInit(args []string, stdout, stderr io.Writer) int {
	host := "127.0.0.1"
	port := 4318
	grpcPort := 4317
	write := false
	install := false
	check := false
	gitHooks := false
	uninstallHooks := false
	agent := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf init [--check] [--host HOST] [--port PORT] [--grpc-port PORT] [--git-hooks | --uninstall-git-hooks] [claude-code|codex|opencode|pi] [--write] [--install]")
			return 0
		case arg == "--write":
			write = true
		case arg == "--install":
			install = true
		case arg == "--check":
			check = true
		case arg == "--git-hooks":
			gitHooks = true
		case arg == "--uninstall-git-hooks":
			uninstallHooks = true
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
	if agent == "" && !check && !gitHooks && !uninstallHooks {
		fmt.Fprintln(stderr, "usage: sdf init [--check] [--host HOST] [--port PORT] [--grpc-port PORT] [--git-hooks | --uninstall-git-hooks] [claude-code|codex|opencode|pi] [--write] [--install]")
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
		res, err := setup.Run(setup.Request{Agent: agent, Endpoint: ep, Write: write, Install: install})
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
