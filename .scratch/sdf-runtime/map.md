# SDF runtime work map

Baseline: 88fbfa6. Active wave: 01 acceptance/Evidence, 05 Herdr verification, 10 architecture sync.

Dependency chain: 01 → 02 → 03 → 04; (04, 05) → 06; (01, 02, 06) → 07 → 08 → 09. Ticket 10 is independent.

Coordinator owns tracker status, integration review and commits. Workers own their assigned files; no overlapping edits or worker commits.

| Ticket | Owner | State | Scope |
| --- | --- | --- | --- |
| 01 | acceptance_wave + coordinator | resolved | Reviewed; 26 local tests pass, PostgreSQL skipped |
| 05 | herdr_verify | needs-info | Herdr 0.9.1 disposable session/workspace/agent start and pane output pass; prompt/reconnect/termination proof outstanding |
| 10 | architecture_sync | needs-info | Content synchronized; full-page render outstanding |
| 02 | integrity_recovery + coordinator | resolved | Dispatch/recovery and relational criterion provenance; concurrency/append-only hardening remains follow-up |
| 03 | tool_policy | needs-info | Structured API boundary, Attempt guard, append-only durable audit/events and replay fence pass locally; OS-level containment and cross-process claim proof outstanding |
| 04 | sandbox | needs-info | Fixture boundary and fail-closed process gate pass locally; macOS host smoke fails; explicit E2B containment backend is implemented but live/network proof remains |
| 06 | runtime_seam | needs-info | Internal AgentRuntime + FakeRuntime seam; Herdr integration remains gated |
| 06b | herdr_e2b | needs-info | e2b-box 0.5.0 disposable sync/exec/pull/kill smoke passes; Attempt-bound adapter and network policy proof outstanding |
| 07a | model_escalation | resolved-local | Evaluator-driven basic → medium → high policy with bounded Attempt lineage; live provider qualification remains open |
| 07b | impact_measurement | resolved-local | Objective metrics, attribution fields and deterministic v0 scorecard; production impact remains unclaimed |
| 07–09 | coordinator | needs-info | Fixture loop, normalized events and measured routing implemented locally; live Herdr/containment gates remain open |

Existing Core tickets 01, 04, 05 and 06 were reopened to record incomplete acceptance criteria. Their historical test results remain partial evidence, not milestone completion.
