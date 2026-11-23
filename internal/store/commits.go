package store

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// commitSHA is a full 40-character commit id, in any case.
var commitSHA = regexp.MustCompile(`(?i)^[0-9a-f]{40}$`)

// CommitEvent is one stored log that names a full commit SHA, by the same
// rules KnownCommit applies: an sdf.git.commit hook event's
// vcs.ref.head.revision, or a claude_code.tool_result log's
// sdf.git_commit_id, vcs.ref.head.revision, git_commit_id, or a
// git_commit_id inside tool_parameters (an object or a JSON string).
//
// SHA is lowercase. SessionID is the row's stored session id (the receiver
// fills it from the event's session.id attribute), empty when the row has
// none. Time is the log record time, or received_at when the record has no
// time, in UTC as 2006-01-02T15:04:05.000000+00:00. Repo and Branch are
// sdf.git.repo and sdf.git.branch on a hook event, as sent. Name is the log
// name. Nothing here reads a commit message, a body, or a tool result: the
// SDF-Session trailer lives only in the git commit message, which the store
// never holds.
type CommitEvent struct {
	Name      string
	SHA       string
	SessionID string
	Time      string
	Repo      string
	Branch    string
}

// LoadCommitEvents returns every CommitEvent in insertion order. It only
// reads.
func (s *Store) LoadCommitEvents() ([]CommitEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`
		SELECT name, session_id, time_unix_nano, received_at, attributes_json
		FROM records
		WHERE signal = 'logs' AND name IN ('sdf.git.commit', 'claude_code.tool_result')
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommitEvent{}
	for rows.Next() {
		var (
			name, receivedAt, attrsJSON string
			session                     sql.NullString
			nano                        sql.NullInt64
		)
		if err := rows.Scan(&name, &session, &nano, &receivedAt, &attrsJSON); err != nil {
			return nil, err
		}
		var attrs map[string]any
		if err := json.Unmarshal([]byte(attrsJSON), &attrs); err != nil {
			// A row whose attributes are not an object names no commit.
			continue
		}
		sha, ok := commitOfEvent(name, attrs)
		if !ok {
			continue
		}
		ev := CommitEvent{Name: name, SHA: sha, Time: eventTime(nano, receivedAt)}
		if session.Valid {
			ev.SessionID = session.String
		}
		if name == "sdf.git.commit" {
			ev.Repo = textAttr(attrs, "sdf.git.repo")
			ev.Branch = textAttr(attrs, "sdf.git.branch")
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func commitOfEvent(name string, attrs map[string]any) (string, bool) {
	var candidates []string
	switch name {
	case "sdf.git.commit":
		candidates = []string{textAttr(attrs, "vcs.ref.head.revision")}
	case "claude_code.tool_result":
		candidates = []string{
			textAttr(attrs, "sdf.git_commit_id"),
			textAttr(attrs, "vcs.ref.head.revision"),
			textAttr(attrs, "git_commit_id"),
		}
		switch p := attrs["tool_parameters"].(type) {
		case map[string]any:
			candidates = append(candidates, textAttr(p, "git_commit_id"))
		case string:
			var inner map[string]any
			if json.Unmarshal([]byte(p), &inner) == nil {
				candidates = append(candidates, textAttr(inner, "git_commit_id"))
			}
		}
	}
	for _, c := range candidates {
		if commitSHA.MatchString(c) {
			return strings.ToLower(c), true
		}
	}
	return "", false
}

func textAttr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// eventTime formats the record time like decision recorded_at. A record
// without a time falls back to received_at, parsed and reformatted when it
// parses and returned as stored otherwise.
func eventTime(nano sql.NullInt64, receivedAt string) string {
	const layout = "2006-01-02T15:04:05.000000-07:00"
	if nano.Valid && nano.Int64 > 0 {
		return time.Unix(0, nano.Int64).UTC().Format(layout)
	}
	if t, err := time.Parse(time.RFC3339Nano, receivedAt); err == nil {
		return t.UTC().Format(layout)
	}
	return receivedAt
}
