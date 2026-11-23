package verify

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
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
	Path        string
	Line        int64
	MatchSHA256 string
}

// scanDiff reads `git diff -U0 --no-renames` output and checks every added
// line. It returns the findings and the paths of binary files it could not
// read.
func scanDiff(diff []byte) (findings []SecretFinding, binary []string) {
	sc := bufio.NewScanner(bytes.NewReader(diff))
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	path := ""
	inHunk := false
	var line int64
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "diff --git "):
			path, inHunk = "", false
		case !inHunk && strings.HasPrefix(l, "+++ "):
			path = diffPath(strings.TrimPrefix(l, "+++ "))
		case !inHunk && strings.HasPrefix(l, "Binary files "):
			binary = append(binary, binaryPath(l))
		case strings.HasPrefix(l, "@@ "):
			inHunk = true
			line = hunkStart(l)
		case inHunk && strings.HasPrefix(l, "+"):
			text := l[1:]
			for _, r := range secretRules {
				for _, m := range r.Re.FindAllString(text, -1) {
					sum := sha256.Sum256([]byte(m))
					findings = append(findings, SecretFinding{Rule: r.ID, Path: path, Line: line, MatchSHA256: hex.EncodeToString(sum[:])})
				}
			}
			line++
		case inHunk && strings.HasPrefix(l, " "):
			line++
		}
	}
	return findings, binary
}

func diffPath(s string) string {
	if s == "/dev/null" {
		return s
	}
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	return strings.TrimPrefix(s, "b/")
}

func binaryPath(l string) string {
	// "Binary files a/x and b/y differ"
	l = strings.TrimSuffix(strings.TrimPrefix(l, "Binary files "), " differ")
	if i := strings.LastIndex(l, " and "); i >= 0 {
		return diffPath(l[i+len(" and "):])
	}
	return l
}

// hunkStart reads c from "@@ -a,b +c,d @@".
func hunkStart(l string) int64 {
	i := strings.Index(l, " +")
	if i < 0 {
		return 0
	}
	rest := l[i+2:]
	if j := strings.IndexAny(rest, ", "); j >= 0 {
		rest = rest[:j]
	}
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}
