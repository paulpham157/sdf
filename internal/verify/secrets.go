package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/paulpham157/sdf/internal/task"
)

// SecretRulesetID names the rule set below. Change it when a rule changes.
const SecretRulesetID = "sdf.secrets/v1"

// secretRule is one pattern. The set is small on purpose: each rule is a
// credential format with a fixed prefix, so a match is rarely a false
// positive, and the whole set can be read in one sitting. It is not a
// replacement for a full scanner such as gitleaks.
type secretRule struct {
	ID string
	Re *regexp.Regexp
}

var secretRules = []secretRule{
	{"private-key", regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)},
	{"aws-access-key-id", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"github-token", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b`)},
	{"github-fine-grained-token", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{50,}\b`)},
	{"gitlab-token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`)},
	{"anthropic-api-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"openai-api-key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}T3BlbkFJ[A-Za-z0-9_-]{20,}`)},
	{"xai-api-key", regexp.MustCompile(`\bxai-[A-Za-z0-9]{40,}\b`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"stripe-live-key", regexp.MustCompile(`\b(?:sk|rk)_live_[0-9A-Za-z]{20,}\b`)},
}

// SecretRulesetHash is the sha256 of the rule set's canonical JSON
// ([{"id","pattern"}], in order), so evidence names exactly the rules used.
func SecretRulesetHash() string {
	rules := make([]any, len(secretRules))
	for i, r := range secretRules {
		rules[i] = map[string]any{"id": r.ID, "pattern": r.Re.String()}
	}
	b, err := task.Canonical(map[string]any{"id": SecretRulesetID, "rules": rules})
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SecretFinding is one match. The matched text is never kept: only its
// sha256, so the evidence does not repeat the secret.
type SecretFinding struct {
	Rule        string
	Commit      string
	Path        string // a file path, or "" for the commit message
	Line        int64
	MatchSHA256 string
}

// scanText checks each line of text (bytes, so binary content is scanned
// too) and returns the findings, numbered from 1. A line in old, the
// parent's copy of the file, is not new and is skipped.
func scanText(commit, path string, text []byte, old map[string]bool) []SecretFinding {
	var out []SecretFinding
	for n, l := range bytes.Split(text, []byte("\n")) {
		if old[string(l)] {
			continue
		}
		for _, r := range secretRules {
			for _, m := range r.Re.FindAll(l, -1) {
				sum := sha256.Sum256(m)
				out = append(out, SecretFinding{Rule: r.ID, Commit: commit, Path: path, Line: int64(n + 1), MatchSHA256: hex.EncodeToString(sum[:])})
			}
		}
	}
	return out
}

// safePath is p, or "path-sha256:<hex>" when p itself matches a rule, so a
// secret used as a file name is not copied into the evidence.
func safePath(p string) string {
	for _, r := range secretRules {
		if r.Re.MatchString(p) {
			sum := sha256.Sum256([]byte(p))
			return "path-sha256:" + hex.EncodeToString(sum[:])
		}
	}
	return p
}

func lineSet(text []byte) map[string]bool {
	set := map[string]bool{}
	for _, l := range bytes.Split(text, []byte("\n")) {
		set[string(l)] = true
	}
	return set
}

// rawChange is one record of `git diff-tree -r -z --no-abbrev` output.
type rawChange struct {
	oldMode, newMode, oldOID, newOID, status, path string
}

func parseRaw(b []byte) []rawChange {
	var out []rawChange
	toks := strings.Split(string(b), "\x00")
	for i := 0; i+1 < len(toks); i += 2 {
		f := strings.Fields(strings.TrimPrefix(toks[i], ":"))
		if len(f) != 5 {
			continue
		}
		out = append(out, rawChange{f[0], f[1], f[2], f[3], f[4], toks[i+1]})
	}
	return out
}
