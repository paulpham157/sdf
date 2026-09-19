# Domain schema and decision edges

Type: task
Status: ready-for-agent
Blocked by: 

Implement the ten SDF Core entities, PostgreSQL migrations and polymorphic decision-edge table. Add source, timestamp, owner, confidence and evidence references where applicable.

Acceptance: migrations run on a clean PostgreSQL database; edge relations are constrained to the approved vocabulary; invalid references are rejected.

## Answer

Current audit: Reopened: general polymorphic references and append-only Evidence remain unenforced; follow sdf-runtime ticket 02. Prior implementation notes below are historical partial evidence.

Added the initial Alembic migration, PostgreSQL-compatible schema, relation check constraint, and application validation for validation targets. PostgreSQL migration and integration test pass on a clean temporary database.
