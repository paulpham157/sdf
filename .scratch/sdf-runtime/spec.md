# SDF runtime execution plan

Status: needs-info
Baseline: 88fbfa6

Persistence promotion gate: resolved (PostgreSQL Compose migration, append-only
triggers, replay idempotency, and concurrent claim verification passed).
Herdr/E2B containment promotion gate: resolved (live reconnect, active
cancellation, descendant teardown, and empty sandbox verification passed on
2026-09-22). `SDF_CONTAINMENT_SMOKE=passed` remains valid. The overall runtime
spec remains `needs-info` only for real fixture-evaluation/coordinator work;
E2B proxy/allowlist is future hardening, not a blocker for agent network access.

## Problem and solution
The current fake-adapter demo lacks criterion coverage, dependable evidence provenance and containment. Deliver a verified one-agent fixture loop through an internal Agent Runtime and intended HerdrRuntime, with SDF domain ownership preserved under ADR-0004. Local deterministic and provider-neutral slices are implemented; live provider and containment gates remain open.

## Implementation and testing decisions
Use current HTTP task/run/evidence/trace interfaces and ExecutionService/Evaluator public boundaries for behavior tests. Introduce only the accepted AgentRuntime and Tool Proxy seams where needed. Tests exercise missing criteria, negative checks, failure recovery and denied side effects; deterministic checks are not live-provider proof. No arbitrary passing command certifies business assumptions. PostgreSQL validation must distinguish migrations from create_all-based tests.

## Scope and ordering
Follow numbered tickets and their blocking edges. Initial fan-out and local implementation slices are complete; each slice records deterministic evidence without claiming live provider behavior. Runtime capabilities remain unverified until ticket 05 establishes a real contract.

## Out of scope
ACP/A2A, full UI, production deployment, credentials provisioning and automatic paid provider calls. No real repository execution until containment is verified.

## Acceptance
Objective → Task → one contained real agent Attempt → diff → criterion evaluation → independently recorded Evidence → complete trace. A negative case and retry/cancellation/reconnect behavior are required before claiming milestone completion.
