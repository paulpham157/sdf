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
3. **The key is `SDF_EVIDENCE_MAC_KEY`, at least 32 characters.** It is not
   stored in the database and it is not logged. If it is unset or shorter,
   the process does not start, an Evidence insert is refused, and
   verification does not use a fallback key. `SDF_EVIDENCE_MAC_ALLOW_INSECURE_DEV_KEY=1`
   with no key set is the only opt-in for the public development constant,
   and a match under that constant is not `verified`. The MAC binds the
   stored verifier string. It does not prove the verifier is independent.
4. **`verify_attempt_revision` recomputes the MAC** from the fields and the
   artifact sha256 as they stand now. A mismatch is `failed`, with a problem
   that names the evidence id. File-digest and revision-binding checks are
   unchanged. A NULL MAC on a legacy row is not itself a failure.


## MAC versions

Migration `0031_evidence_mac_v2` adds `evidence.content_mac_version`. A NULL
MAC is accepted only on a genuinely legacy row:

- **NULL**: the row was written before migration 0029, or a writer cleared
  both the MAC and the version. It is not authentic. Verification fails and
  the row is not re-signed.
- **1**: the row was written after 0029 and before 0031, or was backfilled
  because it already had a `content_mac`. The v1 input does not bind
  acceptance columns, so verification fails and the row is not re-signed.
- **2**: the current write path, and the only version that can verify. The
  v2 input is the v1 input plus `mac_version` 2 and these acceptance
  columns: `verifier_identity`, `verifier_role`, `expires_at`,
  `oracle_visibility`, `subject_artifact_sha256`, `oracle_digest`,
  `evidence_class`, `evidence_mode`, `producer_identity`,
  `producer_role`, `environment_digest`, `collected_at`, and `input_seed`.

`created_at` and `measured_at` are not in the MAC. They are not an
accept/reject input. Routing qualification and retry refusal require a
current MAC on the evaluator rows they treat as PASS. They do not apply the
independence or expiry policy. The scorecard loader sets `content_authentic`
from that MAC. Timeline and API reads show stored columns and do not accept
an artifact.

## Consequences

- A rewrite of the verdict, or of the artifact sha256 the verdict is bound
  to, fails verification unless the MAC is recomputed with the server key.
- The append-only rules are unchanged. On PostgreSQL the trigger still
  rejects the `UPDATE`. The MAC is the check for engines and paths that
  allow the write.
- Downgrade of 0029 drops `content_mac`. Existing verdicts stay; their MACs are gone.
- Downgrade of 0031 drops `content_mac_version`. The MACs remain. A later
  upgrade classifies a remaining MAC as v1 again.
- A rewrite of an acceptance column on a v2 row fails verification unless
  the MAC is recomputed with the server key. A stored v1 row is still
  verified with the v1 input, so those columns remain unsigned on v1 rows
  until the row is rewritten through a process that stamps v2.
- Rotating `SDF_EVIDENCE_MAC_KEY` makes previously stamped rows fail
  verification. Rotation is a deliberate re-stamp, not a silent change.
