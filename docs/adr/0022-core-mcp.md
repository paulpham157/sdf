# ADR 0022: read-only SDF Core access for agents (`sdf-core`)

Status: accepted (branch `feat/core-mcp`, pending review)

## Context

Agents in a multi-agent run (ADR 0014) only see what the coordinator puts in
their prompt and what arrives through `sdf-msg`. The owner wants them to pull
more context from SDF Core on the master host: the approved plan and its hash,
their task, decisions and evidence, hub messages, and run status. SDF Core runs
on the owner's real machine, so the access path must not open ports, put
credentials in a sandbox, or let an agent approve anything (ADR 0014's human
gate, ADR 0015's roles).

## Decision

1. **Transport: the ADR 0014 file relay, not a network server.** An in-sandbox,
   stdlib-only shim `/usr/local/bin/sdf-core` (`sdf_core/sdf_core_shim.py`)
   writes `~/.sdf-core/req/<32-hex>.json` atomically and waits for
   `~/.sdf-core/resp/<id>.json`. The coordinator polls those directories through
   the E2B file API in the same loop that relays `sdf-msg` outboxes (also during
   a turn), answers, and deletes the request. No listener inside or outside the
   sandbox; nothing binds any port.
2. **Identity is the sandbox binding.** A request is attributed to the agent
   whose sandbox the file came from (`AgentRunMemberRow.sandbox_id`). The
   sandbox carries no token, and agent ids stay reserved names (ADR 0015) so no
   API token can impersonate one.
3. **Scope: a host-held `run_viewer` capability** (`core_mcp.AgentScope`):
   one run, one plan, one agent; `roles == {"run_viewer"}`, `kind == "agent"`,
   `can_approve` is a constant `False`. `CoreQueryService` refuses any other
   scope.
4. **Authorization** (`CoreQueryService.authorize`): only `READ_TOOLS`
   (`run_status, get_plan, get_task, list_decisions, get_decision,
   get_evidence, list_messages, read_message`). Anything else — including
   `approve_plan`, `reject_plan`, `resolve_decision`, `record_*`, `cancel_run`,
   `start_run`, `create_task` — is denied and logged. A `run_id`/`plan_id`
   argument other than the agent's own, and any task, attempt, decision or
   message outside the run, is denied. Workers see only messages from/to them
   or broadcasts; the lead sees the whole run. `get_plan` re-hashes the stored
   content (`hash_verified`) and strips host paths (`fixture_dir`); decision
   graph metadata loses `principal`. At most 300 queries per agent, 16 KiB per
   request, 100 items per list.
5. **The only write stays the existing one**: `send_message` in the shim shells
   out to `sdf-msg send`, i.e. the ADR 0014 hub path with its own checks.
6. **Registration is host-controlled and user-level** (never in the repo /
   project config, so ADR 0021 trust markers cannot add or remove it):
   * OpenCode: `~/.config/opencode/opencode.json` server `sdfcore`, tool
     permission `sdfcore_*`.
   * Claude: `~/.claude.json` `mcpServers.sdf-core`.
   * Codex: `[mcp_servers.sdf_core]` in `~/.codex/config.toml`.
   * Pi, Qwen, Goose: **CLI only** (the prompt tells the agent to run
     `sdf-core <tool> '<json>'`). Their hardened turn flags (`--no-mcp`, `--bare`,
     `--no-profile`) deliberately disable configured MCP; re-enabling it would
     also re-enable repo-supplied MCP. Goose could take it per turn with
     `--with-extension "python3 /usr/local/bin/sdf-core mcp"`
     (`goose_extension_args()`), not wired by default.
7. **No image change.** The shim is uploaded at sandbox start like `sdf-msg`;
   the public template is untouched.

## Consequences

* Each answered query costs one E2B file read/write round trip (~0.5 s poll).
* The coordinator only runs `opencode` and `pi` today, so the Claude/Codex/
  Qwen/Goose registrations are unit-tested only, not exercised in runs.
* Every query is in `run.summary.core_queries` (agent, tool, ok, denied).
* A compromised agent can read its own run's plan/evidence/messages — the
  same information it is already given — and nothing else.
