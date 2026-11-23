---
name: sdf-decisions
description: Record important user and agent choices as sdf decision records. Use when the user or the agent makes, discusses, changes, or wants to write down a decision, approval, rejection, constraint, architectural choice, or working hypothesis, or asks to link a decision to a session or commit.
---

# sdf decision records

Write important choices down with the `sdf` CLI. Record both decisions the agent makes and decisions the user makes. sdf never infers a decision from prompts or telemetry. `sdf summary` does not show them; they appear in `sdf decision list`, in the UI, and at `GET /api/decisions`. The web UI and API are read-only, so do not try to POST one. `sdf decision add` and `sdf decision link` print nothing on success; tell the user what you recorded.

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

`REL` is one of `constrains`, `implements`, `supersedes`, `validates`, or `contradicts`. Each endpoint is `KIND:REF` with `KIND` one of `decision`, `session`, or `commit`.

- A `decision` ref must already exist in the store.
- A `session` or `commit` endpoint is accepted only when the store already has telemetry for it. A missing one fails the link and does not alter the record; leave it unlinked and say so. You can link later once telemetry exists.
- A commit `REF` must be the full 40-character SHA. Case is ignored and the stored ref is lowercase. Abbreviated SHAs are rejected.

Link rules. A rejected link stores nothing and exits 1 with one `sdf decision link: …` line on stderr:

- **No self-links.** `--from` and `--to` must be different endpoints, for every relation.
- **No duplicates.** The same relation, `--from`, and `--to` can be stored only once. Before linking again, check `sdf decision list`.
- **`supersedes` is decision → decision only.** `--from` is the new decision and `--to` is the one it replaces. A session or commit endpoint is rejected.
- **No supersedes cycles.** `NEW supersedes OLD` is rejected when OLD already supersedes NEW, directly or through a chain. Several decisions may supersede the same one.
- **validates + contradicts warns, but does not block.** If the same two endpoints now have both, the link is stored (exit 0) and stderr shows a `warning:` line. Tell the user the evidence conflicts.

Directions to use: `implements` from a session or commit to a decision; `validates` and `contradicts` from a session, commit, or decision to a decision; `constrains` from a decision to whatever it limits. Only the `supersedes` direction is enforced.

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

Never try to change a record to mark it replaced: the store is append-only. Add the new record and a `supersedes` link instead. The text table shows the same thing in its `EFFECTIVE` column (`active` or `superseded by ID`), plus `DECIDED BY`, and `alternatives:` / `reason:` lines when set.

## Errors

- `sdf` not on `PATH`, or the command cannot be run: say so, and do not claim the record was written. The user needs `sdf` installed and on `PATH` (build it with `make build` in the sdf repo if needed).
- `unknown reference: session …` or `unknown reference: commit …`: the add already succeeded; leave the record unlinked.
- `UNIQUE constraint failed: decisions.id`: choose a new id and add again.
- `edge already recorded: …`: that link exists already; nothing to do.
- `an edge cannot link an endpoint to itself`, `supersedes links a decision to a decision`, or `supersedes cycle: …`: the link is invalid and was not stored. `supersedes` takes two decision endpoints, from the new decision to the one it replaces; do not supersede back along an existing chain.
- `warning: … have both validates and contradicts edges`: the link was stored (exit 0). Tell the user the evidence now conflicts.
- Other validation errors (bad status, bad decided-by, bad id, multi-line statement, bad relation) come back on stderr. Fix the argument and retry. A failed add writes nothing.

After a successful add or link, briefly confirm the id, who decided, statement, status, alternatives and reason if any, optional source, and any links made (or that the record was left unlinked).
