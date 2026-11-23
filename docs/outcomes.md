# Outcomes

Telemetry says a tool call succeeded. It does not say the change was merged.
`sdf summary` attaches an outcome from the local checkout, and stops when the
match would be a guess. Nothing in this step calls the network unless you
pass `--github`.

## Linking a session to a commit

A link is **exact** when `claude_code.tool_result` carries
`vcs.ref.head.revision` or a full 40-character `git_commit_id` (including one
inside `tool_parameters`). Claude Code adds those only when
`OTEL_LOG_TOOL_DETAILS=1`, and an abbreviated id is ignored. The SHA has to
exist in the checkout. If it does not, the session stays unlinked. SDF does
not substitute a hook commit or a different commit from the same window.

A link is **hook** when an `sdf.git.commit` log from `sdf init --git-hooks`
is already on this session. The hook copied the session id the agent put in
the environment, so the SHA is that session's commit. Several commits in one
session are all kept. A `post-rewrite` event drops the SHA it replaced.
The checkout path on the event is used to verify the object when `--repo`
is not set. `--repo PATH` is that checkout instead, including when the path
on the hook event still exists on disk. If `--repo` or the hook path
disagrees with a working directory the agent exported, the session stays
unlinked. A hook SHA that is not in this checkout can still match an
`SDF-Session` line in a commit that is. A missing exported SHA does not fall
through to the hook.

