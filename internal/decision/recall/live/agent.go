package live

import (
	"context"
	"fmt"
	"strings"
)

type protocol string

const (
	protocolChat      protocol = "openai-chat"
	protocolResponses protocol = "openai-responses"
	protocolAnthropic protocol = "anthropic"
)

// support is whether an agent can call the configured provider. A false
// result is reported and the CLI is not started.
type support struct {
	OK       bool
	Reason   string
	Protocol protocol
}

// localTokenEnv is the variable a CLI reads when it needs a credential for
// the localhost proxy. The value is a per-run placeholder, not the provider
// key. The runner does not set SDF_RECALL_API_KEY on the CLI.
const localTokenEnv = "SDF_RECALL_LOCAL_TOKEN"

type spec struct {
	Repo          string
	Home          string
	OpenAIBase    string
	AnthropicBase string
	Model         string
	OTLP          string
	SkillSrc      string
	Prompt        string
	// LocalToken is the placeholder the CLI sends to the proxy. Empty
	// means the fixed non-secret word recall-local.
	LocalToken string
}

func (s spec) token() string {
	if s.LocalToken != "" {
		return s.LocalToken
	}
	return "recall-local"
}

type invocation struct {
	// Argv[0] is the CLI name. The runner replaces it with the looked-up path.
	Argv []string
	// Env is non-secret process environment. A value that contains the
	// provider key is dropped.
	Env map[string]string
	// SecretEnv names variables the runner sets to the local placeholder.
	// The provider key is not one of those values.
	SecretEnv []string
}

type agent interface {
	ID() string
	Name() string
	Binary() string
	Support() support
	InstallCommands() [][]string
	// IgnoreInstallScripts is false only for a package whose postinstall
	// is required to produce a runnable binary. OpenCode is that package.
	IgnoreInstallScripts() bool
	InstallHint() string
	Prepare(ctx context.Context, spec spec) (invocation, error)
}

func registry() []agent {
	return []agent{
		opencodeAgent{},
		piAgent{},
		qwenAgent{},
		grokAgent{},
		copilotAgent{},
		codexAgent{},
		claudeAgent{},
		geminiAgent{},
	}
}

func selectAgents(ids []string) ([]agent, error) {
	all := registry()
	if len(ids) == 0 {
		return all, nil
	}
	by := map[string]agent{}
	var known []string
	for _, a := range all {
		by[a.ID()] = a
		known = append(known, a.ID())
	}
	seen := map[string]bool{}
	var out []agent
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate agent %q", id)
		}
		seen[id] = true
		a, ok := by[id]
		if !ok {
			return nil, fmt.Errorf("unknown agent %q (agents: %s)", id, strings.Join(known, ", "))
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no agents")
	}
	return out, nil
}

func signalURL(origin, signal string) string {
	return strings.TrimRight(origin, "/") + "/v1/" + signal
}
