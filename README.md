# SDF Core

**Software Decision Fabric Core** links business intent to engineering work and the independent evidence that validates or contradicts it.

## What it provides

- A durable **Decision Graph**: Business Context → Objective → Task → Attempt → Artifact → Evidence.
- An evaluator-led execution loop: agents do work; independent checks determine the outcome.
- Bounded, evidence-driven model escalation (`basic` → `medium` → `high`).
- Classed Evidence: execution (the Attempt ran), verification (a sealed-oracle verdict) and outcome (an approver-recorded business result). Only verification accepts a Task, and every row carries a content MAC.
- Exactly-once dispatch: a retried `POST /tasks/{id}/run` replays its Attempt, and an Attempt whose executor died is settled `failed`.
- Policy-controlled, Attempt-bound tool actions with optional containment.
- A FastAPI API, SQLAlchemy persistence, Alembic migrations, and PostgreSQL support.

## Quick start

Requires Python 3.11+ and [uv](https://docs.astral.sh/uv/).

```bash
uv sync --all-groups
uv run pytest
SDF_AUTH_DEV_INSECURE=1 uv run uvicorn sdf_core.api:app --reload --host 127.0.0.1
```

### Offline evidence demo

`scripts/demo-evidence.sh` walks the public HTTP loop with the default fake adapter: Business Context, Objective, Task, `POST /tasks/{id}/run`, then evaluator Evidence, the Attempt revision, the task trace, and `python -m sdf_core.cli runs show`. It needs the install above and no E2B or model keys. `SDF_LIVE_FULL_LOOP` stays unset. It also needs a Linux x86_64 host with Landlock: the sealed oracle that judges the fixture's `python -c` check runs nowhere else, and on any other host that check is a non-pass ([ADR 0039](docs/adr/0039-evidence-independence.md)). On macOS, on arm64, or in an emulated amd64 container, the script stops before it starts the API. This is the fixture loop in [docs/live-full-loop.md](docs/live-full-loop.md). This fixture loop is not production proof.

```bash
scripts/demo-evidence.sh
```

The script binds the API to `127.0.0.1` with `SDF_AUTH_DEV_INSECURE=1`, sets `SDF_RUN_ADAPTER=fake` and `SDF_FIXTURES_ROOT` to `./fixtures`, and posts `fixture_dir=offline-evidence`. That directory sits under the fixtures root. The default task run has no plan, so this path does not call `POST /plans/{id}/approve`. The script unsets `SDF_REQUIRE_PLAN_APPROVAL`. A task that already belongs to a plan still waits for approval.

`FakeNativeAdapter` writes `app.py` as `print('updated')`. The check `app-updated` in `fixtures/offline-evidence/sdf-checks.json` parses that file. The PASS row is verification Evidence from `sdf-evaluator`, bound to the Attempt. The adapter stdout (`fake adapter applied: ...`) stays on the process-log artifact. The run also writes one execution Evidence row (producer `native`, no verifier). That row records that the adapter ran, and it never accepts the Task ([ADR 0042](docs/adr/0042-evidence-classes.md)).

The script prints these results:

- Run response: `task_status=succeeded`, `provider=fake`, `evidence_mode=synthetic`.
- `GET /evidence/{id}`: `status=PASS`, `verifier_identity=sdf-evaluator`, `revision_id`, and `tree_sha256`.
- `GET /attempts/{id}/revision`: `result_tree_sha256`. `base_commit` is null because this fixture records a tree id, not a git commit.
- `GET /tasks/TASK-OFFLINE-001/trace`: Business Context, Objective, Task, Attempt, and both Evidence rows. Only the verification row `validates` the Assumption. The fake adapter records no runtime events.
- `python -m sdf_core.cli runs show <attempt id>`: the same timeline. `usage_ledger` has no rows for this adapter. `cost_usd` is `0` on the Attempt. That zero is unmetered. It is not an invoice and not an `SDF_PRICE_*` or `SDF_E2B_USD_PER_SECOND` estimate.

The state database is a temporary SQLite file. Workspaces and artifacts land in `.sdf-workspaces/` and `.sdf-artifacts/`.

The API starts with an in-memory SQLite database for local development. For a persistent deployment, configure PostgreSQL and an Evidence MAC key. Without `SDF_EVIDENCE_MAC_KEY` the server uses a fixed development key from the source, so a database writer can recompute the MAC of a rewritten verdict ([ADR 0043](docs/adr/0043-evidence-content-mac.md)):

```bash
export SDF_DATABASE_URL='postgresql+psycopg://USER:PASSWORD@HOST:5432/sdf_core'
export SDF_EVIDENCE_MAC_KEY='<long random secret, kept out of the database>'
uv run alembic upgrade head
uv run uvicorn sdf_core.api:app --host 127.0.0.1
```

For the live E2B/Herdr path, copy `.env.example` to `.env` and provide the
matching E2B key. The wrapper loads that file before running the plugin:

```bash
cp .env.example .env
scripts/e2b-env.sh e2b-box doctor
scripts/e2b-env.sh e2b-box open -t sdf-herdr-agents --template-any
```

The persistent Herdr runtime uses the `sdf-herdr-agents` template
([infra/e2b/herdr-agents](infra/e2b/herdr-agents/README.md)) for both Codex and
Claude Code; the Codex-only `sdf-herdr-codex` template is retired
([ADR 0009](docs/adr/0009-retire-sdf-herdr-codex-template.md)). Each agent kind
needs an explicit Credential Mode, `subscription` or `api-key`
(ADR 0007).

OpenCode 2 (`opencode`, `@opencode/cli` pinned `2.2.0`) is a headless agent kind
(`sdf_core.opencode_runtime.OpenCodeHeadlessRuntime`) that uses any
OpenAI-compatible endpoint. Set `OPENCODE_BASE_URL`, `OPENCODE_API_KEY` and
optionally `OPENCODE_MODEL` (`<local provider id>/<model>`, default
`vultr/glm-5.3`; that prefix is the seeded provider id, not a host)
([ADR 0034](docs/adr/0034-openai-or-claude-compatible-llm-transport.md),
[ADR 0011](docs/adr/0011-opencode-agent-kind-and-per-agent-provider-config.md));
each turn runs `opencode run --server` against a private, password-protected
loopback `opencode serve` ([ADR 0018](docs/adr/0018-opencode-2-and-pi-1-upgrade.md)).

Pi (`pi`, `@earendil-works/pi-coding-agent` pinned `1.4.0`) is a second headless agent kind
(`sdf_core.pi_runtime.PiHeadlessRuntime`, `pi -p --mode json`). Set
`PI_BASE_URL`, `PI_API_KEY` and optionally `PI_MODEL` (default `glm-5.3`). It refuses a workspace
containing `.pi/` and gets write tools only in a fresh E2B sandbox
([ADR 0012](docs/adr/0012-pi-agent-kind.md)).

Every agent turn runs in its own process session and cgroup v2 leaf; leftovers
(detached, double-forked or reparented to PID 1) are killed (TERM, then KILL)
when the turn ends, times out, or is cancelled
([ADR 0019](docs/adr/0019-per-turn-process-containment.md)). On Linux, a
child process started inside the Attempt workspace also runs under Landlock: it
can write only inside the workspace, and credential-shaped environment
variables that were not allow-listed are dropped
([ADR 0004](docs/adr/0004-internal-runtime-and-enforced-tool-policy.md)).

Qwen Code (`qwen`, `0.26.2`) and Goose (`goose`, `1.56.0`) are further
headless kinds for any OpenAI-compatible endpoint (`QWEN_BASE_URL` /
`QWEN_API_KEY` / `QWEN_MODEL`, and `GOOSE_BASE_URL` / `GOOSE_API_KEY` /
`GOOSE_MODEL`).
They refuse workspaces carrying their project config and run read-only outside
a fresh E2B sandbox ([ADR 0013](docs/adr/0013-openai-compatible-headless-agent-kinds.md)).

The supported agent kinds are `codex`, `claude`, `opencode`, `pi`, `qwen` and
`goose`. Each kind uses one LLM transport: OpenAI-compatible HTTP, or the
Claude Messages API. `codex` needs a host that implements the OpenAI Responses
API, not Chat Completions only
([ADR 0034](docs/adr/0034-openai-or-claude-compatible-llm-transport.md)).
Aider and the Cursor Agent CLI were removed
([ADR 0016](docs/adr/0016-remove-aider-and-cursor-agent-kinds.md)).
CLI-internal sub-agents are disabled for every kind by host-controlled flags
and user config ([ADR 0020](docs/adr/0020-no-subagents.md)).
In a fresh E2B sandbox every kind trusts the Attempt workspace's own project
config (`AGENTS.md`, `CLAUDE.md`, `.pi/`, `.qwen/`, `.goose/`, `opencode.json`,
skills, hooks …) because nobody can answer a trust prompt there; set
`SDF_<AGENT>_PROJECT_TRUST=never` to restore the refusal / ignore behaviour.
SDF's endpoint, key, model and sub-agent bans still outrank project config
([ADR 0021](docs/adr/0021-project-trust.md)).

Multi-agent runs ([ADR 0014](docs/adr/0014-plan-gate-and-agent-messaging.md)):
`POST /plans` proposes a plan, a human approves it with `POST /plans/{id}/approve`
(no Attempt or run starts before that; 409), then `POST /plans/{id}/runs` runs a
lead (default OpenCode) and one worker per subtask (default Pi), one E2B sandbox
each on the configured OpenAI-compatible endpoint (example default `glm-5.3`),
with `SDF_AGENT_SANDBOX=e2b` (lead and worker kinds:
`opencode` or `pi` only for now). Agents talk only through
the host's append-only message hub with the in-sandbox `sdf-msg` CLI / MCP
server; a worker's "done" is a claim, and the run succeeds only when the host's
own checks give PASS Evidence for every subtask. Agent questions wait in
`GET /decision-requests` for `POST /decision-requests/{id}/resolve`.
Agents can also read their own run from SDF Core with the in-sandbox
`sdf-core` CLI / MCP server (plan + hash, task, decisions, evidence, messages,
status): read-only, scoped to their run, never able to approve, no ports and no
token in the sandbox ([ADR 0022](docs/adr/0022-core-mcp.md)).

Tracing ([ADR 0024](docs/adr/0024-otel-tracing.md), [docs/tracing.md](docs/tracing.md)):
`pip install 'sdf-core[otel]'` and `SDF_OTEL_ENABLED=1` export OpenTelemetry spans
(HTTP → task → attempt → agent turn / sandbox create / cleanup, coordinator
runs) over OTLP to any backend (Jaeger, Tempo, Langfuse, LangSmith...) via the
standard `OTEL_*` variables. Off by default; span attributes never carry
secrets, prompts or code, and nothing runs inside sandboxes.

### Go orchestrator (`sdf-orch`)

The Go process in `go/cmd/sdf-orch` ([ADR 0035](docs/adr/0035-port-orchestration-core-to-go.md))
is separate from the Python API above. With no `-outcome-db` and no
`SDF_TOOL_OUTCOME_DB`, the Tool Proxy idempotency fence stays in memory and
is discarded on exit. A single-process deploy that must keep tool outcomes
across restarts sets that path to a file under a durable data directory
(parent directory created first; one process per file). Operator notes:
[docs/sdf-orch.md](docs/sdf-orch.md).

```bash
mkdir -p /var/lib/sdf
export SDF_TOOL_OUTCOME_DB=/var/lib/sdf/tool-outcomes.db
cd go && go run ./cmd/sdf-orch
```

### API authentication ([ADR 0015](docs/adr/0015-api-authentication.md))

Every route except `GET /healthz` needs `Authorization: Bearer <token>`; the
API refuses to start without `SDF_AUTH_PEPPER` (32+ random characters, kept
out of the database). Tokens are stored HMAC-hashed, expire (default 90 days)
and are managed only from the CLI; the plaintext is printed once:

```bash
uv run python -m sdf_core.auth_cli create --principal hung --role operator --role approver
uv run python -m sdf_core.auth_cli list
uv run python -m sdf_core.auth_cli revoke <token-id>
```

Roles: `viewer` (read), `operator` (create tasks/plans, start/cancel runs),
`approver` (approve/reject plans, resolve decision requests, record Decisions,
ADRs and outcome Evidence), `admin`. Outcome Evidence
(`POST /objectives/{id}/evidence`) names a verifier who is neither the
recording principal nor an agent that worked the Objective
([ADR 0042](docs/adr/0042-evidence-classes.md)). The
proposer, approver and run starter recorded in the Decision Graph are always
the token's principal, never a request field. Nobody approves their own plan
unless `SDF_AUTH_SINGLE_USER=1` (solo deployments; recorded as
`self_approval`). Agent/sandbox names and `service` tokens can never approve.
For local hacking only, `SDF_AUTH_DEV_INSECURE=1` serves header-less requests
as admin `local-dev` from loopback only. Always bind to loopback unless a TLS
reverse proxy sits in front:

```bash
uv run uvicorn sdf_core.api:app --host 127.0.0.1
```

Use a bare E2B domain such as `e2b.dev` or `e2b.app`; the SDK adds the `api.`
prefix itself.

See [CHANGELOG.md](CHANGELOG.md) for breaking API changes (registered checks,
server-measured telemetry, run prices, keyless run replay, dead-executor
recovery and Evidence classes).

## Core principle

An agent reporting completion is not success. A Task has an **Accepted Outcome** only when independent Evidence satisfies its explicit acceptance criteria.

## Project layout

- `sdf_core/` — domain model, execution services, policy, runtime, and API.
- `go/` — Go orchestration core (`sdf-orch`). Durable fence path: [docs/sdf-orch.md](docs/sdf-orch.md).
- `tests/` — deterministic unit and integration tests.
- `alembic/` — database migrations.
- `CONTEXT.md` — canonical domain vocabulary.
- `docs/adr/` — architecture decisions.
- `docs/architecture.md` — current architectural overview.
- `docs/SDF Core Architecture.tldraw` — architecture, Task/Attempt lifecycle, run sequence and Evidence verification diagrams (tldraw; dashed means proposed or not built).
- `docs/CONTRIBUTING.md` — contributor workflow, verification boundaries, and E2B/Herdr setup.

## License

[Apache-2.0](LICENSE)
