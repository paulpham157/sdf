# 06 One agent through HerdrRuntime

Status: needs-info
Blocked by: 04, 05

## What to build and acceptance

Use the verified provider contract for start/send/stream/status/cancel/terminate/reconnect, correlate session to Attempt, retain fake runtime and prevent replay from dispatching twice.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

## Answer

Added `sdf_core/runtime.py` with the internal `AgentRuntime` lifecycle seam
and a deterministic `FakeRuntime`. Sessions correlate to `attempt_id`,
duplicate starts return the existing session, and send/stream/status/
cancel/terminate/reconnect are covered by local tests.

Verification: focused runtime tests pass; full suite and compile checks pass.
This is only a local seam. The provider lifecycle and real-provider process
cleanup still require a Herdr-managed live run; ticket 05 remains the live
verification gate.

## Follow-up implementation

Added `sdf_core/herdr_runtime.py` as a provider adapter over the Herdr 0.9.x
CLI surface. It correlates workspace/pane/agent-session identifiers to the SDF
Attempt, maps start/prompt/read/status/reconnect, and is idempotent per Attempt.
It accepts Herdr's wrapped JSON control responses and raw terminal output, and
restores durable bindings without redispatching.

Cancellation sends `ctrl+c` and only records `CANCELLED` after
`pane process-info` shows that the pane has no foreground process other than
its shell. Termination closes the bound pane and performs the same process
check, accepting an already-closed pane as cleanup evidence. A regression test
keeps a `sleep` child in the foreground and proves cleanup is rejected. These
checks cover the Herdr-observable pane process tree; they do not establish SDF
containment for detached/background descendants.

`RuntimeAgentAdapter` now binds the per-Attempt disposable workspace into a
workspace-aware runtime before `start()`. `HerdrRuntime` preserves that binding
and passes it to `workspace create --cwd`, so a future provider-backed run can
produce artifacts from the same workspace that SDF evaluates. This remains a
local contract test until it is run from a Herdr-managed environment with the
pinned binary; provider prompt/reconnect/cancel/terminate and descendant
cleanup remain live gates.

Added `HerdrBindingSnapshot`/`restore_binding()` so a durable Attempt/session,
workspace and pane identity can be restored after process restart without
calling `agent start` again. Conflicting restored identities are rejected;
provider reconnect remains live-unverified.

The long-term topology is now explicit: SDF remains the control plane and
Herdr plus the coding agent run in the execution environment. `HerdrRuntime`
accepts an injected `HerdrTransport`; the preferred `E2BHerdrTransport` keeps
one persistent E2B sandbox for repeated Herdr commands, while
`SshHerdrTransport` is an alternate hosted deployment. Local subprocess
execution remains only a development fallback. `bind_remote_workspace()` keeps
the sandbox path opaque to the SDF host. Transport is not containment and does
not replace E2B/process policy.

The binding record has an explicit `as_dict()`/`from_mapping()` contract with
strict non-empty identity validation, so durable storage cannot silently
restore a partial Attempt/session binding.

Live lifecycle verification (2026-09-21): Herdr 0.9.1 named session
`sdf-lifecycle-smoke` created workspace `w1`/pane `w1:p1`, started Codex,
returned the marker `HERDR_LIFECYCLE_OK`, and exposed a persistent Herdr agent
snapshot. A fresh adapter instance restored the durable binding without
redispatch, then completed `reconnect -> cancel -> terminate`; the final
snapshot had no agents or workspaces and the session was stopped. Active-work
cancellation, E2B network policy, and descendant cleanup remain separate live
gates, so this ticket stays `needs-info`.

Active-work cancellation follow-up (2026-09-21): a disposable Herdr 0.9.1
session ran a Codex `sleep 60` task. `HerdrRuntime.cancel()` sent `ctrl+c`,
and Herdr reported the agent `idle`, but `pane process-info` still reported
the foreground `codex` PID; the adapter therefore failed closed with
`Herdr pane still has a foreground child process after cleanup`. Explicit
`terminate()` closed the pane, the snapshot became empty, and the named
session stopped. Cancellation needs a provider-supported process cleanup path,
not only an interrupt request.
