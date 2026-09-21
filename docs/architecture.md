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

## Execution architecture

```text
SDF Control Plane (this host)
  → Internal Agent Runtime
  → HerdrRuntime + persistent E2B Herdr transport (SSH/API is an alternate deployment)
  → Herdr workspace inside the execution sandbox
  → one coding agent
  → Tool Proxy + Policy Decision
  → evaluator artifacts and Evidence pulled back to SDF
```

The control plane must not require the coding agent to run on the SDF host.
`HerdrRuntime` therefore accepts an injected transport; the local subprocess
runner is only a test/development fallback. The preferred deployment transport
is a persistent E2B sandbox (`create --detach`, repeated `sandbox exec`, then
`kill`), so prompt/reconnect/cancel can address the same Herdr process and
workspace. SSH remains a compatibility transport for a controlled host. The
long-term remote shape is `HerdrEndpointTransport`: SDF sends authenticated
HTTPS JSON commands to a small deployment-owned bridge running beside Herdr
inside the sandbox. The bridge owns the local Herdr socket/CLI; SDF never starts
Herdr or Codex locally. The endpoint must be HTTPS-only, token-authenticated,
and implement the command-response contract tested in
`tests/test_herdr_endpoint_transport.py`.

`HERDR_ENDPOINT_TOKEN` is an endpoint credential created by the deployment
operator. It is unrelated to `E2B_API_KEY`, Codex authentication, or any model
provider key. The bridge can read it from the `HERDR_ENDPOINT_TOKEN` environment
variable or from the mounted secret file named by `HERDR_ENDPOINT_TOKEN_FILE`
(default `/run/secrets/herdr_endpoint_token`). The token must never be committed
to the repository, baked into the image, or printed in logs.

Transport is not containment: the E2B/process boundary must still enforce
filesystem, process and network limits.

The current implementation retains deterministic fake adapters for tests. A live provider must demonstrate session start, input, output, cancellation, termination, reconnect, and non-duplicated execution before it can support milestone claims.

## Model escalation

SDF starts with the lowest suitable Model Tier (`basic`, `medium`, then `high`). Escalation occurs only after a verified evaluator outcome, such as failure, inconclusive evidence, timeout, or policy/tool failure. Each escalation creates a new Attempt with lineage to its predecessor and remains bounded by maximum attempts, maximum tier, and a hard cost ceiling.

## Persistence and measurement

PostgreSQL is the deployed source of truth for graph state, Tasks, Attempts, transitions, idempotency keys, and Evidence metadata. SQLite is permitted only for isolated tests and local deterministic demonstrations.

Objectives may declare a metric, baseline, target, source, owner, and measurement window. Local evaluator results are Evidence, not proof of production impact; impact requires a measured Objective outcome against its baseline.

## Protocol status

ACP and A2A are historical research topics, not active Core dependencies. A future protocol integration requires a concrete interoperability requirement and a new architecture decision. See [ADR-0004](adr/0004-internal-runtime-and-enforced-tool-policy.md) and [the active roadmap](roadmap.md).
