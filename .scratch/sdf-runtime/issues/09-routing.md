# 09 Second agent and measured routing

Status: resolved
Blocked by: none for local qualification; live provider verification remains open

## What to build and acceptance

Qualify a second agent using same runtime; routing uses measured capability/availability/latency/cost/success; no ACP/A2A.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

## Resolution

Implemented deterministic measured routing in `sdf_core/routing.py`. A second
agent is eligible only when measured availability, capability, success rate,
latency, cost, load, and sample thresholds pass; stale static metadata is not a
fallback. Local qualification and tie-breaking tests pass. Live provider and
Herdr qualification remain unverified.
