// Package cli is the sdf command line.
package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/paulpham157/sdf/internal/server"
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
		fmt.Fprintln(stderr, "usage: sdf <serve|summary> [flags]")
		return 2
	}
	switch args[0] {
	case "serve":
		return cmdServe(args[1:], stdout, stderr)
	case "summary":
		return cmdSummary(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "usage: sdf <serve|summary> [flags]")
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
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "usage: sdf summary [--db PATH] [--session ID] [--json] [--all-spans]")
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
		default:
			fmt.Fprintf(stderr, "unrecognized arguments: %s\n", arg)
			return 2
		}
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
	report := summary.Summarize(records, allSpans)
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
		report = summary.Report{Sessions: filtered, Unattributed: []summary.Unattributed{}}
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
