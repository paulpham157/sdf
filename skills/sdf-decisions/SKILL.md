---
name: sdf-decisions
description: Record important user and agent choices as sdf decision records. Use when the user or the agent makes, discusses, changes, or wants to write down a decision, approval, rejection, constraint, architectural choice, or working hypothesis, or asks to link a decision to a session or commit. At session start, run `scripts/brief` (or `sdf decision brief`). Before a push, merge, release, deploy, or architecture change, run `scripts/check` (or `sdf decision check`) and stop if it fails.
---

# sdf decision records

Write important choices down with the `sdf` CLI. Record both decisions the agent makes and decisions the user makes. sdf never infers a decision from prompts or telemetry. `sdf summary` does not show them; they appear in `sdf decision list`, in the UI, and at `GET /api/decisions`. The web UI and API are read-only, so do not try to POST one. `sdf decision add` and `sdf decision retract` print nothing on success. `sdf decision link` prints nothing on success either, except when the new link leaves the same two endpoints with both a `validates` and a `contradicts` edge: the link is still stored and the command exits 0, but it writes one `sdf decision link: warning: …` line on stderr (see Link rules). So check stderr, not only the exit code, and tell the user what you recorded.

## At session start

This step is mandatory. At the start of a session, run this skill's `scripts/brief` helper and treat any constraints it prints as in force. The helper runs `sdf decision brief`. Claude Code and Codex may already receive this text from a SessionStart hook after `sdf init --write`; if that brief is not already in the session, run the helper yourself. Other agents do not get that hook.

The helpers live in `scripts/` next to this file. After a `cp -R skills/sdf-decisions …` install, that is `…/sdf-decisions/scripts/brief` and `…/sdf-decisions/scripts/check`. Run them with `sh` or as executables from the session working directory; they use the process current working directory for the default store, not the skill folder.

```bash
# Relative to this skill folder
sh scripts/brief
# Underlying command: sdf decision brief
```

The helper takes no flags by default. It reads `sdf-telemetry.sqlite` in the current working directory, the same default as `sdf serve` and the other `sdf decision` commands. There is no environment variable for the path, and you should not pass `--db` unless the store is not at that default. Start the session from the directory that holds the store, or pass the same `--db` as `sdf serve`:

```bash
sh scripts/brief --db PATH
```

You can also run `sdf decision brief` directly.

The SessionStart hook is fail-open, has a 2 second timeout, and runs:

```bash
command -v sdf >/dev/null 2>&1 || exit 0; sdf_out=$(sdf decision brief </dev/null 2>/dev/null) || exit 0; [ -n "$sdf_out" ] && printf '%s\n' "$sdf_out"; exit 0
```

Claude Code's command is prefixed with `[ -z "${GROK_HOOK_EVENT:-}" ] || exit 0` so a Grok session that also reads `~/.claude/settings.json` does not inject a Claude brief. `sdf init` updates only the exact command text an sdf build has written, including an earlier unreleased form. A user-tuned positive hook timeout is kept; the default 2 second timeout is added only when it is missing or not a positive number. The hook stays this inline command; `sdf init` must work even when the skill folder is not installed at a known path. `scripts/brief` is fail-open in the same spirit (missing `sdf` or a failed brief exits 0), but it is not the hook: it has no 2 second timeout and no `GROK_HOOK_EVENT` guard. Brief's 500ms load deadline is CLI-internal, not a script timeout.

- A missing store, or no matching decisions: the command prints nothing and exits 0. Continue; do not invent constraints.
- When the store exists but cannot be read: stdout is `sdf decision brief unavailable: run sdf decision list`, and the command exits 0. Tell the user, and run `sdf decision list` if you need the records.
- `sdf` missing from `PATH`, or the command cannot be run: continue without a brief; do not invent constraints. Unlike `sdf decision check`, a missing brief is not a reason to stop the session.
- Locked user decisions come first. When items are cut, the text ends with `N more: run sdf decision list`, or `N more (K locked): run sdf decision list` when locked rules were dropped. Run `sdf decision list` to see the rest.

