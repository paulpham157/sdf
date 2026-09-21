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
scripts/e2b-env.sh e2b-box open -t sdf-herdr-codex --template-any
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
