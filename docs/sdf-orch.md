# Running `sdf-orch`

`sdf-orch` (`go/cmd/sdf-orch`, [ADR 0035](adr/0035-port-orchestration-core-to-go.md))
is the Go orchestration process: API gateway, in-process message bus, task
queue, and Attempt dispatcher. It listens on `-addr` (default `:8080`) and
exposes `/healthz` and `/metrics`.

Build and run it from the Go module:

```bash
cd go
go build -o sdf-orch ./cmd/sdf-orch
./sdf-orch -addr :8080
```

`go run ./cmd/sdf-orch` is the same process. Nothing in this repository
(Dockerfile, Compose, Helm, systemd, Makefile, or Paseo) starts it. The
commands below are the single-process deploy example.

## Tool Proxy fence (`-outcome-db` / `SDF_TOOL_OUTCOME_DB`)

The flag selects where the Tool Proxy idempotency fence keeps its event
log, keyed by `attempt_id` + `action_id`
([ADR 0037](adr/0037-go-tool-proxy-fence-audit-parity.md)). Each delivery
appends a `policy_decided` row. A claim, an `action_executed` row, and an
`action_failed` row are each written at most once per action, so a later
delivery returns the stored terminal result and does not run the action
again. That record survives a process restart only when the next process
opens the same file.

| Input | Store | Restart |
| --- | --- | --- |
| `-outcome-db` set | SQLite file at that path | Survives if the file does |
| flag omitted, `SDF_TOOL_OUTCOME_DB` set | SQLite file at the env path | Survives if the file does |
| both unset or empty | in-process `MemoryStore` | Lost when the process exits |

When both are set, `-outcome-db` wins: the flag's default is the environment
value, and an explicit flag replaces it. Startup logs the choice as
`tool fence in-memory` or `tool fence <path>`.

This file is the Go Tool Proxy fence only. Decision Graph state, Tasks,
Attempts, and Evidence stay on PostgreSQL (`SDF_DATABASE_URL`,
[ADR 0001](adr/0001-postgresql-as-sdf-state-source.md)). The fence does not
share rows with the Python `tool_audits` table. Sharing that table is a
later decision (ADR 0037), not this file.

An empty path stays `MemoryStore` in the orchestrator, including tests and
`sdf-orch` with no flag and no env. There is no compiled-in filename.

## Single-process path

For one `sdf-orch` process, put the file under a data directory that remains
after the process stops (a host directory or a mounted volume, including
across container restarts). The recommended path is
`/var/lib/sdf/tool-outcomes.db`:

```bash
mkdir -p /var/lib/sdf
export SDF_TOOL_OUTCOME_DB=/var/lib/sdf/tool-outcomes.db
cd go && go run ./cmd/sdf-orch
```

The same path on the flag:

```bash
mkdir -p /var/lib/sdf
./sdf-orch -outcome-db /var/lib/sdf/tool-outcomes.db
```

The parent directory must already exist. `sdf-orch` creates the file and
exits before listen if the parent is missing. Create the directory in the
deploy unit, image entrypoint, or the `mkdir` above. Do not point the path
at an empty ephemeral disk (`tmpfs`, a container writable layer that is
thrown away on restart).

One process may open the file. Two `sdf-orch` processes (or any second
writer) on the same file are unsupported: the store uses a single connection
in one process and does not coordinate writers. Stop the process, then start
one new process on the same path.

A crash after a claim is recorded and before the terminal outcome leaves
that action claimed. A later delivery does not execute it; it needs a new
action id. Rows stay until the file is removed. There is no TTL.
