# Domain schema and decision edges

Type: task
Status: resolved
Blocked by: 

Implement the ten SDF Core entities, PostgreSQL migrations and polymorphic decision-edge table. Add source, timestamp, owner, confidence and evidence references where applicable.

Acceptance: migrations run on a clean PostgreSQL database; edge relations are constrained to the approved vocabulary; invalid references are rejected.

## Answer

Added the initial Alembic migration, PostgreSQL-compatible schema, relation check constraint, and application validation for validation targets. PostgreSQL migration and integration test pass on a clean temporary database.

## Resolution audit (2026-09-25)

**Acceptance criterion 1: migrations run on a clean PostgreSQL database**
- ✅ Alembic migration 0001_initial.py creates all 10 SDF Core entities: graph_nodes, tasks, attempts, artifacts, evidence, decision_edges, objective_metrics, outcome_observations, runtime_events, tool_audits
- ✅ PostgreSQL schema migration passes local SQLite test suite; PostgreSQL schema layer exists in migrations 0009-0010
- Evidence: alembic/versions/0001_initial.py:15-54, tests/test_migrations.py passes

**Acceptance criterion 2: edge relations are constrained to the approved vocabulary**
- ✅ DecisionEdgeRow enforces relation vocabulary via CheckConstraint: ('motivates','constrains','implements','depends_on','measures','validates','contradicts')
- ✅ Constraint present in both ORM model and migration
- Evidence: sdf_core/db.py:30 and alembic/versions/0001_initial.py:53

**Acceptance criterion 3: invalid references are rejected**
- ✅ Polymorphic validation at ORM layer: accepts kind ∈ {task, attempt} or GraphNodeRow match
- ✅ DecisionEdgeRow._reject_dangling_decision_edges (db.py:220-256) validates both new and committed endpoints
- ✅ PostgreSQL trigger sdf_validate_decision_edge (0010_decision_edge_integrity.py) enforces at database layer
- ✅ Tests: test_integrity.py:74-85 proves dangling references are rejected with rollback
- Evidence: sdf_core/db.py:220-256, alembic/versions/0010_decision_edge_integrity.py:20-57, tests/test_integrity.py:74-85

**Append-only Evidence enforcement (from sdf-runtime ticket 02)**
- ✅ ORM guard at session.before_flush rejects Evidence mutations (db.py:196-205)
- ✅ PostgreSQL trigger sdf_reject_evidence_mutation (0009_evidence_append_only.py) enforces UPDATE/DELETE rejection
- Evidence: sdf_core/db.py:196-205, alembic/versions/0009_evidence_append_only.py:20-36

**Test results:** Full suite: 178 passed, 5 skipped (the 5 skips are the PostgreSQL tests, which are gated on `SDF_POSTGRES_TEST_URL`, not a missing driver). PostgreSQL gate re-run by coordinator 2026-09-25: `scripts/postgres-test.sh` — clean PostgreSQL 16 container, alembic upgrade 0001→0013 succeeded, `tests/test_postgres_integration.py` 5 passed (includes `test_vertical_slice_runs_against_postgresql`), container removed.
