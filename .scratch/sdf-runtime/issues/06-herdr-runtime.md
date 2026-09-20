# 06 One agent through HerdrRuntime

Status: claimed
Blocked by: 04, 05

## What to build and acceptance

Use the verified provider contract for start/send/stream/status/cancel/terminate/reconnect, correlate session to Attempt, retain fake runtime and prevent replay from dispatching twice.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.
