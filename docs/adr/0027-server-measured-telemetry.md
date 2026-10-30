# ADR 0027: Attempt telemetry is server-measured

Status: proposed (branch `sec/50-server-telemetry`, issue #50; stacked on ADR 0025 / #52)

## Context

`POST /tasks/{id}/run` accepted `cost_usd`, `provider`, `latency_ms`,
`input_tokens`, `output_tokens` and `escalation_reason` in the request body and
wrote them to the Attempt. Only the live run adapter overwrote them with
metered values afterwards, so in fake mode (and for any adapter that does not
meter itself) the caller set the numbers that `/scorecard`
(`cost_per_accepted_task`) and `/impact-scorecard` report. A single request
could make an expensive run look free, or a free run look expensive.

## Decision

1. **The fields are gone from the API.** `RunTaskRequest` no longer has them
   and keeps `extra="forbid"` (ADR 0025), so a request that sends any of them
   gets `422` and creates no Attempt. (`error_class` was never a request
   field; it is rejected the same way as any unknown key.)
2. **`ExecutionService.run` measures them in every mode.** It no longer
   accepts the telemetry arguments. Around `adapter.run` it measures wall-clock
   latency. A metering adapter (the live run adapter) has already written the
   values from the usage ledger (ADR 0023), and those are kept. Otherwise:
   - `provider` is the adapter's identity (`"fake"` for `FakeNativeAdapter`);
   - `latency_ms` is the measured wall clock;
   - `cost_usd`, `input_tokens` and `output_tokens` are summed from the Attempt's
     `usage_ledger` rows (0 / NULL when there are none);
   - `error_class` comes from `classify_error` when the adapter raises.
3. **`escalation_reason` is derived.** When `parent_attempt_id` is given and no
   server component supplied a reason, `ExecutionService.begin` writes
   `escalated from <parent> (tier <tier>, <status>[/<outcome>][, error <class>])`.
4. Internal server callers of `ExecutionService.begin` (the coordinator) still
   pass their own metered values; that is server-side data.
5. **Plan-run prices and Evidence mode are server-set.** `/plans/{id}/runs`
   rejects `price_in_per_m`, `price_out_per_m`, `sandbox_usd_per_s` and
   `evidence_mode` in `config` with `422` (`RunConfig.from_request`), so a
   caller can no longer price a run at 0 and defeat `max_cost_usd`. Prices come
   from `SDF_RUN_PRICE_IN_PER_M`, `SDF_RUN_PRICE_OUT_PER_M` and
   `SDF_RUN_SANDBOX_USD_PER_S` (defaults otherwise); an invalid value
   (negative, NaN, infinite, non-numeric or above the sanity bound) is a server
   misconfiguration and returns `503` with no run, never a free run. Evidence
   mode is `live` only when the agent factory is the E2B factory, else
   `synthetic`. `RunConfig.from_dict` validates stored configs the same way.
   (Since ADR 0031, `/run` follows the same rule, and Attempts are `live` only
   once a real sandbox existed.)
   Config errors are checked before the coordinator is built, so `422` still
   precedes `503` for a missing live configuration.
6. **Retries are constrained.** With `parent_attempt_id` the parent must belong
   to the same Task, be in a terminal state (`only a finished attempt can be
   retried`) and must not be a completed Attempt whose Evidence all passed
   (`nothing to retry`). `model_tier` defaults to the parent's tier on a retry
   (and to `basic` otherwise) and may not be lower than the parent's tier; it is
   the caller's declared routing tier, not a measured value. Violations return
   `409`.
7. **Observations name their recorder.** `POST /objectives/{id}/observations`
   is caller-reported data by design; the server logs `observation_recorded`
   with the observation id, objective, authenticated principal and mode.
   Persisting the principal on the row is out of scope (it needs a migration
   and is tracked separately), so the audit trail is the log.

## Consequences

- **Breaking API change:** clients that sent any telemetry field to
  `/tasks/{id}/run` now get `422`. Remove the fields; the response still
  returns them, measured by the server.
- In fake mode the scorecard cost is `0` unless something writes ledger rows.
  That is the truth, not a default the caller picked.
- **Breaking API change:** plan-run configs that set prices or
  `evidence_mode` now get `422`; operators set prices through the env vars
  above. Retries of running or fully passed Attempts, and retries at a lower
  tier, now get `409`.
- Observations remain caller-reported values; only the recorder is audited.
- `latency_ms` covers the adapter run only (not evaluation), the same as the
  live adapter's measurement.
