# SDF runtime work map

Baseline: 88fbfa6. Active wave: 01 acceptance/Evidence, 05 Herdr verification, 10 architecture sync.

Dependency chain: 01 → 02 → 03 → 04; (04, 05) → 06; (01, 02, 06) → 07 → 08 → 09. Ticket 10 is independent.

Coordinator owns tracker status, integration review and commits. Workers own their assigned files; no overlapping edits or worker commits.

| Ticket | Owner | State | Scope |
| --- | --- | --- | --- |
| 01 | acceptance_wave | claimed | Evaluator/execution/API/artifacts and behavioral tests |
| 05 | herdr_verify | claimed | Primary-source provider verification report |
| 10 | architecture_sync | claimed | DOCX, converted Markdown, historical research notice |
| 02–04, 06–09 | coordinator | queued | Start only after blocking tickets pass review |

Existing Core tickets 01, 04, 05 and 06 were reopened to record incomplete acceptance criteria. Their historical test results remain partial evidence, not milestone completion.
