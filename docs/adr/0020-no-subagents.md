# No sub-agents for any agent kind

Status: accepted (2026-10-29). Applies to every kind in
[ADR 0016](0016-remove-aider-and-cursor-agent-kinds.md) at the pins in
[ADR 0018](0018-opencode-2-and-pi-1-upgrade.md).

## Context

SDF schedules, budgets, contains (ADR 0019) and audits one agent process per
turn. A sub-agent the CLI spawns by itself is a second agent the plan gate
(ADR 0014) never approved. It has its own context and its own spend, and it
can outlive or escape the turn's bookkeeping. Multi-agent work goes through
SDF plans and the message hub, never through CLI-internal delegation.

The mechanism has to be host-controlled. It must be set on the command line
or in SDF-owned user config, never in the workspace, so a project config
(which ADR 0021 starts honouring) can't turn it back on.

## Decision

All mechanisms live in `sdf_core/subagents.py` and are applied on every turn.

| Kind | Mechanism | Probe evidence (installed pin, in-sandbox mock endpoint) |
| --- | --- | --- |
| Claude 2.1.308 | `--disallowedTools Agent,Task,Workflow,ListAgents,SendMessage,TeamCreate,TeamDelete` | tools absent from the request, also with `--setting-sources user,project,local` and a project `settings.json` that allows them |
| Codex 0.165.0 | `-c features.multi_agent=false -c features.multi_agent_v2={enabled=false,max_concurrent_threads_per_session=1}` | v1 tools (`spawn_agent` …) gone; a forced v2 `spawn_agent` call returns "agent thread limit reached" with 0 sub-agent sessions, also under a hostile trusted `.codex/config.toml` (`-c` outranks project config) |
| OpenCode 2.2.0 | user config permission `deny` for `subagent` and `task` (pushed last) | `subagent` removed from the tool list |
| Qwen 0.26.2 | `--exclude-tools agent,create_sub_session,list_agents,send_message,team_*,request_shutdown,workflow` | removed from both the direct and the deferred (`tool_search`) catalogue; works without `--bare` |
| Goose 1.56.0 | `--no-profile --with-builtin developer` | `delegate`/summon not loaded, even if user or project `config.yaml` enables summon |
| Pi 1.4.0 | none needed | core has no sub-agent tool; the extension allowlist has none; `pi-fabric` stays rejected |

### Known limitation: Codex v2 catalog models

With an OpenAI catalog model that has `multi_agent_version: v2` (the default
when `OPENAI_MODEL` is unset), Codex 0.165.0 still *advertises* the
`collaboration` tools even when both features are disabled (upstream
openai/codex#50880). The thread cap of 1 makes every spawn fail, so no
sub-agent runs, but the model may waste a tool call on it. With non-catalog
models (e.g. `glm-5.3` on Vultr), the tools are not advertised at all.
Revisit when upstream fixes the feature gate.

## Consequences

- Unit tests (`tests/test_subagents.py`) pin each mechanism and check that it
  comes before anything project-controlled.
- The live harnesses (`SDF_LIVE_DELEGATE=1`) prompt each kind to delegate. They
  assert that the turn still succeeds, that no sub-agent tool was used, and
  (Claude/Codex) that no sub-agent transcript or session exists.
- A new CLI release must re-run the probes. Tool names change (OpenCode 2
  renamed `task` to `subagent`).
