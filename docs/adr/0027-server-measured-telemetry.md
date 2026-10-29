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
   and keeps `extra="forbid"` (ADR 0025), so a request that sends any of them,
   or `error_class`, gets `422` and creates no Attempt.
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
   The parent must still belong to the same Task.
4. Internal server callers of `ExecutionService.begin` (the coordinator) still
   pass their own metered values; that is server-side data.

## Consequences

- **Breaking API change:** clients that sent any telemetry field to
  `/tasks/{id}/run` now get `422`. Remove the fields; the response still
  returns them, measured by the server.
- In fake mode the scorecard cost is `0` unless something writes ledger rows.
  That is the truth, not a default the caller picked.
- `latency_ms` covers the adapter run only (not evaluation), the same as the
  live adapter's measurement.
