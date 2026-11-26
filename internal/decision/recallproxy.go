package decision

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ProxyRecallSchema names the shape of sdf decision recall --json.
// A change that removes or renames a field gets a new name.
const ProxyRecallSchema = "sdf.decision.recall/v1"

// ProxyRecallKind is the metric LinkRecallByProxy reports. It has no
// labels: a commit whose session has a decision, and which has no
// implements edge, is a miss.
const ProxyRecallKind = "link_recall_by_proxy"

// ProxyRecall is link recall by proxy over one audit window.
//
// CommitsInDecisionSessions is the denominator: commits in the window
// whose session has a decision (AuditCommitLink.SessionHasDecision).
// CommitsLinked is how many of those have an implements edge from the
// commit (AuditCommitLink.Linked). Misses is the difference. The ratio
// is nil when the denominator is 0, which is unknown, not zero.
// Wilson95 is the 95% Wilson interval of that ratio, also nil when the
// denominator is 0.
//
// SessionsWithDecisionAndCommit counts distinct session ids among the
// denominator. SessionsWithUnlinkedCommit counts how many of those
// sessions have at least one miss.
type ProxyRecall struct {
	Schema                        string          `json:"schema"`
	Kind                          string          `json:"kind"`
	Window                        AuditWindow     `json:"window"`
	CommitsInDecisionSessions     int             `json:"commits_in_decision_sessions"`
	CommitsLinked                 int             `json:"commits_linked"`
	Misses                        int             `json:"misses"`
	LinkRecallByProxy             *float64        `json:"link_recall_by_proxy"`
	Wilson95                      *WilsonInterval `json:"wilson95"`
	SessionsWithDecisionAndCommit int             `json:"sessions_with_decision_and_commit"`
	SessionsWithUnlinkedCommit    int             `json:"sessions_with_unlinked_commit"`
	MissedCommits                 []ProxyMiss     `json:"missed_commits"`
}

// ProxyMiss is one commit in a session that has a decision and no
// implements edge from the commit. SHA is lowercase. Timestamp is the
// audit commit time.
type ProxyMiss struct {
	SHA       string `json:"sha"`
	SessionID string `json:"session_id"`
	Timestamp string `json:"timestamp"`
}

// LinkRecallByProxy reduces an Audit report to the unlabeled link
// metric. It reads the report only.
func LinkRecallByProxy(r AuditReport) ProxyRecall {
	out := ProxyRecall{
		Schema:        ProxyRecallSchema,
		Kind:          ProxyRecallKind,
		Window:        r.Window,
		MissedCommits: []ProxyMiss{},
	}
	type sess struct{ unlinked bool }
	var order []string
	seen := map[string]*sess{}
	for _, c := range r.Commits {
		if !c.SessionHasDecision {
			continue
		}
		out.CommitsInDecisionSessions++
		if c.Linked {
			out.CommitsLinked++
		} else {
			out.Misses++
			out.MissedCommits = append(out.MissedCommits, ProxyMiss{
				SHA: c.SHA, SessionID: c.SessionID, Timestamp: c.Timestamp,
			})
		}
		s := seen[c.SessionID]
		if s == nil {
			s = &sess{}
			seen[c.SessionID] = s
			order = append(order, c.SessionID)
		}
		if !c.Linked {
			s.unlinked = true
		}
	}
	out.SessionsWithDecisionAndCommit = len(order)
	for _, id := range order {
		if seen[id].unlinked {
			out.SessionsWithUnlinkedCommit++
		}
	}
	if out.CommitsInDecisionSessions == 0 {
		return out
	}
	ratio := float64(out.CommitsLinked) / float64(out.CommitsInDecisionSessions)
	out.LinkRecallByProxy = &ratio
	if w, ok := Wilson95(out.CommitsLinked, out.CommitsInDecisionSessions); ok {
		out.Wilson95 = &w
	}
	return out
}

// WriteProxyJSON writes the report as indented JSON.
func WriteProxyJSON(w io.Writer, r ProxyRecall) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteProxyText writes the ratio, the Wilson interval when the
// denominator is non-zero, the session counts, and one line per miss.
func WriteProxyText(w io.Writer, r ProxyRecall) error {
	var b strings.Builder
	if r.Window.Since != "" || r.Window.Until != "" {
		fmt.Fprintf(&b, "Window: since %s, until %s\n", orDash(r.Window.Since), orDash(r.Window.Until))
	}
	if r.LinkRecallByProxy == nil {
		fmt.Fprintf(&b, "Link recall by proxy: unknown (no commits in a session that has a decision)\n")
	} else {
		fmt.Fprintf(&b, "Link recall by proxy: %d/%d = %s\n",
			r.CommitsLinked, r.CommitsInDecisionSessions,
			strconv.FormatFloat(*r.LinkRecallByProxy, 'f', 6, 64))
	}
	if r.Wilson95 != nil {
		fmt.Fprintf(&b, "Wilson 95%%: [%s, %s]\n",
			strconv.FormatFloat(r.Wilson95.Low, 'f', 6, 64),
			strconv.FormatFloat(r.Wilson95.High, 'f', 6, 64))
	}
	fmt.Fprintf(&b, "Sessions with a decision and a commit: %d\n", r.SessionsWithDecisionAndCommit)
	fmt.Fprintf(&b, "Sessions with an unlinked commit: %d\n", r.SessionsWithUnlinkedCommit)
	fmt.Fprintf(&b, "Misses: %d\n", r.Misses)
	for _, m := range r.MissedCommits {
		fmt.Fprintf(&b, "  commit:%s  session:%s  %s\n", m.SHA, m.SessionID, m.Timestamp)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
