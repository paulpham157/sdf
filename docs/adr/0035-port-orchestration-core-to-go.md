# ADR 0035: port the orchestration core to Go

Status: proposed (2026-10-31). Written to record the product owner's
direction for the orchestration, message-bus, API-gateway and task-queue
layers. It becomes accepted when a maintainer accepts the implementing
pull request. Credential Mode, Tool Proxy, sandbox credential scope,
Attempt lifecycle, Evidence provenance and the agent runtime contract
(ADR 0002, ADR 0007, ADR 0026, ADR 0029, ADR 0032, ADR 0034) are unchanged
in meaning; only the process that hosts those invariants moves.

## Context

SDF today runs the coordinator, message hub, HTTP API and attempt
scheduling in the Python stack (`sdf_core`). That stack is fine for
moderate concurrency, but four layers are becoming IO-bound and
high-traffic:

1. **Orchestration core** — dispatching Attempts to agents in disposable
   sandboxes, tracking lifecycle, cancellation and revision.
2. **Message bus** — high-volume streaming between agents
   (`sdf-msg`, hub fan-out, live evidence).
3. **API gateway** — high request rates for run control, decisions and
   telemetry.
4. **Task queue** — long queues with scheduling, retry, cancellation and
   structured error handling.

Python's concurrency model (asyncio + threads) makes cheap fan-out,
strict cancellation and stable backpressure expensive to prove under
load. Compile-edit-test cycles for the hot path are also slower than a
statically compiled language. The product owner therefore wants a
language whose concurrency primitives match agent-to-agent streaming:
goroutines and channels, fast compile, and cheap concurrency for
IO-bound work.

## Decision

Port the four layers above to **Go**:

1. **Orchestration core** — one Go process (or small set of processes)
   owns Attempt dispatch into sandboxes, lifecycle transitions,
   cancellation and revision bookkeeping. Invariants stay those of
   ADR 0032 and ADR 0029; the implementation language changes.
2. **Message bus** — agent-to-agent and hub streaming uses **goroutines
   + channels** in-process for the hot path. Bounded channels carry
   backpressure. Cross-process or durable fan-out, if needed later, is
   a separate decision and must not weaken Evidence independence
   (ADR 0002).
3. **API gateway** — the high-traffic HTTP surface moves to Go so request
   handling shares the same concurrency model as the bus and queue.
4. **Task queue** — long queues, scheduling, retry, cancellation and
   error handling are implemented in Go with explicit context
   cancellation and idempotent enqueue keys.

Python may remain for peripheral tooling, evaluators and adapters that
are not on the hot path. The common runtime contract between agent kinds
(Codex, Claude, OpenCode, Pi, Qwen, Goose) stays language-agnostic: the
Go core still speaks the same sandbox, credential and event surfaces.

## Consequences

**Benefits**

- Cheap concurrency for thousands of concurrent Attempts and streams
  without a thread-per-connection cost model.
- Natural cancellation via `context.Context` wired through dispatch,
  bus and queue — a single cancel can stop an Attempt's goroutine tree.
- Bounded channels make backpressure observable and testable.
- Fast compile shortens the edit–test loop on the orchestration hot path.
- One concurrency model across orchestration, bus, gateway and queue
  reduces impedance mismatches that today hide cancellation races.

**Costs**

- Dual-language codebase until the port completes; interfaces between
  Go and remaining Python must be explicit (gRPC/HTTP/events), not
  shared memory.
- Team needs Go review capacity and CI for Go tests, race detector and
  goroutine metrics.
- Rewrite risk: Attempt lifecycle, idempotency and Evidence provenance
  must be re-proven, not assumed carried over.
- Operational change: new binaries, health probes and queue-depth
  dashboards.

## Alternatives considered

1. **Stay on Python (asyncio / trio).** Rejected for the four hot
   layers: cancellation and backpressure under long queues and streaming
   are harder to prove; compile/runtime feedback is slower. Python
   remains acceptable off the hot path.
2. **External message broker (NATS / Kafka / Redis Streams) instead of
   in-process channels.** Deferred. A broker helps multi-node fan-out
   and durability, but adds ops cost and does not by itself fix
   cancellation races or idempotency. This ADR chooses **in-process
   goroutines + channels** for the first port. Introducing NATS/Kafka
   later is compatible if the bus API stays stable and Evidence
   independence is preserved.
3. **Other languages (Rust, Java/Kotlin).** Not chosen. Rust raises
   rewrite cost and hiring bar without a clear win for this IO-bound
   shape. JVM brings heavier runtime and GC tuning for little gain over
   Go's goroutine model for this workload.
4. **Partial port (queue only, or gateway only).** Rejected as the
   primary path: the four layers share cancellation and backpressure;
   splitting languages across them recreates the impedance problem.

## Risks and mitigations

| Risk | Why it matters | Mitigation |
| --- | --- | --- |
| **Cancellation race / goroutine leak** | A cancelled Attempt must release sandbox work and bus subscribers; a leaked goroutine holds RAM and may keep writing Evidence. | Every dispatch path takes `context.Context`. Cancel closes related channels. Chaos test: goroutine count before/after cancel must return to baseline (see Verification). |
| **Idempotency under retry** | Queue retry or client reconnect must not double-execute an Attempt or duplicate Evidence. | Stable idempotency keys on enqueue and Attempt creation; store records a single execution per key (align with ADR 0032 revisions). Test: same key twice → one execution. |
| **Backpressure on bounded channels** | Unbounded buffers hide overload until OOM. | All hot-path channels are bounded. Producers block or fail-fast when full; metrics expose depth. Load test: millions of messages with stable goroutine count and RAM. |
| **Invariant drift during rewrite** | Port could silently weaken Tool Proxy binding, credential scope or Evidence independence. | No new abstractions without proving existing ones. Port checklist ties each Go module to the ADR invariant it must preserve (0002, 0007, 0026, 0029, 0032). |
| **Split-brain with leftover Python** | Two coordinators must not both own Attempts. | Cutover plan: one writer for Attempt state; feature flag or process role so only the Go core advances lifecycle. |

## Verification plan

These proofs are required before Status moves from proposed to accepted
for production traffic:

1. **Cancellation chaos test** — start N Attempts, cancel mid-flight,
   assert goroutine count returns to baseline within a bound and no
   further Evidence or sandbox side effects after cancel. Fail on
   leaked goroutines.
2. **Idempotency test** — enqueue / start the same idempotency key
   twice under retry and reconnect; assert a single Attempt execution
   and a single authoritative Evidence close.
3. **Backpressure test** — inject millions of bus messages against
   bounded channels; assert goroutine count and RSS stay within agreed
   ceilings, and producers observe block or controlled shed rather than
   unbounded growth.
4. **CI / live metrics** — continuous export of live goroutine count and
   queue depth (plus channel fullness where practical). CI runs the race
   detector on the Go packages. Alerts fire when goroutines or depth
   exceed thresholds after a quiet period following cancel.

Until those tests pass in CI, the Go port remains proposed and must not
be the sole writer of Attempt state in production.
