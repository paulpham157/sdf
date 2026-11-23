package decision

import (
	"fmt"
	"io"
	"strings"

	"github.com/paulpham157/sdf/internal/store"
)

// PushCommit is one commit a git push would send: its SHA (lowercase hex)
// and the values of its SDF-Decision trailers, read by the caller with
// git log --format=%(trailers:key=SDF-Decision,valueonly). Nothing else
// from the commit message, and nothing from the diff, is read.
type PushCommit struct {
	SHA      string
	Trailers []string
}

// PushReport is what CheckPush found for the pushed commits.
//
//   - Pushed: commits checked (merge commits are left out by the caller).
//   - Recorded: of those, the commits SDF knows (a stored event names the
//     SHA, by the rules of store.LoadCommitEvents and KnownCommit) or that
//     carry an SDF-Decision trailer. A commit SDF never saw and with no
//     trailer is not judged: SDF has no evidence either way.
//   - Covered: recorded commits with a decision (see CheckPush).
//   - Missing: recorded commits without one, in push order.
type PushReport struct {
	Pushed   int
	Recorded int
	Covered  int
	Missing  []PushMissing
}

// PushMissing is a recorded commit with no decision. Sessions are the
// session ids stored events name for the commit. SessionDecisions are the
// active decisions some edge joins to one of those sessions, offered as the
// likely link target. Retracted names decisions the commit points at
// (implements edge or trailer) that are retracted. UnknownTrailers are
// SDF-Decision values that name no stored decision.
type PushMissing struct {
	SHA              string
	Sessions         []string
	SessionDecisions []string
	Retracted        []string
	UnknownTrailers  []string
}

// CheckPush reads the decision graph (Load) and the stored commit events
// (store.LoadCommitEvents, the same rows sdf decision audit reads) and
// classifies each pushed commit. It only reads.
//
// A recorded commit has a decision when an implements edge runs from
// commit:SHA to a stored decision that is not retracted, or when one of its
// SDF-Decision trailers names such a decision. A superseded decision still
// counts: the commit implemented what was decided at the time. Session
// links alone do not count, because the gate asks whether this commit is
// tied to a decision, not whether its session made one.
func CheckPush(s *store.Store, commits []PushCommit) (PushReport, error) {
	g, err := Load(s)
	if err != nil {
		return PushReport{}, err
	}
	events, err := s.LoadCommitEvents()
	if err != nil {
		return PushReport{}, err
	}
	return checkPush(g, events, commits), nil
}

func checkPush(g Graph, events []store.CommitEvent, commits []PushCommit) PushReport {
	byID := make(map[string]Record, len(g.Decisions))
	for _, d := range g.Decisions {
		byID[d.ID] = d
	}
	implements := map[string][]string{}
	sessionDecisions := map[string][]string{}
	for _, e := range g.Edges {
		if e.Relation == RelImplements && e.From.Kind == KindCommit && e.To.Kind == KindDecision {
			if _, ok := byID[e.To.Ref]; ok {
				implements[e.From.Ref] = appendOnce(implements[e.From.Ref], e.To.Ref)
			}
		}
		for _, pair := range [][2]Endpoint{{e.From, e.To}, {e.To, e.From}} {
			sess, dec := pair[0], pair[1]
			if sess.Kind != KindSession || dec.Kind != KindDecision {
				continue
			}
			if d, ok := byID[dec.Ref]; ok && d.EffectiveStatus == EffectiveActive {
				sessionDecisions[sess.Ref] = appendOnce(sessionDecisions[sess.Ref], dec.Ref)
			}
		}
	}
	known := map[string]bool{}
	sessions := map[string][]string{}
	for _, ev := range events {
		known[ev.SHA] = true
		if ev.SessionID != "" {
			sessions[ev.SHA] = appendOnce(sessions[ev.SHA], ev.SessionID)
		}
	}
	var out PushReport
	for _, c := range commits {
		sha := strings.ToLower(c.SHA)
		out.Pushed++
		var trailers []string
		for _, v := range c.Trailers {
			if v = strings.TrimSpace(v); v != "" {
				trailers = appendOnce(trailers, v)
			}
		}
		if !known[sha] && len(trailers) == 0 {
			continue
		}
		out.Recorded++
		miss := PushMissing{SHA: sha, Sessions: sessions[sha]}
		covered := false
		consider := func(id string) {
			if byID[id].EffectiveStatus == EffectiveRetracted {
				miss.Retracted = appendOnce(miss.Retracted, id)
				return
			}
			covered = true
		}
		for _, id := range implements[sha] {
			consider(id)
		}
		for _, id := range trailers {
			if _, ok := byID[id]; !ok {
				miss.UnknownTrailers = appendOnce(miss.UnknownTrailers, id)
				continue
			}
			consider(id)
		}
		if covered {
			out.Covered++
			continue
		}
		for _, sid := range miss.Sessions {
			for _, id := range sessionDecisions[sid] {
				miss.SessionDecisions = appendOnce(miss.SessionDecisions, id)
			}
		}
		out.Missing = append(out.Missing, miss)
	}
	return out
}

