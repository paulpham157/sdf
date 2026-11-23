package ledger_test

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger"
)

func TestValidIDRules(t *testing.T) {
	for _, tc := range []struct {
		id   string
		rule string // "" means valid
	}{
		{"add-widget", ""},
		{"01JD5Z9X8Y7W6V5T4S3R2Q1P0N", ""}, // canonical ULID, uppercase
		{"01jd5z9x8y7w6v5t4s3r2q1p0n", ""}, // the same ULID in lowercase is a different id
		{"Add_Widget.v2", ""},
		{"a..b", ""}, // ".." inside an id is not a path: there are no separators
		{"9", ""},
		{strings.Repeat("a", 128), ""},
		{"", "empty"},
		{strings.Repeat("a", 129), "longer than 128 bytes"},
		{"a\xffb", "not valid UTF-8"},
		{"a\nb", "control character"},
		{"a\x00b", "control character"},
		{"a\x7fb", "control character"},
		{"a b", "whitespace"},
		{"a\tb", "control character"},
		{"a\u00a0b", "whitespace"},
		{"a/b", "path separator"},
		{`a\b`, "path separator"},
		{"../etc", "path separator"},
		{"..", "start with an ASCII letter or digit"},
		{".hidden", "start with an ASCII letter or digit"},
		{"-rf", "start with an ASCII letter or digit"},
		{"_x", "start with an ASCII letter or digit"},
		{"é", "start with an ASCII letter or digit"},
		{"aé", "may contain only"},
		{"a:b", "may contain only"},
		{"a*b", "may contain only"},
		{"a\u2028b", "whitespace"},
	} {
		err := ledger.ValidID(tc.id)
		if tc.rule == "" {
			if err != nil {
				t.Errorf("%q: %v", tc.id, err)
			}
			continue
		}
		if !errors.Is(err, ledger.ErrInvalidID) || !errors.Is(err, ledger.ErrInvalid) || !strings.Contains(err.Error(), tc.rule) {
			t.Errorf("%q: %v, want %q", tc.id, err, tc.rule)
		}
	}
}

// The verifier's task.ValidID (PR #190) is UTF-8, no control character,
// and this pattern. ValidID must accept exactly the same ids.
func TestValidIDMatchesTheVerifierRule(t *testing.T) {
	const verifierPattern = `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`
	if ledger.IDPattern != verifierPattern {
		t.Fatalf("IDPattern %q drifted from the verifier's %q", ledger.IDPattern, verifierPattern)
	}
	re := regexp.MustCompile(verifierPattern)
	var corpus []string
	for c := 0; c < 256; c++ {
		corpus = append(corpus, string([]byte{byte(c)}), "a"+string([]byte{byte(c)}), "a"+string([]byte{byte(c)})+"b")
	}
	corpus = append(corpus, "", "é", "a\u2028", "a\u00a0", strings.Repeat("Z", 128), strings.Repeat("Z", 129))
	for _, id := range corpus {
		if got, want := ledger.ValidID(id) == nil, re.MatchString(id); got != want {
			t.Errorf("%q: ValidID %v, verifier %v", id, got, want)
		}
	}
}

func TestAppendAndGetRejectAnInvalidID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	s := open(t, path)
	p := []byte("{}")
	for _, id := range []string{"a/b", "-x", "a b", "../x", ""} {
		if _, err := s.Append(ctx, taskKind, id, p, ledger.PayloadHash(taskKind, p)); !errors.Is(err, ledger.ErrInvalidID) {
			t.Fatalf("append %q: %v", id, err)
		}
		if _, _, err := s.Get(ctx, taskKind, id); !errors.Is(err, ledger.ErrInvalidID) {
			t.Fatalf("get %q: %v", id, err)
		}
	}
	if rep, _ := s.Verify(ctx); !rep.Empty {
		t.Fatalf("a rejected append wrote: %+v", rep)
	}
}

func TestVerifyFlagsAStoredInvalidID(t *testing.T) {
	path, s := seeded(t)
	head, _ := s.Head(context.Background())
	_ = s.Close()
	// Rewrite the head's id consistently, entry hash included, so only the
	// id rule can catch it.
	forged := head
	forged.ID = "../task-3"
	forged.EntryHash = ledger.EntryHash(forged)
	tamper(t, path, `UPDATE ledger_entries SET id = ?, entry_hash = ? WHERE seq = ?`, forged.ID, forged.EntryHash, head.Seq)
	verifyFails(t, path, head.Seq, ledger.ProblemModified, "id contains a path separator")
}
