package decision

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

// AttrGitDecision is the sdf.git.commit log attribute the post-commit hook
// uses to send SDF-Decision trailer values. AutoLinkGitCommits reads it and
// appends commit-implements-decision edges. The attribute itself is not
// stored; see WithoutGitDecisionAttributes.
const AttrGitDecision = "sdf.git.decision"

const gitHookCommitEvent = "sdf.git.commit"

// WithoutGitDecisionAttributes returns a copy of records with
// AttrGitDecision removed from sdf.git.commit events, so the store holds
// the hook event without trailer values. The input slice and its attribute
// maps are not modified.
func WithoutGitDecisionAttributes(records []record.Record) []record.Record {
	if records == nil {
		return nil
	}
	out := make([]record.Record, len(records))
	copy(out, records)
	for i, rec := range out {
		if !isGitHookCommit(rec) || rec.Attributes == nil {
			continue
		}
		if _, ok := rec.Attributes[AttrGitDecision]; !ok {
			continue
		}
		attrs := make(map[string]any, len(rec.Attributes)-1)
		for k, v := range rec.Attributes {
			if k == AttrGitDecision {
				continue
			}
			attrs[k] = v
		}
		out[i].Attributes = attrs
	}
	return out
}

// AutoLinkGitCommits appends implements edges for each sdf.git.commit
// record, after those events have been inserted so KnownCommit succeeds.
//
// When the record carries one or more AttrGitDecision values (Option B,
// from SDF-Decision trailers), each valid id is linked. Unknown, retracted,
// or duplicate ids are rejected and logged; invalid ids are dropped and
// logged. No ghost edge is created.
//
// When no decision ids were sent (Option A), and the commit's session
// already implements exactly one active decision, that decision is linked.
// Zero targets are a no-op. More than one is logged and skipped, so the
// graph is not guessed. A validates or contradicts session edge is not an
// implements target. Option B, when any id was sent, does not also apply
// Option A.
//
// log receives rejection lines that name only the commit SHA and decision
// or session id, never a commit message. A nil log is discarded.
func AutoLinkGitCommits(s *store.Store, records []record.Record, log io.Writer) error {
	if log == nil {
		log = io.Discard
	}
	for _, rec := range records {
		if !isGitHookCommit(rec) {
			continue
		}
		sha := hookCommitSHA(rec)
		if sha == "" {
			continue
		}
		if err := autoLinkOne(s, rec, sha, decisionIDsFrom(rec), log); err != nil {
			return err
		}
	}
	return nil
}

func autoLinkOne(s *store.Store, rec record.Record, sha string, ids []string, log io.Writer) error {
	if len(ids) > 0 {
		for _, id := range ids {
			if !idPattern.MatchString(id) {
				fmt.Fprintf(log, "sdf: git.commit: rejected auto-link of commit:%s to decision:%s: invalid id\n", sha, id)
				continue
			}
			if err := linkCommit(s, sha, id, log); err != nil {
				return err
			}
		}
		return nil
	}
	var session string
	if rec.SessionID != nil {
		session = *rec.SessionID
	}
	if session == "" {
		return nil
	}
	targets, err := sessionImplementTargets(s, session)
	if err != nil {
		return err
	}
	switch len(targets) {
	case 0:
		return nil
	case 1:
		return linkCommit(s, sha, targets[0], log)
	default:
		fmt.Fprintf(log, "sdf: git.commit: skipped auto-link of commit:%s: session %s implements more than one decision (%s)\n", sha, session, strings.Join(targets, ", "))
		return nil
	}
}

func linkCommit(s *store.Store, sha, id string, log io.Writer) error {
	_, err := Link(s, EdgeInput{
		Relation: RelImplements,
		From:     Endpoint{Kind: KindCommit, Ref: sha},
		To:       Endpoint{Kind: KindDecision, Ref: id},
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrUnknownRef) || errors.Is(err, ErrRetracted) || errors.Is(err, ErrDuplicateEdge) {
		fmt.Fprintf(log, "sdf: git.commit: rejected auto-link of commit:%s to decision:%s: %v\n", sha, id, err)
		return nil
	}
	return err
}

func sessionImplementTargets(s *store.Store, session string) ([]string, error) {
	g, err := Load(s)
	if err != nil {
		return nil, err
	}
	var targets []string
	seen := map[string]bool{}
	for _, e := range g.Edges {
		if e.Relation != RelImplements || e.From.Kind != KindSession || e.From.Ref != session || e.To.Kind != KindDecision {
			continue
		}
		if seen[e.To.Ref] {
			continue
		}
		for _, d := range g.Decisions {
			if d.ID == e.To.Ref && d.EffectiveStatus == EffectiveActive {
				seen[e.To.Ref] = true
				targets = append(targets, e.To.Ref)
				break
			}
		}
	}
	return targets, nil
}

func isGitHookCommit(rec record.Record) bool {
	if rec.Signal != "logs" || rec.Name == nil || *rec.Name != gitHookCommitEvent {
		return false
	}
	return true
}

func hookCommitSHA(rec record.Record) string {
	sha, ok := rec.Attributes["vcs.ref.head.revision"].(string)
	if !ok {
		return ""
	}
	sha = strings.ToLower(strings.TrimSpace(sha))
	if !shaPattern.MatchString(sha) {
		return ""
	}
	return sha
}

func decisionIDsFrom(rec record.Record) []string {
	if rec.Attributes == nil {
		return nil
	}
	return uniqueDecisionIDs(rec.Attributes[AttrGitDecision])
}

func uniqueDecisionIDs(v any) []string {
	var raw []string
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); s != "" {
			raw = append(raw, s)
		}
	case []any:
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				continue
			}
			if s = strings.TrimSpace(s); s != "" {
				raw = append(raw, s)
			}
		}
	case []string:
		for _, s := range t {
			if s = strings.TrimSpace(s); s != "" {
				raw = append(raw, s)
			}
		}
	default:
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, id := range raw {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
