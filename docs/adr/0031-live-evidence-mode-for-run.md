# ADR 0031: an Attempt is `live` once its run reached a real sandbox

Status: proposed (Decision #8 from the #58 / ADR 0030 review, option B)

## Context

ADR 0027 §4 made the plan-run coordinator derive the Evidence mode from its
agent factory: the E2B factory is `live`, everything else is `synthetic`. It
also forbade callers from setting the mode. `POST /tasks/{id}/run`
(`ExecutionService`) never followed this rule. Every Attempt and Evidence row
it wrote stayed `synthetic`, even when `LiveRunAdapter` had run a real agent in
a real sandbox. Plan-run Attempts also stayed `synthetic` while their Evidence
was `live`, so the scorecard basis (`impact.py`, `_basis`) for them was
`mixed`. ADR 0030 therefore identified live Attempts only by their
usage-ledger `sandbox` row.

## Decision

1. **One value set.** `sdf_core.impact.EvidenceMode` (`synthetic`, `live`)
   is the only definition. `EVIDENCE_MODE_VALUES` is derived from it. The
   coordinator (`EVIDENCE_MODES`), `ExecutionService.begin`,
   `factory_evidence_mode` and the ORM CHECK constraints all use it.

2. **The run path declares its mode; the request never does.**
   - `declared_evidence_mode(obj)` is `live` only when `obj.evidence_mode ==
     "live"` exactly. Anything else is `synthetic`.
   - `LiveRunAdapter` (the shared live adapter for **E2B and Daytona**) and the
     E2B agent factory declare `live`; `FakeNativeAdapter` declares
     `synthetic`.
   - `/run` and `/plans/{id}/run` still reject a caller-supplied
     `evidence_mode` with `422` (ADR 0027).

3. **The rule: `live` means the run reached a real sandbox.** An Attempt is
   `live` only when a real sandbox (E2B or Daytona) existed for it. A failure
   after that point (the agent crashed, the provider returned 4xx, a
   timeout) is still a **live** failure, so live failure rates stay honest.
   Failures before a sandbox exists are never `live`:
   - **Config errors** fail before any Attempt row exists.
     `configured_run_adapter()` checks the provider credentials
     (`E2B_API_KEY` / `DAYTONA_API_KEY`) along with the rest of the
     configuration, so `/run` returns `400` and writes no Attempt.
   - **Sandbox-creation failures** leave the Attempt `synthetic`. This covers
     a transport that cannot be created, and a lazily created sandbox that
     fails on first use.

4. **Each Attempt is created with the mode that is true at creation.**
   `ExecutionService.begin(evidence_mode=...)` writes the row with its mode;
   nothing rewrites it afterwards except the one promotion below.
   - A fake `/run` is created `synthetic`.
   - A live `/run` is created `synthetic` (`live_after_sandbox = True`,
     `initial_evidence_mode`). The adapter promotes it to `live` as soon as the
     transport reports a sandbox id (`LiveRunAdapter.confirm_live`, called
     after `start`, after each turn, and before teardown).
   - Plan runs create their first Attempts before any sandbox exists, so they
     start `synthetic`. Each one takes the factory's mode when its worker's
     sandbox has started. Retries are created after that, in an
     already-started sandbox, so they start with the factory's mode.
   - The promotion is one-way (`synthetic` → `live`, never back) and
     **committed at once**. If the process dies mid-run, the RUNNING Attempt
     is `live` exactly when a sandbox existed.
   - Evidence takes the Attempt's stored mode when `finish()` writes it, so an
     Attempt and its Evidence always agree. Plan-run bases are now `live`
     (or `synthetic`), no longer `mixed`.
   - An idempotent replay (same `dispatch_key`) returns the stored Attempt
     without touching its mode.

5. **The database holds only known modes.**
   - Migration `0021_evidence_mode_check` adds `CHECK (evidence_mode IN
     ('synthetic', 'live'))` on `attempts` and `evidence`
     (`ck_attempts_evidence_mode`, `ck_evidence_evidence_mode`). The
     migration's copy of the rule is frozen.
   - If any stored row already breaks the rule, the migration refuses and
     names the count. It never rewrites rows.
   - On PostgreSQL, `ADD CONSTRAINT` takes `ACCESS EXCLUSIVE` and scans the
     table. Splitting it into `NOT VALID` + `VALIDATE` *inside* the migration
     would not shorten the lock, because Alembic holds it in one transaction
     until commit. An autocommit block would let the constraint and the
     version stamp diverge on a crash. Instead, for a large table an operator
     can add the constraint `NOT VALID` and `VALIDATE` it beforehand. The
     migration then adds only the missing constraints and validates any that
     are still `NOT VALID`.
   - `tool_audits.evidence_mode` is not constrained. Nothing writes it, and
     the SQLite table rebuild would have to carry its partial unique index
     (follow-up: #71).

6. **History is not rewritten.** Evidence is append-only (ADR 0002 and the
   triggers). `/run` and plan-run rows written before this change keep the
   mode they were written with. The scorecard may undercount live history.
   It never overcounts.

7. **Routing stays ledger-authoritative.** `qualification.py` still counts a
   sample only if the Attempt has a usage-ledger `sandbox` row. It does not
   accept `evidence_mode = "live"` as a substitute, because:
   - older live rows are `synthetic`, and only the ledger identifies them;
   - plan-run Attempts are now `live` but have no per-Attempt ledger sandbox
     row or agent-kind attribution, so accepting the mode alone would admit
     samples without a measured cost or agent (contrary to ADR 0030
     Decision 1).

   A test pins this: live Evidence without a ledger sandbox row is not a
   sample.

8. **Meaning.** `live` means a real agent was dispatched to a real sandbox and
   worked on a fixture. It is not production impact; ADR 0006 still applies.

## Consequences

The mode is visible in four places:
- the `POST /tasks/{id}/run` response (`evidence_mode`);
- the timeline's `attempt_created` entry and its Evidence entries;
- the `sdf.attempt` span (`sdf.evidence_mode`);
- the scorecard basis.

Other consequences:
- A misconfigured live server (a missing key) now returns `400` with no
  Attempt row. Before, it also persisted a failed Attempt.
- Upgrading a database whose `evidence_mode` columns hold an unknown value
  fails loudly at `0021`. An operator has to investigate; the migration does
  not guess.

## Local tests vs. live provider evidence

Local tests:
- `tests/test_evidence_mode.py` covers:
  - fake, E2B and Daytona runs (fake transports);
  - the commit-at-sandbox rule, seen from a second session mid-run;
  - config errors, both directly and through the API (`400`, no Attempt);
  - both sandbox-creation failure shapes, and a failure after the sandbox
    existed;
  - idempotent replay;
  - the API response, the timeline, and the `422` on an existing Task;
  - the routing rule;
  - the CHECK constraint, and migration `0021` on SQLite (bad `evidence` and
    bad `attempts` rows).
- `tests/test_agent_comm.py` covers plan-run Attempts and retries matching
  their Evidence, and a worker whose sandbox never started staying
  `synthetic`.
- `tests/test_postgres_integration.py` covers the constraint, the refusal for
  each table, and the operator `NOT VALID` path on PostgreSQL when
  `SDF_POSTGRES_TEST_URL` is set.

Live checks:
- `tests/test_live_full_loop_http.py` asserts that a live `/run` produces a
  live Attempt and live Evidence.
