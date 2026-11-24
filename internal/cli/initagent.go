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
// close. An agent name that arg starts (at least three letters: claude,
// gem) wins, as does arg being an agent name plus a "-" or "_" suffix
// (opencode-v2). Otherwise the agent at the smallest edit distance is
// suggested if that distance is at most a third of the agent's name, so a
// two-letter name such as pi is never suggested for unrelated input.
func suggestAgent(arg string) string {
	in := strings.ToLower(strings.TrimSpace(arg))
	if in == "" {
		return ""
	}
	var prefixed []string
	for _, a := range initAgents {
		agentPrefix := len(in) >= 3 && strings.HasPrefix(a, in)
		withSuffix := len(in) > len(a) && strings.HasPrefix(in, a) && (in[len(a)] == '-' || in[len(a)] == '_')
		if agentPrefix || withSuffix {
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
	if bestDist <= len(best)/3 {
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
