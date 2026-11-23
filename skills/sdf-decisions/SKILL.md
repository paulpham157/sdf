---
name: sdf-decisions
description: Record important user and agent choices as sdf decision records. Use when the user or the agent makes, discusses, changes, or wants to write down a decision, approval, rejection, constraint, architectural choice, or working hypothesis, or asks to link a decision to a session or commit.
---

# sdf decision records

Write important choices down with the `sdf` CLI. Record both decisions the agent makes and decisions the user makes. sdf never infers a decision from prompts or telemetry. `sdf summary` does not show them; they appear in `sdf decision list`, in the UI, and at `GET /api/decisions`. The web UI and API are read-only, so do not try to POST one. `sdf decision add` and `sdf decision retract` print nothing on success. `sdf decision link` prints nothing on success either, except when the new link leaves the same two endpoints with both a `validates` and a `contradicts` edge: the link is still stored and the command exits 0, but it writes one `sdf decision link: warning: …` line on stderr (see Link rules). So check stderr, not only the exit code, and tell the user what you recorded.

## When to record

Record a **user** decision when the user:

- approves or rejects a proposed action
- chooses between options that were on the table
- sets a constraint later work must follow (for example "don't push", "don't merge main")
- settles, proposes, or revisits an important technical choice or an assumption later work depends on

Record an **agent** decision when you settle an important technical choice or working hypothesis that later work depends on (storage, API shape, a library, a security posture, an approach).

If a new choice replaces an earlier one, add a new record and link it with `supersedes`.

Do not record routine implementation details, typo fixes, or obvious next steps. Do not invent a decision the user did not make, and do not re-record one that already exists with the same meaning.

## What each record must capture

Every record needs:

- **who decided**: `user` or `agent`, always set with `--decided-by`
- **what was decided**: the choice itself, in `--statement`
- **alternatives**: the options that were actually on the table, in `--alternatives`. Omit the flag if none were stated or discussed.
- **reason**: only if one was given, in `--reason`. For a user decision, quote or paraphrase the user faithfully. If they gave no reason, omit `--reason`. Never invent a reason.
- **time**: `recorded_at` is filled by `sdf`; do not pass a timestamp
- **session**: link it after the add, when you know the session id and the store already has telemetry for it

## Add a record

```bash
sdf decision add --id ID --statement TEXT --status hypothesis|decision \
  [--decided-by user|agent] [--alternatives TEXT] [--reason TEXT] \
  [--source TEXT] [--db PATH]
```

- `--id`: a stable slug, 1–128 characters, starting with a letter or digit, then letters, digits, `.`, `_`, or `-`. Prefer a short kebab-case name of the choice (`dont-push`, `use-sqlite`). Ids are append-only and cannot be reused; if the id is taken, pick a more specific unused one.
- `--statement`: one non-empty line, at most 500 characters, no newlines. State the choice itself, not the discussion around it.
- `--status`: `hypothesis` for an assumption that is not settled; `decision` for a settled choice. Approvals, rejections, chosen options, and constraints are `decision`.
- `--decided-by`: `user` or `agent`. Always set it. The flag is optional on the CLI so older records can omit it, but new records from this skill must include it.
- `--alternatives`: optional one line, at most 500 characters. The options that were on the table and not chosen. Omit the flag when there were none.
- `--reason`: optional one line, at most 500 characters. Omit it when no reason was given. Do not invent one.
- `--source`: optional one-line pointer (issue, PR, doc, or conversation), at most 500 characters. Omit it when there is none.
- `--db`: optional path to the store. The default is `sdf-telemetry.sqlite` in the current working directory. There is no environment variable for this path. Use the same file `sdf serve` and `sdf summary` use; if the collector was started with `--db`, pass that path here too.

Examples:

```bash
# The user forbade pushing
sdf decision add --id dont-push \
  --statement "Do not push until the user says so." \
  --status decision --decided-by user \
  --alternatives "push the branch now" \
  --reason "The user said don't push."

# The agent chose a storage approach
sdf decision add --id use-sqlite \
  --statement "Keep the collector store in SQLite." \
  --status decision --decided-by agent \
  --alternatives "PostgreSQL, or an in-memory store" \
  --reason "The collector is local and already uses a SQLite file."
```

## Link a record

```bash
sdf decision link --relation REL --from KIND:REF --to KIND:REF [--db PATH]
```

`REL` is one of `constrains`, `implements`, `supersedes`, `validates`, `contradicts`, `blocks`, `conflicts_with`, or `refines`. `blocked_by` and `depends_on` are aliases of `blocks` with the ends swapped. Each endpoint is `KIND:REF` with `KIND` one of `decision`, `session`, or `commit`.

- A `decision` ref must already exist in the store.
- A `session` or `commit` endpoint is accepted only when the store already has telemetry for it. A missing one fails the link and does not alter the record; leave it unlinked and say so. You can link later once telemetry exists.
- A commit `REF` must be the full 40-character SHA. Case is ignored and the stored ref is lowercase. Abbreviated SHAs are rejected.

