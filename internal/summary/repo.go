package summary

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/paulpham157/sdf/internal/record"
)

// fullSHA is a commit id that can be looked up without expanding an abbreviation.
var fullSHA = regexp.MustCompile(`(?i)^[0-9a-f]{40}$`)

// repositoryFacts reads working directories and produced commits from attributes
// the agents actually export.
//
// pi.cwd is set by pi-otel (ATTR_PI_CWD in pi-otel 0.3.0).
// workspace.host_paths is a Claude Code event attribute (monitoring-usage).
// vcs.ref.head.revision and git_commit_id on claude_code.tool_result are the
// commit a successful git commit tool created, and only when
// OTEL_LOG_TOOL_DETAILS=1. An abbreviated git_commit_id is not used.
// vcs.repository.* is the session repository when
// OTEL_METRICS_INCLUDE_REPOSITORY=true. Those keys are documented at
// https://code.claude.com/docs/en/monitoring-usage
func repositoryFacts(records []record.Record) RepositoryFacts {
	facts := RepositoryFacts{WorkDirs: []string{}, CommitSHAs: []string{}}
	var (
		dirs    []string
		shas    []string
		minNano int64
		maxNano int64
		seen    bool
	)
	url, urlSet := "", false
	owner, ownerSet := "", false
	name, nameSet := "", false
	provider, providerSet := "", false
	for _, rec := range records {
		for _, stamp := range stamps(rec) {
			if !seen || stamp < minNano {
				minNano = stamp
			}
			if !seen || stamp > maxNano {
				maxNano = stamp
			}
			seen = true
		}
		for _, source := range []map[string]any{rec.Attributes, rec.Resource} {
			dirs = append(dirs, pathsFrom(source["pi.cwd"], &facts.AmbiguousWorkDirs)...)
			dirs = append(dirs, pathsFrom(source["workspace.host_paths"], &facts.AmbiguousWorkDirs)...)
			noteIdentity(&url, &urlSet, &facts.AmbiguousRepository, textAt(source, "vcs.repository.url.full"))
			noteIdentity(&owner, &ownerSet, &facts.AmbiguousRepository, textAt(source, "vcs.owner.name"))
			noteIdentity(&name, &nameSet, &facts.AmbiguousRepository, textAt(source, "vcs.repository.name"))
			noteIdentity(&provider, &providerSet, &facts.AmbiguousRepository, textAt(source, "vcs.provider.name"))
		}
		if rec.Signal == "logs" && deref(rec.Name) == "claude_code.tool_result" {
			shas = append(shas, commitSHAs(rec.Attributes)...)
		}
	}
	if seen {
		facts.StartUnixNano = &minNano
		facts.EndUnixNano = &maxNano
	}
	if !facts.AmbiguousRepository {
		facts.RepositoryURL = url
		facts.Owner = owner
		facts.RepositoryName = name
		facts.Provider = provider
	}
	facts.WorkDirs = nonNilStr(uniqueStrings(dirs))
	facts.CommitSHAs = nonNilStr(uniqueStrings(shas))
	sort.Strings(facts.CommitSHAs)
	return facts
}

func pathsFrom(value any, ambiguous *bool) []string {
	switch t := value.(type) {
	case nil:
		return nil
	case string:
		if text := strings.TrimSpace(t); text != "" {
			return []string{text}
		}
	case []any:
		if len(t) > 1 {
			*ambiguous = true
			return nil
		}
		if len(t) == 1 {
			if text, ok := t[0].(string); ok && strings.TrimSpace(text) != "" {
				return []string{strings.TrimSpace(text)}
			}
		}
	case []string:
		if len(t) > 1 {
			*ambiguous = true
			return nil
		}
		if len(t) == 1 && strings.TrimSpace(t[0]) != "" {
			return []string{strings.TrimSpace(t[0])}
		}
	}
	return nil
}

func textAt(source map[string]any, key string) string {
	if source == nil {
		return ""
	}
	text := record.SummaryText(source[key])
	if text == nil {
		return ""
	}
	return *text
}

func noteIdentity(dst *string, set *bool, ambiguous *bool, value string) {
	if value == "" {
		return
	}
	if !*set {
		*dst = value
		*set = true
		return
	}
	if *dst != value {
		*ambiguous = true
		*dst = ""
	}
}

func commitSHAs(attributes map[string]any) []string {
	var out []string
	if sha, ok := fullCommit(textAt(attributes, "vcs.ref.head.revision")); ok {
		out = append(out, sha)
	}
	if sha, ok := fullCommit(textAt(attributes, "git_commit_id")); ok {
		out = append(out, sha)
	}
	out = append(out, parameterCommit(attributes["tool_parameters"])...)
	return out
}

func parameterCommit(value any) []string {
	var raw string
	switch t := value.(type) {
	case string:
		raw = t
	case map[string]any:
		return oneCommit(t["git_commit_id"])
	default:
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	return oneCommit(parsed["git_commit_id"])
}

func oneCommit(value any) []string {
	text := record.SummaryText(value)
	if text == nil {
		return nil
	}
	if sha, ok := fullCommit(*text); ok {
		return []string{sha}
	}
	return nil
}

func fullCommit(value string) (string, bool) {
	if !fullSHA.MatchString(value) {
		return "", false
	}
	return strings.ToLower(value), true
}

func uniqueStrings(values []string) []string {
	return unique(values)
}
