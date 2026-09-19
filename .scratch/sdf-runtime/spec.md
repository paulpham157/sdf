# SDF runtime execution plan

Status: ready-for-agent
Baseline: 88fbfa6

## Problem and solution
The current fake-adapter demo lacks criterion coverage, dependable evidence provenance and containment. Deliver a verified one-agent fixture loop through an internal Agent Runtime and intended HerdrRuntime, with SDF domain ownership preserved under ADR-0004.

## Implementation and testing decisions
Use current HTTP task/run/evidence/trace interfaces and ExecutionService/Evaluator public boundaries for behavior tests. Introduce only the accepted AgentRuntime and Tool Proxy seams where needed. Tests exercise missing criteria, negative checks, failure recovery and denied side effects; deterministic checks are not live-provider proof. No arbitrary passing command certifies business assumptions. PostgreSQL validation must distinguish migrations from create_all-based tests.

## Scope and ordering
Follow numbered tickets and their blocking edges. Initially fan out 01, 05 and 10; later tickets remain queued. Each worker owns explicit files, records test evidence, and does not commit. Coordinator reviews and commits coherent slices. Runtime capabilities remain unverified until ticket 05 establishes a real contract.

## Out of scope
ACP/A2A, full UI, production deployment, credentials provisioning and automatic paid provider calls. No real repository execution until containment is verified.

## Acceptance
Objective → Task → one contained real agent Attempt → diff → criterion evaluation → independently recorded Evidence → complete trace. A negative case and retry/cancellation/reconnect behavior are required before claiming milestone completion.