Link rules. A rejected link stores nothing and exits 1 with one `sdf decision link: …` line on stderr:

- **No self-links.** `--from` and `--to` must be different endpoints, for every relation.
- **No duplicates.** The same relation, `--from`, and `--to` can be stored only once. Before linking again, check `sdf decision list`.
- **Endpoint kinds are enforced:**
  - `supersedes`, `blocks`, `conflicts_with`, `refines`: decision → decision only.
  - `implements`: session or commit → decision.
  - `validates`, `contradicts`: session, commit, or decision → decision.
  - `constrains`: decision → decision, session, or commit.
- **No cycles** through `supersedes`, `blocks`, or `refines` edges (each relation on its own). A `blocks` cycle would be a deadlock. Several decisions may supersede, block, or refine the same one.
- **`conflicts_with` is symmetric.** sdf stores it in one canonical order, so linking the same pair again in either order is a duplicate.
- **`conflicts_with` and `refines` are refused when one decision already supersedes the other.** The replacement already settled it.
- **Warnings do not block.** validates + contradicts on the same pair, or `blocks` on a pair that already has `supersedes` or `conflicts_with`, stores the link (exit 0) and prints a `warning:` line. Tell the user.

## Which relation to use

| Situation | Link |
| --- | --- |
| A new record fully replaces an old one, and the old one no longer applies | `supersedes` from NEW to OLD |
| A new record adds detail, narrows, or partly amends an old one, and the old one still holds | `refines` from NEW to OLD |
| B cannot be settled or acted on until A is settled | `blocks` from A to B (or `depends_on` / `blocked_by` from B to A) |
| Two records cannot both hold, and neither has replaced the other yet | `conflicts_with` (either order) |
| A rule limits a decision, session, or commit (for example "don't push" limits a session) | `constrains` from the rule |
| This session or commit carries out the decision | `implements` |
| This session, commit, or decision is evidence for or against the decision | `validates` / `contradicts` |

**supersedes vs refines.** "Don't push" followed by "push feature branches once the user says OK" is `refines`: the rule still holds, with a stated exception. "Use the default journal" followed by "use WAL mode" is `supersedes`: the old choice is gone. If you are unsure whether the old record still applies, ask.

**blocks and open questions.** Records never change status, so an open question is a `hypothesis` record. A blocker is settled once a `decision` record supersedes it. For example, when the user still has to approve a push:

```bash
sdf decision add --id await-push-ok --statement "Waiting for the user's OK to push decision-relations." --status hypothesis --decided-by agent
sdf decision add --id push-decision-relations --statement "Push decision-relations and open a PR." --status decision --decided-by agent
sdf decision link --relation blocks --from decision:await-push-ok --to decision:push-decision-relations
# later, when the user says OK:
sdf decision add --id user-ok-push --statement "User approved pushing decision-relations." --status decision --decided-by user
sdf decision link --relation supersedes --from decision:user-ok-push --to decision:await-push-ok
```

Other blocks examples: "choose the database" blocks "design the schema"; "user picks the auth provider" blocks "write the login flow". Do not act on a record that reads `blocked` until its open blockers are settled. Do not use `constrains` for ordering; it is a rule, not a dependency.

Typical links (FROM relation TO):

```bash
# This session confirmed or discussed the decision
sdf decision link --relation validates --from session:SESSION_ID --to decision:ID

# This commit implements the decision
sdf decision link --relation implements --from commit:SHA --to decision:ID

# The new record replaces an older one
sdf decision link --relation supersedes --from decision:NEW_ID --to decision:OLD_ID
```

Link the current session when you know its id and the store already has it. Link a commit only after that SHA is in the store (an `sdf` git hook event, or a Claude Code tool result that exported the full SHA). Do not invent either endpoint.

### Finding the current session id

Read the environment of the shell you are running in. Use the first value that is set, and only one agent at a time:

- Claude Code: `$CLAUDE_CODE_SESSION_ID` (set in Bash, PowerShell, and hook subprocesses).
- Codex: `$CODEX_THREAD_ID` (this is `conversation.id`). Do not use `$CODEX_SESSION_ID`; a subagent can set that to the root id instead.
- Qwen Code: `$QWEN_CODE_SESSION_ID`, if it is set.
- OpenCode: `$OPENCODE_SESSION_ID`.
- Pi: the basename of `$PI_SESSION_FILE` with a trailing `.jsonl` removed. Do not use `$PI_SESSION_ID` (it is only the short trailing id).
- Grok CLI: `$GROK_SESSION_ID`.

The sdf git hook reads `$CODEX_THREAD_ID`, `$OPENCODE_SESSION_ID`, `$PI_SESSION_FILE`, and `$GROK_SESSION_ID`. It does not read Claude Code or Qwen variables. If no session id is available, or a link returns `unknown reference`, keep the record and skip the session link.

For a commit SHA, `git rev-parse HEAD` is the usual source, but link it only when it is already in the store.

## Read records back

