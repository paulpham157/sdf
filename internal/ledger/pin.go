package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// pinHash is "sha256:" followed by 64 lowercase hex digits.
var pinHash = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ChainError is a broken chain reported by VerifyPrefix. Unwrap returns
// ErrChainBroken. Failure is the first problem Verify found.
type ChainError struct {
	Failure Failure
}

func (e *ChainError) Error() string {
	if e == nil {
		return ErrChainBroken.Error()
	}
	f := e.Failure
	where := fmt.Sprintf("seq %d", f.Seq)
	if f.Kind != "" || f.ID != "" {
		where += fmt.Sprintf(" (%q %q)", f.Kind, f.ID)
	}
	return fmt.Sprintf("%s: %s: %s: %s", ErrChainBroken, where, Safe(string(f.Problem)), Safe(f.Detail))
}

func (e *ChainError) Unwrap() error { return ErrChainBroken }

// ParsePin reads a prefix pin. s may be "seq:sha256:…", the text
// `sdf audit head` prints ("seq N entry_hash HASH"), or the JSON
// `sdf audit head --json` prints. seq is a non-negative integer. The
// hash must be "sha256:" and 64 lowercase hex digits.
func ParsePin(s string) (int64, string, error) {
	s = strings.TrimSpace(s)
	var doc struct {
		Head struct {
			Seq       *int64 `json:"seq"`
			EntryHash string `json:"entry_hash"`
		} `json:"head"`
	}
	if strings.HasPrefix(s, "{") {
		if err := json.Unmarshal([]byte(s), &doc); err != nil {
			return 0, "", fmt.Errorf("ledger: pin JSON: %w", err)
		}
		if doc.Head.Seq == nil || doc.Head.EntryHash == "" {
			return 0, "", errors.New("ledger: pin JSON is missing head.seq or head.entry_hash")
		}
		if err := checkPin(*doc.Head.Seq, doc.Head.EntryHash); err != nil {
			return 0, "", err
		}
		return *doc.Head.Seq, doc.Head.EntryHash, nil
	}
	if fields := strings.Fields(s); len(fields) == 4 && fields[0] == "seq" && fields[2] == "entry_hash" {
		if fields[1] != "0" && (strings.HasPrefix(fields[1], "0") || strings.Trim(fields[1], "0123456789") != "") {
			return 0, "", fmt.Errorf("ledger: pin %q is not seq:sha256: followed by 64 lowercase hex digits", s)
		}
		seq, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, "", fmt.Errorf("ledger: pin %q is not seq:sha256: followed by 64 lowercase hex digits", s)
		}
		if err := checkPin(seq, fields[3]); err != nil {
			return 0, "", err
		}
		return seq, fields[3], nil
	}
	return parseSeqHash(s)
}

func parseSeqHash(s string) (int64, string, error) {
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return 0, "", fmt.Errorf("ledger: pin %q is not seq:sha256: followed by 64 lowercase hex digits", s)
	}
	seqStr, hash := s[:i], s[i+1:]
	if seqStr != "0" && (strings.HasPrefix(seqStr, "0") || strings.Trim(seqStr, "0123456789") != "") {
		return 0, "", fmt.Errorf("ledger: pin %q is not seq:sha256: followed by 64 lowercase hex digits", s)
	}
	seq, err := strconv.ParseInt(seqStr, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("ledger: pin %q is not seq:sha256: followed by 64 lowercase hex digits", s)
	}
	if err := checkPin(seq, hash); err != nil {
		return 0, "", err
	}
	return seq, hash, nil
}

func checkPin(seq int64, hash string) error {
	if seq < 0 || !pinHash.MatchString(hash) {
		return fmt.Errorf("ledger: pin %s is not seq:sha256: followed by 64 lowercase hex digits", formatPin(seq, hash))
	}
	return nil
}

func formatPin(seq int64, hash string) string {
	return fmt.Sprintf("%d:%s", seq, hash)
}

func formatPinSafe(seq int64, hash string) string {
	return fmt.Sprintf("%d:%s", seq, Safe(hash))
}

// VerifyPrefix reports whether the entry at seq is a valid prefix of the
// chain. It is nil only when that entry exists, its entry_hash equals
// entryHash, and the whole chain verifies, so later legitimate appends
// after the pin are allowed.
//
// A missing seq is ErrPrefixMismatch wrapping ErrNotFound. A stored
// entry_hash that does not equal entryHash is ErrPrefixMismatch, with
// expected and actual in the message (stored strings via Safe). A broken
// chain is a *ChainError (Unwrap is ErrChainBroken), even if the pinned
// entry still happens to match. Other failures (I/O, ErrCorruptFile, a
// corrupt row) are returned as from Verify or GetBySeq.
func (s *Store) VerifyPrefix(ctx context.Context, seq int64, entryHash string) error {
	rep, err := s.Verify(ctx)
	if err != nil {
		return err
	}
	if !rep.OK {
		if rep.Failure == nil {
			return ErrChainBroken
		}
		return &ChainError{Failure: *rep.Failure}
	}
	_, e, err := s.GetBySeq(ctx, seq)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: %w: expected %s, actual none", ErrPrefixMismatch, ErrNotFound, formatPinSafe(seq, entryHash))
	}
	if err != nil {
		return err
	}
	if e.EntryHash != entryHash {
		return fmt.Errorf("%w: expected %s, actual %s", ErrPrefixMismatch, formatPinSafe(seq, entryHash), formatPinSafe(e.Seq, e.EntryHash))
	}
	return nil
}
