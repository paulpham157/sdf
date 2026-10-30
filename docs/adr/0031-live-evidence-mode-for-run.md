# ADR 0031: `/run` records `evidence_mode = "live"` when a real sandbox ran

Status: proposed (Decision #8 from the #58 / ADR 0030 review, option B)

## Context

ADR 0027 §4 made the plan-run coordinator derive Evidence mode from its agent
factory (the E2B factory is `live`, everything else `synthetic`), and forbade
callers from setting it. `POST /tasks/{id}/run` (`ExecutionService`) never
followed: every Attempt and Evidence it wrote stayed `synthetic`, even when
`LiveRunAdapter` had run a real agent in an E2B sandbox. ADR 0030 therefore
identified live `/run` Attempts only by their usage-ledger `sandbox` row, and
the scorecard (`impact.py`, `_basis`) reported every `/run` result as
`synthetic`, even ones that had run in a real sandbox.

## Decision

1. **One value set.** `sdf_core.impact.EvidenceMode` (`synthetic`, `live`) is
   the only definition. `EVIDENCE_MODE_VALUES` is derived from it, and the
   coordinator (`EVIDENCE_MODES`), the ORM CHECK constraints and
   `factory_evidence_mode` all use it.
2. **The run path declares its mode; the request never does.**
   `declared_evidence_mode(obj)` is `live` only when `obj.evidence_mode ==
   "live"` exactly, and `synthetic` otherwise (missing, other case, unknown).
   `LiveRunAdapter.evidence_mode = "live"`, `FakeNativeAdapter.evidence_mode =
   "synthetic"`, and the E2B agent factory is already `live`. `/run` and
   `/plans/{id}/run` still reject a caller-supplied `evidence_mode` (ADR 0027).
3. **`/run` writes it on the Attempt and its Evidence.** `ExecutionService.run`
   sets `attempt.evidence_mode` when the Attempt begins, before the adapter
   runs, so a live Attempt that fails is still recorded as a live Attempt.
   Every Evidence row from `finish()` carries the same mode.
4. **The database holds only known modes.** Migration
   `0021_evidence_mode_check` adds `CHECK (evidence_mode IN ('synthetic',
   'live'))` on `attempts` and `evidence` (`ck_attempts_evidence_mode`,
   `ck_evidence_evidence_mode`). The migration's rule is frozen, so a later
   value needs its own migration. If any stored row already breaks the rule,
   the migration refuses and names the count; it never rewrites rows.
   `tool_audits.evidence_mode` is not constrained: `/run` doesn't write it,
   and the SQLite table rebuild would have to carry its partial unique index.
5. **History is not rewritten.** Evidence is append-only (ADR 0002 and the
   triggers), so `/run` Attempts and Evidence written before this change stay
   `synthetic` even if they ran live. The scorecard may undercount live
   history. It never overcounts.
6. **Routing stays ledger-authoritative.** `qualification.py` still counts a
   sample only if the Attempt has a usage-ledger `sandbox` row. It does not
   accept `evidence_mode = "live"` as a substitute, because:
   - older live `/run` rows are `synthetic` and only the ledger identifies them;
   - plan-run Attempts already write live Evidence, but they have no
     per-Attempt ledger sandbox row and no agent-kind attribution, so
     accepting the mode alone would admit samples without a measured cost or
     agent (contrary to ADR 0030 Decision 1).
   A test pins this: live Evidence without a ledger sandbox row is not a
   sample.
7. **Meaning.** `live` means a real agent ran in a real sandbox, against a
   fixture. It is not production impact. ADR 0006 still applies:
   no metric may claim production impact from `/run` Evidence.

## Consequences

- A live `/run` now shows `live` in the trace, the timeline, MCP reads and the
  scorecard basis. A fake run still shows `synthetic`.
- Plan-run Attempts keep `attempts.evidence_mode = "synthetic"` while their
  Evidence is `live` (unchanged; the scorecard basis for those is `mixed`).
  Aligning them is follow-up work.
- Upgrading a database whose `evidence_mode` columns hold an unknown value
  fails loudly at `0021`. An operator has to investigate; the migration does
  not guess.

## Local tests vs. live provider evidence

`tests/test_evidence_mode.py` covers both modes through `ExecutionService`
(fake adapter, and `LiveRunAdapter` with a fake transport), including the
failure path, the routing rule, the CHECK constraint and migration `0021` on
SQLite. `tests/test_postgres_integration.py` covers the constraint and the
migration's refusal on PostgreSQL when `SDF_POSTGRES_TEST_URL` is set.
