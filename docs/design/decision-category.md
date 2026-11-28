# Decision categories

Status: phase 1 is implemented, including the unset-display and attribution
proposals. The decided_by lock described below is also implemented.
`category` is an optional column. Unset is stored as SQL `NULL`
and displayed as `implementation`. `decided_by` stores a GitHub login.
`user` and `agent` remain legal writes and display as legacy. Existing rows
are not rewritten. There is no add-time warning that tries to tell a person
from an agent. Current behavior is described in
[Decision records](../outcomes.md#decision-records) and
[Checking an action](../outcomes.md#checking-an-action).

Deviation from the attribution proposal: a new `decided_by` must be a
GitHub login (letters, digits, and single hyphens, at most 39 characters,
no leading or trailing hyphen, no `--`) or the legacy words `user` or
`agent`. The proposal's 500-character one-line cap is not what the CLI
stores. GitHub compares logins without regard to case. sdf stores the
bytes it was given and does not fold them. A login resolved from the local
identity lookup that is not in that form, or that is the word `user` or
`agent`, is skipped, so a display name does not fail the add and a
container default of `user` is not stored as a locked constraint. An
explicit `--decided-by` that is not in that form is rejected and stores
nothing. An explicit empty value stays empty.

Second deviation: when `--decided-by` is omitted, sdf does not call
`gh api user` and it does not use the network. It reads
`gh config get -h github.com user` (the `user` key for github.com in the
GitHub CLI's hosts.yml), then `git config --get github.user`. That git
read uses every scope, including the repository-local config. The value
is self-asserted attribution, not authentication. `git config user.name`
is not read. The hosts.yml login takes precedence over `github.user`.
`gh` has up to 3 seconds. If it hangs or fails, `git` still gets about
500ms of its own. The worst case is about 4.5 seconds: those budgets, plus up to 500ms for each killed command to close its pipes. The budgets themselves are unchanged. A configured
`user` or `agent` is skipped without regard to case. If neither command
yields a login, sdf uses the OS username (`os/user` current user, with a
Windows `DOMAIN\` prefix removed). That name is local and self-asserted,
not a GitHub authentication. It is stored only when it is a login and not
`user` or `agent` in any case. An explicit `--decided-by` is stored with
its exact case. Captured stdout is capped at 64 KiB, and a command that
does not exit has 500ms to close its pipes after the deadline. On Unix
the deadline kills the process group, so a child of `gh` does not keep
running. The lookup runs only after the rest of the record validates.

An active settled decision recorded with a GitHub login, or with the exact
legacy word `user`, is a locked brief constraint. The exact word `agent`
is not: those rows stay out of the constraint group unless they are the
source of a `constrains` edge, and then they are not locked. That keeps
the brief behavior of rows stored as `agent` before logins. Brief JSON
does not carry `category` (the brief text line still has no category
column), and the graph and audit do not tally by category. Those counts
do not key on `decided_by`, so a login is included the same way any other
decision is. `omitted_locked` counts a dropped login rule the same way it
counts a dropped `user` rule. The
brief text and JSON do mark stored `user` and `agent` as legacy. The UI
shows the category next to status, with a category prefix, and on the
graph detail panel. The decided-by filter labels `user` and `agent` as
legacy and has a Logins choice, and the legend names each login.

The sections below are the design record. Phase 1 landed the category
column and the local login lookup, with the deviations above. The
decided_by lock and the OS-username fallback are also implemented.

A decision record gains one optional field, `category`. It says what kind of
choice the record is, so a reader can separate an architecture choice, a
policy, a product choice, an implementation choice, and an operations choice.
`status` stays `hypothesis` or `decision`. The brief's derived group stays
named `constraint`. `sdf decision check` stays a match on the statement.

This page also says what `decided_by` is for. It is the GitHub login of
the team member responsible for the decision. The record does not
distinguish that person from their agent. The CLI stores that login, the
legacy words `user` and `agent`, or empty.

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
   and `operations`. The field is optional. An agent is not required to set
   it. When it is unset, the category is `implementation`. Unset is still
   stored as SQL `NULL`: the column has no SQL default and no backfill.
   Readers show that `NULL` as Unset specifies, and that display is
   implemented. There is no
   `other`. Security, data, and UX do not get values: they cut across the
   five. A security rule that forbids an action is a `policy`. A security
   boundary in the design is `architecture`. Free tags that could name
   those cross-cutting concerns are deferred to phase 4.
3. The value is `policy`, not `constraint`. `constraint` is already the
   brief's derived group. `isConstraint` in
   `internal/decision/brief/brief.go` selects an active record whose
   `status` is `decision` and whose stored `decided_by` is a human
   identity, or that is the source of a `constrains` edge.
   `decision.LockedDecidedBy` is that identity: a GitHub login, or the
   exact legacy word `user`. The exact word `agent` is not. sdf does not
   distinguish a person from their agent; a login is the team member
   responsible. `RelConstrains` (`constrains`) is a stored relation.
   A category and that derived group answer different questions, and a
   record can be one without being the other. See Attribution.
4. `policy` requires `status=decision`. `sdf decision add` rejects a
   `policy` hypothesis and stores nothing. A `policy` with
   `status=decision` is stored for whatever `decided_by` the caller
   passed. There is no warning on that add. The brief sets `Locked` when
   `LockedDecidedBy` is true (`Item.Locked` in the same file). A `policy`
   is not a constraint merely because its category is `policy`. A login
   is. The exact word `agent` stays unlocked, including when a
   `constrains` edge makes that row a constraint.
5. `sdf decision check` never reads `category`. It stays statement-only
   (`CheckAction` in `internal/decision/check.go`). A forbid that was filed
   under the wrong category must still block. Gating the check on
   `category=policy` would make a miscategorised forbid fail open.
6. `decided_by` records the GitHub login of the team member responsible
   for the decision, for example `paulpham157`. An agent acting for a
   person records that person's login. The decision record does not
   distinguish a person from their agent. The purpose of the field is to
   show who on the team decided. The CLI stores that login, the legacy
   words `user` and `agent`, or empty. Resolving the login, and how rows
   already stored as `user` or `agent` are shown, are under Attribution.
   Phase 1 implements that resolution and the legacy display, with the
   login-format deviation in the status above.

The only pair the CLI refuses, once `category` exists, is `policy` with
`hypothesis`. `decided_by` is not an access check.

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

Both `hypothesis` and `decision` are allowed. The category does not say
whether a person or an agent wrote the row. Useful relations: `supersedes`
and `refines` against another architecture record, `implements` from the
session or commit that built it, and `constrains` when this choice limits
a later record.

### policy

A rule, not a topic. "Do not push until the user says so" is a policy.
"Waiting for the user's OK to push" is a hypothesis and cannot be category
`policy`. Use `operations` when the open question is operational. Omitting
the category stores `NULL` and displays as `implementation`, so set
`operations` when that is the category.

`status` must be `decision`. `sdf decision check` still matches the
statement, because check uses `decision.Active` (every in-force settled
decision, whatever `decided_by` is), not the brief's locked set. See
`activeForCheck` in `internal/cli/cli.go`. There is no warning when the
caller records a policy.

Useful relation: `constrains`, from this decision to whatever it limits.
The relation name stays `constrains`. The category name stays `policy`.

A decision of any category can still land in the brief's `constraint`
group, because that group follows `LockedDecidedBy` and the `constrains`
edge, not this field. A GitHub login is a locked constraint. That is the
responsible team member's login, not proof that a person rather than
their agent typed the row. The exact word `agent` is not a locked
constraint.

### product

A choice about what a person can do or see. "Show agent status in the UI"
is product. The library used to draw it is implementation.

Both `hypothesis` and `decision` are allowed. Useful relations:
`supersedes`, `refines`, and `implements` from the session that built the
behavior. `blocks` fits while an open question still holds the work.

### implementation

A choice about how code is written inside an architecture that already
holds. "The SQLite driver stays pure Go" is implementation.

Both `hypothesis` and `decision` are allowed. An omitted category is this
one, by the default under Unset. Useful relations: `implements`,
`refines`, and `supersedes`.

### operations

A choice about running the system. "Do not deploy on Friday" is operations.
The same sentence is also a forbid for `sdf decision check deploy`, because
the statement matches, whatever the category is.

Both `hypothesis` and `decision` are allowed. Useful relations:
`constrains` when the rule limits later work, and `supersedes` when the
rule is replaced.

### Unset

Omitted means the default category, `implementation`. The column is still
`NULL`. Nothing in phase 1 guesses a category from the statement. Old rows
stay `NULL`. There is no backfill.

Implemented: readers display `NULL` and an empty string as
`implementation`, and they leave the stored `NULL` in place. JSON still
omits the key (`json:"category,omitempty"`), and a missing key means
`implementation`. A stored token is shown as itself. Explicit
`--category implementation` stores the token `implementation`. Omitting
the flag stores `NULL`. Both display as `implementation`. A reader sees
one category either way, which is what a default is for when the caller
is not required to set the flag.

Storing the token `implementation` whenever the flag is omitted was the
other option, and it is not the one this proposal takes. That write would
make an explicit choice and an omitted flag the same bytes, it would
change the empty-is-unset rule in `newRecord`, and a file from before the
column would need a backfill this spec already rejects. Display keeps the
accepted storage rule (nullable, no SQL default, no backfill) and still
gives every reader one category for an unset field.

The text list prints `implementation` for `NULL` and
empty. `-` stays the placeholder `source` and `decided_by` use when empty.

## Status

| Category | Status |
| --- | --- |
| `architecture` | `hypothesis` or `decision` |
| `policy` | `decision` only |
| `product` | `hypothesis` or `decision` |
| `implementation` | `hypothesis` or `decision`. This is the default when `category` is unset |
| `operations` | `hypothesis` or `decision` |

No category is tied to a person or an agent. There is no add-time warning
that tries to tell them apart. See Attribution.

## Attribution

`decided_by` names who on the team is responsible for the decision. The
value is that person's GitHub login, for example `paulpham157`. An agent
acting for a person records that person's login. The decision record does
not distinguish a person from their agent. The hash-chained ledger in
[Ledger](../ledger.md) is a different file. It does not gain this field,
and its format does not change. Decisions are not ledger entries.

sdf cannot tell a decision a person made from one their agent made. The
caller can pass `--decided-by user` or present a token, and an agent on
the same machine can do each of those itself. A confirm control in the
web UI is not a way to set the value: the UI and `GET /api/decisions` are
read-only, and an agent on the same machine could drive a confirm anyway.
A self-declared value is not evidence of a person. Signing every decision
was considered and rejected: the cost of a signature on every record is
too high for what this store is for. sdf does not warn when a record is
added, and it does not treat `decided_by` as proof that a person, rather
than an agent, settled the choice.

Phase 1 accepts a login. `newRecord` in `internal/decision/decision.go`
allows a GitHub login, empty, `user`, or `agent`. Any other
`--decided-by` is rejected and the add stores nothing. The login is at
most 39 characters. It cannot start or end with a hyphen, and it cannot
contain `--`. The 500-character cap below was the proposal and is not
what the CLI stores.

Implemented, with the local-read deviation in the status above. How the
login is chosen, in this order:

1. An explicit `--decided-by`. The stored value is that string, trimmed,
   with its exact case. `User` is not the legacy word `user`.
   sdf does not ask GitHub whether the account exists. An explicit empty
   value stays empty and does not fall through to the next step. An
   explicit value that is not a login or `user` or `agent` is rejected
   and the add stores nothing.
2. When the flag is omitted, and only after the rest of the record
   validates, a configured identity read on this machine. First
   `gh config get -h github.com user`, which reads hosts.yml and does not
   use the network. If that command is missing, fails, or is not an
   accepted login, `git config --get github.user`. That read uses every
   scope, including the repository, and the value is self-asserted
   attribution, not authentication. `git config user.name` is not a
   source. A value that is empty, not a login, or `user` or `agent` in
   any case is skipped.
3. When neither command yields a login, the OS username. sdf reads
   `os/user` current user and strips a Windows `DOMAIN\` prefix. The
   remainder is accepted only when it is a login and not `user` or
   `agent` in any case. The name is local and self-asserted, not a
   GitHub authentication, and this step does not run a command. When it
   is not accepted, the field stays empty. A nil command runner skips
   this step too, so a caller that turns lookup off stays empty.

Implemented. The allow-list of only `user` and `agent` is gone. `user`
and `agent` stay legal writes, so a caller that still passes them keeps
working. They are not a check of who typed them. Auto-resolution never
stores those words.

Implemented. Rows already stored as `user` or `agent` are
kept as those strings. Nothing rewrites them. List, brief, check, the
graph, and the UI display that pair as legacy: the text form prints the
stored word and a marker, for example `user (legacy)`, and JSON keeps the
stored string and sets `decided_by_legacy` true for that pair only. The
marker is omitted otherwise. A login displays as the login, with no
marker. Empty stays empty and still prints as `-`. A new write of `user`
or `agent` uses the same legacy display.

Implemented. The brief's lock is `LockedDecidedBy`: a GitHub login, or
the exact legacy word `user`. The exact word `agent` does not become
locked. An `agent` row is a constraint only as the source of a
`constrains` edge, and then it is not locked. Rows already stored as
`agent` keep that behavior. A login is locked the same way `user` is,
including the `--max-items` exemption and `omitted_locked`. Graph chapter
counts and audit totals do not filter on `decided_by`.

## Check stays on the statement

`CheckAction` matches the statement and nothing else. The comment on
`CheckAction` in `internal/decision/check.go` says the id, the reason, and
the source are not read, and edges are not read. `category` joins that
list. `decision.Active` (`internal/decision/decision.go`) is the set that
reaches the matcher: `status` `decision`, `effective_status` `active`,
whatever `decided_by` is, blocked and conflicted included. A hypothesis
never reaches it.

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

Schema, domain, CLI, docs, and tests. The list and the graph detail panel
already show the category. Brief JSON `category` and the graph and audit
category tallies are not implemented. The decided_by lock is: a login and
legacy `user` are locked constraints, and the exact word `agent` is not.
There is no
add-time warning. Phase 3 is withdrawn. The skill does not gain a
requirement to set `--category`.

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
`migrate`. The commands that do are `cmdServe`, `cmdDecisionAdd`,
`cmdDecisionLink`, and `cmdDecisionRetract` in `internal/cli/cli.go`.
Each calls `store.Open` on the user's store. The recall harness also
calls `store.Open`, in `runOne` (`internal/decision/recall/recall.go`),
on a temporary file it creates, not on the user's store.
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

- Trim space. Empty after trim is unset (`NULL`). Readers display it as
  `implementation`, as specified under Unset.
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
tokens, or `none`. A token other than `implementation` keeps records
whose stored category is that token, including superseded and retracted
rows. `effective_status` is not a filter. `none` is only this filter
sentinel. It is not a stored category, so a row that somehow holds the
text `none` does not match `--category none` and does not match any of
the five tokens. Any other filter value exits 2. A missing value exits 2.

Implemented, the same display rule as Unset.
`--category implementation` keeps a stored `implementation` and a `NULL`
or empty category. A reader sees both as `implementation`.
`--category none` is a filter sentinel, not a stored category and not
that display default. It keeps a stored string outside the five tokens.
It does not keep `NULL` or empty: those display as `implementation` and
match `--category implementation` instead. A row that holds the text
`none` matches neither the sentinel nor any of the five tokens.

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
Implemented: an empty category prints as `implementation`,
not as `-` via `orDash`. Alternatives and reason stay on their own lines
under `STATEMENT`. A `--category` filter that keeps nothing prints
`No decisions match.` and does not say the store is empty.

`--json` stays the graph `WriteJSON` already writes, which is the same
document as `GET /api/decisions` when no filter is set. With a filter it is
the filtered graph, as `--session` already is. `category` is omitted when
unset. Phase 1 adds no query parameter on the HTTP route. The API returns
every record, with the field present only when set.

`sdf decision recall` does not read the field. Scoring is unchanged.

### Skill and docs

The skill does not require `--category`. An agent may omit it. The earlier
proposal that phase 1 would tell `skills/sdf-decisions/SKILL.md` to always
set `--category` is withdrawn. The flag stays optional on the CLI so an
older record, and an agent that does not send it, still adds. The skill
states that the field is optional, that unset means `implementation`, that
`policy` requires `status=decision`, and that `sdf decision check` does
not read `category`. It does not say that a person, rather than an agent,
must record a given category.

The skill's working `--decided-by` examples are GitHub logins. Omitting
the flag auto-resolves. `user` and `agent` still store and display as
legacy. An agent acting for a person records that person's login. sdf
does not check the value. A settled decision stored with that login is a
locked brief constraint. The exact word `agent` is not. `sdf decision
check` still blocks from the statement.

`docs/outcomes.md` gains the field on the decision-record description, the
`add` and `list` usage lines, and the list columns, when the field is
implemented. One sentence in Checking an action says the match ignores
`category`. This page remains the design record. Until that
implementation, `docs/outcomes.md` describes the CLI that ships.

## Phase 2

Brief JSON, the graph, the UI, and audit counts.

### Brief

`brief.Item` gains `Category` with `json:"category,omitempty"`.
`Classify` copies it from the record and does not read it for grouping.
`isConstraint` does not read `category`, so a `policy` is not
automatically a brief constraint. It does treat a GitHub login, and the
exact legacy word `user`, as a locked constraint. The exact word `agent`
is not. A decision stored as `user` of another category can still be a
constraint. A login of another category can too.

Making an active settled policy a constraint by category
(`status` `decision`, `category` `policy`, effective status `active`)
is a later amendment. It is not required for the login lock. A person-set
rule is recorded with the responsible person's login, or with
`--decided-by` omitted.

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

Each chapter gains a tally of decision nodes by category. Proposal, not
implemented, the same display rule as Unset: `NULL` and empty count as
`implementation`. A stored string outside the five counts on its own, not
as `implementation` and not as a sixth category. Alternatives and endings
are not decision nodes and are not counted. `Facts` and `Explored` stay as
they are. The text form of `sdf decision graph` prints the same counts the
JSON carries.

The UI (`internal/ui/assets/app.js`) shows the category next to the
decision's status in the list, and on the graph detail panel. The chapter
tally sits with the existing facts line. A label is enough. No new color
token.

Accepted deviation, shipped with phase 1: the decision list, the graph
list, node labels, and the detail panel already show the category, and
the decided-by filter names legacy `user` and `agent` plus Logins. The
chapter tally and the audit totals stay in this phase.

`sdf decision audit` keeps schema `sdf.decision.audit/v1`
(`AuditSchema` in `internal/decision/audit.go`). That name changes when a
field is removed or renamed. Phase 2 adds total fields and removes none.
`AuditTotals` gains a count of active decisions for each of the five
values, plus a count of stored strings outside the five, using the same
bucket rule as the graph. `NULL` and empty count as `implementation`.
Finding lists stay as they are. A reader that ignores unknown JSON keys
keeps working.

## Phase 3

Withdrawn. There is no warning at add time, for any category, that tries
to tell a person from an agent. The phase 3 warning for an agent `policy`,
and the proposed warning when an agent settled a `product` or
`operations` decision, are both dropped. The reason is Attribution: a
self-declared `--decided-by` and a token can each be produced by an agent
on the same machine. The web UI is not a confirm step: it and
`GET /api/decisions` are read-only. Signing every decision was rejected
as too costly. The phase 1 rejection of a `policy` hypothesis
stays. It is a validation error, exit 1, and it stores nothing. It is not
a warning, and it does not read `decided_by`.

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
  and for `none`, combined with `--session`, text column `implementation`
  when unset (the display proposal), `--json` omits the key when unset and
  matches `GET /api/decisions` when unfiltered.
- Check regression described above, in `internal/decision/check_test.go`.
  It must fail if `CheckAction` starts reading `category`.

Phase 2: brief JSON carries the field and `textLine` does not; the byte
cap counts it; graph and audit totals; the UI list and the graph panel
show the label. Phase 3 has no tests. The warning is withdrawn.

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
| 1 | Implemented. Nullable column via the schema string and `addMissingColumns`. Validation in `newRecord`. `add --category`, optional. `list --category` with a value or `none`. JSON `omitempty`. Unset displays as `implementation` (stored `NULL`, not rewritten). `decided_by` is a GitHub login, with legacy `user` / `agent` kept. `docs/outcomes.md`. Tests, including the check regression. The skill does not require `--category`. |
| 2 | Category brief JSON, graph category tallies, and audit category totals are not implemented. The decided_by lock is: an active settled decision whose `decided_by` is a GitHub login, or legacy `user`, is a locked brief constraint. The exact word `agent` is not, unless a `constrains` edge makes it an unlocked constraint. An omitted `--decided-by` falls back to the OS username after `gh` and `git config github.user`. |
| 3 | Withdrawn. No add-time warning that tries to tell a person from an agent. |
| 4 | Optional free tags, in their own field, if a later spec still wants them. |
