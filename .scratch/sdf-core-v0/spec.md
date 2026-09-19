# SDF Core v0 vertical slice

## Outcome

Prove that SDF can trace one business Objective through Task, Attempt, native agent execution, diff, deterministic evaluation and Evidence that validates or contradicts an Assumption.

## Scope

The slice uses Python, FastAPI, PostgreSQL, SQLAlchemy/Alembic and one fixture repository. It starts with one native adapter and a fake adapter for deterministic tests. ACP, A2A, UI, OpenViking, CI/CD and graph-database infrastructure are out of scope.

## Domain model

BusinessContext, Objective, Assumption, Constraint, Decision/ADR, Requirement, Task, Attempt, Artifact and Evidence.

Decision edges use one polymorphic table with relations: `motivates`, `constrains`, `implements`, `depends_on`, `measures`, `validates`, `contradicts`.

Every node and edge carries source, timestamp, owner, evidence reference where applicable and confidence. Evidence is append-only.

## API

- `POST /business-contexts`
- `POST /objectives`
- `POST /tasks`
- `POST /tasks/{id}/run`
- `GET /tasks/{id}/trace`
- `GET /evidence/{id}`

## State

Task: `created → ready → running → evaluating → succeeded|failed|inconclusive`.

Attempt: `created → dispatched → running → completed|failed|cancelled`.

Invalid transitions are rejected. Task and Attempt dispatch keys are unique; retries create new Attempts.

## Milestone acceptance

One integration test must demonstrate:

`Objective → Task → Attempt → native agent edits fixture → diff Artifact → evaluator PASS → Evidence → validates Assumption/ADR → trace query returns the full chain`.

The same flow must support a failing evaluator result that creates Evidence with `contradicts` or `INCONCLUSIVE` rather than silently becoming a fact.
