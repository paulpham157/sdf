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
uv run uvicorn sdf_core.api:app --reload
```

The API starts with an in-memory SQLite database for local development. For a persistent deployment, configure PostgreSQL:

```bash
export SDF_DATABASE_URL='postgresql+psycopg://USER:PASSWORD@HOST:5432/sdf_core'
uv run alembic upgrade head
uv run uvicorn sdf_core.api:app
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

OpenCode (`opencode`, pinned `1.19.4`) is a headless agent kind
(`sdf_core.opencode_runtime.OpenCodeHeadlessRuntime`) that uses any
OpenAI-compatible endpoint; Vultr Serverless Inference with `vultr/glm-5.3` is
only the default. Base URL, key variable name and model are configurable per
agent kind ([ADR 0011](docs/adr/0011-opencode-agent-kind-and-per-agent-provider-config.md)).

Pi (`pi`, pinned `0.73.1`) is a second headless agent kind
(`sdf_core.pi_runtime.PiHeadlessRuntime`, `pi -p --mode json`) with the same
Vultr defaults (key in `SDF_PI_API_KEY`, `glm-5.3`); it refuses a workspace
containing `.pi/` and gets write tools only in a fresh E2B sandbox
([ADR 0012](docs/adr/0012-pi-agent-kind.md)).

Qwen Code (`qwen`, `0.26.2`), Goose (`goose`, `1.56.0`) and Aider (`aider`,
`0.86.2`) are further headless kinds for any OpenAI-compatible endpoint (same
Vultr defaults; keys `SDF_QWEN_API_KEY`, `SDF_GOOSE_API_KEY`,
`SDF_AIDER_API_KEY` or a name in `SDF_<AGENT>_API_KEY_ENV`). They refuse
workspaces carrying their project config and run read-only outside a fresh E2B
sandbox ([ADR 0013](docs/adr/0013-openai-compatible-headless-agent-kinds.md)).

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
