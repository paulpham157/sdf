# Evidence rows carry a content MAC

Status: proposed

Extends append-only Evidence ([ADR 0002](0002-evidence-closes-the-decision-loop.md)).
Does not replace the PostgreSQL append-only trigger or the ORM session guard.
Verification of artifact file bytes stays as [ADR 0032](0032-attempt-revisions.md)
defined it.

## Context

Evidence cannot be updated through the ORM, and PostgreSQL rejects `UPDATE`
and `DELETE` on `evidence`. Two holes remain:

1. SQLite has no append-only trigger. A SQL `UPDATE` that does not go through
   the session changes `status`, `command`, and `confidence`.
2. `verify_attempt_revision` checks that each artifact file matches the sha256
   stored on that same artifact row. A writer who replaces the file and
   updates `artifacts.sha256` to the new digest still gets `verified`. The
   graph node title and the decision-graph edge are copies from insert time
   and are not consulted.

Append-only storage does not detect a same-store rewrite. The verdict needs a
commitment that the rewrite in those columns does not update.

## Decision

1. **One MAC per Evidence row**, column `evidence.content_mac` (migration
   `0029_evidence_content_mac`). Nullable. Rows written before this migration
   are not backfilled. NULL means the row has no content commitment; it is
   not a pass for a row this process inserts.
2. **The MAC is an HMAC-SHA256** of a canonical JSON object: evidence id,
   attempt id, kind, status, command, exit code, confidence, criterion,
   artifact ref, the sha256 of that artifact at insert, revision id,
   result-tree sha256, and objective id (ADR 0042). The ORM stamps it on
   insert. It is not a column the
   application updates later.
3. **The key is `SDF_EVIDENCE_MAC_KEY`.** When the variable is unset, insert
   and verify use one fixed development key so hermetic tests agree. That key
   is not stored in the database. Production deployments set
   `SDF_EVIDENCE_MAC_KEY`. A writer who can change the row and
   `artifacts.sha256` but does not have the key cannot produce a matching MAC.
4. **`verify_attempt_revision` recomputes the MAC** from the fields and the
   artifact sha256 as they stand now. A mismatch is `failed`, with a problem
   that names the evidence id. File-digest and revision-binding checks are
   unchanged. A NULL MAC on a legacy row is not itself a failure.

## Consequences

- A rewrite of the verdict, or of the artifact sha256 the verdict is bound
  to, fails verification unless the MAC is recomputed with the server key.
- The append-only rules are unchanged. On PostgreSQL the trigger still
  rejects the `UPDATE`. The MAC is the check for engines and paths that
  allow the write.
- Downgrade drops `content_mac`. Existing verdicts stay; their MACs are gone.
- Rotating `SDF_EVIDENCE_MAC_KEY` makes previously stamped rows fail
  verification. Rotation is a deliberate re-stamp, not a silent change.
