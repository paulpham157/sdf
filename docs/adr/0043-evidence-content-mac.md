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
   are not backfilled and are not re-signed. NULL is not a pass. There is no
   later migration: the payload version lives inside the HMAC, so an old
   value can be recognized without altering stored bytes.
2. **The current MAC is HMAC-SHA256 of canonical JSON, `mac_version` 2.** It
   binds the format-1 fields (evidence id, attempt id, kind, status, command,
   exit code, confidence, criterion, artifact ref, the sha256 of that
   artifact, revision id, result-tree sha256, objective id) and the
   acceptance metadata a same-store writer could otherwise change: evidence
   class, evidence mode, producer identity and role, verifier identity and
   role, subject artifact sha256, oracle digest, environment digest, oracle
   visibility, input seed, and the measured, collected, and expiry
   timestamps. The ORM stamps this on insert and replaces a caller-supplied
   MAC. It does not update an existing row.
3. **Format 1 is the previous payload** (verdict and binding only, no
   `mac_version`). A stored MAC that still matches format 1 fails
   verification with a problem that names the evidence id and says the MAC
   is an old format and was not re-signed. Historical bytes are not trusted
   and are not rewritten.
4. **A missing or malformed MAC fails verification.** That includes NULL, a
   blank, and any value that is not 64 lowercase hex characters. This
   replaces the earlier rule that a NULL MAC on a legacy row was not itself
   a failure. Pre-MAC rows stay readable. They do not verify.
5. **The key is `SDF_EVIDENCE_MAC_KEY`.** When the variable is unset or
   empty, insert and verify use one fixed development key so hermetic tests
   agree. That key is the public constant `_DEV_KEY` in `sdf_core/evidence_mac.py`.
   It is not in the database. A match under it is not authenticity against
   an attacker who knows the constant: they can recompute the HMAC after any
   rewrite. `verify_attempt_revision` reports `mac_key` `development` or
   `configured`. Production deployments set `SDF_EVIDENCE_MAC_KEY`. With a
   configured key, a MAC produced under the development key does not verify.
6. **`verify_attempt_revision` recomputes the current MAC** from the fields
   and the artifact sha256 as they stand now. A mismatch, a missing MAC, or
   an old-format MAC is `failed`, with a problem that names the evidence id.
   File-digest and revision-binding checks are unchanged. The same check
   applies when a stored row is accepted (`is_verified_pass`) and when the
   scorecard loader copies Evidence into facts (`content_authentic`).
7. **The MAC does not establish that the verifier is an independent party.**
   It binds the `verifier_identity` and `verifier_role` strings that were
   stored. A holder of the key can stamp any string. Acceptance still
   refuses a verifier that is missing, has role `agent`, or equals the
   Attempt's agent. That comparison is policy over the stored string, not
   proof of who signed.

## Consequences

- A rewrite of the verdict, the bound artifact sha256, the row id, the
  Attempt or revision binding, the class or mode, the oracle fields, or the
  expiry fails verification unless the MAC is recomputed with the process key.
- Clearing `content_mac` fails verification. It is not a legacy pass.
- The append-only rules are unchanged. On PostgreSQL the trigger still
  rejects the `UPDATE`. The MAC is the check for engines and paths that
  allow the write, and for a raw `INSERT` that omits the MAC.
- No migration past `0029_evidence_content_mac` is required for this payload.
  Downgrade of 0029 still drops `content_mac`. Existing verdicts stay; their
  MACs are gone. Format-1 rows already stored are left as they are.
- Rotating `SDF_EVIDENCE_MAC_KEY` makes previously stamped rows fail
  verification. Rotation is a deliberate re-stamp, not a silent change.
  This code does not re-stamp.
- `created_at` is not in the MAC. Qualification's recorded-sample check
  (`status == PASS` on non-execution, non-outcome rows) does not consult the
  MAC; it is the historical sample, not an authenticity pass.
- Timeline and API reads show the stored columns. Authenticity is the
  verification report, not the column values alone.
