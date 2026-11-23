package verify

import (
	"strings"
	"testing"
)

func TestScanDiffLinesAndBinary(t *testing.T) {
	key := "xoxb-" + strings.Repeat("1234567890", 2)
	diff := strings.Join([]string{
		"diff --git a/a.txt b/a.txt",
		"--- a/a.txt",
		"+++ b/a.txt",
		"@@ -0,0 +10,2 @@",
		"+nothing here",
		"+token " + key,
		"diff --git a/b.bin b/b.bin",
		"Binary files /dev/null and b/b.bin differ",
		"diff --git a/c.txt b/c.txt",
		"--- a/c.txt",
		"+++ b/c.txt",
		"@@ -1 +1 @@",
		"-token " + key, // removed lines are not findings
		"+++ not a header, an added line starting with ++",
		"",
	}, "\n")
	findings, binary := scanDiff([]byte(diff))
	if len(findings) != 1 || findings[0].Path != "a.txt" || findings[0].Line != 11 || findings[0].Rule != "slack-token" {
		t.Fatalf("%+v", findings)
	}
	if strings.Contains(findings[0].MatchSHA256, key) || len(findings[0].MatchSHA256) != 64 {
		t.Fatal(findings[0].MatchSHA256)
	}
	if len(binary) != 1 || binary[0] != "b.bin" {
		t.Fatalf("%v", binary)
	}
}

func TestEachRuleMatchesItsFormat(t *testing.T) {
	samples := map[string]string{
		"private-key":               "-----BEGIN " + "RSA PRIVATE KEY-----",
		"aws-access-key-id":         "AKIA" + strings.Repeat("Z", 16),
		"github-token":              "ghp_" + strings.Repeat("x", 36),
		"github-fine-grained-token": "github_pat_" + strings.Repeat("x", 60),
		"gitlab-token":              "glpat-" + strings.Repeat("x", 20),
		"slack-token":               "xoxp-" + strings.Repeat("1", 12),
		"anthropic-api-key":         "sk-ant-" + strings.Repeat("x", 30),
		"openai-api-key":            "sk-proj-" + strings.Repeat("x", 20) + "T3BlbkFJ" + strings.Repeat("y", 20),
		"xai-api-key":               "xai-" + strings.Repeat("x", 48),
		"google-api-key":            "AIza" + strings.Repeat("x", 35),
		"stripe-live-key":           "sk_" + "live_" + strings.Repeat("x", 24),
	}
	if len(samples) != len(secretRules) {
		t.Fatalf("%d samples for %d rules", len(samples), len(secretRules))
	}
	for _, r := range secretRules {
		if !r.Re.MatchString(samples[r.ID]) {
			t.Errorf("%s does not match its sample", r.ID)
		}
		if r.Re.MatchString("ordinary text with sk- and AKIA and ghp_ prefixes") {
			t.Errorf("%s matches plain text", r.ID)
		}
	}
	if h := SecretRulesetHash(); !strings.HasPrefix(h, "sha256:") || h != SecretRulesetHash() {
		t.Fatal(h)
	}
}
