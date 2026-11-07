# SDF Core Architecture

## Purpose

SDF Core is a decision-and-evidence system. It traces business intent through engineering work and back to independent Evidence that can validate or contradict the assumptions behind a Decision.

## Core flow

```text
Business Context → Objective → Task → Attempt → Artifact → Evidence
                                      ↓
                         Evaluator determines outcome
```

The Decision Graph preserves these typed relationships. An agent's completion message is not an accepted result; an **Accepted Outcome** requires independent Evidence that meets the Task's acceptance criteria.

## Ownership boundaries

SDF owns the Task, Attempt, state, budget, Policy Decision, Evaluator, Evidence, and Decision Graph. An Agent Runtime only controls a Runtime Session associated with an Attempt; it never becomes the source of truth for SDF domain state.

Tool actions are Attempt-bound and pass through the Tool Proxy. The proxy obtains a Policy Decision before execution. Containment must independently limit filesystem, process, and network access because policy cooperation by an agent is insufficient.

On Linux, a process started inside an Attempt workspace is confined even when it skips the Tool Proxy (`sdf_core/process_containment.py`). Fixture sandbox children and the live adapter's workspace subprocesses install a Landlock ruleset (read the system, write only the workspace and roots the trusted caller names) and lose credential-shaped environment variables that the Attempt's credential scope did not grant.

## Execution architecture

```text
SDF Control Plane (this host)
  → Internal Agent Runtime
  → Agent Credential per agent kind, resolved on the host (Credential Mode)
  → HerdrRuntime + persistent sandbox Herdr transport
      (E2B default; Daytona selectable via SDF_SANDBOX_PROVIDER)
  → Herdr workspace inside the sandbox (credential as create-time envs)
  → one coding agent (Codex or Claude Code)
  → Tool Proxy + Policy Decision
  → evaluator artifacts and Evidence pulled back to SDF
```

The control plane must not require the coding agent to run on the SDF host.
`HerdrRuntime` therefore accepts an injected transport; the local subprocess
runner is only a test/development fallback. The preferred deployment transport
defaults to a persistent E2B sandbox created with the E2B Python SDK
(`Sandbox.create(envs=)`, repeated `commands.run`, then `kill`), so
prompt/reconnect/cancel can address the same Herdr process and workspace.
Operators may select Daytona beside E2B with `SDF_SANDBOX_PROVIDER=daytona`
(`DaytonaHerdrTransport`: create with `env_vars=`, `process.exec`, SDK
filesystem stage/collect, then `delete`). E2B remains the default; existing
`SDF_E2B_*` / `E2B_*` knobs are unchanged. SSH remains a compatibility
transport for a controlled host. The long-term remote shape is
`HerdrEndpointTransport`: SDF sends authenticated HTTPS JSON commands to a
small deployment-owned bridge running beside Herdr inside the sandbox. The
bridge owns the local Herdr socket/CLI; SDF never starts Herdr or Codex
locally. The endpoint must be HTTPS-only, token-authenticated, and implement
the command-response contract tested in
`tests/test_herdr_endpoint_transport.py`.

`HERDR_ENDPOINT_TOKEN` is an endpoint credential created by the deployment
operator. It is unrelated to `E2B_API_KEY`, `DAYTONA_API_KEY`, Codex
authentication, or any model provider key. The bridge can read it from the
`HERDR_ENDPOINT_TOKEN` environment variable or from the mounted secret file
named by `HERDR_ENDPOINT_TOKEN_FILE` (default
`/run/secrets/herdr_endpoint_token`). The token must never be committed to the
repository, baked into the image, or printed in logs.