A link is **trailer** when a commit message has a line `SDF-Session: <id>`
for this session id. Summary reads that line from the message. It does not
use git's trailer block, which keeps only the last block: a GitHub squash
message often puts a blank line and then `Co-authored-by` after the session
line, and that block would hide `SDF-Session`. The line is how a squash
merge can stay attributed after the original SHA is gone, and only when the
squash message still contains it. See [Git hooks](receiver.md#git-hooks).

A link is **heuristic** when none of those fired, the session has one
absolute working directory (`pi.cwd` from pi-otel, a single
`workspace.host_paths` value from Claude Code, or `sdf.session.cwd` from the
Grok CLI session hook that `sdf init grok --write` installs), that directory is a git
checkout on this machine, and exactly one commit has a committer time inside
the session window widened by two seconds. Zero commits, or more than one,
stays unlinked. The same commit in two sessions' windows unlinks both.
A commit that carries an `SDF-Session` trailer, or that a hook already
attributed to another session, is not given out by the heuristic.
`--repo PATH` supplies a checkout when the telemetry has no directory, and
it is the checkout used for a hook or trailer link when you pass it. If it
disagrees with a directory the agent did export, the session stays unlinked.

Without the git hooks, Codex and OpenCode do not export a working directory
or a commit SHA on the signals this receiver stores, so those sessions stay
unlinked unless you pass `--repo` and the time window contains one commit.
Grok CLI's telemetry has none either, and Grok ignores
`OTEL_RESOURCE_ATTRIBUTES`, so one cannot be added there; the Grok session
hook from `sdf init grok --write` supplies `sdf.session.cwd`, and `--repo`
remains the fallback when that hook is not installed (see
[Grok CLI](agents.md#grok-cli)).
Pi exports `pi.cwd` and can use the heuristic, and the hook when the
session file stem matches `pi.session.id`. A commit with no session id is
not attached to a session by the hook. Under Codex's default
`workspace-write` sandbox the hook does not run, because `.git` is
read-only. See [Git hooks](receiver.md#git-hooks).

## Pull requests and CI

`--github` asks for the pull request and CI status of a commit that is already
linked. It uses `gh` when `gh` is on `PATH`, and otherwise `GH_TOKEN` or
`GITHUB_TOKEN` against `api.github.com`. The token is a header, not an
argument. The remote has to be `github.com`. The commit's pull request list
is `GET /repos/{owner}/{repo}/commits/{sha}/pulls`. That list is empty for a
closed pull request that was not merged. When it is empty and the hook
recorded a branch for that commit, summary lists
`GET /repos/{owner}/{repo}/pulls?state=all&head={owner}:{branch}` and keeps
a pull request whose head is that commit. A closed, unmerged pull request
is then reported, `accepted` is false, and the reason says the pull request
was closed without merging. No pull request means not accepted. Several
pull requests, or a failed query, means accepted is unknown. CI is
`success`, `pending`, `failure`, or `none`. GitHub's combined status is
`pending` with `total_count` 0 when the commit has no statuses. That is
`none` when there are no check runs either, not a pending check.

`cost_per_accepted_outcome_usd` is the sum of `computed_cost_usd` for sessions
whose pull request is merged, divided by how many such sessions there are.
It is `unknown`, not zero, when any session in the set has no known outcome,
when an accepted session has unknown computed cost, when the same commit is
accepted on two sessions, or when nothing was merged.

## Decision records

A decision is something a caller writes down. The summary does not infer one
from telemetry, and recording one changes nothing about collection or
summarization.

Each record has an id, one statement, and a status of `hypothesis` or
`decision`. A hypothesis is an assumption, not a settled fact. Optional fields
name who decided (`user` or `agent`), the alternatives that were on the table,
a reason, and a source pointer. Time is `recorded_at`. A session is not stored
on the record; link it with an edge when the store already has that session.
An edge connects two endpoints and uses one of
`constrains`, `implements`, `supersedes`, `validates`, `contradicts`,
`blocks`, `conflicts_with`, or `refines`. `blocked_by` and `depends_on` are
accepted as aliases of `blocks` (see Link rules). An
endpoint is a decision id, a session id, or a full 40-character commit SHA. A
session or commit endpoint is stored only when the store already has telemetry
for it; the edge is not a copy of that telemetry. A commit SHA is matched
without regard to case, so a SHA the agent exported in uppercase still matches
the lowercase ref the edge stores.

`GET /api/decisions` returns the records and edges in insertion order. An empty
store returns empty lists. `sdf decision add` writes one record. It needs an id,
a statement, and a status of `hypothesis` or `decision`. `--decided-by`,
`--alternatives`, `--reason`, and `--source` are optional. `sdf decision link`
writes one edge. It needs a relation and two `KIND:REF` endpoints, where
`KIND` is `decision`, `session`, or `commit`. No route writes a record. An
agent skill that uses these commands is in [skills/sdf-decisions](../skills/sdf-decisions/SKILL.md).

### Link rules

`sdf decision link` checks an edge before storing it, and stores nothing when a
check fails. It exits 1 with one `sdf decision link: ...` line on stderr, the
same as for an unknown session, commit, or decision. Usage errors, such as a
missing flag, exit 2.

| Relation | Valid direction | Extra checks | Drives at read time |
| --- | --- | --- | --- |
| `supersedes` | decision → decision: `from` replaces `to` | cycle | `effective_status`, `superseded_by` |
| `blocks` | decision → decision: `to` cannot be settled or acted on until `from` is settled | cycle; warns when the pair also has `supersedes` or `conflicts_with` | `blocked_by`, `open_blockers`, `blocked` |
| `conflicts_with` | decision ↔ decision: the two cannot both hold. Symmetric | stored in canonical order; refused when one already supersedes the other | `conflicts_with`, `conflicted` |
| `refines` | decision → decision: `from` elaborates, narrows, or partly amends `to`, which still holds | cycle; refused when one already supersedes the other | `refined_by` |
| `implements` | session or commit → decision: the work implements the decision | | nothing |
| `validates` | session, commit, or decision → decision: evidence that it holds | warns when the pair also has `contradicts` | `challenged` |
| `contradicts` | session, commit, or decision → decision: evidence against it | warns when the pair also has `validates` | `challenged` |
| `constrains` | decision → decision, session, or commit: the decision is a rule that limits it | | nothing |

Every endpoint kind in the second column is enforced. `constrains` is a rule,
not an ordering: use `blocks` for "this must wait for that".

**Aliases.** `--relation blocked_by --from decision:b --to decision:a` and
`--relation depends_on --from decision:b --to decision:a` both store
`a blocks b`. An alias is never stored, and it is the same edge as the
canonical form for the duplicate and cycle checks. Refs and endpoint kinds
are checked as typed, before the swap, so the message names the alias and the
flag you gave: `depends_on links a decision to a decision: to is session:sess-1`.

**Canonical order.** `conflicts_with` between two decisions is stored with the
endpoint that sorts first (`decision:ID`, byte order) as `from`. `a
conflicts_with b` and `b conflicts_with a` are therefore one edge, and the
second is refused as a duplicate whichever order it was given in.

Every relation also has these rules:

- **Self-link.** `from` and `to` cannot be the same endpoint. The message is
  `an edge cannot link an endpoint to itself: decision:a`.
- **Retracted.** A retracted decision takes no new edges, at either end and
  for any relation. The message is
  `decision retracted: decision:a takes no new edges`. Edges recorded before
  the retraction stay. See Retracting a decision.
- **Duplicate.** The same relation, `from`, and `to` cannot be stored twice.
  A commit SHA is compared in lowercase. The message is
  `edge already recorded: decision:a constrains decision:b`. The other
  direction, or another relation between the same two endpoints, is a
  different edge.
- **Endpoint kinds.** An endpoint of a kind the relation does not take (see
  the table) is rejected before the store is asked whether the ref exists. The
  message names the rule and the end, for example
  `supersedes links a decision to a decision: from is session:sess-1`,
  `blocks links a decision to a decision: to is commit:SHA`,
  `implements links a session or commit to a decision: from is decision:a`,
  `validates points to a decision: to is session:sess-1`, or
  `constrains starts from a decision: from is session:sess-1`.
- **Cycles.** `supersedes`, `blocks`, and `refines` each reject an edge that
  would close a cycle through edges of the same relation. `a supersedes b` is
  rejected when `b` already reaches `a` through `supersedes` edges, including
  `b supersedes a`. The message names the path, for example
  `supersedes cycle: decision:c already reaches decision:a through supersedes (c -> b -> a), so decision:a cannot supersede decision:c`,
  and in the same form `blocks cycle: ... so decision:c cannot block decision:a`
  and `refines cycle: ... so decision:c cannot refine decision:a`. A blocks
  cycle would be a deadlock. Two decisions may supersede, block, or refine the
  same one, and edges of other relations do not count toward a cycle.
- **Already superseded.** `conflicts_with` and `refines` are refused between
  two decisions when one already supersedes the other, in either direction:
  `pair already linked by supersedes: decision:a and decision:b; one already replaces the other, so refines is not recorded`.
  The other way round is allowed: `supersedes` after `conflicts_with` is
  stored, and resolves the conflict.
- **Blocks over a linked pair.** A `blocks` edge between two decisions that
  already have a `supersedes` or `conflicts_with` edge is stored, and prints
  `sdf decision link: warning: decision:a and decision:b also have a conflicts_with edge`.
- **Validates and contradicts.** When the same two endpoints, in either
  direction and in either order, end up with both a `validates` and a
  `contradicts` edge, the edge is still stored and the command exits 0. It
  prints
  `sdf decision link: warning: session:sess-1 and decision:a have both validates and contradicts edges`
  on stderr.

The duplicate, cycle, and warning checks read the stored edges inside the same
SQLite transaction as the insert (`BEGIN IMMEDIATE`, which takes the write lock
first). Two `sdf decision link` runs at the same moment therefore cannot both
pass a check that only one of them should. The checks only read: no edge or
record is updated or deleted. Edges that an older sdf stored without these
checks stay readable, count as existing edges for the duplicate and cycle
checks, and are handled at read time as described below.

**Older rows that break the endpoint kinds.** Before the kinds were enforced,
sdf stored `implements`, `validates`, `contradicts`, and `constrains` between
any known endpoints, for example `decision:a implements decision:b`. Such a
row is kept as stored: nothing is rewritten or deleted, `sdf decision list`,
`GET /api/decisions`, and the UI list it with the other edges, and it still
counts as an existing edge for the duplicate check. It drives no read-time
field: `challenged` counts a `validates` or `contradicts` edge only when it
points to a stored decision, and `implements` and `constrains` drive nothing.
The same holds for any `blocks`, `conflicts_with`, or `refines` row with a
session or commit end, a self-link, or an id never stored. Writing the same
row again is refused by the kind check, which runs before the duplicate
check. No HTTP route writes
an edge, so `sdf decision link` is the only way to write one.

### Blocked, conflicted, refined, and challenged at read time

These fields are computed each time the records are read, like
`effective_status`, and are never stored. Each is omitted from JSON when it is
empty or false, so a store without these relations reads exactly as before.
`effective_status` keeps its values (`active`, `superseded`, `retracted`);
the new fields sit beside it. Only an edge between two different stored
decisions counts for `blocks`, `conflicts_with`, and `refines`. Ids are listed
once each, in the order the edges were recorded.

- `blocked_by`: every decision with a `blocks` edge to this one.
- `open_blockers`: the blockers that are still unsettled. Records never change
  status, so a hypothesis is settled by superseding it with a decision record.
  sdf follows `superseded_by` from the blocker to its current heads, the active
  decisions at the end of each replacement path. A retracted decision on the
  way contributes no head, because a withdrawn blocker blocks nothing. The
  blocker is open when any head has status `hypothesis`. So a blocking
  hypothesis stays open when another hypothesis replaces it, and clears once a
  `decision` record replaces it or it is retracted. A blocker that was
  recorded as a `decision` is settled from the start. A blocker that reaches
  no head only because its `supersedes` chain runs into a cycle (rows an older
  sdf stored) is open: no decision record settled it.
- `blocked`: `true` when the record is `active` and `open_blockers` is not
  empty. A superseded or retracted record is never blocked, though its
  `blocked_by` and `open_blockers` are still listed.
- `conflicts_with`: the other side of every `conflicts_with` edge, from either
  end.
- `conflicted`: `true` when the record is `active` and at least one of those
  is `active`. Superseding or retracting one side resolves the conflict; the
  pair stays listed.
- `refined_by`: every decision with a `refines` edge to this one. The refined
  record keeps its own `effective_status`; `refines` never supersedes.
- `challenged`: `true` when the record is `active` and the latest `validates`
  or `contradicts` edge to it, in edge order, is `contradicts`. A later
  `validates` clears it. A `validates` or `contradicts` edge from a decision
  that is not `active` (retracted or superseded) is ignored, as a withdrawn
  blocker blocks nothing: it neither challenges nor clears a challenge. One
  from a session or commit always counts.

`sdf decision list` adds `, blocked`, `, conflicted`, and `, challenged` to
the `EFFECTIVE` cell, in that order, and prints `blocked by: IDS (open: IDS)`
(or `(settled)`), `conflicts with: IDS`, and `refined by: IDS` lines under the
statement:

```text
Decisions: 6
ID             STATUS      EFFECTIVE           DECIDED BY  RECORDED AT                       SOURCE  STATEMENT
which-db       hypothesis  active              agent       2026-11-23T17:18:46.360788+00:00  -       Pick the store: SQLite or Postgres.
design-schema  decision    active, blocked     agent       2026-11-23T17:18:46.367151+00:00  -       Design the rejections schema.
                                                                                                     blocked by: which-db (open: which-db)
use-sqlite     decision    active, conflicted  user        2026-11-23T17:18:46.373929+00:00  -       Keep the store in SQLite.
                                                                                                     conflicts with: use-postgres
use-postgres   decision    active, conflicted  agent       2026-11-23T17:18:46.381759+00:00  -       Move the store to Postgres.
                                                                                                     conflicts with: use-sqlite
dont-push      decision    active              user        2026-11-23T17:18:46.390932+00:00  -       Do not push without an OK.
                                                                                                     refined by: push-after-ok
push-after-ok  decision    active              user        2026-11-23T17:18:46.397456+00:00  -       Push feature branches once the user says OK.

Edges: 3
RELATION        FROM                    TO                      RECORDED AT
blocks          decision:which-db       decision:design-schema  2026-11-23T17:18:46.403783+00:00
conflicts_with  decision:use-postgres   decision:use-sqlite     2026-11-23T17:18:46.410590+00:00
refines         decision:push-after-ok  decision:dont-push      2026-11-23T17:18:46.418467+00:00
```

The edges above were written with `depends_on` (design-schema on which-db),
`conflicts_with` from use-sqlite to use-postgres, and `refines`; the table
shows the stored, canonical form. The UI shows "blocked by" (open blockers),
"conflicts with" (only the sides still active), and "challenged" next to the
record, and "refined by", settled or still-open blockers of a record that is
not blocked, and resolved conflicts on its detail line.

### Superseded at read time

A `supersedes` edge reads as "`from` supersedes `to`": the `from` decision
replaces the `to` decision. Only an edge between two different decisions,
both of them stored records, counts. `sdf decision link` no longer stores a
`supersedes` edge with a session or commit endpoint, a self-link, or a cycle
(see Link rules). A database written by an older sdf may still hold such
edges, or an edge naming a decision id that was never stored. Those edges stay
as stored and are listed with the other edges, but they change no status. The
cycle check uses the same rule, so it does not follow them either.

The stored record never changes. Each time the records are read, every record
gets two fields that are not stored:

- `effective_status`: `superseded` when at least one decision supersedes it,
  otherwise `active`. `status` stays what was recorded (`hypothesis` or
  `decision`).
- `superseded_by`: the ids of the decisions that supersede it, once each, in the
  order the edges were recorded. It is omitted when the record is active.

Replacement is direct, not transitive. If `b` supersedes `a` and `c` supersedes
`b`, then `a` is superseded by `b`, `b` is superseded by `c`, and `c` is
active. To find the latest replacement, follow `superseded_by`. A cycle, such as
`a` and `b` superseding each other, marks both superseded. `sdf decision list`,
`GET /api/decisions`, and the UI show the same values, because all three read
them from the same code. The UI shows "superseded by ID" next to the record.

`sdf decision list` prints the records and then the edges, each as a table in
insertion order. An empty source or `DECIDED BY` prints as `-`. When a record
has alternatives or a reason, each follows its row on its own line in the
`STATEMENT` column, as `alternatives: TEXT` and `reason: TEXT`. They are free
text up to 500 bytes, so they are kept out of the columns. `--json` carries
the same fields as `decided_by`, `alternatives`, and `reason`. A cell that holds a
non-printing character, such as a tab, DEL, or a right-to-left override like
U+202E, or that is not valid UTF-8, is printed Go-quoted so it cannot break the
columns or reorder the terminal line. Every column follows this rule, including
`EFFECTIVE`, `DECIDED BY`, and the alternatives and reason values (the label
stays outside the quotes), and any column added later must too. `--json` does not quote: it
leaves DEL and RTL characters raw in the JSON strings, exactly as
`GET /api/decisions` does, so a reader that prints JSON values to a terminal
has to escape them itself.
`sdf decision list --json` prints exactly what `GET /api/decisions` returns,
because both read the store through the same code. Like `sdf summary`, it reads
`sdf-telemetry.sqlite` unless `--db` names another file, and it opens the file
read-only: it never writes, never migrates, and never creates a file. With no
records and no edges it prints `No decisions in the store.`, and `--json`
prints empty lists. A file that does not exist, in a directory that does, reads
as such an empty store and is not created (exit 0). A missing directory exits
1 with `sdf decision list: store: cannot open "PATH": directory "DIR" does not
exist`, the same message `sdf summary` gives. A file written before a later
column or table, such as one without `decision_retractions`, is read as is:
the missing columns read as empty and the missing table as no retractions.

```bash
sdf decision add --id use-sqlite --statement "Store telemetry in SQLite" \
  --status decision [--decided-by user|agent] [--alternatives TEXT] \
  [--reason TEXT] [--source TEXT] [--db PATH]
sdf decision link --relation implements --from session:SESSION_ID \
  --to decision:use-sqlite [--db PATH]
sdf decision link --relation supersedes --from decision:use-wal \
  --to decision:use-sqlite [--db PATH]   # use-wal replaces use-sqlite
sdf decision list [--db PATH] [--json]
sdf decision retract --id use-sqlite [--reason TEXT] [--retracted-by user|agent] [--db PATH]
```

```text
Decisions: 3
ID          STATUS      EFFECTIVE              DECIDED BY  RECORDED AT                       SOURCE     STATEMENT
use-sqlite  decision    superseded by use-wal  user        2026-11-23T15:00:00.000000+00:00  issue 168  Store telemetry in SQLite
                                                                                                        alternatives: Postgres, DuckDB
                                                                                                        reason: One file, no server
pure-go     hypothesis  active                 agent       2026-11-23T15:00:01.000000+00:00  -          The driver stays pure Go
use-wal     decision    active                 -           2026-11-23T15:00:02.000000+00:00  -          Store telemetry in SQLite with WAL

Edges: 2
RELATION    FROM              TO                   RECORDED AT
constrains  decision:pure-go  decision:use-sqlite  2026-11-23T15:00:03.000000+00:00
supersedes  decision:use-wal  decision:use-sqlite  2026-11-23T15:00:04.000000+00:00
```

### Retracting a decision

`sdf decision retract --id ID [--reason TEXT] [--retracted-by user|agent]`
withdraws a decision without deleting or changing it. It appends one row to its
own table, `decision_retractions`: the decision id, the optional one-line
reason, who retracted it (`user` or `agent`, optional), and when, filled by sdf.
The `decisions` and `decision_edges` rows stay exactly as they were. Like `add`
and `link`, it prints nothing on success. It exits 1 when the store refuses and
2 on a usage error.

A retraction is a row in its own table and not an edge, for three reasons:

- It has one subject and a reason, which an edge has no column for.
- A `UNIQUE` decision id lets the database itself hold "at most once".
- An older sdf ignores a table it does not know, while it would list an
  unknown `retracts` edge.

Rules:

- **Unknown id.** Retracting an id that was never recorded is rejected with
  `unknown reference: decision ID`.
- **Already retracted.** Retracting a decision a second time is rejected, as a
  duplicate edge is, with
  `decision already retracted: ID at RETRACTED_AT`. The first retraction, and
  its reason, stay as recorded. The check and the insert run in one
  `BEGIN IMMEDIATE` transaction, so concurrent retracts store one row.
- **No new edges.** A retracted decision cannot be linked or superseded again,
  in either direction. To replace a retracted choice, add a new decision; it
  needs no `supersedes` edge to the retracted one. Edges recorded before the
  retraction stay and are still listed.

At read time, `effective_status` is `retracted`. A `retraction` object carries
`retracted_at`, plus `reason` and `retracted_by` when set. Precedence is
**retracted > superseded > active**. A decision that is both retracted and
superseded reads `retracted`, and `superseded_by` is still listed, so neither
fact is lost. Retraction changes only the retracted record. If `b` supersedes
`a` and `b` is later retracted, `a` stays `superseded` (by `b`). sdf does not
guess that `a` is back in force; record a new decision if it is.
`sdf decision list` shows `retracted` (or `retracted, superseded by ID`) in
`EFFECTIVE`, and a
`retracted: TIME [by WHO][; reason: TEXT]` line under the statement, quoted by
the same cell rule as every column. The UI shows the same, and `--json` equals
`GET /api/decisions`.

```text
ID          STATUS    EFFECTIVE                         DECIDED BY  RECORDED AT                       SOURCE  STATEMENT
use-sqlite  decision  retracted, superseded by use-wal  user        2026-11-23T15:00:00.000000+00:00  -       Store telemetry in SQLite
                                                                                                              retracted: 2026-11-23T15:00:03.000000+00:00 by user; reason: Benchmark was wrong
use-wal     decision  active                            -           2026-11-23T15:00:01.000000+00:00  -       Store telemetry in SQLite with WAL
```

**Migration.** Opening a store runs `CREATE TABLE IF NOT EXISTS
decision_retractions`, in its own migration step, after the existing column
migration. It only adds a table: no column or table changes, and running it
again does nothing. A database written before this, including the Python
receiver's, gains the empty table on its first open by a command that writes
(`sdf decision add`, `link`, `retract`, or the receiver). `sdf summary` and
`sdf decision list` open read-only and do not add it. An older sdf binary reads
a newer database as before. It does not know the table, so it shows a retracted
decision by its stored status and edges, without the retraction. A file that
cannot be written, such as a read-only copy, is opened without the table and
reads as having no retractions, as does any file `sdf decision list` reads
before a writing command has added the table. `retract` then fails on it.

## Local budget

A budget is one ceiling in USD over one window, read from a local file and
compared with the computed session cost. The comparison is reported. Nothing is
blocked or throttled when the total passes the ceiling.

The file names `ceiling_usd` and `window`. `window` is `store`, for every
session, or `day`, for one UTC calendar day named as `YYYY-MM-DD`. A day window
places a session by its start time. A session with no start time cannot be
placed, so its cost counts as unknown instead of being dropped.

The result is `within` when the total is at or under the ceiling, `over` when
it is above, and `unknown` otherwise. `unknown` carries no total. One session
with an unknown computed cost makes the whole window unknown, and so does a
total that is not a terminating decimal. A window with no sessions at all says
`no sessions`. A day window whose sessions all lack a start time does not: it
has sessions, and the cost is unknown.

The budget is applied when a summary is built with a budget file. `sdf summary
--budget PATH` and `sdf serve --budget PATH` pass that file. Without one, the
report is unchanged and the `budget` field is absent. A file that fails to load
exits non-zero and prints no report. The page reads the summary, so a budget
that is in the report is in `GET /api/summary`.
