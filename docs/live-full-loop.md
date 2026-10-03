# Live full loop via public `/tasks/{id}/run` (#46)

Prove one disposable Objective → Task → `POST /tasks/{id}/run` → Agent Runtime →
Herdr in an E2B sandbox → independent Evaluator Evidence → `/trace` join.

## What this claims

- The **public** HTTP run path can select a real Herdr/E2B adapter when
  `SDF_RUN_ADAPTER=herdr` (alias `live`).
- Evidence is Attempt-bound and evaluator-derived (not agent self-report).
- On the happy path the Attempt's sandbox is closed/killed after the run.
- Default `SDF_RUN_ADAPTER=fake` keeps CI offline without live keys.

## What this does **not** claim

- Production readiness or multi-tenant deployment.
- Execution against a real customer repository.
- Daytona (`SDF_SANDBOX_PROVIDER=daytona`) — that provider is owned by issue #44
  and is rejected on this branch until it merges.
- Multi-agent routing (P4), Judge/H3 Path B, or UI.

## Env knobs

| Variable | Role |
| --- | --- |
| `SDF_RUN_ADAPTER` | `fake` (default, CI) or `herdr` / `live` |
| `SDF_RUN_AGENT` | Agent kind for the live path (default `codex`) |
| `SDF_E2B_AGENTS_TEMPLATE` | E2B template (default `sdf-herdr-agents`) |
| `SDF_SANDBOX_PROVIDER` | `e2b` only here; `daytona` deferred to #44 |
| `SDF_CREDENTIAL_MODE_CODEX` / `_CLAUDE` | `subscription` or `api-key` (ADR-0007; required) |
| `SDF_CONNECTION_CODEX` / `_CLAUDE` | Plugin connection id in `subscription` mode |
| `SDF_*_API_KEY` / `SDF_*_BASE_URL` | `api-key` mode only |
| `SDF_HERDR_CODEX_MODEL` | Optional Codex `-m` pin (live tests use `gpt-6-luna`) |
| `SDF_HERDR_TIMEOUT_MS` | Herdr turn timeout (default `240000`) |
| `SDF_E2B_SANDBOX_TIMEOUT_SECONDS` | E2B sandbox lifetime (default `900`) |
| `E2B_API_KEY` / `E2B_DOMAIN` | E2B provider credentials |

Live HTTP proof gate: `SDF_LIVE_FULL_LOOP=1` **or** `SDF_LIVE_E2B=1`, plus
`E2B_API_KEY` and a working Codex Credential Mode.

```bash
# deterministic suite (no live keys)
uv run pytest -q

# live public-path proof (keys from env / .env; never echo them)
SDF_LIVE_FULL_LOOP=1 uv run pytest -q tests/test_live_full_loop_http.py
```

See also `.env.example` and `tests/test_real_agent_loop_live.py` (same Herdr stack
via `ExecutionService` directly — not the public endpoint).
