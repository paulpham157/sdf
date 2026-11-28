package live

import (
	"flag"
	"fmt"
	"io"
	"strings"
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
	k := fs.Int("k", 0, "runs per scenario")
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
	mode := ModeBoth
	switch {
	case *asJSON && !*asMD:
		mode = ModeJSON
	case *asMD && !*asJSON:
		mode = ModeMarkdown
	}
	return opt, mode, nil
}
