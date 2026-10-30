# Changelog

## Unreleased

### Breaking API changes

- **Review follow-ups for #73.**
  - `POST /attempts/{id}/actions` now reports the mode of the `policy_decided`
    row this delivery wrote, looked up by id (no timestamp ties).
    `ToolExecution.audit_records` now also carries the decision record on
    replay and lost-claim returns.
  - On SQLite, migration `0022` runs its table rebuild in a `SAVEPOINT`, so a
    failed verify leaves the database exactly at 0021.
  - Removed a dead `_evidence_mode` copy from the `ReplayAwareAuditSink`
    Protocol.
- **Tool Proxy audits carry the evidence mode (#71, ADR 0031 §9).**
  - Every `tool_audits` row is stamped with its Attempt's `evidence_mode`
    when it is written, so audits from a live `/run` are `live`. Rows
    written before the Attempt's promotion stay `synthetic` (append-only,
    never rewritten). A row for an unknown Attempt is `synthetic`.
  - `POST /attempts/{id}/actions` and the timeline's tool-audit entries now
    include `evidence_mode`.
  - Migration `0022_tool_audit_mode_check` adds `ck_tool_audits_evidence_mode`
    (`synthetic`, `live`). It refuses to run if a stored row holds another
    value, and never rewrites rows. On SQLite it rebuilds the table and
    verifies that every index (including the partial unique fence) and the
    column default are unchanged. On PostgreSQL it reuses an operator's
    `NOT VALID` constraint.
- **Live Evidence mode (Decision #8 option B, ADR 0031).**
  - An Attempt and its Evidence are `evidence_mode = "live"` once the run
    reached a real sandbox. That means E2B or Daytona via the live `/run`
    adapter, or a started plan-run sandbox.
  - Fake runs stay `synthetic`, and so do sandbox-creation failures. A
    failure after the sandbox existed is live.
  - The Attempt is created with its mode (`ExecutionService.begin(evidence_mode=...)`).
    A live `/run` is promoted `synthetic` → `live` once, committed as soon as
    a sandbox id exists. Plan-run Attempts now match their Evidence, so their
    basis is no longer `mixed`.
  - A missing `E2B_API_KEY` / `DAYTONA_API_KEY` is now a `400` raised before
    any Attempt is written.
  - `POST /tasks/{id}/run` returns `evidence_mode`. The timeline's
    `attempt_created` entry and the `sdf.attempt` span (`sdf.evidence_mode`)
    carry it too.
  - Migration `0021_evidence_mode_check` adds a CHECK (`synthetic`, `live`) on
    `attempts` and `evidence`. It refuses to run if a stored row holds another
    value, and never rewrites rows. On PostgreSQL it reuses constraints an
    operator pre-created `NOT VALID`, validating them.
  - Rows written before this change keep their mode.
  - Routing still identifies live samples by the usage-ledger `sandbox` row,
    not by Evidence mode. `live` is not production impact (ADR 0006).
- **Qualified routing (#58, ADR 0030).** `SDF_RUN_AGENT=auto` with
  `SDF_RUN_ROUTE_AGENTS` picks the agent from trusted measurements, and fails
  with `400` when no agent qualifies. Every live `/run` Attempt now gets an
  append-only `routing_decisions` row (migration `0020_routing_decisions`).
  New read endpoints: `GET /attempts/{id}/routing` and
  `GET /routing/qualifications`.
- **Runtime event contract (#57, ADR 0029).**
  - `runtime_events` gains migration `0019_runtime_event_provenance`. `tool_*`
    kinds are accepted only from `source = 'tool-proxy'`, which emits nothing
    else, and rows are append-only. The migration refuses to run if stored rows
    break the rule.
  - The Tool Proxy now records `tool_requested` before `tool_policy_decided`.
  - `GET /attempts/{id}/runtime-events` and `/tasks/{id}/trace` return events
    in the contract's deterministic order, with `category`,
    `trusted_boundary` and `acceptance_evidence` (always `false`). The new
    `GET /attempts/{id}/event-streams` summarises each stream, including gaps.
  - `RuntimeController.recover(attempt_id)` restores a session binding from
    the durable stream. `start` then asks the runtime whether it still has
    that session. If not, a new session id rebinds the Attempt (the new
    stream starts with `runtime_started` carrying `supersedes_session_id`,
    and the old session is retired); the same id is recorded as a new
    `runtime_started` with `replaces_lost_session: true`.
- **Registered checks (#52, ADR 0025).** `POST /tasks/{id}/run` no longer
  accepts `commands`; `criterion_checks` maps criteria to check ids registered
  in the fixture's `sdf-checks.json` or the server checks file. Unknown request
  keys return `422`. Live `/run` and plan-run checks default to
  `SDF_EVALUATOR_CONTAINMENT=require`, so without a promoted containment
  backend they are `INCONCLUSIVE` until one is promoted or containment is set
  to `off` explicitly.
- **Server-measured telemetry (#50, ADR 0027).**
  - `POST /tasks/{id}/run` rejects `cost_usd`, `provider`, `latency_ms`,
    `input_tokens`, `output_tokens` and `escalation_reason` with `422`; the
    server measures or derives them.
  - Retries (`parent_attempt_id`) must name a finished Attempt of the same Task
    that did not pass every check, at a `model_tier` no lower than the parent's
    (default: the parent's tier); otherwise `409`.
  - `POST /plans/{id}/runs` rejects `price_in_per_m`, `price_out_per_m`,
    `sandbox_usd_per_s` and `evidence_mode` in `config` with `422`. Set prices
    with `SDF_RUN_PRICE_IN_PER_M`, `SDF_RUN_PRICE_OUT_PER_M` and
    `SDF_RUN_SANDBOX_USD_PER_S`; an invalid value returns `503`.
