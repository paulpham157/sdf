# SDF Core

**Software Decision Fabric** links a business goal to engineering work and to independent Evidence that the work actually met the goal. An agent saying it is done is not success: a Task is accepted only by independent verification Evidence.

## Quick start

Requires [uv](https://docs.astral.sh/uv/) and Python 3.11+. No E2B, cloud, or model API key. The sealed evaluator oracle currently produces a PASS only on Linux x86_64 ([ADR 0039](docs/adr/0039-evidence-independence.md)); on a Mac or another host, use GitHub Codespaces:

[![Open in GitHub Codespaces](https://github.com/codespaces/badge.svg)](https://codespaces.new/paulpham157/sdf)

```bash
uv sync --all-groups
uv run scripts/demo.py
```

You should see two tasks against the same criterion: an honest edit accepted because the evaluator verified that `app.py` prints `updated`, and a plausible-but-wrong edit the agent claimed was done (`print('updated ')` plus a weak test it rewrote) that SDF rejects. The local HTML story page (`file://`) shows both side by side. `scripts/demo-evidence.sh` is a wrapper for the same command. Pass `--only accepted` or `--only rejected` to run one task.

## What it provides

- A durable **Decision Graph**: Business Context → Objective → Task → Attempt → Artifact → Evidence.
- An evaluator-led execution loop: agents do work; independent checks determine the outcome.
- Bounded, evidence-driven model escalation (`basic` → `medium` → `high`).
- Classed Evidence: execution (the Attempt ran), verification (a sealed-oracle verdict) and outcome (an approver-recorded business result). Only verification accepts a Task, and every row carries a content MAC.
- Exactly-once dispatch: a retried `POST /tasks/{id}/run` replays its Attempt, and an Attempt whose executor died is settled `failed`.
- Policy-controlled, Attempt-bound tool actions with optional containment.
- A FastAPI API, SQLAlchemy persistence, Alembic migrations, and PostgreSQL support.

## Develop

Requires Python 3.11+ and [uv](https://docs.astral.sh/uv/).

```bash
uv sync --all-groups
uv run pytest
export SDF_EVIDENCE_MAC_KEY='local-dev-evidence-mac-key-not-a-deployment-secret'
SDF_AUTH_DEV_INSECURE=1 uv run uvicorn sdf_core.api:app --reload --host 127.0.0.1
```

The API starts with an in-memory SQLite database for local development. It refuses to start without `SDF_EVIDENCE_MAC_KEY` (at least 32 characters); there is no silent fallback to the public development constant. Swagger and ReDoc are off; `GET /` is not a UI. For the product loop, use `uv run scripts/demo.py` above.

### Offline evidence demo

`uv run scripts/demo.py` (or `scripts/demo-evidence.sh`) walks the public HTTP loop twice with the default fake adapter: Business Context, Objective, two Tasks, `POST /tasks/{id}/run` for each, then evaluator Evidence, the Attempt revision, and the task trace. It prints a four-beat story (intent, the agent's claim for A and B, the independent check for each, and the outcome) and writes a two-column HTML page (`--out DIR` to choose the directory; `--open` to open it; `--only accepted|rejected` to run one task). The outcome prints each verification Evidence `revision_id` and `tree_sha256`, the Assumption edge from that Evidence (`validates` for Task A, `contradicts` for Task B), and the cost caveat below. The script exits non-zero unless the accepting Evidence is one verification row from `sdf-evaluator` — not the execution row, whose id ends in `-EXEC` — with status PASS, bound to that revision, and the source of the `validates` edge. It needs the install above and no E2B or model keys. `SDF_LIVE_FULL_LOOP` stays unset. It also needs a Linux x86_64 host with Landlock: the sealed oracle that judges the fixture's `python -c` check runs nowhere else, and on any other host that check is a non-pass ([ADR 0039](docs/adr/0039-evidence-independence.md)). On macOS, on arm64, or in an emulated amd64 container, the script stops before it starts the API and points at the Codespaces button above. This is the fixture loop in [docs/live-full-loop.md](docs/live-full-loop.md). This fixture loop is not production proof.

The script binds the API to `127.0.0.1` with `SDF_AUTH_DEV_INSECURE=1`, sets `SDF_RUN_ADAPTER=fake` and `SDF_FIXTURES_ROOT` to `./fixtures`, and posts `fixture_dir=offline-evidence`. If `SDF_EVIDENCE_MAC_KEY` is unset, it sets a demo-only fixture key (not a deployment secret) so verification can accept Task A; a user-supplied key is kept when it is at least 32 characters, and a shorter value is rejected. That directory sits under the fixtures root. The default task run has no plan, so this path does not call `POST /plans/{id}/approve`. The script unsets `SDF_REQUIRE_PLAN_APPROVAL`. A task that already belongs to a plan still waits for approval. Provider keys in the environment are ignored and reported as unused.

Both tasks reuse fixture `fixtures/offline-evidence` and the sealed `app-updated` check, which requires a single `print('updated')` with no trailing space. Task A is an honest `FakeNativeAdapter` write of `print('updated')`; verification Evidence from `sdf-evaluator` PASSes and the Task is accepted. Task B writes `print('updated ')` (trailing space), claims `Done! app.py updated, all tests pass`, and also writes a weak `tests/test_app.py` substring check plus a rewritten workspace `sdf-checks.json`. A naive CI run of those files would go green; the sealed oracle ignores workspace checks and FAILs Task B, so SDF does not accept it. Each run also writes execution Evidence (producer `native`, no verifier). That row records that the adapter ran, and it never accepts the Task ([ADR 0042](docs/adr/0042-evidence-classes.md)). Adapter stdout stays on the process-log artifact.

The loop records these results:

- Task A run response: `task_status=succeeded`, `provider=fake`, `evidence_mode=synthetic`. Task B: the adapter attempt is `completed` and the agent claimed success, but `task_status` is not `succeeded` and verification is a non-pass from `sdf-evaluator`.
- `GET /evidence/{id}`: Task A `status=PASS`, Task B a non-pass; both `verifier_identity=sdf-evaluator`, with `revision_id` and `tree_sha256`.
- `GET /attempts/{id}/revision`: `result_tree_sha256`. `base_commit` is null because this fixture records a tree id, not a git commit.
- `GET /tasks/TASK-OFFLINE-001/trace` and `GET /tasks/TASK-OFFLINE-002/trace`: Business Context, Objective, Task, Attempt, and both Evidence rows. Only a verification PASS `validates` the Assumption. The fake adapter records no runtime events.
- `python -m sdf_core.cli runs show <attempt id>` still prints the same timeline against `SDF_DATABASE_URL`. `usage_ledger` has no rows for this adapter. `cost_usd` is `0` on the Attempt. The script prints that zero as unmetered: it is not an invoice and not an `SDF_PRICE_*` or `SDF_E2B_USD_PER_SECOND` estimate. It exits non-zero if either task has a ledger row or a non-zero `cost_usd`.

The state database is a temporary SQLite file. Workspaces and artifacts land in that temporary directory (not in the repo working tree).

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
export SDF_EVIDENCE_MAC_KEY='local-dev-evidence-mac-key-not-a-deployment-secret'
uv run uvicorn sdf_core.api:app --host 127.0.0.1
```

The server also refuses to start without that key (at least 32 characters).

## Run for real

For a persistent deployment, configure PostgreSQL and an Evidence MAC key of at least 32 characters. The server does not start, and does not stamp Evidence, without `SDF_EVIDENCE_MAC_KEY`. There is no silent fallback to the public development constant ([ADR 0043](docs/adr/0043-evidence-content-mac.md)). Rotation is an operator re-stamp of existing rows; the process does not rotate the key:

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
- `scripts/demo.py` — one-command offline evidence demo.
- `.devcontainer/` — GitHub Codespaces / VS Code dev container.
- `CONTEXT.md` — canonical domain vocabulary.
- `docs/adr/` — architecture decisions.
- `docs/architecture.md` — current architectural overview.
- `docs/SDF Core Architecture.tldraw` — architecture, Task/Attempt lifecycle, run sequence and Evidence verification diagrams (tldraw; dashed means proposed or not built).
- `docs/CONTRIBUTING.md` — contributor workflow, verification boundaries, and E2B/Herdr setup.

## License

[Apache-2.0](LICENSE)