```bash
sdf decision list [--db PATH] [--json]
```

`--json` is the same document as `GET /api/decisions`. Each record keeps the `status` it was recorded with (`hypothesis` or `decision`). sdf also adds two fields that are **computed when the records are read, not stored**:

- `effective_status`: `superseded` when another stored decision supersedes it, otherwise `active`.
- `superseded_by`: the ids of the decisions that supersede it. It is omitted when the record is active. Replacement is direct, so to find the current choice, follow `superseded_by` until a record is `active`.

- `blocked_by`, `open_blockers`, `blocked`: the decisions that block this one, the ones still unsettled (a blocker is unsettled while its current replacement is a `hypothesis`; a retracted blocker blocks nothing), and `true` when this record is active with an open blocker.
- `conflicts_with`, `conflicted`: the other side of each conflict, and `true` while this record and at least one of them are both active. Resolve a conflict by superseding or retracting one side.
- `refined_by`: the decisions that refine this one. A refined record keeps its own `effective_status`.
- `challenged`: `true` when the record is active and its latest evidence is `contradicts`. A `validates` or `contradicts` from a decision that is retracted or superseded does not count, so it neither challenges nor clears a challenge; one from a session or commit always does. Tell the user.

These are omitted when empty or false. `sdf decision list` shows them as `, blocked`, `, conflicted`, `, challenged` in `EFFECTIVE` and as `blocked by:`, `conflicts with:`, `refined by:` lines under the statement.

- `retraction`: present when the decision was retracted (`retracted_at`, and `reason` / `retracted_by` when given). `effective_status` is then `retracted`, even if it is also superseded; `superseded_by` is still listed.

Never try to change or delete a record: the store is append-only. To mark it replaced, add the new record and a `supersedes` link. To withdraw it without a replacement, retract it. The text table shows the same thing in its `EFFECTIVE` column (`active`, `superseded by ID`, `retracted`, or `retracted, superseded by ID`) and a `retracted:` line, plus `DECIDED BY`, and `alternatives:` / `reason:` lines when set.

## Retract a record

```bash
sdf decision retract --id ID [--reason TEXT] [--retracted-by user|agent] [--db PATH]
```

Retract when the user (or you) withdraws a decision or hypothesis and nothing replaces it, for example "forget that, we are not doing X". If a new choice replaces it, add the new record and link `supersedes` instead. Give `--reason` only when one was stated, quoted or faithfully paraphrased, and set `--retracted-by` to whoever withdrew it. A retraction is appended; the record and its links are not changed or deleted. On success it exits 0 and prints nothing on stdout or stderr; it has no warnings. Any stderr line means it failed (exit 1 or 2) and stored nothing.

- A decision can be retracted once. A second retract is rejected.
- A retracted decision takes no new links in either direction; edges from before stay.

## Check for gaps

```bash
sdf decision audit [--db PATH] [--json] [--since RFC3339] [--until RFC3339]
```

Read-only; it never writes or infers a record. It lists active decisions no `implements` edge points at, commits (from the git hook or a Claude Code tool result) whose session is not linked to any decision and that are not linked themselves, and edges that touch a retracted or superseded decision. Exit 0 means nothing to do, 1 means findings, 2 a usage error, 3 the store could not be read.

Run it before you finish a task that recorded decisions or made commits. For each finding, link only what you know: an `implements` edge from the session or commit that did the work, if it really implements that decision. Do not add a decision just to clear a finding, and do not link a commit to a decision it did not implement; an unlinked finding is better than a wrong edge. A stale edge needs no fix unless the work now follows a newer decision, in which case link it to that one.

## Errors

- `sdf` not on `PATH`, or the command cannot be run: say so, and do not claim the record was written. The user needs `sdf` installed and on `PATH` (build it with `make build` in the sdf repo if needed).
- `unknown reference: session …` or `unknown reference: commit …`: the add already succeeded; leave the record unlinked.
- `UNIQUE constraint failed: decisions.id`: choose a new id and add again.
- `edge already recorded: …`: that link exists already; nothing to do.
- `an edge cannot link an endpoint to itself`, `supersedes links a decision to a decision`, or `supersedes cycle: …`: the link is invalid and was not stored. `supersedes` takes two decision endpoints, from the new decision to the one it replaces; do not supersede back along an existing chain.
- `warning: … have both validates and contradicts edges`: the link was stored (exit 0). Tell the user the evidence now conflicts.
- `decision already retracted: ID at …`: it was retracted before; nothing to do. Do not retract it again with a new reason.
- `decision retracted: decision:ID takes no new edges`: do not link a retracted decision; record a new decision if the choice came back.
- `unknown reference: decision ID` from retract: no such id; check `sdf decision list`.
- Other validation errors (bad status, bad decided-by, bad id, multi-line statement, bad relation) come back on stderr. Fix the argument and retry. A failed add writes nothing.

After a successful add, link, or retract, briefly confirm the id, who decided, statement, status, alternatives and reason if any, optional source, and any links made (or that the record was left unlinked).
