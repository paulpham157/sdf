# 07 Real agent fixture evidence loop

Status: needs-info
Blocked by: 05, 06

## What to build and acceptance

One real agent edits bounded fixture; independent criterion checks create artifacts/Evidence and full trace; prove positive and negative cases. Requires provider access, no fabricated live proof.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

## Follow-up implementation

Added `AgentFixtureLoop`, which drives start/send/stream/status through the
internal runtime seam and then runs independent deterministic evaluator checks.
The result exposes `runtime_completed` separately from `accepted`; a runtime
`done` state cannot certify acceptance. Fake-runtime tests cover positive and
negative criterion results and missing-workspace rejection. A real Herdr agent
session and provider-backed fixture edit remain unverified, so this ticket is
not resolved.

`AgentFixtureLoop` now accepts the `RuntimeController` seam (including durable
event sinks) rather than requiring a raw provider runtime. A local integration
test verifies lifecycle events are persisted while evaluator acceptance remains
independent. Artifact/Evidence/trace persistence still belongs to the full
ExecutionService/provider-backed loop and live agent proof remains open.

The HTTP acceptance surface now exposes normalized lifecycle events in
`GET /tasks/{task_id}/trace` alongside graph nodes/edges, while the structured
`POST /attempts/{attempt_id}/actions` boundary persists Tool Proxy events in the
same attempt trace. This closes the local trace-assembly gap; provider-backed
artifact capture and real Herdr evidence remain unverified.
