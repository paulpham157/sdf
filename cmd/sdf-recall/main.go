// Command sdf-recall runs the opt-in live decision-recall measurement.
// It is not the sdf collector. With no API key it exits 0 and does not
// call a model.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/paulpham157/sdf/internal/decision/recall/live"
)

func main() {
	opt := live.ConfigFromEnv()
	opt.Install = os.Getenv("SDF_RECALL_NO_INSTALL") != "1"
	opt, mode, err := live.ParseArgs(os.Args[1:], opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sdf-recall: %s\n", live.Redact(err.Error(), opt.Key))
		fmt.Fprintln(os.Stderr, "usage: sdf-recall [--agents id,id] [--k N] [--json] [--markdown] [--install] [--no-install]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rep, err := live.Run(ctx, opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sdf-recall: %s\n", live.Redact(err.Error(), opt.Key))
		os.Exit(1)
	}
	if rep.Skipped {
		fmt.Fprintln(os.Stderr, "sdf-recall: skipped, no API key (set SDF_RECALL_API_KEY or TOKENROUTER_API_KEY)")
	}
	if mode != live.ModeJSON {
		fmt.Fprint(os.Stdout, rep.Markdown())
	}
	if mode != live.ModeMarkdown {
		body, err := rep.JSON()
		if err != nil {
			fmt.Fprintf(os.Stderr, "sdf-recall: %s\n", live.Redact(err.Error(), opt.Key))
			os.Exit(1)
		}
		fmt.Fprint(os.Stdout, body)
	}
}
