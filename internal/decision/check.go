package decision

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

// The action words sdf decision check treats as named actions. Any other
// non-empty action is an architecture change: one free-text string, matched
// only against architecture forbids, not against these four words.
const (
	ActionPush         = "push"
	ActionMerge        = "merge"
	ActionRelease      = "release"
	ActionDeploy       = "deploy"
	ActionArchitecture = "architecture"
)

// ErrEmptyAction is returned when the action is empty or only whitespace.
var ErrEmptyAction = errors.New("action is empty")

// strongCue is a forbid phrase that names the next word as the action.
// "no" is separate: "no push" only counts when push ends the clause, so
// "no push notification" does not forbid push.
const strongCue = `(?:do not|don't|don’t|dont|never|must not|forbidden to|forbid)`

// archCue is the same kind of phrase for an architecture change, plus
// "forbidden" so "forbidden architecture" counts. "no" is not in this list;
// only the whole phrase "no architecture change" is, so "no architecture
// decision yet" does not forbid an architecture change.
const archCue = `(?:do not|don't|don’t|dont|never|must not|forbidden to|forbidden|forbid)`

type actionRule struct {
	tag    *regexp.Regexp
	phrase *regexp.Regexp
	bareNo *regexp.Regexp
}

var namedRule = map[string]actionRule{}

var (
	archTag    = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(forbid:architecture)(?:$|[^A-Za-z0-9_])`)
	archPhrase = regexp.MustCompile(`(?i)\b(` + archCue + `\b(?:\s+\w+){0,4}\s+architecture)\b`)
	archNo     = regexp.MustCompile(`(?i)\b(no architecture changes?)\b`)
)

func init() {
	for _, action := range []string{ActionPush, ActionMerge, ActionRelease, ActionDeploy} {
		quoted := regexp.QuoteMeta(action)
		namedRule[action] = actionRule{
			tag:    regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(forbid:` + quoted + `)(?:$|[^A-Za-z0-9_])`),
			phrase: regexp.MustCompile(`(?i)\b(` + strongCue + `\s+` + quoted + `)\b`),
			bareNo: regexp.MustCompile(`(?i)\b(no\s+` + quoted + `)\b\s*(?:$|[.,;:])`),
		}
	}
}

// Verdict is the result of checking one action. Action is the canonical
// word for a named action, or the trimmed free text for an architecture
// change. Kind is that word or ActionArchitecture. Blocking is in the
// query's order and is empty when Allowed is true.
type Verdict struct {
	Action   string `json:"action"`
	Kind     string `json:"kind"`
	Allowed  bool   `json:"allowed"`
	Blocking []Hit  `json:"blocking"`
}

// Hit is one in-force decision whose statement forbids the action. Match is
// the tag or phrase that fired, lowercased, with the whitespace collapsed.
type Hit struct {
	ID        string `json:"id"`
	DecidedBy string `json:"decided_by"`
	Statement string `json:"statement"`
	Match     string `json:"match"`
}

// CheckAction reports whether an in-force decision forbids action.
// Matching reads only the statement, and only as whole words:
//
//   - Named actions are exactly push, merge, release, or deploy, compared
//     case-insensitively after trimming. Anything else, including a longer
//     phrase such as "git push" or "replace sqlite", is an architecture change.
//   - A named action is forbidden when the statement contains the tag
//     forbid:<action>, or a phrase whose cue is immediately followed by that
//     word: "do not", "don't", "dont", "never", "must not", "forbidden to",
//     or "forbid". "no <action>" counts only when the action ends the
//     statement or is followed by . , ; or :.
//   - An architecture change is forbidden when the statement contains the
//     tag forbid:architecture, the phrase "no architecture change" (or
//     "changes"), or a cue followed within four words by the word
//     architecture. A named-action forbid does not forbid an architecture
//     change, and an architecture forbid does not forbid a named action.
//   - The tag is reported in preference to a phrase when both match.
//     Matching is case-insensitive. The id, the reason, and the source are
//     not read. Edges are not read; supersede and retract matter only
//     because Query.Active has already dropped those records.
func CheckAction(q Query, action string) (Verdict, error) {
	kind, canonical, err := classifyAction(action)
	if err != nil {
		return Verdict{}, err
	}
	recs, err := q.Active()
	if err != nil {
		return Verdict{}, err
	}
	v := Verdict{Action: canonical, Kind: kind, Allowed: true, Blocking: []Hit{}}
	for _, d := range recs {
		match, ok := matchStatement(kind, d.Statement)
		if !ok {
			continue
		}
		v.Blocking = append(v.Blocking, Hit{
			ID:        d.ID,
			DecidedBy: d.DecidedBy,
			Statement: d.Statement,
			Match:     match,
		})
	}
	v.Allowed = len(v.Blocking) == 0
	return v, nil
}

func classifyAction(action string) (kind, canonical string, err error) {
	action = strings.TrimSpace(action)
	if action == "" {
		return "", "", ErrEmptyAction
	}
	folded := strings.ToLower(action)
	switch folded {
	case ActionPush, ActionMerge, ActionRelease, ActionDeploy:
		return folded, folded, nil
	default:
		return ActionArchitecture, action, nil
	}
}

func matchStatement(kind, statement string) (string, bool) {
	if kind == ActionArchitecture {
		return firstMatch(statement, archTag, archNo, archPhrase)
	}
	rule, ok := namedRule[kind]
	if !ok {
		return "", false
	}
	return firstMatch(statement, rule.tag, rule.phrase, rule.bareNo)
}

func firstMatch(statement string, patterns ...*regexp.Regexp) (string, bool) {
	for _, p := range patterns {
		m := p.FindStringSubmatch(statement)
		if m == nil {
			continue
		}
		return normMatch(m[1]), true
	}
	return "", false
}

func normMatch(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// WriteCheckJSON writes v the way the other decision JSON is written.
// Blocking is an empty array when nothing forbids the action.
func WriteCheckJSON(w io.Writer, v Verdict) error {
	if v.Blocking == nil {
		v.Blocking = []Hit{}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteCheckText writes one verdict line, then one tab-separated row per
// blocking decision. A named action prints "allowed: push" or
// "forbidden: push". An architecture change adds " (architecture)". An empty
// decided-by prints as "-". A field that holds a control character or
// invalid UTF-8 is Go-quoted, the same rule as the decision list cells.
func WriteCheckText(w io.Writer, v Verdict) error {
	status := "allowed"
	if !v.Allowed {
		status = "forbidden"
	}
	line := status + ": " + v.Action
	if v.Kind == ActionArchitecture {
		line += " (architecture)"
	}
	if _, err := io.WriteString(w, line+"\n"); err != nil {
		return err
	}
	if v.Allowed {
		return nil
	}
	if _, err := io.WriteString(w, "ID\tDECIDED BY\tMATCH\tSTATEMENT\n"); err != nil {
		return err
	}
	for _, h := range v.Blocking {
		who := h.DecidedBy
		if who == "" {
			who = "-"
		}
		row := cell(h.ID) + "\t" + cell(who) + "\t" + cell(h.Match) + "\t" + cell(h.Statement) + "\n"
		if _, err := io.WriteString(w, row); err != nil {
			return err
		}
	}
	return nil
}
