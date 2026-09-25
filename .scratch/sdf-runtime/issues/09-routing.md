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

## 2026-09-25 Live verification attempt

**Status (corrected by coordinator)**: Codex now runs live (ticket 07: two attempts, sandboxes iba42paezsp7j6n3qq9ma and iv0bt559idr11wkhgl5hn). Qualifying a *second* agent with real measurements is still open. Routing stays resolved for its local slice.

**Locally verified** (all 178 tests pass):
- Deterministic measured routing in `sdf_core/routing.py`
- Tie-breaking behavior when multiple agents meet thresholds
- Stale metadata rejected as fallback
- Qualification tests pass with synthetic measurement data

**Live qualification blocked:**
- Only one agent (Codex) ever provisioned; halted at authentication
- No measured availability/capability/success rate data collected
- No real provider latency or cost observed
- No second agent routing decision tested
- Sandbox ID: im41p6h0cq8rr3joitci6 (captured and killed)
