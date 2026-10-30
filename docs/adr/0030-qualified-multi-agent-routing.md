# ADR 0030: multi-agent routing qualified from trusted execution evidence

Status: proposed (issue #58, P4 in `docs/roadmap.md`; builds on ADR 0029 / #57)

## Context

`sdf_core.routing.ModelRouter` could already score profiles from
`RoutingMeasurement`s, but nothing produced those measurements and nothing
used the router. `POST /tasks/{id}/run` ran whichever agent `SDF_RUN_AGENT`
named. P4 asks for routing across agents by demonstrated capability,
historical success, latency, cost, availability and load. Every input has to
be a provenance-bearing measurement, never caller-asserted Attempt telemetry
(#50, ADR 0027), and every agent has to stay behind the same runtime, policy and
sandbox boundaries. No ACP/A2A work is involved (ADR 0004).

Two facts shaped the design. `/run` Attempts carry `agent = "native"`, so the
agent kind that ran is recorded only on the usage ledger. Evidence written by
`/run` keeps `evidence_mode = "synthetic"` even in live mode, so a live
Attempt is identified by its ledger sandbox row, not by Evidence mode.

## Decision

1. **Measured inputs** (`sdf_core/qualification.py`) come only from rows the
   server writes. A sample is a finished Attempt that has a `usage_ledger`
   `sandbox` row (only the live adapter writes one), taking the newest 20 per
   agent:
   - *success*: the Attempt has Evidence and all of it is `PASS`;
   - *cost*: the mean ledger cost of samples whose turn and sandbox rows all
     have a measured cost (`cost_source` is not `missing`). A sample with any
     `missing` row is left out of the cost mean only; it still counts as a
     sample for success. The provenance lists the samples used
     (`cost_attempt_ids`);
   - *latency*: the mean server-measured sandbox `seconds` (a sample without
     a finite, non-negative value is left out the same way);
   - *availability*: the newest sample has a `runtime_started` event from a
     trusted `run-<provider>` stream (ADR 0029) and its server-set
     `error_class` is not an infrastructure class (`sandbox_error`,
     `provider_4xx/429/5xx`, `cleanup_failed`, `config_error`);
   - *load*: in-progress Attempts attributed to the agent (by a routing
     decision or a ledger row) divided by `SDF_RUN_MAX_CONCURRENT`;
   - *capability*: `verified-fixture-loop` after one accepted live Attempt,
     plus `tool:<tool>.<action>` for each trusted `tool_action_executed` event
     in an accepted Attempt.

   Ignored: `attempts.cost_usd/latency_ms/provider/tokens`, outcome
   observations, agent Claims, runtime output text and configured profiles.
2. **Fail safe.** An agent with no finished live sample, or with **no sample
   at all** that has a measured cost (or latency), is not qualified, and the
   reason is recorded (`unmeasured cost`). One unmeasured sample does not
   disqualify an agent that has other measured samples. There is no
   fallback to configured values. Routing is refused entirely until **at least
   one agent has an accepted live Attempt** (the verified fixture loop).
3. **Deterministic selection.** `ModelRouter.route_measured` keeps the existing
   score. Ties are broken by more samples, then lower cost, then lower
   latency, then profile name (reverse lexical, as before). An unavailable or
   out-of-limit agent is rejected with a reason and the next eligible agent is
   chosen. If none is eligible, there is no route.
4. **Wiring.** `SDF_RUN_AGENT=auto` with `SDF_RUN_ROUTE_AGENTS=pi,opencode,…`
   makes `/run` pick the kind from these measurements. The limits are server
   environment only: `SDF_RUN_ROUTE_REQUIRE` (default
   `verified-fixture-loop`), `SDF_RUN_ROUTE_MAX_COST_USD` (10),
   `SDF_RUN_ROUTE_MAX_LATENCY_MS` (3 600 000) and `SDF_RUN_ROUTE_MIN_SUCCESS`
   (0). No route is a configuration error (`400`) before any sandbox exists.
   Routing chooses only the agent kind; the `LiveRunAdapter`, credential
   resolution, egress lock, turn guard, Tool Proxy and evaluator are the
   same as for an explicit `SDF_RUN_AGENT`. The run request has no routing
   or telemetry fields (`extra="forbid"`).
5. **Recorded and correlated.** Every live Attempt gets one append-only
   `routing_decisions` row (migration 0020, an ORM guard and a PostgreSQL
   trigger), written before the agent starts. It records the `mode`
   (`explicit` or `measured`), reason, request limits and each candidate's
   measurement or rejection, with the ids of the Evidence, ledger and
   Attempt rows behind it. `GET /attempts/{id}/routing` adds the Attempt's
   runtime sessions and its outcome, read at request time from Evidence, the
   error class and the ledger. `GET /routing/qualifications` shows the
   current inputs.

## Local tests vs. live provider evidence

`tests/test_qualified_routing.py` proves the selection rules, fail-safe
behaviour and wiring against SQLite rows written to look like live runs. That
is a **local contract test**, not live evidence. A live qualification exists
only when real `/run` Attempts (E2B or Daytona with a real model provider)
have written the ledger, runtime events and Evidence that the measurements
read. Until then `GET /routing/qualifications` reports `verified_loop_completed:
false` and `SDF_RUN_AGENT=auto` refuses to run.

## Consequences

- An agent is never chosen by routing until it has been run explicitly
  (`SDF_RUN_AGENT=<kind>`) and has produced live samples. There is no
  automatic exploration of unmeasured agents.
- Plan runs (`/plans/{id}/runs`) still assign agent kinds per subtask and are
  not measured: the coordinator writes no per-Attempt ledger rows.
- Evidence from containment-`require` runs without a promoted backend is
  `INCONCLUSIVE`, so those samples count as unsuccessful. That is the truthful
  reading.