The agent's Agent Credential enters the sandbox only at creation (ADR 0007).
`create_credentialed_transport` resolves one Credential Mode per agent kind
before the sandbox exists: `subscription` reads a herdr-e2b plugin connection
through the plugin's own code (`PluginConnectionBridge`), and `api-key` reads
dedicated `SDF_*` host variables. The values become sandbox-wide envs; seed
steps write agent config by variable name, including
`~/.config/sdf/agent-env.sh` because the image's Herdr server starts before
the seeds and panes would otherwise miss the envs. The Runtime Session and its
`runtime_started` event record the mode and connection id, never the secret.
Claude Code receives `--dangerously-skip-permissions` only from the E2B
transport (ADR 0008); Daytona does not inherit that capability merely by using
Herdr. Codex gets its workspace marked trusted. The
E2B template remains `sdf-herdr-agents` (ADR 0009); Daytona uses
`SDF_DAYTONA_SNAPSHOT` (operator-chosen snapshot). The headless `e2b-box run`
path is separate: the plugin selects the credential itself and SDF records only
the terminal result.

Transport is not containment: the provider process boundary must still enforce
filesystem, process and network limits. Daytona as
`SDF_CONTAINMENT_BACKEND` is not yet offered.

The current implementation retains deterministic fake adapters for tests. A live provider must demonstrate session start, input, output, cancellation, termination, reconnect, and non-duplicated execution before it can support milestone claims.

## Model escalation

SDF starts with the lowest suitable Model Tier (`basic`, `medium`, then `high`). Escalation occurs only after a verified evaluator outcome, such as failure, inconclusive evidence, timeout, or policy/tool failure. Each escalation creates a new Attempt with lineage to its predecessor and remains bounded by maximum attempts, maximum tier, and a hard cost ceiling.

## Dispatch and recovery

[ADR 0041](adr/0041-keyless-dispatch-and-orphan-recovery.md) (proposed; implemented on `main`):

- A `POST /tasks/{id}/run` without `dispatch_key` gets a derived key: `keyless-` plus the SHA-256 of the canonical request fields (task id, resolved fixture, instructions, checks, validation target, parent Attempt, Model Tier). Repeating the same request returns the stored Attempt and does not run the adapter again, also after the Attempt is terminal. A new execution sends a new explicit `dispatch_key`.
- `ExecutionService.begin` claims an executor lease (`attempts.executor_id`, `executor_heartbeat_at`, migration `0027_attempt_executor_lease`) in the commit that moves the Attempt to `running`. A process-local refresher renews it every 10 seconds; another process's lease expires after 30 seconds.
- Orphan recovery runs at API startup, at shutdown, after each heartbeat and in `begin`. It moves a `running` Attempt whose lease is dead to `failed` (`error_class` becomes `killed` unless one was already recorded), and its Task `running -> evaluating -> failed` when no other Attempt of that Task is running. No state-machine transition is added.
- Attempt and Task writes in recovery, `finish` and cancel are compare-and-swap on the stored status, so recovery cannot overwrite a finished Attempt and a stale worker cannot resurrect a recovered one.

## Persistence and measurement

PostgreSQL is the deployed source of truth for graph state, Tasks, Attempts, transitions, idempotency keys, and Evidence metadata. SQLite is permitted only for isolated tests and local deterministic demonstrations.

Objectives may declare a metric, baseline, target, source, owner, and measurement window. Local evaluator results are Evidence, not proof of production impact; impact requires a measured Objective outcome against its baseline.

## Evidence classes and integrity

Every new Evidence row has an Evidence class
([ADR 0042](adr/0042-evidence-classes.md), proposed; implemented on `main`):

- **execution**: what the Attempt did (diff and process log), written by `finish`, with the Attempt's agent as producer. It never accepts an artifact or satisfies a criterion.
- **verification**: an Evaluator verdict from a sealed oracle ([ADR 0039](adr/0039-evidence-independence.md), [ADR 0040](adr/0040-held-out-oracles.md)). A coverage gap is verification with status `INCONCLUSIVE` and no oracle digest. Only a verification `PASS` that passes the acceptance gate counts as a verified pass.
- **outcome**: an Objective-level result recorded through `POST /objectives/{id}/evidence` by an approver, with a verifier who is neither that approver nor the agent of any Attempt under the Objective. It `measures` the Objective and never validates a Task.

