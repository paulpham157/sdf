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
