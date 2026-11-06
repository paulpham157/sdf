# ADR 0041: a keyless /run is exactly once, and startup settles a dead running Attempt

Status: proposed. The exactly-once rule records the owner's decision on PR #112
(2026-11-06): a `/run` request with a missing `dispatch_key` is handled
exactly once, and `RunTaskRequest.dispatch_key` stays optional. This ADR
becomes accepted when that pull request is accepted.

## Context

`POST /tasks/{id}/run` used to fill a missing `dispatch_key` with
`http-{uuid4().hex}`. ADR 0031 replays an Attempt only when the same key is
presented again, so a client that retried one logical request after a timeout
started a second Attempt and ran the adapter twice.

A host crash between `ExecutionService.begin` and `finish` left the Attempt
and the Task `running`. The FastAPI lifespan on `sdf_core.api.app` did not
settle those rows. `RuntimeController.recover` (ADR 0029) only restores a
runtime-event stream. It does not change Attempt or Task status. A later
dispatch then failed with `invalid task transition: running -> ready`,
because `running` has no edge to `ready`. The legal way out is
`running -> evaluating -> failed` (or `inconclusive`). `failed` and
`inconclusive` can both enter `ready`.

## Decision

### 1. Derived dispatch key

When `dispatch_key` is omitted, the server derives one. An explicit key is
stored unchanged.

The derived key is `keyless-` plus the SHA-256 hex digest of a canonical
JSON object (UTF-8, keys sorted, no extra whitespace). The object version
is `v: 1`. The fields are:

| Field | Value hashed | Why it is in the key |
| --- | --- | --- |
| `task_id` | Path task id | Dispatch keys are unique across tasks. The same body on two tasks must be two Attempts. |
| `fixture_dir` | Resolved fixture directory (`os.fspath` of the path `resolve_fixture` returns) | This is the directory the server will run. Two spellings of that directory are one execution. |
| `instructions` | Request value, schema default `complete the task` | The instructions are part of what the Attempt does. |
| `criterion_checks` | The map as sent, or null. Object key order is normalized. Check-id list order is kept. | The checks are part of the execution. List order is part of the request. |
| `validation_target_kind`, `validation_target_id` | Both values when both are present, otherwise both null | `run_task` ignores a target unless both halves are set. |
| `parent_attempt_id` | The request value, or null | A retry of a different parent is a different execution. |
| `model_tier` | `basic` when the request omits the tier and names no parent. Otherwise the wire value, and null means inherit the parent. | Omitted and explicit `basic` are the same first Attempt. Omitted on a retry means inherit, which is a different execution from an explicit tier. |

Not in the key: the caller's principal, the request id, timestamps, and the
fixture's file contents. A client retry of the same request must replay even
if the files under that directory changed. A deliberate new execution sends
a new explicit `dispatch_key`.

The same derived key is the ADR 0031 dispatch key. `ExecutionService.begin`
returns the stored Attempt, and `run` does not call the adapter again. That
includes a keyless POST after the Attempt is already terminal. A keyless
request is one logical execution, not a "start another one" API. The second
POST replays.

A new Attempt is an explicit new `dispatch_key`. When that Attempt continues
a finished one, the client also sends `parent_attempt_id`, which is the
existing retry rule.

`test_run_without_dispatch_key_gets_unique_claim_per_task_request` stays as
it is. It posts the same body to two tasks and requires two Attempt ids.
`task_id` is inside the hash, so the claims stay unique per task per request.

Changing `v` or the field list changes every derived key. That is an ADR
change, because stored Attempts would no longer match a repeated request.

### 2. Orphan recovery

On FastAPI lifespan startup the process:

1. Starts the lease refresher when the database can take a second connection.
2. Loads every Attempt in `running`.
3. Leaves an Attempt alone when an executor still holds it (below).
4. Moves each other Attempt `running -> failed`. `error_class` becomes
   `killed` when it was empty. `killed` is the existing taxonomy class for
   an execution that was terminated before it finished.
5. For each Task that owned a settled Attempt, if the Task is `running` and
   no Attempt of that Task is still `running`, moves the Task
   `running -> evaluating -> failed`.

No new state-machine transitions are added. A later `begin` can move that
Task `failed -> ready -> running`.

The function commits once. A second call finds no matching `running` Attempt
and writes nothing. Startup runs it, and the refresher runs it again after
each heartbeat interval, so a lease that expires after startup is settled
without another restart.

