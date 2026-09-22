# SDF runtime work map

Baseline: 88fbfa6. Active wave: 01 acceptance/Evidence, 05 Herdr verification, 10 architecture sync.

Dependency chain: 01 → 02 → 03 → 04; (04, 05) → 06; (01, 02, 06) → 07 → 08 → 09. Ticket 10 is independent.

Coordinator owns tracker status, integration review and commits. Workers own their assigned files; no overlapping edits or worker commits.

| Ticket | Owner | State | Scope |
| --- | --- | --- | --- |
| 01 | acceptance_wave + coordinator | resolved | Reviewed; 26 local tests pass, PostgreSQL skipped |
| 05 | herdr_verify | resolved | Live reconnect, active cancellation, pane cleanup, and empty E2B-list proof pass |
| 10 | architecture_sync | needs-info | Content synchronized; full-page render outstanding |
| 02 | integrity_recovery + coordinator | resolved | Dispatch/recovery, relational criterion provenance, PostgreSQL migration/append-only/idempotency/claim-race gate passed |
| 03 | tool_policy | resolved | Structured policy boundary and live E2B containment/lifecycle promotion pass |
| 04 | sandbox | resolved | E2B containment, active cancellation, descendant teardown, and cleanup proof pass |
| 06 | runtime_seam | resolved | HerdrRuntime live reconnect/cancel/terminate and E2B cleanup pass |
| 06b | herdr_e2b | resolved | Persistent E2B Herdr transport lifecycle and sandbox teardown pass |
| 07a | model_escalation | resolved-local | Evaluator-driven basic → medium → high policy with bounded Attempt lineage; live provider qualification remains open |
| 07b | impact_measurement | resolved-local | Objective metrics, attribution fields and deterministic v0 scorecard; production impact remains unclaimed |
| 07–09 | coordinator | needs-info | Fixture loop, normalized events and measured routing implemented locally; real fixture evaluation remains open |

Existing Core tickets 01, 04, 05 and 06 were reopened to record incomplete acceptance criteria. Their historical test results remain partial evidence, not milestone completion.
