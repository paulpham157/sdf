# ADR 0038: Go Attempt evidence mode is stamped onto the local Tool Proxy fence

Status: proposed (2026-10-31). Records how the Go orchestrator learns
ADR 0031's evidence mode for Attempts it starts, and copies that mode
onto the local Tool Proxy fence from
[ADR 0037](0037-go-tool-proxy-fence-audit-parity.md). It becomes
accepted when a maintainer accepts the implementing pull request. It
does not make the Go process a writer of PostgreSQL `attempts` or
`tool_audits`, and it does not change Credential Mode, cancellation, or
Evidence independence (ADR 0002, ADR 0007, ADR 0026, ADR 0031).

## Context

ADR 0031 defines one evidence-mode value set, `synthetic` and `live`.
An Attempt is created with the mode that is true at creation. `live`
means a real sandbox existed. The promotion is one-way
(`synthetic` → `live`), committed at once, and never rewritten onto
rows already stored. Tool Proxy audits copy the Attempt's stored mode
at the moment the row is written. A row for an unknown Attempt is
`synthetic`. The HTTP request does not set the mode (ADR 0027).

The Python path implements that rule on `attempts` and `tool_audits`.
The Go Tool Proxy fence (ADR 0037) is a different SQLite file. ADR 0037
left `evidence_mode` off that file because the Go Attempt did not carry
a mode: writing `synthetic` on every row would mislabel a live Attempt.
It also deferred a shared PostgreSQL fence until one writer is chosen.

The Go orchestrator is the process that dispatches the Attempt and is
the process that can see a sandbox id. It does not need to read or
write the Python `attempts` row to apply ADR 0031 to its own dispatch.

## Decision

1. **Same value set, same read and write rules.** Only `synthetic` and
   `live` are stored. On read, only an exact `live` is live; anything
   else, including a missing Attempt, is `synthetic`. On write, an empty
   mode is stored as `synthetic`. Any other unknown value is refused.
   The diagnostic names ADR 0031.

2. **The Go Attempt record is local.** `Start` records the Attempt as
   `synthetic` in the Tool Proxy `OutcomeStore` (the in-process
   `MemoryStore`, or the SQLite file selected by `-outcome-db`). A
   later `Start` does not demote a `live` Attempt. A `Start` that never
   enters the queue drops a `synthetic` record it just created, so a
   failed enqueue cannot be promoted. This record is not PostgreSQL
   `attempts.evidence_mode`. Go remains not a second writer of Attempt
   state in the Decision Graph (ADR 0035).

3. **`ConfirmLive(attemptID, sandboxID)` is the only promotion.** It
   promotes when `sandboxID` is non-empty after trimming and the Attempt
   was recorded by this process. An unknown Attempt is not inserted. The
   call is one-way and idempotent. On SQLite the update commits before
   the call returns. The Attempt's credential-scope `SandboxID` is not a
   sandbox and does not promote.

4. **Observations copy the mode at insert.** `policy_decided`,
   `action_claimed`, `action_executed`, and `action_failed` each store
   the Attempt's mode as of that insert. Rows are not updated when the
   Attempt is promoted later. `Result.EvidenceMode` is the mode of the
   `policy_decided` row this delivery wrote, including a replay or a
   denial (ADR 0031 §9). A Proxy with no mode source stamps `synthetic`,
   because it has no Attempt record to read. The orchestrator always
   installs itself as that source.

5. **The request still cannot set the mode.** `POST /v1/attempts`
   rejects a body field `evidence_mode` with HTTP 422 and does not start
   the Attempt. The accept response reports the stored mode.

6. **History is not rewritten.** A fence file from before this column,
   and rows copied out of the #107 `tool_outcomes` table, stay
   `synthetic`. Adding the column does not classify them as `live`.

7. **PostgreSQL `tool_audits` stays a separate decision.** Sharing that
   table still needs one writer and a rule for the executor value
   (ADR 0037). This ADR does not take that step.

## Consequences

- Operators do not configure a new path. The Attempt mode uses the same
  `-outcome-db` file as the fence, or memory when that flag is empty.
  There is no TTL. A restarted process that opens the same file still
  sees `live` for an Attempt that was promoted, and does not demote it
  if the idempotency key is presented again. Queue idempotency keys
  themselves remain in memory (ADR 0035); this ADR does not make them
  durable.
- The placeholder runner in `sdf-orch` never reports a sandbox id, so
  Attempts it starts stay `synthetic`.
- Two processes on one fence file are still unsupported (ADR 0037).
- Downgrade that expects no `evidence_mode` column must drop the file
  or ignore the added column. The file is not migrated back by this
  process.

## Alternatives considered

1. **Hardcode `synthetic` on every Go observation.** Rejected. ADR 0037
   already records that this mislabels a live Attempt.
2. **Read and write Python `attempts.evidence_mode` from Go.** Rejected.
   That makes Go a second writer of Attempt state before the ADR 0035
   cutover, and it couples the fence to `SDF_DATABASE_URL`.
3. **Treat `Attempt.SandboxID` as proof a sandbox exists.** Rejected.
   That field is the credential-scope id and defaults to the Attempt id
   when omitted. Using it as `live` would mark every start live.
4. **Shared PostgreSQL `tool_audits` in this change.** Rejected. Still
   the larger decision ADR 0037 deferred.

## Verification

Local tests (no provider credentials):

- `TestEvidenceMode_PromotionAndAuditStamp` — scope id does not promote;
  observations before `ConfirmLive` stay `synthetic`; a replay after
  promotion appends a `live` `policy_decided` and leaves the terminal
  row `synthetic`; `Result.EvidenceMode` is the decision row; a later
  action is `live`; an unknown `ConfirmLive` inserts nothing; duplicate
  `Start` does not demote.
- `TestEvidenceMode_FailureBeforeAndAfterSandbox` — a runner error
  before a sandbox id stays `synthetic`; an error after `ConfirmLive`
  stays `live`.
- `TestEvidenceMode_StampAtWriteDuringExecute` — a promotion inside the
  executor leaves the decision and claim `synthetic` and the terminal
  row `live`, and the result still reports the decision row.
- `TestEvidenceMode_SQLiteSurvivesRestart` — the same stamp rules after
  reopening `-outcome-db`.
- `TestStartEnqueueFailureDropsSyntheticAttempt` — a start that does not
  enter the queue leaves no Attempt record to promote.
- `TestAttemptStartReportsSyntheticAndRejectsCallerMode` — HTTP 422
  names ADR 0031 and does not run the Attempt; a clean accept reports
  `synthetic`.
- `TestEvidenceMode_StoreRulesAndLegacyColumn` — empty write is
  `synthetic`, an unknown write is refused and stores nothing, promote
  does not insert or demote, a pre-column fence gains the column
  without rewriting existing rows, and a value outside the set fails
  the CHECK constraint.
