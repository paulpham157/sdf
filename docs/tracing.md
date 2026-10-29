# Tracing (OpenTelemetry)

SDF Core can emit OpenTelemetry traces to any OTLP backend. It is off by
default; see ADR 0024 for the design.

```bash
pip install 'sdf[otel]'          # or: uv sync --extra otel
export SDF_OTEL_ENABLED=1
```

| Variable | Meaning |
| --- | --- |
| `SDF_OTEL_ENABLED` | `1`/`true` turns tracing on. Unset: no SDK import, no overhead. |
| `SDF_OTEL_EXPORTER` | `otlp` (default) or `console` (dev; prints spans to stdout). Falls back to `OTEL_TRACES_EXPORTER`. |
| `SDF_OTEL_DB` | `1` adds SQLAlchemy spans (statements without values). |
| `OTEL_SERVICE_NAME` | Defaults to `sdf-core`. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Base URL; `/v1/traces` is appended. Or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` for the full URL. |
| `OTEL_EXPORTER_OTLP_HEADERS` | Backend auth, `key=value,key2=value2` (URL-encode spaces as `%20`). Never logged or put on spans. |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | Standard sampling, e.g. `parentbased_traceidratio` / `0.1`. |

Only OTLP over HTTP/protobuf is bundled.

## Backends

Jaeger (all-in-one, local):

```bash
docker run --rm -p 127.0.0.1:4318:4318 -p 127.0.0.1:16686:16686 jaegertracing/all-in-one
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
```

Grafana Tempo (or an OpenTelemetry Collector in front of it):

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://tempo.internal:4318
```

Langfuse:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=https://cloud.langfuse.com/api/public/otel
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Basic%20$(printf '%s:%s' "$LANGFUSE_PUBLIC_KEY" "$LANGFUSE_SECRET_KEY" | base64 -w0)"
```

LangSmith:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=https://api.smith.langchain.com/otel
export OTEL_EXPORTER_OTLP_HEADERS="x-api-key=$LANGSMITH_API_KEY,Langsmith-Project=sdf"
```

Check each vendor's current OTLP endpoint (EU/self-hosted regions differ).

## What you will see

```
POST /tasks/{task_id}/run
└─ sdf.task.run                 sdf.task_id, sdf.principal, sdf.attempt_id, sdf.status
   └─ sdf.attempt               sdf.attempt_id, sdf.model_tier, sdf.cost_usd, sdf.tokens.*
      ├─ sdf.agent.run          sdf.agent.kind, sdf.model, sdf.sandbox.id, sdf.turns, sdf.error_class
      │  ├─ sdf.sandbox.create  sdf.sandbox.provider, sdf.sandbox.template
      │  ├─ sdf.agent.start
      │  ├─ sdf.agent.turn      sdf.turn, sdf.seconds, sdf.tokens.*, sdf.cost_usd, sdf.usage_source
      │  └─ sdf.sandbox.cleanup
      └─ sdf.evaluate
```

Coordinator runs use `sdf.run` as the root, with `sdf.agent.start`,
`sdf.agent.turn`, `sdf.verify` and `sdf.sandbox.cleanup` beneath it. Failed
spans have ERROR status plus `sdf.error_class` from the ADR-0023 taxonomy.

`/tasks/{id}/run` returns `trace_id`, `runs show <id>` prints it, and every
JSON log line inside a span has `trace_id`/`span_id`, so you can jump from a
log line or run to the trace.

## Rules

- **Never put secrets, API keys, prompts, instructions, agent output or code
  content in span attributes.** Use `tracing.span(...)` /
  `tracing.set_attributes(...)` with keys from `tracing.ATTRIBUTE_KEYS`; other
  keys are dropped, strings are redacted (`observability.redact`) and
  truncated, and the exporter redacts again. Exceptions record the class name
  only. Add a new key to the allow-list only for ids, enums or numbers.
- **Tracing never runs in sandboxes.** No exporter, collector or OTEL env var
  is passed to an agent sandbox; sandbox egress stays Vultr-only.
- Bind local collectors to `127.0.0.1`.