## Before a push, merge, release, deploy, or architecture change

This step is mandatory. Run this skill's `scripts/check` helper before any of those actions, including a second attempt after a failure. The helper runs `sdf decision check` and forwards its exit code. Do not push, merge, release, deploy, or change the architecture until it exits 0.

```bash
# Relative to this skill folder
sh scripts/check ACTION [--db PATH] [--json]
# Underlying command: sdf decision check ACTION [--db PATH] [--json]
```

`ACTION` is one argument: exactly `push`, `merge`, `release`, or `deploy`, or one free-text architecture change such as `replace sqlite`. `git push` is not the named action `push`; pass `push`. Use the same `--db` as the other `sdf decision` commands. `--json` prints `action`, `kind`, `allowed`, and `blocking` (`id`, `decided_by`, `statement`, `match`). The helper passes `ACTION` and those flags through as arguments (no eval). You can also run `sdf decision check` directly.

- Exit 0, and a line starting with `allowed`: nothing in force forbids it. You may proceed.
- Exit 1, and a line starting with `forbidden`: stop. Do not do the action. Show the user every blocking id, who set it (`decided_by`), and the statement, and ask how to proceed. Do not retract, supersede, or reword a decision to get past this check unless the user tells you to.
- Exit 2: the command was called wrong. Fix it and run it again. Do not proceed without an exit 0.
- Exit 3: the check did not run. Stop and tell the user. A failed check is not permission.
- Exit 127 from `scripts/check` (or a shell `command not found` from the raw command): `sdf` is not on `PATH`, so the check did not run. Stop and tell the user.
- Any other non-zero exit: stop and ask the user. Only exit 0 allows the action.

A missing store file is an empty store: exit 0, and the file is not created. A hypothesis does not forbid an action. A superseded or retracted decision does not either. The check reads the statement of each settled decision that is still in force. Put the forbid in that statement, not only in the id, the reason, or a `blocks` / `constrains` edge:

- `Do not push until the user says so.` or `forbid:push` (the same shape for `merge`, `release`, and `deploy`)
- `Do not change the architecture.` or `forbid:architecture` or `no architecture change`

`sdf decision check push` exits 1 on the "Do not push" example below. The match rules are in [docs/outcomes.md](../../docs/outcomes.md).

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

The sdf git hook reads `$CLAUDE_CODE_SESSION_ID`, `$CODEX_THREAD_ID`, `$QWEN_CODE_SESSION_ID`, `$OPENCODE_SESSION_ID`, `$PI_SESSION_FILE`, and `$GROK_SESSION_ID`, and attributes a commit only when exactly one of them is set. If no session id is available, or a link returns `unknown reference`, keep the record and skip the session link.

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

## Before you push

If the repository has the sdf git hooks (`sdf init --git-hooks`), `git push` runs a pre-push check that warns about pushed commits SDF recorded that implement no decision. It prints `sdf: pre-push: … implement no decision`, then one `commit:SHA` block per commit with a ready `sdf decision link … --relation implements --from commit:SHA --to decision:ID` line. The push still succeeds unless the repository set `git config sdf.prePush block`.

When you see it: if that commit really implements a recorded decision (the warning names the ones linked to your session), run the printed link command and tell the user. If the work followed a decision nobody recorded, record it first (see When to record), then link. Do not link a commit to a decision it did not implement, and do not add a decision only to silence the warning. A commit can also carry `--trailer "SDF-Decision: ID"`; the check accepts it, and when the post-commit hook can reach the receiver it also appends an implements edge. The trailer is public once pushed. Do not bypass the check (`--no-verify`, `SDF_PRE_PUSH=off`) unless the user asks. A line starting `sdf: pre-push decision check skipped` means it could not check (no store, no `sdf`); the push went on, and nothing needs fixing in the decision graph.

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
