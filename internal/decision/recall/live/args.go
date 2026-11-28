package live

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/paulpham157/sdf/internal/decision/recall"
)

// Mode selects which report encodings the command writes.
type Mode int

const (
	// ModeBoth writes the markdown table and then the JSON document.
	ModeBoth Mode = iota
	// ModeMarkdown writes the table only.
	ModeMarkdown
	// ModeJSON writes the JSON document only.
	ModeJSON
)

// ParseArgs applies command flags to opt. The key and the provider URLs
// stay in the environment. --k must be from 1 to 100 when it is set.
func ParseArgs(args []string, opt Options) (Options, Mode, error) {
	fs := flag.NewFlagSet("sdf-recall", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agents := fs.String("agents", "", "comma-separated agent ids")
	scenarios := fs.String("scenarios", "", "comma-separated fixture names")
	k := fs.Int("k", 0, "runs per scenario")
	parallel := fs.Int("parallel", 0, "scenario runs at once")
	out := fs.String("out", "", "directory for per-run stderr logs")
	merge := fs.String("merge", "", "comma-separated JSON reports to pool")
	asJSON := fs.Bool("json", false, "write JSON")
	asMD := fs.Bool("markdown", false, "write markdown")
	install := fs.Bool("install", opt.Install, "install missing CLIs this repo names")
	noInstall := fs.Bool("no-install", false, "do not install CLIs")
	if err := fs.Parse(args); err != nil {
		return Options{}, 0, err
	}
	if fs.NArg() != 0 {
		return Options{}, 0, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	kSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "k" {
			kSet = true
		}
	})
	if kSet && (*k < 1 || *k > 100) {
		return Options{}, 0, fmt.Errorf("--k must be from 1 to 100")
	}
	if *k > 0 {
		opt.K = *k
	}
	pSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "parallel" {
			pSet = true
		}
	})
	if pSet && (*parallel < 1 || *parallel > 32) {
		return Options{}, 0, fmt.Errorf("--parallel must be from 1 to 32")
	}
	if *parallel > 0 {
		opt.Parallel = *parallel
	}
	if *out != "" {
		opt.OutDir = *out
	}
	if *merge != "" {
		paths := splitList(*merge)
		if len(paths) == 0 {
			return Options{}, 0, fmt.Errorf("--merge requires a list")
		}
		opt.Merge = paths
	}
	if *scenarios != "" {
		names := splitList(*scenarios)
		if len(names) == 0 {
			return Options{}, 0, fmt.Errorf("--scenarios requires a list")
		}
		all, err := recall.Fixtures()
		if err != nil {
			return Options{}, 0, err
		}
		if _, err := filterFixtures(all, names); err != nil {
			return Options{}, 0, err
		}
		opt.FixtureNames = names
	}
	if *agents != "" {
		var ids []string
		for _, part := range strings.Split(*agents, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				ids = append(ids, part)
			}
		}
		if len(ids) == 0 {
			return Options{}, 0, fmt.Errorf("--agents requires a list")
		}
		if _, err := selectAgents(ids); err != nil {
			return Options{}, 0, err
		}
		opt.Agents = ids
	}
	opt.Install = *install
	if *noInstall {
		opt.Install = false
	}
	if len(opt.Merge) > 0 && (len(opt.Agents) > 0 || len(opt.FixtureNames) > 0) {
		return Options{}, 0, fmt.Errorf("--merge does not take --agents or --scenarios")
	}
	mode := ModeBoth
	switch {
	case *asJSON && !*asMD:
		mode = ModeJSON
	case *asMD && !*asJSON:
		mode = ModeMarkdown
	}
	return opt, mode, nil
}
