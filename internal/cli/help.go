package cli

import (
	"fmt"
	"io"
	"strings"
)

// command is one top-level subcommand and the line sdf --help shows for it.
type command struct {
	name, summary string
}

var commands = []command{
	{"serve", "Run the OTLP receiver and the read-only UI at http://127.0.0.1:4320/"},
	{"summary", "Print one block per stored session (tokens, cost, tools, outcome)"},
	{"check", "Run the case set embedded in the binary against the summary"},
	{"init", "Print or write an agent's telemetry settings; --check probes the receiver"},
	{"prices", "Refresh the cached OpenRouter price table (the only network call for prices)"},
	{"decision", "Record, link, list, or retract decisions (add, link, list, retract)"},
	{"audit", "Verify the hash-chained ledger, or print its head"},
	{"task", "Declare a task's acceptance checks, or show a declared task"},
	{"verify", "Run a declared task's checks against a commit and print the verdict"},
	{"redact", "Drop stored prompt, response, tool, and error text from a database"},
	{"version", "Print the version, commit, build date, Go version, and platform"},
}

// initAgents are the agent names sdf init accepts, in the order help lists them.
var initAgents = []string{"claude-code", "codex", "opencode", "pi", "qwen", "grok", "gemini", "copilot"}

func knownCommand(name string) bool {
	for _, c := range commands {
		if c.name == name {
			return true
		}
	}
	return false
}

func writeTopHelp(w io.Writer) {
	var b strings.Builder
	b.WriteString(usageLine + "\n\n")
	b.WriteString("Start here: run sdf serve in its own terminal, then sdf init <agent> --write,\n")
	b.WriteString("restart the agent, and open http://127.0.0.1:4320/ or run sdf summary.\n\n")
	b.WriteString("Commands:\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-9s %s\n", c.name, c.summary)
	}
	b.WriteString("\nAgents for init: " + strings.Join(initAgents, ", ") + "\n")
	b.WriteString("Run sdf help <command> or sdf <command> --help for its flags.\n")
	fmt.Fprint(w, b.String())
}

// cmdHelp runs sdf help [command]: the top-level help, or the command's own
// --help output.
func cmdHelp(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeTopHelp(stdout)
		return 0
	}
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: sdf help [command]")
		return 2
	}
	name := args[0]
	if name == "help" {
		writeTopHelp(stdout)
		return 0
	}
	if !knownCommand(name) {
		fmt.Fprintf(stderr, "unrecognized command %q; run sdf --help for the list\n", name)
		return 2
	}
	return mainWith([]string{name, "--help"}, stdout, stderr)
}

const serveHelp = `Runs the OTLP receiver and, unless --no-ui, the read-only UI. Leave it running
while an agent works; stop it with Ctrl-C.

  --host HOST            OTLP bind host (default 127.0.0.1; keep it on localhost)
  --port PORT            OTLP/HTTP port (default 4318)
  --grpc-port PORT       OTLP/gRPC port (default 4317)
  --db PATH              SQLite file (default sdf-telemetry.sqlite in the current directory)
  --ui-addr HOST:PORT    UI address (default 127.0.0.1:4320)
  --no-ui                OTLP ports only
  --otlp-token TOKEN     require Authorization: Bearer TOKEN on OTLP writes
  --otlp-token-file PATH read that token from a file
  --prices, --repo, --github, --budget   as for sdf summary, applied to the UI`

const summaryHelp = `Prints one block per stored session, then the unattributed records.

  --db PATH              SQLite file sdf serve wrote (default sdf-telemetry.sqlite
                         in the current directory)
  --session ID           only that session
  --json                 machine-readable output
  --all-spans            also print the internal spans the tree hides
  --prices bundled|PATH  price table for computed cost (default: cache, then bundled)
  --repo PATH            git checkout used to link sessions to commits
  --github               ask GitHub whether linked pull requests merged
  --budget PATH          local budget file`

const checkHelp = `Runs the case set embedded in the binary against the summary and prints one
line per case. not_checked is reported and is not a failure. No network.

  --cases DIR            run the cases in DIR instead`

const initHelp = `Prints the settings that point an agent at sdf serve. Nothing is written
without --write. Agents: claude-code, codex, opencode, pi, qwen, grok, gemini, copilot.

  --write                write the settings to the agent's file (an existing file is
                         copied to a .sdf-bak sibling first); see docs/receiver.md
  --install              pi: run pi install npm:pi-otel
  --global               pi: write ~/.pi/agent/settings.json, not the project file
  --check                probe the receiver on --port and --grpc-port, and whether
                         it would accept a write with the token the hooks send
  --host, --port, --grpc-port   receiver address (defaults 127.0.0.1, 4318, 4317)
  --git-hooks            install git hooks in this checkout that record commit SHAs
  --uninstall-git-hooks  remove them
  --git-template         install the hooks into git's template dir (--set-template-dir sets init.templateDir)
  --uninstall-git-template  remove them (--unset-template-dir clears init.templateDir)`

const pricesHelp = `Fetches the OpenRouter model catalog and caches it for computed cost. This is
the only command that calls the network for prices.

  --url URL              catalog URL (default OpenRouter)
  --out PATH             where to write it (default the sdf cache directory)`

const decisionHelp = `Records decisions and the links between decisions, sessions, commits, and
pull requests in the same SQLite file as sdf serve.

  add      sdf decision add --id ID --statement TEXT --status hypothesis|decision
  link     sdf decision link --relation REL --from KIND:REF --to KIND:REF
  list     sdf decision list [--json]
  graph    sdf decision graph [--json]
  retract  sdf decision retract --id ID [--reason TEXT]
  audit    sdf decision audit [--json] [--since RFC3339] [--until RFC3339]
Run sdf decision <add|link|list|graph|retract|audit> --help for the flags.`

const auditHelp = `Checks the hash-chained ledger that sdf task declare --ledger and sdf verify
--ledger append to.

  verify Walk the chain and check every entry's hash
  head   Print the hash of the last entry`

const taskHelp = `Declares a task's acceptance checks before an agent runs, or shows one.

  declare  sdf task declare --id ID --check NAME=COMMAND ...
  show     sdf task show ID
Run sdf task declare --help or sdf task show --help for the flags.`
