# Non-interactive agent model for SDF

Status: researched; implementation decision pending

Date: 2026-09-22

## Question

Can SDF manage coding agents non-interactively inside disposable sandboxes,
without using terminal key presses as the lifecycle API?

## Executive conclusion

Yes, but the control plane must own an explicit execution identity and a
supervisor contract. A sandbox API can create/connect/timeout/kill a sandbox
and can run or kill individual commands. That is process/job control, not
conversation control for a coding agent. E2B documents command handles, process
listing, command kill, stdin, PTY and sandbox kill/timeout operations
([E2B Python SDK sandbox reference](https://e2b.dev/docs/sdk-reference/python-sdk/v2.5.0/sandbox_sync)).

Herdr is a terminal workspace manager: its public model is a real agent in a
real pane, with prompt/wait/read/status and pane process inspection. Its socket
API exposes `agent.prompt`, `agent.wait`, `session.snapshot`,
`pane.process_info`, and pane close, but does not establish a provider-neutral
per-agent cancel/terminate contract
([Herdr socket API](https://herdr.dev/docs/socket-api/),
[Herdr agents documentation](https://github.com/herdrdev/herdr/blob/master/docs/next/website/src/content/docs/agents.mdx)).

Therefore SDF should not model `idle` as proof that a process was cancelled.
For a non-interactive path, the preferred boundary is:

```text
SDF control plane
  -> execution supervisor in the sandbox
    -> coding-agent process / native session
      -> workspace and tools
```

The supervisor owns start, input/job submission, status, cancellation,
termination, reconnect and descendant cleanup. Herdr can remain an optional
interactive observation/session adapter, but it should not be the only source
of process termination truth.

## Source facts

### E2B provides process and sandbox primitives

The E2B SDK documents:

- `Sandbox.connect()` to reconnect to a running or paused sandbox;
- `Sandbox.kill()` and `Sandbox.set_timeout()` for sandbox lifecycle;
- `sandbox.commands.run(..., background=True)` returning a command handle;
- `sandbox.commands.list()` to enumerate running commands and PTYs;
- `sandbox.commands.connect(pid)` to reconnect to a running command;
- `sandbox.commands.kill(pid)` using `SIGKILL`;
- stdin and PTY operations for interactive commands.

These are strong primitives for a supervisor, but they do not define a coding
agent's conversation/session semantics. A supervisor must add that layer and
persist the mapping between SDF Attempt and provider identifiers.

E2B describes each sandbox as an isolated microVM/session and exposes network
and lifecycle capabilities through its API
([E2B product overview](https://e2b.dev/)). E2B's public material also states
that the sandbox itself is not an LLM: the application brings the model and
agent logic, while E2B supplies the environment and execution boundary
([E2B sandbox overview](https://e2b.dev/resources/e2b-sandbox)).

### Herdr provides terminal/session control

Herdr's agent model tracks an agent recognized inside a pane. The official
agent guide describes sessions, workspaces, tabs, panes and agents as separate
objects, with panes keeping terminal processes alive across client detach or
disconnect ([Herdr agent guide](https://github.com/herdrdev/herdr/blob/master/distribution/agent-guide.md)).

The socket API exposes `agent.prompt`, `agent.wait`, `agent.read`,
`session.snapshot`, `events.subscribe`, `pane.process_info`, and
`pane.close`. `session.snapshot` is a bootstrap snapshot; it is not a replay
log. Clients must subscribe to events first, then snapshot, to avoid a gap
([Herdr socket API](https://github.com/herdrdev/herdr/blob/master/docs/next/website/src/content/docs/socket-api.mdx)).

Herdr's documented `idle`, `working`, `blocked` and `done` values are semantic
observations. `pane.process_info` separately reports shell PID, foreground
process group and foreground processes. Those signals must not be collapsed
into one `cancelled` state.

## Proposed non-interactive contract

The minimum supervisor API should be:

```text
start(attempt_id, workspace, agent_spec) -> ExecutionBinding
submit(execution_id, input_or_task) -> CommandReceipt
status(execution_id) -> ExecutionStatus
read(execution_id, cursor) -> OutputChunk[]
cancel(execution_id, grace_ms) -> CancellationResult
terminate(execution_id) -> TerminationResult
reconnect(execution_id) -> ExecutionStatus
```

`ExecutionBinding` must persist at least:

```text
attempt_id
sandbox_id
execution_id / command_pid
native_agent_session_id (optional)
workspace_id
started_at
```

### State rules

- `submitted` means the supervisor accepted a job, not that an agent ran it.
- `working` means a process/job is observed as active.
- `idle` means the agent says it is ready; it does not prove child cleanup.
- `cancel_requested` records intent and the deadline.
- `cancelled` is valid only after the supervisor/provider confirms the target
  process and descendants are gone.
- `terminated` is the hard cleanup result, normally by process-group or sandbox
  kill.
- `unknown` is fail-closed and requires reconciliation, not automatic retry.

Cancellation must be idempotent. Repeating it with the same execution identity
must not start a second process or produce a second side effect. A timeout must
always have a final cleanup path, normally `terminate` in `finally`.

## Network boundary

Three different network paths must remain separate:

1. **Agent egress:** the coding process reaches package registries, model APIs
   or other destinations according to the E2B/provider egress policy.
2. **Structured SDF network action:** `network.request` goes through Tool
   Proxy policy, audit and an explicitly authorized capability.
3. **Provider control plane:** sync/exec/pull/kill traffic reaches E2B's API.

Allowing (1) does not authorize (2). Blocking (2) does not prove that (1) is
firewalled. E2B's current product documentation describes network as a
sandbox capability governed by egress policy
([E2B product overview](https://e2b.dev/)); the policy must be configured and
tested at that provider boundary if SDF requires destination allowlisting.

For the current SDFA implementation, the live evidence proves:

- an agent process in E2B can reach `https://e2b.dev`;
- SDF structured `network.request` is denied before execution in E2B mode;
- the box is killed and no tracked box remains after the smoke.

It does **not** prove arbitrary outbound destination filtering. If the desired
policy is “agent egress allowed, structured SDF network denied,” the current
separation is correct. If the desired policy is “agent egress allowlisted,” an
E2B/provider network policy and a destination matrix are still required.

## Decision for SDFA

Use a non-interactive supervisor for the primary execution path. Keep
`HerdrRuntime` as an optional interactive adapter and observation surface until
Herdr exposes or the deployment supplies a verified process-control contract.
Do not use `ctrl+C` as the definition of cancellation; it is only an
interactive input fallback. Do not treat Herdr `idle` as process termination.

The next implementation slice should be a disposable supervisor proof:

1. start one agent job in E2B and persist its binding;
2. reconnect from a fresh SDF process using the binding;
3. submit one deterministic task and capture output/cursor;
4. cancel during active work and verify process-group disappearance;
5. repeat cancel to prove idempotency;
6. terminate the sandbox in `finally` and verify no tracked sandbox remains;
7. record the exact network policy mode and test both agent egress and
   structured SDF network denial separately.

Until those checks pass, Herdr active cancellation and provider-level network
allowlisting remain open gates. The existing PostgreSQL promotion gate is
independent of these runtime/provider claims.

## Verification boundary

This document is a source-backed design/research note. It does not claim that
the proposed supervisor exists in SDFA or that E2B provides a ready-made
coding-agent conversation API. The provider facts above come from official E2B
and Herdr documentation/source; the supervisor contract and SDFA recommendation
are explicit design inferences.
