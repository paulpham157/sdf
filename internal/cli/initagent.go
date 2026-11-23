package cli

import (
	"fmt"
	"strings"
)

// unknownAgentMessage explains an sdf init argument that is not a flag and
// not an agent: the closest agent name, when one is close, and the list.
func unknownAgentMessage(arg string) string {
	msg := fmt.Sprintf("sdf init: unknown agent %q.", arg)
	if s := suggestAgent(arg); s != "" {
		msg += fmt.Sprintf(" Did you mean %s?", s)
	}
	return msg + " Agents: " + strings.Join(initAgents, ", ") + "."
}

// suggestAgent returns the agent name closest to arg, or "" when none is
// close. A name that one agent starts with (claude, gem) wins; otherwise the
// smallest edit distance, if it is at most a third of the name (at least 2).
func suggestAgent(arg string) string {
	in := strings.ToLower(strings.TrimSpace(arg))
	if in == "" {
		return ""
	}
	var prefixed []string
	for _, a := range initAgents {
		if strings.HasPrefix(a, in) || strings.HasPrefix(in, a) {
			prefixed = append(prefixed, a)
		}
	}
	if len(prefixed) == 1 {
		return prefixed[0]
	}
	best, bestDist := "", -1
	for _, a := range initAgents {
		d := editDistance(in, a)
		if bestDist < 0 || d < bestDist {
			best, bestDist = a, d
		}
	}
	limit := len(best) / 3
	if limit < 2 {
		limit = 2
	}
	if bestDist <= limit {
		return best
	}
	return ""
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
