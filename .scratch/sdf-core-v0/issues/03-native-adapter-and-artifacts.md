# Native adapter and immutable artifacts

Type: task
Status: resolved
Blocked by: 01, 02

Implement a native adapter seam plus fake adapter for tests. Run one fixture workspace per Attempt, capture diff/stdout/stderr as immutable Artifacts with SHA-256 metadata, and never allow the agent to write Evidence directly.

Acceptance: a fake Attempt produces a reproducible diff Artifact and duplicate dispatch does not create duplicate Attempts.

## Answer

Implemented the native adapter seam, deterministic fake adapter, isolated per-attempt workspace manager, immutable diff/process artifacts and SHA-256 metadata. Duplicate artifact IDs are rejected.
