# SDF Core

**Software Decision Fabric Core** links business intent to engineering work and the independent evidence that validates or contradicts it.

## What it provides

- A durable **Decision Graph**: Business Context → Objective → Task → Attempt → Artifact → Evidence.
- An evaluator-led execution loop: agents do work; independent checks determine the outcome.
- Bounded, evidence-driven model escalation (`basic` → `medium` → `high`).
- Policy-controlled, Attempt-bound tool actions with optional containment.
- A FastAPI API, SQLAlchemy persistence, Alembic migrations, and PostgreSQL support.

## Quick start

Requires Python 3.11+ and [uv](https://docs.astral.sh/uv/).

```bash
uv sync --all-groups
uv run pytest
SDF_AUTH_DEV_INSECURE=1 uv run uvicorn sdf_core.api:app --reload --host 127.0.0.1
```

The API starts with an in-memory SQLite database for local development. For a persistent deployment, configure PostgreSQL:

```bash
export SDF_DATABASE_URL='postgresql+psycopg://USER:PASSWORD@HOST:5432/sdf_core'
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
OpenAI-compatible endpoint; Vultr Serverless Inference with `vultr/glm-5.3` is
only the default. Base URL, key variable name and model are configurable per
agent kind ([ADR 0011](docs/adr/0011-opencode-agent-kind-and-per-agent-provider-config.md));
each turn runs `opencode run --server` against a private, password-protected
loopback `opencode serve` ([ADR 0018](docs/adr/0018-opencode-2-and-pi-1-upgrade.md)).

Pi (`pi`, `@earendil-works/pi-coding-agent` pinned `1.4.0`) is a second headless agent kind
(`sdf_core.pi_runtime.PiHeadlessRuntime`, `pi -p --mode json`) with the same
Vultr defaults (key in `SDF_PI_API_KEY`, `glm-5.3`); it refuses a workspace
containing `.pi/` and gets write tools only in a fresh E2B sandbox
([ADR 0012](docs/adr/0012-pi-agent-kind.md)).

Qwen Code (`qwen`, `0.26.2`) and Goose (`goose`, `1.56.0`) are further
headless kinds for any OpenAI-compatible endpoint (same Vultr defaults; keys
`SDF_QWEN_API_KEY`, `SDF_GOOSE_API_KEY` or a name in `SDF_<AGENT>_API_KEY_ENV`).
They refuse workspaces carrying their project config and run read-only outside
a fresh E2B sandbox ([ADR 0013](docs/adr/0013-openai-compatible-headless-agent-kinds.md)).

The supported agent kinds are `codex`, `claude`, `opencode`, `pi`, `qwen` and
`goose`. Aider and the Cursor Agent CLI were removed
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
each on Vultr `glm-5.3`, with `SDF_AGENT_SANDBOX=e2b` (lead and worker kinds:
`opencode` or `pi` only for now). Agents talk only through
the host's append-only message hub with the in-sandbox `sdf-msg` CLI / MCP
server; a worker's "done" is a claim, and the run succeeds only when the host's
own checks give PASS Evidence for every subtask. Agent questions wait in
`GET /decision-requests` for `POST /decision-requests/{id}/resolve`.

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
`approver` (approve/reject plans, resolve decision requests), `admin`. The
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

## Core principle

An agent reporting completion is not success. A Task has an **Accepted Outcome** only when independent Evidence satisfies its explicit acceptance criteria.

## Project layout

- `sdf_core/` — domain model, execution services, policy, runtime, and API.
- `tests/` — deterministic unit and integration tests.
- `alembic/` — database migrations.
- `CONTEXT.md` — canonical domain vocabulary.
- `docs/adr/` — architecture decisions.
- `docs/architecture.md` — current architectural overview.
- `docs/CONTRIBUTING.md` — contributor workflow, verification boundaries, and E2B/Herdr setup.

## License

[Apache-2.0](LICENSE)
