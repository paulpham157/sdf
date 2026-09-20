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
This is only a local seam. Herdr start/send/stream/cancel/terminate/reconnect
and real-provider process cleanup remain unverified because tickets 04 and 05
are still gated.

## Follow-up implementation

Added `sdf_core/herdr_runtime.py` as a provider adapter over the Herdr 0.9.x
CLI surface. It correlates workspace/pane/agent-session identifiers to the SDF
Attempt, maps start/prompt/read/status/reconnect, and is idempotent per Attempt.
It accepts Herdr's wrapped JSON control responses and raw terminal output, and
restores durable bindings without redispatching.

Cancellation sends `ctrl+c` and only records `CANCELLED` after
`pane process-info` shows that the agent is no longer foreground. Termination
closes the bound pane and performs the same process check, accepting an
already-closed pane as cleanup evidence. These checks are covered by local
fake-runner tests; they do not establish SDF containment or descendant cleanup.

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

The binding record has an explicit `as_dict()`/`from_mapping()` contract with
strict non-empty identity validation, so durable storage cannot silently
restore a partial Attempt/session binding.