func appendOnce(list []string, v string) []string {
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}

// WritePushText writes the pre-push hook's report. It writes nothing when
// no pushed commit is recorded, one line when every recorded commit has a
// decision, and otherwise one block per commit with the command that links
// it. dbFlag is " --db PATH" (already shell-quoted) or empty. Values from
// the store and from trailers are quoted by the same rule as WriteText.
func WritePushText(w io.Writer, r PushReport, dbFlag string, block bool) error {
	if r.Recorded == 0 {
		return nil
	}
	var b strings.Builder
	if len(r.Missing) == 0 {
		fmt.Fprintf(&b, "sdf: pre-push: %d of %d pushed commits are recorded in SDF, and each implements a decision.\n", r.Recorded, r.Pushed)
		_, err := io.WriteString(w, b.String())
		return err
	}
	fmt.Fprintf(&b, "sdf: pre-push: %d of %d pushed commits recorded in SDF implement no decision:\n", len(r.Missing), r.Recorded)
	for _, m := range r.Missing {
		fmt.Fprintf(&b, "  commit:%s\n", m.SHA)
		if len(m.Sessions) > 0 {
			fmt.Fprintf(&b, "    session: %s\n", cellList(m.Sessions))
		}
		for _, id := range m.UnknownTrailers {
			fmt.Fprintf(&b, "    SDF-Decision trailer names %s, which is not a recorded decision\n", cell(id))
		}
		for _, id := range m.Retracted {
			fmt.Fprintf(&b, "    decision:%s is retracted\n", cell(id))
		}
		target := "ID"
		if len(m.SessionDecisions) > 0 {
			fmt.Fprintf(&b, "    its session is linked to: %s\n", cellList(prefixAll("decision:", m.SessionDecisions)))
			if len(m.SessionDecisions) == 1 {
				target = cell(m.SessionDecisions[0])
			}
		}
		fmt.Fprintf(&b, "    link it: sdf decision link%s --relation implements --from commit:%s --to decision:%s\n", dbFlag, m.SHA, target)
	}
	b.WriteString("No decision yet? Record one first: sdf decision add --id ID --statement TEXT --status decision\n")
	b.WriteString("A commit can also name its decision with an \"SDF-Decision: ID\" trailer.\n")
	if block {
		b.WriteString("Push blocked (sdf pre-push mode is block). Bypass once with git push --no-verify, or SDF_PRE_PUSH=warn.\n")
	} else {
		b.WriteString("The push continues (warn mode). Skip the check with git push --no-verify or SDF_PRE_PUSH=off.\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func cellList(list []string) string {
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = cell(v)
	}
	return strings.Join(out, ", ")
}

func prefixAll(prefix string, list []string) []string {
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = prefix + v
	}
	return out
}
