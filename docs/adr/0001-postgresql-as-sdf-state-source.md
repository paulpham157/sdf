# PostgreSQL as the SDF state source

SDF Core uses PostgreSQL as the source of truth for Tasks, Attempts, graph nodes and edges, state transitions, idempotency keys and Evidence metadata in deployed environments. A relational schema with a polymorphic decision-edge table is preferred over introducing a graph database in the first vertical slice because the core proof is traceability and durable state, not graph-database scale. The in-memory SQLite URL is permitted only for isolated tests and local deterministic demos; production must set `SDF_DATABASE_URL` to PostgreSQL.
