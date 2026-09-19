# Domain schema and decision edges

Type: task
Status: resolved
Blocked by: 

Implement the ten SDF Core entities, PostgreSQL migrations and polymorphic decision-edge table. Add source, timestamp, owner, confidence and evidence references where applicable.

Acceptance: migrations run on a clean PostgreSQL database; edge relations are constrained to the approved vocabulary; invalid references are rejected.

## Answer

Added the initial Alembic migration, PostgreSQL-compatible schema, relation check constraint, and application validation for validation targets. PostgreSQL migration and integration test pass on a clean temporary database.
