# Human-approved plans gate multi-agent runs; agents talk through an SDF message hub

Status: accepted

SDF can now run one lead agent and N worker agents on an approved plan, one
disposable sandbox per agent, with agents talking to each other only through
an append-only message hub on the SDF host ("approach A" from the P4
comparison; the Paseo-style peer daemon was rejected because it needs a
listening port and tokens inside agent sandboxes).

## Plan-approval gate (Decision Graph)

- `POST /plans` stores a `PlanRow` (immutable content + `content_sha256`), one
  `TaskRow` per subtask (`<plan>--<key>`, joined by `plan_tasks`) and a pending
  `DecisionRequestRow` of kind `plan_approval`.
- Only a human approves: `POST /plans/{id}/approve|reject` with a `decided_by`
  that is not an agent id, role or `policy:*`/`agent:*` name, optionally pinned
  to `expected_sha256`. Approval writes a `decision` graph node and a
  `constrains` edge to the plan. A resolved request is final; a plan moves once
  (`proposed -> approved|rejected`).
- No bypass: `ExecutionService.begin` refuses an Attempt for any plan task
  whose plan is not approved (`PlanGateError`, HTTP 409), a SQLAlchemy
  `before_flush` guard refuses the row itself, and on PostgreSQL triggers
  refuse direct `INSERT INTO attempts/agent_runs` and any forced status change.
  `SDF_REQUIRE_PLAN_APPROVAL=1` also refuses `/tasks/{id}/run` for tasks that
  belong to no plan.

## Message hub

- Envelopes are JSON files the in-sandbox `sdf-msg` tool writes to
  `~/.sdf-msg/outbox`. The host collects them through the sandbox provider's
  file API, so **the sender is the sandbox the file came from**; the envelope's
  `from` is recorded as `claimed_from_ignored` when it differs.
- Bodies are sanitized (control/bidi/zero-width characters removed),
  capped (32 KiB envelope, 8000 chars body, then truncated and flagged), ids
  must be 32 hex, and each agent has a message cap per run. Workers may write
  to the lead and siblings; only the lead may broadcast to `workers`; agents
  never send `decision` or `system` messages.
- `agent_messages` and `agent_claims` are append-only (session guard +
  PostgreSQL trigger). Redelivering the same envelope is idempotent.
- `report` creates an `AgentClaimRow`. **A claim is never Evidence**; it only
  asks the host to verify.
- `request_decision` creates a pending `DecisionRequestRow` (kind
  `agent_request`). It is resolved by a human (`POST
  /decision-requests/{id}/resolve`, always recorded as `human`) or by an
  in-process `DecisionPolicy`. The default `NoAutoResolvePolicy` resolves
  nothing; `RuleDecisionPolicy` may only reject or answer, never approve.
  Answers are delivered to the asker as `decision` messages. A pending request
  suspends stall detection (the run waits for the human, up to the wall cap).
  Requests still pending when a run ends stay pending and are listed in the run
  summary.

## Agent tools

`sdf_core/sdf_msg_shim.py` is installed as `/usr/local/bin/sdf-msg`
(stdlib only): a CLI (`send`, `ask`, `read`, `report`, `request-decision`,
`whoami`) and an MCP stdio server (`sdf-msg mcp`) registered as the `sdf`
MCP server in OpenCode's config. Pi uses the CLI. No network, no credentials,
no listening port: the host is the only party that moves messages.

## Coordinator

`sdf_core.coordinator.Coordinator` (`POST /plans/{id}/runs`):

1. Refuses unless the plan is approved; creates an `AgentRunRow` with members
   `lead-1` and `worker-i`, each worker bound to one subtask.
2. Begins one Attempt per subtask (`require_plan_approval=True`) **before**
   any sandbox exists, then starts the sandboxes in parallel (default lead
   `opencode`, workers `pi`, Vultr `glm-5.3`; configurable per role).
3. Posts the plan brief to the lead; the lead assigns work by message.
4. Relays outboxes to inboxes, wakes idle agents with mail (one turn each,
   bounded by `max_concurrent_turns` and `max_turns_per_agent`).
5. When a worker reports done/failed (or everyone goes quiet), the host
   collects that worker's workspace, resets a scratch copy to the plan
   fixture and applies **only** changes inside the subtask's
   `allowed_paths` and outside its `protected_paths` (others are listed as
   ignored), then runs the subtask's `criterion_checks` with the
   `DeterministicEvaluator` and finishes the Attempt with Evidence. FAIL with
   rounds left begins a child Attempt (`parent_attempt_id`) and tells the
   worker and the lead.
6. The run **succeeds only if every subtask has PASS Evidence**; a lead's
   "done" report is a claim and is not required when everything has passed
   and the agents are idle. Otherwise it fails (stall, exhausted rounds, caps)
   or is cancelled (`POST /runs/{id}/cancel`).
7. Caps: concurrency, cost (`max_cost_usd`, tokens at Vultr prices plus E2B
   seconds, hard ceiling $5), wall time, turns, verify rounds.
8. `finally`: every sandbox is killed, then (E2B) the provider is asked which
   of them are still running; the answer is stored in the run summary.

## Wiring

`/tasks/{id}/run` stays the synchronous, single-Task fake-adapter endpoint;
it only gains the gate. Multi-agent runs have their own endpoints because they
are long-running (background thread by default, `wait=true` inline) and need
a sandbox factory (`SDF_AGENT_SANDBOX=e2b` or `app.state.agent_sandbox_factory`
for tests; otherwise 503).

## Security

- No inbound ports: E2B sandboxes are created with
  `allow_public_traffic=False`; egress allows only `api.vultrinference.com`.
  The host polls over the E2B API (outbound HTTPS).
- No SDF tokens in agent environments: messaging needs none. The only secret
  in a sandbox is the inference key injected per ADR 0007/0011/0012, which
  agents already had.
- Agents are bound to their subtask by the host, not by what they say; agent
  text is labelled untrusted in every inbox read.
- The HTTP API is unauthenticated (as before): `decided_by`/`resolved_by` are
  self-asserted human names. Bind it to 127.0.0.1 and front it with auth before
  exposing it.

## Consequences / limits

- Delivery retries are in memory (3 tries); a host crash mid-run loses
  undelivered messages (they stay in `agent_messages`) and the run must be
  restarted.
- Cancel is in-process (`request_cancel`); the API also stamps
  `cancel_requested_at`.
- Verification uses the subtask's own fixture snapshot; subtasks that need
  each other's changes are not supported yet.
- `wait=false` runs execute in a host background thread with their own DB
  session; use Postgres for concurrent runs (SQLite serialises writers).
- `max_cost_usd` is enforced from agent-reported usage between polls, so a
  run can overshoot by one poll interval's spend; the hard ceiling is $5.
