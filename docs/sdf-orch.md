# Running `sdf-orch`

`sdf-orch` (`go/cmd/sdf-orch`, [ADR 0035](adr/0035-port-orchestration-core-to-go.md))
is the Go orchestration process: API gateway, in-process message bus, task
queue, and Attempt dispatcher. It listens on `-addr` (default `:8080`) and
exposes `/healthz` and `/metrics` (goroutine count, queue depth, and
Tool Proxy counters).

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
writer) on the same file are unsupported. The store keeps a single SQLite
connection in that process (`SetMaxOpenConns(1)`) and does not take an OS
file lock or set SQLite `locking_mode=EXCLUSIVE`. A second open of the same
path succeeds while the first connection is still open. That success is
not a multi-writer mode: this process does not coordinate another writer.
Stop the process, then start one new process on the same path.
`TestConcurrentMultiProcessWritersUnsupported` records the limit:
`locking_mode` stays `normal`, a second open is not rejected, and reopen
after close still works.

A crash after a claim is recorded and before the terminal outcome leaves
that action claimed. A later delivery does not execute it; it needs a new
action id. Each such delivery increments `tool_claim_stuck_total` on
`/metrics`. Rows stay until the file is removed. There is no TTL.

## Tool Proxy counters (`GET /metrics`)

`/metrics` is the same JSON document as goroutine count and queue depth.
The Tool Proxy fields are counters for this process. They start at zero,
increase by one per event, and reset when the process exits. They are not
rows in the fence file. A restart therefore drops the numbers to zero
while claimed and terminal rows in the file stay. Compare two scrapes and
alert on the increase.

| Field | When it increases |
| --- | --- |
| `tool_executed_total` | The executor ran and the `action_executed` row was stored. This is a successful first execution. A replay does not increase it. An executor error does not increase it. |
| `tool_replay_total` | A delivery returned a stored terminal outcome (`executed` or `failed`) and did not run the executor. |
| `tool_claim_stuck_total` | A delivery found the action already claimed and not yet terminal, and did not run the executor. That is the crash-after-claim path, and the same path when another delivery still holds the claim. |
| `tool_policy_deny_total` | The proxy denied the delivery (actor, principal, credential scope, allowlist, missing sandbox scope, or a reserved `sdf_*` context key). |

Replay rate is not a separate field. Over one scrape window:

```text
delta(tool_replay_total) / delta(tool_executed_total + tool_replay_total)
```

Suggestions:

- **Claim stuck rising.** `tool_claim_stuck_total` increased. A short increase can be two deliveries overlapping while the first still holds the claim; those losers do not execute. An increase that continues after the executor should have finished means a claim was left open (the process died after claim, before the terminal row). Further deliveries of that action id keep the counter rising and still do not execute. Issue a new action id, or remove the fence file if the claim is abandoned.
- **Replay ratio anomaly.** A ratio near 1 while `tool_executed_total` stays flat is a retry storm against finished actions. A ratio near 0 while clients are retrying the same action means those calls are not hitting this fence (new action ids, or the file was replaced). Judge the shift against the usual retry pattern. There is no fixed good ratio.
- **Policy deny rising.** `tool_policy_deny_total` climbing on its own points at a bad principal, a claimed actor that does not match the sandbox, or an allowlist miss.

The counters are process-local. Two processes on one file would each report their own totals, which is another reason a single process owns the file.
