# Live full loop via public `/tasks/{id}/run` (#46)

Prove one disposable Objective → Task → `POST /tasks/{id}/run` → Agent Runtime →
Herdr in an E2B sandbox → independent Evaluator Evidence → `/trace` join.

## What this claims

- The **public** HTTP run path selects a real sandboxed agent when
  `SDF_RUN_ADAPTER=live` (alias `herdr`): codex, claude (Herdr) or opencode,
  pi, qwen, goose (headless), built from main's per-agent configuration (ADR 0023).
- With no other configuration every agent runs on Vultr Inference
  (`VULTR_INFERENCE_API_KEY`, model `glm-5.3`).
- **Egress:** E2B sandboxes use `agent_sandbox.DEFAULT_NETWORK`, so they reach
  only the Vultr Inference host, with all other egress denied and no public
  inbound traffic. Add hosts with `SDF_RUN_EGRESS_ALLOW` (subscription modes
  need their vendor hosts). Daytona cannot lock egress at creation and is refused
  unless `SDF_RUN_DAYTONA_UNRESTRICTED_EGRESS=1`.
- Evidence is Attempt-bound and evaluator-derived (not agent self-report).
  The request names **check ids** only (`"criterion_checks": {"<criterion>":
  ["<id>"]}`); argv comes from the server registry (ADR 0025), and checks run
  with a reduced environment, never the server's.
- Teardown kills the sandbox by id with retries. A failed kill is surfaced as
  `cleanup_failed` on the Attempt, the usage ledger and a runtime event; it is
  never swallowed.
- Every turn and sandbox lifetime is recorded in `usage_ledger` with its tokens,
  cost and error class. `GET /runs/{id}/timeline` and
  `python -m sdf_core.cli runs show <id>` show the merged history.
- The default `SDF_RUN_ADAPTER=fake` keeps CI offline without live keys.

## What this does **not** claim

- Production readiness or multi-tenant deployment; runs are synchronous, with
  a per-process cap (429 past `SDF_RUN_MAX_CONCURRENT`).
- Execution against a real customer repository: `fixture_dir` must be under
  `SDF_FIXTURES_ROOT`.
- Multi-agent routing (P4), Judge/H3 Path B, or UI.

## Env knobs

| Variable | Role |
| --- | --- |
| `SDF_RUN_ADAPTER` | `fake` (default, CI) or `live` / `herdr` |
| `SDF_RUN_AGENT` | `codex` (default), `claude`, `opencode`, `pi`, `qwen`, `goose` |
| `SDF_SANDBOX_PROVIDER` | `e2b` (default) or `daytona` (needs `SDF_DAYTONA_SNAPSHOT` + opt-in above) |
| `SDF_E2B_AGENTS_TEMPLATE` | E2B template (default `agent_sandbox.DEFAULT_TEMPLATE`) |
| `SDF_CREDENTIAL_MODE_<AGENT>` etc. | Per-agent configuration as on main (ADR 0007); defaults to Vultr |
| `SDF_CODEX_MODEL` | Codex model (`SDF_HERDR_CODEX_MODEL` is a deprecated alias) |
| `SDF_RUN_EGRESS_ALLOW` | Extra egress hosts, comma-separated |
| `SDF_FIXTURES_ROOT` | Root that `fixture_dir` must resolve into (default `./fixtures`) |
| `SDF_EVALUATOR_CHECKS_FILE` | Server check registry; `criterion_checks` takes ids from it or `<fixture>/sdf-checks.json` (ADR 0025) |
| `SDF_EVALUATOR_CONTAINMENT` | `off`, `prefer` or `require` containment for evaluator checks; unset = `require` for live agents (`SDF_RUN_ADAPTER=live`, every plan run) and `off` for the fake adapter (ADR 0025) |
| `SDF_RUN_MAX_CONCURRENT` | Live runs per process (default `2`; more → 429) |
| `SDF_HERDR_TIMEOUT_MS` / `SDF_RUN_TURN_TIMEOUT_S` | Herdr / headless turn timeouts (`240000` / `420`) |
| `SDF_E2B_SANDBOX_TIMEOUT_SECONDS` | E2B sandbox lifetime (default `900`) |
| `SDF_LOG_FORMAT` / `SDF_LOG_LEVEL` | `json` or `text` logs (redacted, with request/attempt ids) |
| `SDF_PRICE_<MODEL>` / `SDF_E2B_USD_PER_SECOND` | Price overrides used for cost estimates |
| `E2B_API_KEY` / `E2B_DOMAIN` | E2B provider credentials |

`SDF_DOTENV`, `SDF_AUTH_*` and `SDF_DATABASE_URL` are never forwarded to the
agent or sandbox.

Live HTTP proof gate: `SDF_LIVE_FULL_LOOP=1` **or** `SDF_LIVE_E2B=1`, plus
`E2B_API_KEY` and `VULTR_INFERENCE_API_KEY`; choose agents with
`SDF_LIVE_RUN_AGENTS` (default `opencode,pi,codex`). The test kills only
sandboxes tagged with its own `sdf_attempt_id` before asserting none leaked.

```bash
# deterministic suite (no live keys)
uv run pytest -q

# live public-path proof (keys from env / .env; never echo them)
SDF_LIVE_FULL_LOOP=1 uv run pytest -q tests/test_live_full_loop_http.py

# what happened in an attempt
python -m sdf_core.cli runs show ATTEMPT-…
```

See also `.env.example`, ADR 0023, and `tests/test_real_agent_loop_live.py`.