`RuntimeController.recover` is still not called from this path.

### 3. Live versus dead

Two columns on `attempts`, both nullable (migration `0027_attempt_executor_lease`):

- `executor_id`: the process that claimed the Attempt (`EXECUTOR_ID`, minted
  once per process).
- `executor_heartbeat_at`: the last time that process proved it was still
  executing.

`ExecutionService.begin` claims the lease in the same commit that moves the
Attempt to `running`. The claim is also kept in memory on the service object,
through a weak reference. It lasts while that object lives.

An Attempt is live when any of these holds:

- This process still has the service that claimed it, and `executor_id` is
  null or this process's id. Dropping the service (the host-crash test
  deletes it) makes the Attempt dead immediately, even if the heartbeat is
  fresh.
- `executor_id` names another process and `executor_heartbeat_at` is within
  `LEASE_TTL` (30 seconds).

An Attempt is dead when the heartbeat is null, when the heartbeat is older
than the lease, or when this process stamped it and no longer holds the
service. A null lease is how a row written before this migration, or a row
inserted without `begin`, appears: nobody is executing it.

The refresher rewrites `executor_heartbeat_at` every 10 seconds for Attempts
this process still holds. It does not start on the one-connection in-memory
SQLite used by hermetic tests (`StaticPool`), because a second thread cannot
share that connection. Same-process ownership still applies there. Production
PostgreSQL runs the refresher.

A hard kill of another OS process leaves a fresh heartbeat. The next startup
in a new process treats that Attempt as live until the lease expires. The
refresher then settles it. A same-process drop of the executor, which is how
the crash test models the death, is settled on that startup.

### 4. Invariants

- One keyless logical request creates one Attempt and invokes the adapter once,
  including when two requests overlap.
- A repeated keyless POST returns that Attempt and does not run the adapter,
  including after the Attempt is terminal.
- An explicit new `dispatch_key` can create another Attempt. A retry of a
  finished Attempt also sends `parent_attempt_id`.
- Two tasks with the same keyless body get two Attempts.
- Startup recovery is idempotent.
- A live executor is not failed. A stale or missing lease is.

## Consequences

- A rolling start of a new process reaps `running` Attempts that have no
  heartbeat. In-flight work from a process that heartbeats and is inside the
  lease is left alone. In-flight work from a build that does not write the
  lease looks dead and is failed.
- A crash of another process is retryable after the lease expires and the
  refresher or a later startup has run, not necessarily on the first startup
  if that startup happens inside the lease.
- Migration `0027_attempt_executor_lease` revises `0026_oracle_visibility`,
  so the Alembic graph stays a single head. The columns are nullable
  `ADD COLUMN` on SQLite and PostgreSQL. Downgrade drops them.

## How this is proved

Local tests (SQLite, no live provider):

- `tests/test_api.py::test_run_retry_without_dispatch_key_does_not_create_second_attempt`
  posts the same keyless body twice, requires one adapter call and one
  Attempt, then a new explicit key plus `parent_attempt_id` creates a second
  Attempt and runs again.
- `tests/test_api.py::test_run_without_dispatch_key_gets_unique_claim_per_task_request`
  keeps the per-task claim invariant.
- `tests/test_api.py::test_derived_dispatch_key_is_stable_per_task_and_logical_request`
  locks the hashed fields.
- `tests/test_api.py::test_concurrent_keyless_runs_create_one_attempt`
  overlaps two `ExecutionService.run` calls on one derived key.
- `tests/test_crash_recovery.py::test_host_crash_mid_run_is_recovered_and_task_is_retryable`
  drops the service after `begin` and enters the FastAPI lifespan. The orphan
  is `failed` or `cancelled`, and a new dispatch key runs once.
- `tests/test_crash_recovery.py::test_startup_recovery_is_idempotent` enters
  that lifespan twice.
- `tests/test_crash_recovery.py::test_live_executor_is_not_reaped_on_startup`
  keeps a held service and a fresh foreign heartbeat, and fails a stale one.
  Dropping the service and entering startup again fails the held Attempt and
  leaves the foreign one running.
- `tests/test_crash_recovery.py::test_in_memory_database_does_not_start_the_lease_refresher`
- `tests/test_migrations.py` upgrades an empty SQLite database to head
  `0027_attempt_executor_lease` and expects the two columns.

PostgreSQL and live provider runs are not part of this proof.
