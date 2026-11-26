# Decision categories

Status: accepted design, not implemented. This page is phase 0. It specifies
the change and does not change the schema, the CLI, the brief, the checker,
or the UI. Current behavior stays as described in
[Decision records](../outcomes.md#decision-records) and
[Checking an action](../outcomes.md#checking-an-action).

A decision record gains one optional field, `category`. It says what kind of
choice the record is, so a reader can separate an architecture choice, a
policy, a product choice, an implementation choice, and an operations choice.
`status` stays `hypothesis` or `decision`. The brief's derived group stays
named `constraint`. `sdf decision check` stays a match on the statement.

## Decisions

These are decided:

1. The field is named `category`. One record holds one value, and the value
   is optional. It is independent of `status` (`hypothesis` or `decision`),
   except for the `policy` rule below. `type` was rejected. `kind` was
   rejected because it already names an edge endpoint
   (`Endpoint.Kind` in `internal/decision/decision.go`, `KindDecision`,
   `KindSession`, `KindCommit`, stored as `from_kind` and `to_kind`). The
   same word also names a brief item's group, a check verdict's class, and
   a graph node's box.
2. The values are `architecture`, `policy`, `product`, `implementation`,
   and `operations`. Unset is SQL `NULL`, which means uncategorised. There
   is no `other`. Security, data, and UX do not get values: they cut across
   the five. A security rule that forbids an action is a `policy`. A
   security boundary in the design is `architecture`. Free tags that could
   name those cross-cutting concerns are deferred to phase 4.
3. The value is `policy`, not `constraint`. `constraint` is already the
   brief's derived group. `isConstraint` in
   `internal/decision/brief/brief.go` (the closure at line 102) selects an
   active record whose `status` is `decision` and that the user made, or
   that is the source of a `constrains` edge. `RelConstrains` (`constrains`)
   is a stored relation. A category and that derived group answer different
   questions, and a record can be one without being the other.
4. `policy` requires `status=decision`. `sdf decision add` rejects a
   `policy` hypothesis and stores nothing. An agent may record a `policy`
   with `status=decision`. That add succeeds, and phase 3 warns that the
   brief does not lock an agent policy. The brief sets `Locked` only for a
   constraint the user made (`Item.Locked` in the same file).
5. `sdf decision check` never reads `category`. It stays statement-only
   (`CheckAction` in `internal/decision/check.go`). A forbid that was filed
   under the wrong category must still block. Gating the check on
   `category=policy` would make a miscategorised forbid fail open.

Who normally decides is advisory. `--decided-by` is whatever the caller
declares. The only pair the CLI refuses is `policy` with `hypothesis`.

## Categories

| Value | What it is | Example statement |
| --- | --- | --- |
| `architecture` | A choice about structure, boundaries, dependencies, or construction that later work has to fit. | `Store telemetry in SQLite.` |
| `policy` | A rule later work must follow. | `Do not push until the user says so.` |
| `product` | What the product does for a person using it. | `Show agent status in the UI.` |
| `implementation` | How the code is written inside an architecture that already holds. | `The SQLite driver stays pure Go.` |
| `operations` | How the system is run: deploy, release, retention, budget, schedule. | `Do not deploy on Friday.` |

### architecture

A structural choice. "Store telemetry in SQLite" is architecture. "Keep the
driver pure Go" is implementation of that choice, and is a different record.

Either the user or the agent normally records it. Both `hypothesis` and
`decision` are allowed. Useful relations: `supersedes` and `refines` against
another architecture record, `implements` from the session or commit that
built it, and `constrains` when this choice limits a later record.

### policy

A rule, not a topic. "Do not push until the user says so" is a policy.
"Waiting for the user's OK to push" is a hypothesis and cannot be category
`policy`; leave it uncategorised, or use `operations` if the open question
is operational.

`status` must be `decision`. The user normally records a policy. An agent
may record one too. Phase 3 prints a warning on that agent add: the brief
does not lock agent policies. `sdf decision check` still matches the
statement, because check uses `decision.Active` (every in-force settled
decision, user or agent), not the brief's locked set. See
`activeForCheck` in `internal/cli/cli.go`.

Useful relation: `constrains`, from this decision to whatever it limits.
The relation name stays `constrains`. The category name stays `policy`.

A user decision of any category can still land in the brief's `constraint`
group, because that group follows who decided and the `constrains` edge,
not this field. An agent `policy` is not locked unless a later change says
so. This spec does not change `isConstraint`.

### product

A choice about what a person can do or see. "Show agent status in the UI"
is product. The library used to draw it is implementation.

A settled product decision is the user's. `hypothesis` is allowed for the
user or the agent. A phase 3 warning when an agent adds one with
`status=decision` is a proposal, pending owner confirmation. Useful
relations: `supersedes`, `refines`, and
`implements` from the session that built the behavior. `blocks` fits while
an open question still holds the work.

### implementation

A choice about how code is written inside an architecture that already
holds. "The SQLite driver stays pure Go" is implementation.

The agent normally records it. The user may record one as well. No warning
either way: the guidance is who usually does the work, and `--decided-by`
stays self-declared. Both statuses are allowed. Useful relations:
`implements`, `refines`, and `supersedes`.

### operations

A choice about running the system. "Do not deploy on Friday" is operations.
The same sentence is also a forbid for `sdf decision check deploy`, because
the statement matches, whatever the category is.

A settled operations decision is the user's. `hypothesis` is allowed.
A phase 3 warning when an agent adds one with `status=decision` is a
proposal, pending owner confirmation. Useful relations: `constrains` when
the rule limits later work, and `supersedes` when the rule is replaced.

### Unset

Omitted means uncategorised. The column is `NULL`. JSON omits the field.
The text list prints `-`, the same placeholder `source` and `decided_by`
use when empty. Old rows stay in this state. Nothing in phase 1 guesses a
category from the statement.

## Guidance

| Category | Who normally decides a settled record | Status | Warning in phase 3 |
| --- | --- | --- | --- |
| `architecture` | User or agent | `hypothesis` or `decision` | None |
| `policy` | User. An agent may record one | `decision` only | Decided: agent add, the brief does not lock an agent policy |
| `product` | User | `hypothesis` or `decision` | Proposal: agent add of a `decision` |
| `implementation` | Agent | `hypothesis` or `decision` | None |
| `operations` | User | `hypothesis` or `decision` | Proposal: agent add of a `decision` |

"Who normally decides" is advice. It is not an access check. A caller can
pass `--decided-by user` or `--decided-by agent` on any category. An empty
`--decided-by` stays legal, as it is today, including on `product` and
`operations`. The decided phase 3 warning is the agent `policy` warning,
and it fires only when `--decided-by` is `agent`. A warning when an agent
records a settled `product` or `operations` decision is a proposal, pending
owner confirmation. It is not a decided rule.

## Check stays on the statement

`CheckAction` matches the statement and nothing else. The comment on
`CheckAction` in `internal/decision/check.go` says the id, the reason, and
the source are not read, and edges are not read. `category` joins that
list. `decision.Active` (`internal/decision/decision.go`) is the set that
reaches the matcher: `status` `decision`, `effective_status` `active`, user
or agent, blocked and conflicted included. A hypothesis never reaches it.

Tying a forbid to `category=policy` would drop a real forbid that was
stored as `implementation` or left unset. The check would allow the action.
That is a fail-open, so category stays out of the verdict.

Phase 1 adds a regression in `internal/decision/check_test.go`: the same
statements with category unset, with `policy`, and with a misleading
category (`implementation` on "Do not push until the user says so.")
produce the same `Verdict`, including `match`. A `policy` whose statement
does not forbid the action stays allowed. The test calls `CheckAction`
directly so a future filter in the CLI cannot hide a change in the matcher.

## Out of scope: "do not push notifications"

`Do not push notifications to users at night.` makes `sdf decision check
push` forbid the push. That is a false positive in the statement matcher,
and it gets its own follow-up. It is not part of any phase here.

Named actions match a cue immediately followed by the action word. The cue
list is `strongCue` in `internal/decision/check.go` (`do not`, `don't`,
`dont`, `never`, `must not`, `forbidden to`, `forbid`). The phrase pattern
built in `init` is that cue, whitespace, then the action word. `Do not push
notifications…` matches `do not push`. The separate `no` rule was written
so `no push notification` does not match (`bareNo` requires the action to
end the clause). `do not` has no such guard.

Do not paper over this by requiring `category=policy` or
`category=operations` before a forbid counts. A follow-up that changes the
matcher owns the fix. The category regression must keep treating this
sentence as a push forbid for as long as `check.go` does. Expecting it to
be allowed would bake the bug into the new tests.

## Phase 1

Schema, domain, CLI, skill, docs, and tests. The brief, the graph, the UI,
and audit counts wait for phase 2. Warnings wait for phase 3.

### Schema

Add a nullable `category TEXT` column on `decisions`. No `NOT NULL`, no
default, no SQL `CHECK`. Validation lives in `newRecord`, which is where
`status` and `decided-by` are checked today.

Two places learn the column, for the same reason `decided_by`,
`alternatives`, and `reason` are both in the `CREATE TABLE` and in
`migrate`:

- The `decisions` table in the schema string in `internal/store/store.go`
  lists `category TEXT` beside `reason`. A new file gets the column at
  create.
- `migrate` passes `category` to `addMissingColumns("decisions", …)`.
  `addMissingColumns` runs `ALTER TABLE decisions ADD COLUMN category TEXT`
  when the column is absent. Existing rows become `NULL`. It does not
  rewrite them.

`OpenReadOnly` does not call `migrate`. `addCompatViews` in
`internal/store/readonly.go` builds a temporary view from the columns of
that schema string (`schemaColumns`). A column that exists only in
`addMissingColumns` is invisible to the view, and a `SELECT` that names
`category` fails on a file no writer has migrated. Listing it in the schema
string makes `sdf decision list`, `brief`, `check`, `graph`, and the UI
read `NULL` until a command opens that file with `store.Open`, which runs
`migrate`. The commands that do are `sdf serve` (`internal/cli/cli.go`
line 300), `sdf decision add` (line 658), `sdf decision link` (line 746),
and `sdf decision retract` (line 992). The recall harness also calls
`store.Open`, on a temporary file it creates
(`internal/decision/recall/recall.go` line 305), not on the user's store.
`sdf redact` does not call `store.Open`. A scrub that is not a dry run
calls `migrate` from `OpenForScrub` (`internal/store/scrub.go`).

`InsertDecision` and `LoadDecisionsContext` name the column. An empty Go
string is stored with `nullText`, which writes `NULL`. Scan into
`sql.NullString`, as `reason` is scanned today. There is no backfill and no
inference from the statement text.

### Domain

`RecordInput` and `Record` gain `Category`. The JSON tag is
`json:"category,omitempty"`, next to `reason`. Unset is omitted.
`newRecord` in `internal/decision/decision.go`:

- Trim space. Empty after trim is unset.
- Any other value must be exactly one of the five lowercase tokens.
  `Architecture`, `none`, and a second token are rejected. Exact lowercase
  is consistent with `status`: `hypothesis` and `decision` are matched as
  written.
- `policy` with `status` other than `decision` is rejected. Suggested
  error: `category "policy" requires status "decision"`.
- Any other pair of category and status is stored.

`decision.Add` already returns before `InsertDecision` when `newRecord`
fails, so a rejected add writes nothing. The CLI prints
`sdf decision add: …` and exits 1, the same path a bad `--status` uses.

### CLI

`sdf decision add` gains `--category`. The usage line in `cmdDecisionAdd`
(`internal/cli/cli.go`) and the `add` line in `decisionHelp`
(`internal/cli/help.go`) gain
`[--category architecture|policy|product|implementation|operations]`.
The flag is optional. `--category` with no value exits 2.
`--category=` stores `NULL`, the same as omitting the flag. The last
`--category` wins, as the last `--status` wins. `none` is not a stored
value; on add it is an unknown category and exits 1.

`sdf decision list` gains `--category`. The value is one of the five
tokens, or `none`. `none` keeps records whose category is `NULL` or `""`.
A token keeps records whose stored category is that token, including
superseded and retracted rows. `effective_status` is not a filter.
`none` is only this filter sentinel. It is not a stored category, so a
row that somehow holds the text `none` does not match `--category none`
and does not match any of the five tokens. Any other filter value exits
2. A missing value exits 2.
Update `decisionListUsage` and the list help test that compares that string
exactly.

Edges: keep an edge when at least one decision endpoint is a kept decision.
Do not copy `FilterBySession` (`internal/decision/remind.go`), which keeps
decisions named by the surviving edges. That rule would pull the other
category back into the list. A kept edge may still name a decision that
the decision table omitted.

`--session` and `--category` both apply. A decision is listed only when it
survives both. An edge is listed only when it survives the session filter
and touches a decision that survives the category filter. Apply the session
filter first, then the category filter.

Text from `WriteText` gains a `CATEGORY` column after `STATUS`. The header
in `internal/decision/decision.go` is today
`ID, STATUS, EFFECTIVE, DECIDED BY, RECORDED AT, SOURCE, STATEMENT`.
Empty category prints as `-` via `orDash`. Alternatives and reason stay on
their own lines under `STATEMENT`.

`--json` stays the graph `WriteJSON` already writes, which is the same
document as `GET /api/decisions` when no filter is set. With a filter it is
the filtered graph, as `--session` already is. `category` is omitted when
unset. Phase 1 adds no query parameter on the HTTP route. The API returns
every record, with the field present only when set.

`sdf decision recall` does not read the field. Scoring is unchanged.

### Skill and docs

Proposal, pending owner confirmation: phase 1 would tell
`skills/sdf-decisions/SKILL.md` to always set `--category` for new records,
alongside the obligation it already states for `--decided-by`. That
obligation is not a decided rule. If the owner confirms it, the flag stays
optional on the CLI so an older record, and an older agent that does not
send it, still adds. The skill would state the five values, the decided
`policy` status rule, and who normally decides. It would say a settled
product or operations decision is the user's, and that an agent policy is
not a locked brief rule.

`docs/outcomes.md` gains the field on the decision-record description, the
`add` and `list` usage lines, and the list columns. One sentence in
Checking an action says the match ignores `category`. This page remains the
design record.

## Phase 2

Brief JSON, the graph, the UI, and audit counts.

### Brief

`brief.Item` gains `Category` with `json:"category,omitempty"`.
`Classify` copies it from the record and does not read it for grouping.
`isConstraint` is unchanged, so a `policy` is not automatically a brief
constraint, and a user decision of another category can still be one.

`textLine` stays `kind`, id, decided-by, statement. The session-start hook
and the text byte budget must not grow a column. Status is already absent
from that line; category is absent too.

`jsonItem`, `sizeJSON`, and `itemBytes` count the field when it is set.
Otherwise a JSON brief under `--max-bytes` under-counts and can exceed the
cap. Golden JSON in `internal/decision/brief/brief_test.go` changes only
for items that have a category.

### Graph, UI, audit

`decisiongraph.Node` does not embed `*decision.Record`. It has a named
field `Decision *decision.Record` with `json:"decision,omitempty"`
(`internal/decisiongraph/decisiongraph.go` line 170). Once `Record` carries
`category`, the JSON path is `node.decision.category`, omitted when unset.
Do not copy category onto a second node field. Do not add a graph role for
it. Roles stay `path`, `locked`, `hypothesis`, and the rest.

Each chapter gains a tally of decision nodes by category, plus a count of
uncategorised decision nodes (NULL, empty, or any stored string outside
the five). Alternatives and endings are not decision nodes and are not
counted. `Facts` and `Explored` stay as they are. The text form of
`sdf decision graph` prints the same counts the JSON carries.

The UI (`internal/ui/assets/app.js`) shows the category next to the
decision's status in the list, and on the graph detail panel. The chapter
tally sits with the existing facts line. A label is enough. No new color
token.

`sdf decision audit` keeps schema `sdf.decision.audit/v1`
(`AuditSchema` in `internal/decision/audit.go`). That name changes when a
field is removed or renamed. Phase 2 adds total fields and removes none.
`AuditTotals` gains a count of active decisions for each of the five
values, plus uncategorised, using the same bucket rule as the graph.
Finding lists stay as they are. A reader that ignores unknown JSON keys
keeps working.

## Phase 3

Warnings at add time. The record is stored. Exit 0. One line on stderr,
the same shape as `sdf decision link: warning: …`:

- Decided. `category=policy` and `--decided-by agent`: the brief does not
  lock an agent policy. Locked brief rules are user decisions
  (`Item.Locked` in `internal/decision/brief/brief.go`).
- Proposal, pending owner confirmation. `category` is `product` or
  `operations`, `status` is `decision`, and `--decided-by agent`: settled
  product and operations decisions are normally the user's. This warning
  is not a decided rule.

No warning for `architecture`, for `implementation`, for a hypothesis
(other than the phase 1 rejection of a `policy` hypothesis), or for an
empty `--decided-by`. The warning does not change the stored row and is
not itself a category.

## Phase 4

Optional free tags, if they are still wanted. Tags are a list beside
`category`, not a sixth value and not a replacement for the enum. Security,
data, and UX are the sort of thing a tag would name. Tags do not feed
`sdf decision check`. This phase has no schema until a later spec. Phase 4
can be skipped.

## Backward compatibility

Existing rows stay `NULL`. There is no backfill. A new binary does not
guess `policy` from the word "do not".

An old binary's `INSERT` and `SELECT` name their columns
(`InsertDecision`, `LoadDecisionsContext`). A nullable column they do not
mention is ignored. Their `CREATE TABLE IF NOT EXISTS` does not drop a
column a newer binary added. A file an old binary created has no
`category` column until a new binary opens it for write; a new read-only
open still sees `NULL` through the compatibility view.

Records are append-only (`package decision`: a decision is written by a
caller, and nothing in `Add` updates a row). A wrong category is fixed by
adding a new record with the right category and linking `supersedes` from
the new record to the old one. The old row keeps the category it was
stored with, and its `effective_status` becomes `superseded`.
`--category` still finds it. That is the history, not a row to rewrite.

JSON with the field omitted is the document old clients already parse.
Clients that ignore unknown keys accept a record that has `category` set.
Decisions are not ledger entries. The ledger format does not change.
`sdf decision recall` does not change.

## Tests

Phase 1:

- `newRecord`: each of the five values, trim, empty, `none` rejected,
  wrong case rejected, `policy` plus `hypothesis` rejected and stored
  nowhere, `policy` plus `decision` accepted, category not written into
  `status` or `decided_by`. `newRecord` is the only production write path:
  `decision.Add` calls it, then `InsertDecision`. Tests and raw SQL can
  bypass it by calling `InsertDecision` directly, so a store test that
  inserts a bad category does not prove `newRecord` rejected it.
- Store: `addMissingColumns` adds `category` on a file created without it,
  pre-existing rows read back unset, round-trip of each value,
  `OpenReadOnly` of an unmigrated file returns an empty category and does
  not write the file.
- CLI: add with and without `--category`, bad value exits 1 and writes
  nothing, missing flag value exits 2, `list --category` for each value
  and for `none`, combined with `--session`, text column `-` when unset,
  `--json` omits the key when unset and matches `GET /api/decisions` when
  unfiltered.
- Check regression described above, in `internal/decision/check_test.go`.
  It must fail if `CheckAction` starts reading `category`.

Phase 2: brief JSON carries the field and `textLine` does not; the byte
cap counts it; graph and audit totals; the UI list and the graph panel
show the label. Phase 3: the decided agent-policy warning on stderr,
exit 0, and the row stored as given. The product and operations warning
is tested only if the owner confirms that proposal.

## Prior art

Several public formats record a decision. None of them defines this enum.

- [Michael Nygard, *Documenting Architecture Decisions* (2011)](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions)
  uses Title, Context, Decision, Status, Consequences. Status is a
  lifecycle (`proposed`, `accepted`, `deprecated`, `superseded`). That is
  closer to sdf's `status` plus `effective_status` than to `category`.
- [MADR](https://adr.github.io/madr/) is a Markdown template. Its
  [ADR-0010](https://adr.github.io/madr/decisions/0010-support-categories.html)
  is about grouping records, and the chosen option is subfolders with ids
  local to the folder. The folder name is the category. There is no fixed
  vocabulary.
- [Y-statements](https://ozimmer.ch/practices/2020/04/27/ArchitectureDecisionMaking.html)
  are one sentence: context, need, the option taken, the options
  neglected, the benefit, and the drawback. There is no category field.
- [arc42 section 9](https://docs.arc42.org/section-9/) is a slot in a
  documentation outline for architecturally significant decisions. The
  form is a list, a table, or ADRs. It does not name an enum.
- [adr-tools](https://github.com/npryce/adr-tools) manages Nygard-form
  Markdown files and a status lifecycle. It has no category field.
- [log4brains](https://github.com/thomvaill/log4brains) stores ADRs as
  Markdown (MADR by default) and guesses metadata from the text and from
  git. It does not define a category enum.
- [Backstage's ADR plugin](https://github.com/backstage/community-plugins/tree/main/workspaces/adr)
  renders and searches ADR Markdown across repositories. It does not
  define a category enum.

sdf already stores the statement, who decided, alternatives, a reason, and
relations. `category` is the one extra token those formats leave to
folder names or leave out. It stays a closed list so `list --category` and
the counts mean the same thing in every store.

## Phases

| Phase | What lands |
| --- | --- |
| 0 | This spec. |
| 1 | Nullable column via the schema string and `addMissingColumns`. Validation in `newRecord`. `add --category`. `list --category` with a value or `none`. JSON `omitempty`. `docs/outcomes.md`. Tests, including the check regression. Skill text that always sets `--category` is a proposal, pending owner confirmation. |
| 2 | Brief JSON field, brief text columns unchanged. Graph and UI labels and counts. Audit totals. |
| 3 | Decided: a warning at add for an agent policy. Proposal, pending owner confirmation: a warning when an agent settles product or operations. |
| 4 | Optional free tags, in their own field, if a later spec still wants them. |