Each new row also carries `content_mac`, an HMAC-SHA256 over its verdict, Attempt binding, and acceptance metadata ([ADR 0043](adr/0043-evidence-content-mac.md), proposed; migration `0029_evidence_content_mac`, payload version inside the MAC). The key is `SDF_EVIDENCE_MAC_KEY`. Without it a public development key is used, and a match under that key is not authenticity against someone who knows it. `verify_attempt_revision` recomputes the MAC. A missing or old-format MAC fails and is not re-signed. The MAC binds the stored verifier string; it does not prove that verifier is independent.

## Go orchestration core

`sdf-orch` ([ADR 0035](adr/0035-port-orchestration-core-to-go.md), proposed) is a Go process with an API gateway, task queue, Attempt dispatcher, in-process message bus and a local SQLite Tool Proxy fence ([ADR 0037](adr/0037-go-tool-proxy-fence-audit-parity.md), [ADR 0038](adr/0038-go-attempt-evidence-mode.md)). The Python Coordinator remains the single writer of Attempt state until a cutover is decided. See [sdf-orch.md](sdf-orch.md).

## Human interaction

[ADR 0044](adr/0044-human-read-model-and-approver-inbox.md) to [ADR 0047](adr/0047-console-ui.md) are accepted and fix the order of work: a read model and approver inbox over existing rows, then human cancel of one Attempt and human messages on the existing hub, then Server-Sent Events over the runtime-event stream, then a same-origin console that calls only the public API. Human attestation of Evidence is deferred to a later ADR.

ADR 0044 is built (`sdf_core/human_read.py`). Viewer `GET /tasks`, `/attempts`, `/plans` and `/objectives` list existing rows in keyset pages (50 by default, at most 100), and `GET /tasks/{id}` and `GET /attempts/{id}` read one. `GET /inbox` lists the pending decision requests the caller may resolve: it is empty for a principal that cannot approve, and it omits a proposer's own plan unless single-user mode is on. Payloads are redacted and carry no lease columns, dispatch keys or held-out oracle output. Nothing on these routes writes a domain row. ADR 0045 to 0047 are not built yet.

## Diagrams

`docs/SDF Core Architecture.tldraw` (tldraw) has four pages: the component architecture, the Task and Attempt state machines, the `POST /tasks/{task_id}/run` sequence, and the Evidence verification pipeline. Proposed or not-yet-built parts are drawn dashed.

## LLM transport

Each agent kind calls a model over exactly one HTTP API family
([ADR 0034](adr/0034-openai-or-claude-compatible-llm-transport.md)):

- **OpenAI-compatible** (`openai`): `codex`, `opencode`, `pi`, `qwen`, `goose`.
  `codex` requires the OpenAI Responses API (`POST /v1/responses`). A host
  that implements only Chat Completions cannot serve Codex. The other four
  kinds use their existing OpenAI-compatible clients.
- **Claude / Anthropic Messages** (`anthropic`): `claude`.

A provider host is a base URL plus a key plus a model. Vultr Serverless
Inference (`https://api.vultrinference.com/v1`, model `glm-5.3`) is the
example OpenAI-compatible endpoint, not a third protocol. Credential Mode
stays `subscription` or `api-key` (ADR 0007). Egress and `attempts.provider`
follow the configured host (`{protocol}:{host}`).

## Protocol status

ACP and A2A are historical research topics, not active Core dependencies. A future protocol integration requires a concrete interoperability requirement and a new architecture decision. See [ADR-0004](adr/0004-internal-runtime-and-enforced-tool-policy.md) and [the active roadmap](roadmap.md). The LLM transports above are HTTP APIs the existing agent CLIs already speak; they are not ACP or A2A.
