package server

import (
	"bufio"
	"encoding/json"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/record"
)

// sessionEvent is what an agent hook posts to /v1/sessions. Tool is the agent
// name the summary already classifies on (claude-code, codex, opencode, pi).
// Edge is open or close. Reason is whatever the agent said about why, and is
// empty when the agent has nothing to say.
type sessionEvent struct {
	Tool      string `json:"tool"`
	SessionID string `json:"session_id"`
	Edge      string `json:"edge"`
	Reason    string `json:"reason"`
}

// handleSession reads one hook event and stores it. The body is the hook's own
// JSON, not an OTLP export, so it never reaches the OTLP decoder. A body the
// collector cannot use is a 400 and stores nothing. A hook that posts nothing
// useful still gets a response, so the agent is never blocked on it.
func (s *Server) handleSession(reader *bufio.Reader, writer *bufio.Writer, headers []header, method, path string, closeAfter bool) (bool, error) {
	body, err := readBody(reader, headers)
	if err != nil {
		return s.writeBodyError(writer, method, path, err, closeAfter)
	}
	rec, err := sessionRecord(body, time.Now())
	if err != nil {
		return s.writeBodyError(writer, method, path, err, closeAfter)
	}
	if _, err := s.Store.Insert([]record.Record{rec}); err != nil {
		writeResponse(writer, 500, "text/plain", []byte("store error"), true)
		s.logRequest(method, path, 500)
		return true, nil
	}
	writeResponse(writer, 200, "application/json", []byte("{}"), closeAfter)
	s.logRequest(method, path, 200)
	return closeAfter, nil
}

// sessionRecord turns one hook event into the log record the summary reads.
// The name is sdf.session.open or sdf.session.close, the session id is set, and
// service.name is the tool, so the record lands in that session at ingest and
// ToolOf classifies it. The timestamp is the collector's clock, because a hook
// reports the moment it fired and the agents do not send their own.
func sessionRecord(body []byte, now time.Time) (record.Record, error) {
	var ev sessionEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return record.Record{}, &bodyStatus{400, "could not parse session event"}
	}
	ev.Tool = strings.TrimSpace(ev.Tool)
	ev.SessionID = strings.TrimSpace(ev.SessionID)
	ev.Edge = strings.TrimSpace(ev.Edge)
	ev.Reason = strings.TrimSpace(ev.Reason)
	switch ev.Tool {
	case "claude-code", "codex", "opencode", "pi":
	default:
		return record.Record{}, &bodyStatus{400, "session event tool must be claude-code, codex, opencode, or pi"}
	}
	if ev.SessionID == "" {
		return record.Record{}, &bodyStatus{400, "session event requires a session_id"}
	}
	var name string
	switch ev.Edge {
	case "open":
		name = "sdf.session.open"
	case "close":
		name = "sdf.session.close"
	default:
		return record.Record{}, &bodyStatus{400, "session event edge must be open or close"}
	}
	attrs := map[string]any{"session.id": ev.SessionID}
	if ev.Reason != "" {
		attrs["sdf.session.reason"] = ev.Reason
	}
	return record.Record{
		Signal:       "logs",
		ServiceName:  record.Str(ev.Tool),
		SessionID:    record.Str(ev.SessionID),
		Name:         record.Str(name),
		TimeUnixNano: record.Int64(now.UnixNano()),
		Attributes:   attrs,
		Resource:     map[string]any{"service.name": ev.Tool},
	}, nil
}
