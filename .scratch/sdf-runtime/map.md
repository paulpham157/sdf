# SDF runtime work map

Baseline: 88fbfa6. Active wave: 01 acceptance/Evidence, 05 Herdr verification, 10 architecture sync.

Dependency chain: 01 → 02 → 03 → 04; (04, 05) → 06; (01, 02, 06) → 07 → 08 → 09. Ticket 10 is independent.

Coordinator owns tracker status, integration review and commits. Workers own their assigned files; no overlapping edits or worker commits.

| Ticket | Owner | State | Scope |
| --- | --- | --- | --- |
| 01 | acceptance_wave + coordinator | resolved | Reviewed; 26 local tests pass, PostgreSQL skipped |
| 05 | herdr_verify | needs-info | Documented contract researched; live lifecycle proof outstanding |
| 10 | architecture_sync | needs-info | Content synchronized; full-page render outstanding |
| 02 | integrity_recovery + coordinator | resolved | Dispatch/recovery and relational criterion provenance; concurrency/append-only hardening remains follow-up |
| 03 | tool_policy | resolved | Attempt-bound Policy Decision, scoped allowlist, audit seam, zero-side-effect denial |
| 04, 06–09 | coordinator | queued | Start only after blocking tickets pass review |

Existing Core tickets 01, 04, 05 and 06 were reopened to record incomplete acceptance criteria. Their historical test results remain partial evidence, not milestone completion.
