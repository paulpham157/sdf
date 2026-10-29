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
   optional extra: `pip install 'sdf-core[otel]'`.
2. **Off by default, zero overhead.** Nothing happens unless
   `SDF_OTEL_ENABLED=1`. Disabled, the SDK is never imported, no provider or
   exporter is built, and `tracing.span()` returns a shared no-op context.
3. **What is traced.**
   - FastAPI server spans (`/healthz` excluded; no headers captured even if
     `OTEL_INSTRUMENTATION_HTTP_CAPTURE_HEADERS_*` is set; no per-message
     `http send`/`http receive` spans). The lifespan instruments the app and
     the DB engines and uninstruments them on exit.
   - `sdf.task.run` (POST /tasks/{id}/run) → `sdf.attempt` → `sdf.agent.run`
     → `sdf.sandbox.create`, `sdf.agent.start`, `sdf.agent.turn`,
     `sdf.sandbox.cleanup`; `sdf.evaluate` for the evaluator.
   - Coordinator runs: `sdf.run` → `sdf.agent.start` (→ `sdf.sandbox.create`),
     `sdf.agent.turn`, `sdf.verify` (→ `sdf.evaluate`), `sdf.sandbox.cleanup`.
   - Optional DB spans (`SDF_OTEL_DB=1`): parameterised statements only, no
     bound values, no SQL commenter. Off by default because coordinator
     polling is noisy.
   - Background coordinator runs (`POST /plans/{id}/runs` with
     `wait=false`) outlive their request, so they get their own trace: a root
     `sdf.run.background` span (parent of `sdf.run`) with a span link to the
     request span. Other worker threads keep the caller's trace
     (`tracing.in_current_context`).
4. **Attributes are an allow-list.** Only `sdf.*` keys from
   `tracing.ATTRIBUTE_KEYS` are set: task/run/plan/attempt ids, agent
   kind/id/role, model, sandbox id/provider/template, turn, status/outcome,
   seconds, ADR-0023 `error_class`, tokens, cost, usage source. Strings pass
   through `observability.redact` and are truncated. **Secrets, API keys,
   prompts, instructions, agent output and code content are never span
   attributes.** Exceptions are recorded as the class name only (no message,
   no stack, no exception event). A redacting exporter wrapper re-redacts
   every span, event, link and resource attribute, including ones added by
   third-party instrumentation, and also: strips query strings, fragments and
   userinfo from `http.url`/`http.target`/`url.full`; drops `url.query`,
   client address/port, user agent and any `http.*.header.*`; reduces
   `exception` events (recorded by the ASGI instrumentation) to
   `exception.type`; and replaces any status description that is not an
   identifier (SQLAlchemy puts `str(exc)` there, which on Postgres includes
   row values) with the error class or `error`.
5. **Correlation.** Inside a span, every log line carries `trace_id` and
   `span_id`. `attempts.trace_id` and `agent_runs.trace_id` (migration
   `0017_trace_id`) store the trace, `/tasks/{id}/run` returns it, and
   `runs show` / the timeline print it. `trace_id` in a response is always
   the trace stored on the row: the request's trace for `/tasks/{id}/run`
   and `wait=true` runs, the `sdf.run.background` trace for `wait=false`
   runs (its link points back to the request).
7. **Configuration** comes from the environment plus the repo-root `.env`
   (endpoint, headers, service name, SDF switches). OTLP header values are
   parsed by SDF; malformed entries are logged by count and header names
   only, the SDK's own verbatim warning is masked, and
   `OTEL_EXPORTER_OTLP_*HEADERS` values count as secrets for
   `observability.redact`. Supported: OTLP over HTTP/protobuf and console.
   `OTEL_SDK_DISABLED=true` and `OTEL_TRACES_EXPORTER=none` keep tracing off;
   OTLP/gRPC is refused with a warning. Instrumentation packages are pinned
   (`0.66b0` with SDK `1.45.x`) because they are beta and track the SDK.
6. **Nothing inside sandboxes.** Tracing runs only in SDF Core on the
   master. No collector, exporter, env var or endpoint is sent to a sandbox;
   sandbox egress stays Vultr-only (ADR 0023).

## Consequences

- Operators point SDF at their backend with env vars only; switching vendor
  is a config change.
- A background run is a separate trace linked to the request; backends that
  show span links let you jump between them.
- Agent internals (individual LLM calls inside opencode, pi...) are not
  traced: they run in the sandbox, and only turn-level timing, tokens and
  cost are visible.
