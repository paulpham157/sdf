package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/summary"
)

// contentAgents maps the names --store-content accepts to summary.ToolOf
// classes. The short names match sdf init.
var contentAgents = map[string]string{
	"claude-code": "claude-code", "claude": "claude-code",
	"codex":    "codex",
	"opencode": "opencode",
	"pi":       "pi",
	"gemini":   "gemini-cli", "gemini-cli": "gemini-cli",
	"qwen": "qwen-code", "qwen-code": "qwen-code",
	"grok": "grok-cli", "grok-cli": "grok-cli",
	"copilot": "copilot-cli", "copilot-cli": "copilot-cli",
}

// storeContentSpec is the parsed --store-content / SDF_STORE_CONTENT value:
// off, every agent, or a set of agents.
type storeContentSpec struct {
	all    bool
	agents map[string]bool
}

func (s storeContentSpec) on() bool { return s.all || len(s.agents) > 0 }

// parseStoreContent reads "", "0", "false", "off" (off); "1", "true", "on",
// "all" (every agent); or a comma-separated list of agent names.
func parseStoreContent(v string) (storeContentSpec, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "off", "no":
		return storeContentSpec{}, nil
	case "1", "true", "on", "yes", "all":
		return storeContentSpec{all: true}, nil
	}
	spec := storeContentSpec{agents: map[string]bool{}}
	for _, part := range strings.Split(v, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		tool, ok := contentAgents[name]
		if !ok {
			return storeContentSpec{}, fmt.Errorf("unknown agent %q (want all, or a list of: claude-code, codex, opencode, pi, gemini-cli, qwen-code, grok-cli, copilot-cli)", part)
		}
		spec.agents[tool] = true
	}
	if len(spec.agents) == 0 {
		return storeContentSpec{}, nil
	}
	return spec, nil
}

// keep is the store's content predicate, nil when off.
func (s storeContentSpec) keep() func(record.Record) bool {
	switch {
	case s.all:
		return func(record.Record) bool { return true }
	case len(s.agents) > 0:
		agents := s.agents
		return func(rec record.Record) bool { return agents[summary.ToolOf(rec)] }
	}
	return nil
}

func (s storeContentSpec) describe() string {
	if s.all {
		return "every agent"
	}
	names := make([]string, 0, len(s.agents))
	for name := range s.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
