# ADR 0024: vendor-neutral OpenTelemetry tracing

Status: proposed (branch `obs/otel-tracing`)

## Context

ADR 0023 gave every run redacted JSON logs, an error taxonomy, a usage ledger
and a timeline. What it lacks is a causal view across processes and threads:
which HTTP request started which attempt, which sandbox a slow turn ran in,
where the time went. We want that without tying SDF to one vendor (LangSmith,
Langfuse, Grafana Tempo, Jaeger, Honeycomb...).

## Decision

1. **OpenTelemetry, OTLP only.** SDF Core emits spans with the OpenTelemetry
   SDK and exports them over OTLP/HTTP (or to the console for development).
   Any backend that accepts OTLP works through the standard `OTEL_*`
   variables; SDF has no vendor-specific code. The SDK packages are an
   optional extra: `pip install 'sdf[otel]'`.
2. **Off by default, zero overhead.** Nothing happens unless
   `SDF_OTEL_ENABLED=1`. Disabled, the SDK is never imported, no provider or
   exporter is built, and `tracing.span()` returns a shared no-op context.
3. **What is traced.**
   - FastAPI server spans (`/healthz` excluded, no headers captured).
   - `sdf.task.run` (POST /tasks/{id}/run) → `sdf.attempt` → `sdf.agent.run`
     → `sdf.sandbox.create`, `sdf.agent.start`, `sdf.agent.turn`,
     `sdf.sandbox.cleanup`; `sdf.evaluate` for the evaluator.
   - Coordinator runs: `sdf.run` → `sdf.agent.start` (→ `sdf.sandbox.create`),
     `sdf.agent.turn`, `sdf.verify` (→ `sdf.evaluate`), `sdf.sandbox.cleanup`.
   - Optional DB spans (`SDF_OTEL_DB=1`): parameterised statements only, no
     bound values, no SQL commenter. Off by default because coordinator
     polling is noisy.
   - Background runs (`?background=true`) and worker threads keep the
     request's trace (`tracing.in_current_context`).
4. **Attributes are an allow-list.** Only `sdf.*` keys from
   `tracing.ATTRIBUTE_KEYS` are set: task/run/plan/attempt ids, agent
   kind/id/role, model, sandbox id/provider/template, turn, status/outcome,
   seconds, ADR-0023 `error_class`, tokens, cost, usage source. Strings pass
   through `observability.redact` and are truncated. **Secrets, API keys,
   prompts, instructions, agent output and code content are never span
   attributes.** Exceptions are recorded as the class name only (no message,
   no stack, no exception event). A redacting exporter wrapper re-redacts
   every attribute and event, including ones added by third-party
   instrumentation (for example a query string in `http.target`).
5. **Correlation.** Inside a span, every log line carries `trace_id` and
   `span_id`. `attempts.trace_id` and `agent_runs.trace_id` (migration
   `0017_trace_id`) store the trace, `/tasks/{id}/run` returns it, and
   `runs show` / the timeline print it.
6. **Nothing inside sandboxes.** Tracing runs only in SDF Core on the
   master. No collector, exporter, env var or endpoint is sent to a sandbox;
   sandbox egress stays Vultr-only (ADR 0023).

## Consequences

- Operators point SDF at their backend with env vars only; switching vendor
  is a config change.
- Spans for a background run outlive the HTTP span that started it; that is
  expected (same trace, later end time).
- Agent internals (individual LLM calls inside opencode, pi...) are not
  traced: they run in the sandbox, and only turn-level timing, tokens and
  cost are visible.
