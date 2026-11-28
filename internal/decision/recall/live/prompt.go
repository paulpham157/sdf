package live

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paulpham157/sdf/internal/decision/recall"
	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/record"
)

// transcript is the user prompt. It is built from log bodies and the
// commit SHAs already in the store. It does not include labels or notes.
func transcript(fx recall.Fixture) (string, error) {
	recs, err := decodeFixture(fx)
	if err != nil {
		return "", err
	}
	sort.SliceStable(recs, func(i, j int) bool {
		return nano(recs[i]) < nano(recs[j])
	})
	var b strings.Builder
	b.WriteString("This is a recall scenario in a throwaway git repository. ")
	b.WriteString("The sdf store sdf-telemetry.sqlite in this directory already contains the session telemetry below. ")
	b.WriteString("Follow skills/sdf-decisions/SKILL.md and record decisions and commit links with the sdf CLI. ")
	b.WriteString("Do not invent a commit SHA. Link a commit only when that commit implements a decision. ")
	b.WriteString("The store is the default file in this directory.\n\nSession transcript:\n")
	for _, rec := range recs {
		ev := attr(rec, "event.name")
		if ev == "" && rec.Name != nil {
			ev = *rec.Name
		}
		sid := attr(rec, "session.id")
		if sid == "" && rec.SessionID != nil {
			sid = *rec.SessionID
		}
		switch ev {
		case "recall.turn":
			role := attr(rec, "recall.role")
			if role == "" {
				role = "unknown"
			}
			body := ""
			if rec.Body != nil {
				body = *rec.Body
			}
			fmt.Fprintf(&b, "\n[%s] session %s\n%s\n", role, sid, body)
		case "sdf.git.commit":
			sha := strings.ToLower(attr(rec, "vcs.ref.head.revision"))
			branch := attr(rec, "sdf.git.branch")
			fmt.Fprintf(&b, "\n[commit] session %s sha %s branch %s\nThis commit is already in the sdf store.\n", sid, sha, branch)
		}
	}
	return b.String(), nil
}

func decodeFixture(fx recall.Fixture) ([]record.Record, error) {
	var recs []record.Record
	for i, body := range fx.Logs {
		got, err := otlp.Decode("logs", body, "application/json")
		if err != nil {
			return nil, fmt.Errorf("%s logs[%d]: %w", fx.Name, i, err)
		}
		recs = append(recs, got...)
	}
	return recs, nil
}

func nano(r record.Record) int64 {
	if r.TimeUnixNano != nil {
		return *r.TimeUnixNano
	}
	if r.StartUnixNano != nil {
		return *r.StartUnixNano
	}
	return 0
}

func attr(r record.Record, key string) string {
	if r.Attributes == nil {
		return ""
	}
	s, _ := r.Attributes[key].(string)
	return s
}
