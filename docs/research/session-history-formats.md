# Survey of local agent session history formats

Observed 7 November 2026 (America/Los_Angeles) as input to
[ADR 0048](../adr/0048-session-import-and-history-audit.md). This is a field
guide, not a contract. The vendors do not publish these layouts as stable
APIs (except Aider's history *file path* and its `--chat-history-file`
option). Layouts have already drifted. Do not treat a listed field as
present in every file; this document marks what was verified on this host,
what comes from upstream source, and what is community-observed.

The inspection of files on this host was read-only and recorded structure
only: top-level JSON keys, `type` values, SQLite table names, and paths.
Prompt text, tool arguments, command output, and secrets were not copied
into this document.

## How to read the confidence labels

- **Verified here**: seen in a real file or database on this host on
  7 November 2026.
- **Upstream source**: read from the tool's own repository or official docs.
- **Community-observed**: third-party parsers and write-ups, dated where
  known. Useful, but the format may have moved since.
- **Unverified**: reported by a secondary source and not confirmed against
  upstream source or a file on this host.

## Claude Code

### Location

Per project directory, one JSONL file per session:

```
~/.claude/projects/<lossy-cwd-slug>/<session-uuid>.jsonl
```

The slug is the working directory with non-alphanumeric characters replaced
by `-` (so `/home/you/app` becomes `-home-you-app`, and
`C:\Work\app` becomes `C--Work-app`). The encoding is lossy: a dash in the
original path cannot be told apart from a separator. Do not decode the slug
back into a path; read `cwd` from the records instead.
([clog SOURCE_FORMATS](https://github.com/BFriedland/clog/blob/main/docs/SOURCE_FORMATS.md),
as observed 2026-07, Claude Code 2.1.x;
[bough format notes](https://www.bough.run/docs/format/claude-code),
measured August 2026 on Claude Code 2.1.238.)

On Windows the home is `%USERPROFILE%\.claude\projects\`. `$HOME` on macOS
and Linux is `~/.claude/projects/`. A matching directory named after the
session id may hold `subagents/*.jsonl` sidechain traces; those are not
the parent session. Also seen on this host (not a transcript):
`~/.claude/sessions/<id>.json` holding process metadata.

**Verified here:**
`~/.claude/projects/-workspace-tools-try-vk/<uuid>.jsonl` (1 317 bytes, 4
lines, Claude Code `2.1.119`) and
`~/.claude/sessions/209541.json`.

### Format and schema

JSONL: one JSON object per line. Records form a tree through `uuid` and
`parentUuid`, not a flat log. The same `uuid` can be rewritten when a
session is resumed or replayed; counting lines over-counts unique records
([bough](https://www.bough.run/docs/format/claude-code)).

Community-observed line `type` values (clog, 2026-07): `user`, `assistant`,
`summary`, `system`, `attachment`, `progress`, `file-history-snapshot`,
`queue-operation`, `last-prompt`, `ai-title`, `mode`, `permission-mode`.
A third-party source review of Claude Code
([openedclaude chapter 9](https://openedclaude.github.io/claude-reviews-claude/chapters/09-session-persistence))
also lists `custom-title`, `tag`, `worktree-state`, `pr-link`,
`attribution-snapshot`, and others. Those extra types were **not** present
in the file on this host and are **unverified** here.

Common fields reported on many lines (clog): `uuid`, `parentUuid`,
`sessionId`, `timestamp` (ISO 8601), `isSidechain`, `cwd`, `version`,
`slug`, `userType`, `gitBranch`.

**Verified here** on `type: "user"` records: `cwd`, `entrypoint`,
`gitBranch`, `isMeta`, `isSidechain`, `message`, `parentUuid`, `sessionId`,
`timestamp`, `type`, `userType`, `uuid`, `version`. `message` had keys
`content` and `role`. `content` was a string (not an array) on both user
lines. `userType` was `external`. `cwd` was `/workspace/tools/try-vk`.
`gitBranch` was `main`. `version` was `2.1.119`.

**Verified here** on `type: "queue-operation"` records: `content`,
`operation`, `sessionId`, `timestamp`, `type`.

**Verified here** on `~/.claude/sessions/<id>.json` (process metadata, not
the transcript): `cwd`, `entrypoint`, `kind`, `peerProtocol`, `pid`,
`procStart`, `sessionId`, `startedAt`, `version`. Values were not recorded.

clog describes two shapes for `user` `message.content`: a string (human
text) or an array of `tool_result` blocks
(`type`, `tool_use_id`, `content`). Assistant `message.content` is an array
of blocks: `text`, `thinking`, or `tool_use` (`id`, `name`, `input`).
Several assistant lines may share one `message.id` while a response
streams. The file on this host had no assistant or `tool_result` lines, so
those block shapes are community-observed, not verified here.

### Stability

Undocumented. clog and bough both warn that the format moves. Duplicate
`uuid`s after resume, compaction (`system` / `subtype: "compact_boundary"`,
community-observed), and `/branch` (`forkedFrom`, community-observed)
mean a parser must not treat the file as a simple append-only list.

### Tying a session to a repo, commit, or branch

- Repo / directory: read `cwd` from records (first `cwd` is the usual
  project root; later values may be subdirectories). The directory slug is
  only a hint.
- Branch: `gitBranch` was present on user records in the file on this host.
- Commit SHA: **not observed** in that file. Do not invent a commit field.
  A `pr-link` line type is unverified here.

### What a "done" claim looks like

No dedicated "task accepted" or "done" record was present in the file on
this host, and clog does not describe one. The practical signals are
heuristic: assistant `text` that says the work is finished, and
`stop_reason` on assistant messages (community-observed on the API payload,
e.g. `end_turn`). End of turn is not task success.

### Tool calls and test runs

Community-observed: assistant `tool_use` blocks name the tool; user
`tool_result` blocks return the output, including Bash/pytest stdout. That
output can contain secrets. The file on this host had no tool records.

### Privacy

The transcript holds every prompt, every file the model read or edited, and
every tool result ([Pearce, 2026](https://frederick-douglas-pearce.github.io/blog/2026/anatomy-of-a-claude-code-session/)).
Copying `~/.claude/projects` copies that history in plain text.

### Confidence

High for location, JSONL-per-session layout, `cwd` / `gitBranch` /
`sessionId` / `version`, and the `user` + `queue-operation` keys actually
seen. Medium for the fuller clog type table and assistant/tool block
shapes (not seen on this host). Low for undocumented extra types such as
`pr-link`.

## Cursor (IDE Composer and agent transcripts)

### Location

Cursor stores the same conversation in more than one family. They are not
interchangeable backups
([ExAI, 2026](https://extracurricular.ai/en/blog/cursor-chat-history-storage/)).

IDE Composer (VS Code-style application storage):

| OS | Global conversation DB | Per-window workspace DB |
| --- | --- | --- |
| macOS | `~/Library/Application Support/Cursor/User/globalStorage/state.vscdb` | `…/User/workspaceStorage/<id>/state.vscdb` |
| Linux | `~/.config/Cursor/User/globalStorage/state.vscdb` | `…/User/workspaceStorage/<id>/state.vscdb` |
| Windows | `%APPDATA%\Cursor\User\globalStorage\state.vscdb` | `%APPDATA%\Cursor\User\workspaceStorage\<id>\state.vscdb` |

(Community paths; standard VS Code/Electron layout. **Not present** on this
host: `~/.config/Cursor` does not exist.)

Agent transcripts under the user home:

```
~/.cursor/projects/<sanitized-project-path>/agent-transcripts/
```

Community reports two layouts, both still seen in 2026:

- Legacy: `agent-transcripts/<session-id>.txt` (also reported as
  `…/<composerId>.txt` by [cursaves](https://github.com/Callum-Ward/cursaves/blob/main/docs/how-cursor-stores-chats.md)).
- Current Composer 2+: `agent-transcripts/<session-id>/<session-id>.jsonl`
  ([agf #45](https://github.com/subinium/agf/pull/45);
  [CursorDump, July 2026](https://github.com/lpalbou/CursorDump/blob/main/docs/knowledge-base.md)).

Cursor CLI / cursor-agent (community-observed, 2026):

```
~/.cursor/chats/<…>/<session-id>/store.db
```

sometimes with a sibling `meta.json`. ACP sessions have also been seen under
`~/.cursor/acp-sessions/**/store.db`.

**Verified here:** `~/.cursor/projects/workspace/` exists but contains
`terminals/`, `canvases/`, and `agent-tools/` only. No `agent-transcripts/`
directory, no `state.vscdb`, and no `~/.cursor/chats/`.

### Format and schema

`state.vscdb` is SQLite with tables `ItemTable` and `cursorDiskKV`
(community: cursaves, ExAI). Conversation payloads live mainly in
`cursorDiskKV`. Documented key *patterns* (not inventing names beyond these
reports):

| Key pattern | Reported content |
| --- | --- |
| `composer.composerData` (workspace `ItemTable`) | List of composers: `composerId`, `name`, `createdAt`, `lastUpdatedAt`, `unifiedMode`, `forceMode` (cursaves, Cursor ≤2.6 sidebar) |
| `composerData:{composerId}` | Conversation metadata and ordered bubbles |
| `bubbleId:{composerId}:{bubbleId}` | One message |
| `checkpointId:{composerId}:{checkpointId}` | Workspace snapshots / diffs per agent turn |
| `messageRequestContext:{composerId}:{messageId}` | Request context sent to the model |
| `composer.content.{hash}` | Content-addressed blobs |

cursaves notes that in Cursor 3.0+ a `composer.composerHeaders` index lives
in the global `ItemTable`. A third-party parser
([cursor_parser.py](https://github.com/tim-hua-01/cc_transcript_viewer/blob/main/cursor_parser.py))
treats IDE `state.vscdb` as higher fidelity than CLI `store.db` or JSONL.

JSONL transcripts (CursorDump, July 2026) are one object per line with
shapes such as:

- `{"role":"user"|"assistant","message":{"content":[block,…]}}`
- `{"type":"turn_ended","status":"success"|…,"error"?}`

Blocks reported: `{"type":"text","text":…}` and
`{"type":"tool_use","name":…,"input":{…}}`. Subagent transcripts may sit
under `…/<session>/subagents/*.jsonl`.

CLI `store.db` is a **different** schema from the IDE DB. agf #45 found that
querying `cursorDiskKV` / `composerData:*` against the CLI file is wrong;
the CLI file used a `meta` table (`SELECT value FROM meta WHERE key = '0'`).
Do not assume one SQLite layout for every `store.db`.

### Stability

Low-to-medium. The on-disk layout has already shifted from `.txt` to nested
`.jsonl`, and IDE vs CLI databases disagree. Cursor does not publish this as
an API. Any importer must detect which family it opened and fail closed on
an unknown schema.

### Tying a session to a repo, commit, or branch

- Repo: workspaceStorage is per opened window; the
  `~/.cursor/projects/<slug>/` directory name is a sanitized project path
  (slash to `-`). CursorDump reports no workspace-path metadata *inside*
  the JSONL itself; the slug is a best-effort decode.
- Some community parsers mention `cwd` on `composerData` (cursor_parser.py).
  **Unverified here** (no DB present).
- Checkpoints store file diffs for a turn (cursaves); that is content, not
  a git SHA. A commit binding has to be inferred later from git, and is
  lossy.

### What a "done" claim looks like

Community-observed `type: "turn_ended"` with `status: "success"` marks the
end of a turn, not SDF acceptance. Assistant `text` may also say the work
is finished. Both are claims.

### Tool calls and test runs

JSONL `tool_use` blocks record tool name and input. cursaves says the IDE
DB also stores tool outputs on bubbles and is higher fidelity than the
JSONL, which may omit tool outputs (cursor_parser.py notes the JSONL path
has "no tool outputs" when used as a fallback).

### Privacy

`messageRequestContext` and bubble text include prompts. Tool inputs and
outputs can include file contents, env dumps, and tokens. Checkpoints are
workspace diffs. The global `state.vscdb` holds conversations for every
project on the machine.

### Confidence

Medium for the existence and split of the three families (IDE DB, JSONL
transcripts, CLI `store.db`) and for the key *patterns* named above.
Low for any single schema remaining stable, and for fields not listed
here. Nothing in this family was verified against a file on this host.

## Codex CLI

### Location

Upstream comments in
[`codex-rs/rollout/src/recorder.rs`](https://github.com/openai/codex/blob/main/codex-rs/rollout/src/recorder.rs)
describe JSONL rollouts under the Codex home. Community and adapter notes
(clog, 2026-07; [bough](https://www.bough.run/docs/format/codex), September
2026 on Codex Desktop 0.153.4) give:

```
~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<session-id>.jsonl
```

`$CODEX_HOME` overrides the home (default `~/.codex` on every platform).
There is no per-project directory: the project is `session_meta` `cwd`.
Other JSONL files in that tree (`history.jsonl`, logs) are not rollouts;
glob `rollout-*.jsonl`. An `archived_sessions/` tree with the same shape
has been reported (community).

**Verified here:** `~/.codex` exists but has only `tmp/` sandbox helpers.
There is no `sessions/` directory on this host.

### Format and schema

Each line is an envelope. Recent write-ups describe
`{timestamp, ordinal, type, payload}` (bough, 0.153.x). Older notes omit
`ordinal`. `type` is the outer discriminator.

Outer types reported by clog (2026-07) and bough (2026-09):
`session_meta`, `turn_context`, `response_item`, `event_msg`, and (bough,
later) `token_usage_record`, `world_state`, `compacted`.

`session_meta.payload` fields used by clog: `id`, `cwd`, `timestamp`,
`cli_version`, `model_provider`, `forked_from_id`, `thread_source`.
Upstream listing code
([`codex-rs/rollout/src/list.rs`](https://github.com/openai/codex/blob/main/codex-rs/rollout/src/list.rs))
reads `meta.id`, `meta.cwd`, `meta.source`, `meta.parent_thread_id`,
`meta.agent_nickname`, `meta.agent_role`, `meta.model_provider`,
`meta.cli_version`, and optional `git.branch`, `git.commit_hash`,
`git.repository_url`. Git values are collected from the session cwd
([`codex-rs/core/src/git_info.rs`](https://github.com/openai/codex/blob/main/codex-rs/core/src/git_info.rs);
added in [PR #1598](https://github.com/openai/codex/pull/1598)). They are
absent when git is unavailable.

`response_item.payload.type` values reported by clog: `message` (with
`role` and `content` blocks `input_text` / `output_text`),
`function_call` (`name`, `arguments`, `call_id`),
`function_call_output` (`call_id`, `output`), `reasoning`.
`event_msg.payload.type` values used by parsers include `user_message`,
`agent_message`, `task_complete`, `token_count`, `exec_command_end`
(`call_id`, `exit_code`, `status`, `stdout`, `stderr`,
`aggregated_output`). Do not assume every file has every subtype.

Resuming a session writes a **new** rollout and replays earlier items into
it under the ids they already had ([bough](https://www.bough.run/articles/what-is-in-a-codex-rollout-file)).
Deduplicating inside one file is not enough.

### Stability

Undocumented internal format. A Codex maintainer described rollout JSONL
as "an internal implementation detail" when declining extra PR metadata
([openai/codex#21754](https://github.com/openai/codex/issues/21754), May
2026). The envelope has already gained fields (`ordinal`). Parsers written
against an earlier CLI have broken (bough).

### Tying a session to a repo, commit, or branch

This is the strongest of the four tools **when git metadata was written**:
`cwd` plus optional `git.commit_hash`, `git.branch`, and
`git.repository_url` at session start. Grouping by project still requires
reading the head of every rollout, because the directory layout is by date,
not by repo. Git metadata is a snapshot of HEAD; uncommitted work is not
described (PR #1598 notes this).

### What a "done" claim looks like

Community-observed `event_msg` subtype `task_complete` carries
`last_agent_message` and `duration_ms` (daymade notes against Codex CLI
0.144.4, July 2026). That is Codex's turn-complete signal. It is not SDF
verification. Assistant `output_text` and `agent_message` may also claim
success in prose.

### Tool calls and test runs

`function_call` / `function_call_output` correlated by `call_id`, with
`exec_command_end` as a fallback status (clog). `aggregated_output` can be
multi-kilobyte command output, including test logs and secrets.

### Privacy

Rollouts include user prompts, `AGENTS.md` instructions (often injected
into the first records), tool arguments, and command output. `history.jsonl`
(community) has been described as `{session_id, ts, text}` for user
messages — another copy of prompts. Because sessions are not partitioned
by project on disk, a scan of `~/.codex/sessions` sees every repo the
operator used.

### Confidence

Medium-high for the path pattern, envelope `{timestamp, type, payload}`,
`session_meta.payload` `id`/`cwd`, and the git fields named in upstream
source. Medium for the inner `response_item` / `event_msg` subtypes
(community + clog, not seen here). Nothing was verified against a rollout
on this host.

## Aider

### Location

Official option
[`--chat-history-file`](https://aider.chat/docs/config/options.html)
defaults to `.aider.chat.history.md` in the directory Aider is run from
(normally the git root). Environment variable: `AIDER_CHAT_HISTORY_FILE`.
Related official files: `.aider.input.history` (`--input-history-file`)
and optional `--llm-history-file`. Aider adds `.aider*` to `.gitignore` by
default (`--gitignore`).

**Verified here:** no `.aider.chat.history.md` or `.aider.input.history`
under `/home/box` or `/workspace`.

### Format and schema

Append-only Markdown. Aider's own splitter (`aider/utils.py`, described by
[lcc](https://lucasmartins-ai.github.io/lcc/AIDER.html) and
[casr](https://docs.rs/cross_agent_session_resumer/latest/casr/providers/aider/index.html)):

| Line prefix | Meaning |
| --- | --- |
| `# aider chat started at YYYY-MM-DD HH:MM:SS` | Session boundary |
| `#### ` | User message |
| `> ` | Aider tool / UI notification (token counts, "Applied edit …") |
| (no prefix) | Assistant Markdown, often including SEARCH/REPLACE fences |

There is no native session UUID. Multiple sessions accumulate in one file.
casr derives an id from the start timestamp.

### Stability

Higher than the JSONL tools for the *grammar* of the markdown file, because
Aider documents the path and parses the file itself to restore history
(`--restore-chat-history`). The notification lines are still a UI log, not
a versioned schema.

### Tying a session to a repo, commit, or branch

The file lives in the repo (or wherever `--chat-history-file` points).
That is the repo binding. The header records a local timestamp, not a
commit SHA. Aider can auto-commit (`--auto-commits`); those SHAs live in
git, not as structured fields in the markdown.

### What a "done" claim looks like

No structured "done" event. The assistant's unprefixed Markdown is the
claim. `> Applied edit to <file>` records that Aider applied a patch, not
that a Task passed an independent check.

### Tool calls and test runs

Aider does not log LLM tool calls with ids (lcc). Edits appear as
SEARCH/REPLACE in the assistant text. `--test` / `--auto-test` /
`--test-cmd` run tests (official options); their results show up as `>`
notification lines if they show up at all, not as paired tool_use/result
blocks.

### Privacy

The markdown is a full chat log, usually gitignored but still on disk:
prompts, file contents inside SEARCH/REPLACE, and any test output Aider
echoed. Sharing it is a documented Aider feature (`aider.chat/share`).

### Confidence

High for the default path and the four-prefix markdown grammar (official
path + multiple parsers). Medium for reconstructing test runs from `>`
lines. Not verified on this host.

## What this means for `sdf import`

1. Prefer adapters that detect the family they opened (Claude JSONL vs
   Cursor `state.vscdb` vs Cursor JSONL vs Cursor CLI `store.db` vs Codex
   rollout vs Aider markdown) and fail closed on an unknown schema.
2. Bind to a repo through recorded `cwd` (Claude, Codex) or through the
   file's location (Aider, Cursor project slug). Bind to a commit only
   when the source actually stored one (Codex `git.commit_hash` when
   present). Do not invent SHAs.
3. Treat every "done", `task_complete`, `turn_ended`, or "all tests pass"
   string as a Claim. Tool-recorded pytest output is still the agent's
   own log.
4. Default to dropping raw prompts and tool payloads. Keep session id,
   timestamps, `cwd`, branch, commit if present, tool *names*, and
   redacted claim text.
5. Claude Code is the best first parser: the layout is consistent, `cwd`
   and `gitBranch` were visible on this host, and independent write-ups
   agree. Cursor needs family detection. Codex needs a date-tree scan and
   resume dedup. Aider is straightforward markdown with no structured
   "done".

## Sources

- [BFriedland/clog `docs/SOURCE_FORMATS.md`](https://github.com/BFriedland/clog/blob/main/docs/SOURCE_FORMATS.md) (Claude Code and Codex CLI, as observed 2026-07)
- [The Claude Code JSONL transcript format (bough)](https://www.bough.run/docs/format/claude-code) (August 2026, Claude Code 2.1.238)
- [Anatomy of a Claude Code session (Pearce)](https://frederick-douglas-pearce.github.io/blog/2026/anatomy-of-a-claude-code-session/)
- [Session persistence (openedclaude)](https://openedclaude.github.io/claude-reviews-claude/chapters/09-session-persistence) — extra line types unverified here
- [cursaves: how Cursor stores chats](https://github.com/Callum-Ward/cursaves/blob/main/docs/how-cursor-stores-chats.md)
- [CursorDump knowledge base](https://github.com/lpalbou/CursorDump/blob/main/docs/knowledge-base.md) (July 2026)
- [ExAI: where Cursor stores chat history](https://extracurricular.ai/en/blog/cursor-chat-history-storage/)
- [agf #45](https://github.com/subinium/agf/pull/45) (Cursor transcript layout + CLI `store.db`)
- [The Codex CLI rollout file format (bough)](https://www.bough.run/docs/format/codex) (September 2026, 0.153.4)
- [openai/codex `recorder.rs`](https://github.com/openai/codex/blob/main/codex-rs/rollout/src/recorder.rs), [`list.rs`](https://github.com/openai/codex/blob/main/codex-rs/rollout/src/list.rs), [`git_info.rs`](https://github.com/openai/codex/blob/main/codex-rs/core/src/git_info.rs), [PR #1598](https://github.com/openai/codex/pull/1598), [issue #21754](https://github.com/openai/codex/issues/21754)
- [Aider options reference](https://aider.chat/docs/config/options.html)
- [lcc notes on Aider markdown](https://lucasmartins-ai.github.io/lcc/AIDER.html)
- This host, 7 November 2026: one Claude Code `2.1.119` JSONL; no Cursor `state.vscdb` or agent transcripts; no Codex rollouts; no Aider history file
