package decision

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// touchSampleCap is how many changed paths one decision's warning shows.
// The rest are counted as "and N more".
const touchSampleCap = 3

// PushPath is one changed file name on a commit a push would send. SHA is
// the commit (any case); empty when the caller has paths and no commits.
// Trailers are that commit's SDF-Decision values. Path is a git path with
// slashes. Nothing here is file content, and a glob is never used to open
// a file.
type PushPath struct {
	SHA      string
	Path     string
	Trailers []string
}

// TouchReport is what CheckTouches found. Decisions is in graph order and
// holds only decisions that matched at least one path. It is nil when
// nothing matched.
type TouchReport struct {
	Decisions []TouchDecision
}

// TouchDecision is one active settled decision whose globs matched.
// Locked is true when the decision is a locked brief constraint.
// Matches is every match, in path order; WriteTouchText shows at most
// touchSampleCap of them.
type TouchDecision struct {
	ID        string
	Statement string
	Locked    bool
	Matches   []TouchMatch
}

// TouchMatch is one glob that matched one changed path.
type TouchMatch struct {
	Glob string
	Path string
	SHA  string
}

// TouchText chooses the warning's header. Prefix is "sdf: pre-push" or
// "sdf: touches". Unit is "pushed commit", "commit", or "path". Continue
// adds "The push continues." when this finding does not stop the push.
type TouchText struct {
	Prefix   string
	Unit     string
	Continue bool
}

// TouchForPush is the pre-push warning. Continue is false when the push
// is blocked because a recorded commit implements no decision.
func TouchForPush(continuePush bool) TouchText {
	return TouchText{Prefix: "sdf: pre-push", Unit: "pushed commit", Continue: continuePush}
}

// CheckTouches reports changed paths that match an active settled decision's
// globs. A decision qualifies when EffectiveStatus is active, Status is
// decision, and it has at least one glob that still compiles. Superseded
// decisions are not matched and their globs are not inherited. A refines
// edge leaves both decisions active, and each matches only its own globs.
// Retracted decisions and hypotheses are ignored.
//
// A commit already linked to the matched decision is not reported for it:
// an implements edge from commit:SHA, or an SDF-Decision trailer naming
// that decision. The same commit can still match a different decision.
//
// Globs are compiled once per call. A glob that fails validation is
// skipped. At most 32 globs per decision are considered, in recorded
// order. The store and the paths are data, not file names to open.
func CheckTouches(g Graph, paths []PushPath) TouchReport {
	linked := map[string]map[string]bool{}
	for _, e := range g.Edges {
		if e.Relation != RelImplements || e.From.Kind != KindCommit || e.To.Kind != KindDecision {
			continue
		}
		sha := strings.ToLower(e.From.Ref)
		if linked[sha] == nil {
			linked[sha] = map[string]bool{}
		}
		linked[sha][e.To.Ref] = true
	}
	type ready struct {
		rec   Record
		globs []string
		match []func(string) bool
	}
	cache := map[string]func(string) bool{}
	var active []ready
	for _, d := range g.Decisions {
		if d.EffectiveStatus != EffectiveActive || d.Status != StatusDecision || len(d.Paths) == 0 {
			continue
		}
		var one ready
		one.rec = d
		n := 0
		for _, glob := range d.Paths {
			if n >= maxGlobsPerDecision {
				break
			}
			fn, ok := cache[glob]
			if !ok {
				re, err := CompileGlob(glob)
				if err != nil {
					cache[glob] = nil
					continue
				}
				fn = re.MatchString
				cache[glob] = fn
			}
			if fn == nil {
				continue
			}
			one.globs = append(one.globs, glob)
			one.match = append(one.match, fn)
			n++
		}
		if len(one.globs) > 0 {
			active = append(active, one)
		}
	}
	if len(active) == 0 || len(paths) == 0 {
		return TouchReport{}
	}
	hits := make([][]TouchMatch, len(active))
	seen := map[string]bool{}
	for _, p := range paths {
		if p.Path == "" || strings.IndexByte(p.Path, 0) >= 0 {
			continue
		}
		sha := strings.ToLower(strings.TrimSpace(p.SHA))
		key := sha + "\x00" + p.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		trailers := map[string]bool{}
		for _, t := range p.Trailers {
			if t = strings.TrimSpace(t); t != "" {
				trailers[t] = true
			}
		}
		for i, c := range active {
			if linked[sha][c.rec.ID] || trailers[c.rec.ID] {
				continue
			}
			for j, fn := range c.match {
				if fn(p.Path) {
					hits[i] = append(hits[i], TouchMatch{Glob: c.globs[j], Path: p.Path, SHA: sha})
					break
				}
			}
		}
	}
	var out TouchReport
	for i, c := range active {
		if len(hits[i]) == 0 {
			continue
		}
		out.Decisions = append(out.Decisions, TouchDecision{
			ID:        c.rec.ID,
			Statement: c.rec.Statement,
			Locked:    LocksBrief(c.rec.DecidedBy, c.rec.DecidedBySource),
			Matches:   hits[i],
		})
	}
	return out
}

