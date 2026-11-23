package verify

import (
	"strings"
	"testing"
)

func TestScanTextSkipsOldLinesAndNeverKeepsTheMatch(t *testing.T) {
	key := "xoxb-" + strings.Repeat("1234567890", 2)
	text := []byte("nothing here\ntoken " + key + "\nold " + key + "\x00bin\n")
	got := scanText("c1", "a.txt", text, lineSet([]byte("old "+key+"\x00bin")))
	if len(got) != 1 || got[0].Path != "a.txt" || got[0].Line != 2 || got[0].Rule != "slack-token" || got[0].Commit != "c1" {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(got[0].MatchSHA256, key) || len(got[0].MatchSHA256) != 64 {
		t.Fatal(got[0].MatchSHA256)
	}
}

func TestParseRawKeepsQuotedAndTabbedPaths(t *testing.T) {
	z := strings.Repeat("0", 40)
	raw := ":000000 100644 " + z + " " + strings.Repeat("a", 40) + " A\x00we ird\"name\t.txt\x00" +
		":100644 000000 " + strings.Repeat("b", 40) + " " + z + " D\x00gone\x00"
	got := parseRaw([]byte(raw))
	if len(got) != 2 || got[0].path != "we ird\"name\t.txt" || got[0].status != "A" || got[1].status != "D" {
		t.Fatalf("%+v", got)
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
