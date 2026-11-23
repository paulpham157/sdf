package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
)

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestDecisionBriefWriteErrorIsSilent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "dont-push", Statement: "Do not push until the user says so.",
		Status: decision.StatusDecision, DecidedBy: decision.DecidedByUser,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	code := cmdDecisionBrief([]string{"--db", dbPath}, failWriter{}, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code %d stderr %q", code, stderr.String())
	}
}