// WriteTouchText writes the touch warning. It writes nothing when r has no
// matches. Ids, statements, globs, and file names go through cell, the same
// quoting as the decision list, so a control character cannot break the
// terminal. At most touchSampleCap paths are shown per decision.
func WriteTouchText(w io.Writer, r TouchReport, opt TouchText) error {
	if len(r.Decisions) == 0 {
		return nil
	}
	var b strings.Builder
	n := touchCount(r, opt.Unit == "path")
	writeTouchHeader(&b, opt, n, len(r.Decisions))
	for _, d := range r.Decisions {
		writeTouchDecision(&b, d)
	}
	id := "ID"
	if len(r.Decisions) == 1 {
		id = cell(r.Decisions[0].ID)
	}
	fmt.Fprintf(&b, "Touching is not a violation; link the commit if it follows the decision (SDF-Decision: %s).", id)
	if opt.Continue {
		b.WriteString(" The push continues.")
	}
	b.WriteString(" Silence: git config sdf.prePushTouches off.\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func touchCount(r TouchReport, byPath bool) int {
	seen := map[string]bool{}
	n := 0
	for _, d := range r.Decisions {
		for _, m := range d.Matches {
			key := m.SHA
			if byPath {
				key = m.Path
			}
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			n++
		}
	}
	return n
}

func writeTouchHeader(b *strings.Builder, opt TouchText, n, decisions int) {
	noun := opt.Unit
	if n != 1 {
		noun += "s"
	}
	verb := "touch"
	if n == 1 {
		verb = "touches"
	}
	dNoun := "decision"
	if decisions != 1 {
		dNoun += "s"
	}
	if opt.Unit == "path" {
		if n == 1 {
			verb = "matches"
		} else {
			verb = "match"
		}
		fmt.Fprintf(b, "%s: %d %s %s %d active %s:\n", opt.Prefix, n, noun, verb, decisions, dNoun)
		return
	}
	fmt.Fprintf(b, "%s: %d %s %s paths scoped by %d active %s:\n", opt.Prefix, n, noun, verb, decisions, dNoun)
}

func writeTouchDecision(b *strings.Builder, d TouchDecision) {
	fmt.Fprintf(b, "  decision:%s", cell(d.ID))
	if d.Locked {
		b.WriteString(" (locked)")
	}
	b.WriteByte(' ')
	b.WriteString(quotedCell(d.Statement))
	b.WriteByte('\n')
	shown := d.Matches
	extra := 0
	if len(shown) > touchSampleCap {
		extra = len(shown) - touchSampleCap
		shown = shown[:touchSampleCap]
	}
	type group struct {
		glob    string
		matches []TouchMatch
	}
	var groups []group
	for _, m := range shown {
		if len(groups) == 0 || groups[len(groups)-1].glob != m.Glob {
			groups = append(groups, group{glob: m.Glob})
		}
		groups[len(groups)-1].matches = append(groups[len(groups)-1].matches, m)
	}
	for i, g := range groups {
		more := 0
		if i == len(groups)-1 {
			more = extra
		}
		writeTouchLine(b, g.glob, g.matches, more)
	}
}

func writeTouchLine(b *strings.Builder, glob string, matches []TouchMatch, extra int) {
	b.WriteString("    ")
	b.WriteString(cell(glob))
	b.WriteString(" matches ")
	for i, m := range matches {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(cell(m.Path))
		if m.SHA != "" {
			b.WriteString(" (commit ")
			b.WriteString(cell(shortSHA(m.SHA)))
			b.WriteString(")")
		}
	}
	if extra > 0 {
		fmt.Fprintf(b, ", and %d more", extra)
	}
	b.WriteByte('\n')
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func quotedCell(s string) string {
	c := cell(s)
	if strings.HasPrefix(c, `"`) {
		return c
	}
	if strings.ContainsAny(c, `"\`) {
		return strconv.Quote(s)
	}
	return `"` + c + `"`
}
