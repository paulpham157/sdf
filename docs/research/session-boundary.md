# Session boundaries

Checked 2026-11-14. None of the four agents exports both edges of a session as
OTLP, so a collector that wants an explicit open and close has to get them
from a hook or a plugin. The id the collector already keys on differs per
agent ([docs/agents.md](../agents.md)): `session.id` for Claude Code and
OpenCode, `conversation.id` for Codex, `pi.session.id` for Pi.

## Claude Code

Open is `SessionStart`, close is `SessionEnd`
([Hooks](https://code.claude.com/docs/en/hooks)). Both arrive as one JSON
object on the hook command's stdin, and both carry `session_id`, the same
value the exporter puts on `session.id`.

`SessionStart` also sends `source` (`startup`, `resume`, `clear`, `compact`,
`fork`). `SessionEnd` sends `reason`: `clear`, `resume`, `logout`,
`prompt_input_exit`, or `other`. It cannot block the exit, its output is
discarded, and it has a 1.5 second budget, so a hook that posts to a
collector has to finish inside it.

The [monitoring doc](https://code.claude.com/docs/en/monitoring-usage) lists
`claude_code.session.count`, incremented when a session starts. No event or
span marks the close. `claude_code.interaction` is a turn, not a session.

Hooks are configured in `~/.claude/settings.json`, `.claude/settings.json`,
`.claude/settings.local.json`, managed policy, or a plugin's
`hooks/hooks.json`. A cloud session does not read `~/.claude/settings.json`.

A command hook reads the stdin JSON and posts it to `127.0.0.1:4318`. An
`http` hook can POST the same JSON itself.

## Codex CLI

Open is `SessionStart`, close is `SessionEnd`
([Hooks](https://learn.chatgpt.com/docs/hooks)). The generated input schemas
in `codex-rs/hooks/schema/generated/` at `69231e11` (2026-11-13) give both
events `session_id`, `cwd`, `hook_event_name`, and `transcript_path`.
`SessionStart` adds `model`, `permission_mode`, and `source`. `SessionEnd`
adds `reason`, which is the constant `other`.

`session_id` is not always the OTLP id. The request is built with
`sess.session_id()` (`codex-rs/core/src/hook_runtime.rs`), which returns the
identity shared by the root thread and its descendants. The exporter's
`conversation.id` is built from the thread id
(`codex-rs/core/src/session/session.rs`). A root thread with no inherited id
sets them equal; a subagent does not, and a subagent never fires
`SessionEnd`. `codex.conversation_starts` is the only exported open marker,
and nothing exported marks the close.

Hooks load from `hooks.json` and the `[hooks]` table of `config.toml` for
every config layer (`codex-rs/hooks/src/engine/discovery.rs`): `~/.codex` for
the user, `<repo>/.codex` for the project. A hook that is not managed stays
inactive until its hash matches the stored `trusted_hash`, so an edited file
is skipped until it is trusted again. The `hooks` feature is stable and on by
default, and `hooks = false` disables it (`codex-rs/features/src/lib.rs`).

A command hook reads stdin and posts to `127.0.0.1:4318`. On a root thread
its `session_id` matches `conversation.id`; on a descendant it does not, so a
collector that keyed on it would open a session the exported spans never
join.

## OpenCode

OpenCode has no hook events. A plugin subscribes to the server event bus
([Plugins](https://opencode.ai/docs/plugins/)). The events are declared in
`packages/schema/src/session-event.ts` at `f6e0c2d0` (2026-11-13), and the
plugin context exposes the stream as `ctx.event.subscribe`
(`packages/plugin/src/promise/plugin.ts`).

Open is `session.created`, whose payload carries `sessionID`. Nothing marks
the close. `session.deleted` fires when a session is deleted and the
`session.execution.*` events mark one run, not the session ending.
`session.status` going `idle` happens between turns. `global.disposed` is
declared in `packages/schema/src/server-event.ts` and published nowhere, so a
process exit emits nothing either.

`sessionID` is the OTLP id. It is branded to start with `ses`
(`packages/schema/src/session-id.ts`), `SessionRunner.drain` stamps
`session.id` from it (`packages/core/src/session/runner/llm.ts`), and
`Tool.execute` sets the same attribute (`packages/core/src/tool.ts`).

The public stream at `/api/event` drops events when a consumer falls behind
or disconnects (`packages/protocol/src/groups/event.ts`). A plugin on the
in-process bus does not (`packages/core/src/bus.ts`).

Plugins load from `.opencode/plugins/` and `~/.config/opencode/plugins/`, or
from the `plugins` array in `opencode.jsonc`. The plugin's `setup` subscribes
to `session.created` and posts `{sessionID}` to `127.0.0.1:4318`. There is no
event to post for the close, and the cleanup function `setup` may return only
runs when the plugin is unloaded.

## Pi

Pi's own CLI exports no OTLP. The events are in
`packages/coding-agent/src/core/extensions/types.ts` of `earendil-works/pi` at
`8f3f0856` (2026-11-13); the docs are
[Extensions](https://docs.pi.dev/latest/extensions).

`agent_start`, `agent_end`, `agent_before_settle`, and `agent_settled` bracket
one agent loop and its settlement, not a session.

`session_start` and `session_shutdown` are. Each carries a `reason`, and a
session file path when the session is being replaced. Neither carries a
session id. It is `ctx.sessionManager.getSessionId()`.

`pi-otel` 0.3.0 (`NikiforovAll/pi-otel` at `bf00f530`, 2026-09-13) already
emits both edges, but only as logs and only with `PI_OTEL_LOGS=1`:
`pi.session.start` from `session_start` and `pi.session.end` from
`session_shutdown` (`src/index.ts`). The id it exports is not
`getSessionId()`. It uses the session file's stem, and that file is named
`{timestamp}_{sessionId}.jsonl` (`session-manager.ts`), so `pi.session.id` is
the timestamp plus the id. Spans copy the same string into `session.id` and
`gen_ai.conversation.id`.

An extension reads `getSessionId()` in both handlers and posts it to
`127.0.0.1:4318`. `session_shutdown` also runs on `reload`, `new`, `resume`,
and `fork`, which replace the session rather than end it.

## What still gets missed

A commit made outside the agent never passes through any of these events,
which is why the git hook in [docs/receiver.md](../receiver.md) reads the
session id from the environment instead of from telemetry.

A session that crashes or is killed never emits its end event. `SessionEnd`
and `session_shutdown` run only when the process shuts down on its own, and
OpenCode has no end event to lose. The collector is left with an open session
and has to keep inferring the close from the last record.

A hook the user declines to trust never runs. Codex stores that decision as a
hash and skips the hook until it matches, and a Claude Code cloud session
never reads `~/.claude/settings.json`. The boundary is then as absent as it
is today.
