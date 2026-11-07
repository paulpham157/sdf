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
includes a keyless POST after the Attempt is already terminal, including
after crash recovery has moved it to `failed` with `error_class` `killed`.
The client receives that Attempt and the adapter does not run again. A
keyless request is one logical execution, not a "start another one" API.
A new execution still sends an explicit `dispatch_key` (and
`parent_attempt_id` when it continues the finished Attempt).

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
3. Leaves an Attempt alone when an executor still holds it (below), when
   its heartbeat was renewed after this read, or when its remote effects are
   not confirmed stopped (section 5).
4. Moves each other Attempt `running -> failed`. `error_class` becomes
   `killed` when it was empty. `killed` is the existing taxonomy class for
   an execution that was terminated before it finished. Lease expiry by
   itself is not that confirmation.
5. For each Task that owned a settled Attempt, if the Task is `running` and
   no Attempt of that Task is still `running`, moves the Task
   `running -> evaluating -> failed`.

No new state-machine transitions are added. A later `begin` can move that
Task `failed -> ready -> running`.

Each Attempt and Task write is compare-and-swap and checks `rowcount`.
The Attempt predicate is durable status plus the `executor_id` and
`executor_heartbeat_at` observed by the SELECT (`IS NOT DISTINCT FROM`,
so a null lease still matches). A heartbeat renewal committed after that
SELECT matches zero rows. The Task predicate is status only
(`running`, then `evaluating`). Recovery cannot overwrite an Attempt that
`finish` has already moved off `running`. A stale worker whose session still
says `running` cannot overwrite recovery or resurrect the Task:
`finish` and `_fail_attempt` use the owner fence, and they do not write the
Task unless they won the Attempt. Cancel keeps a status-only compare-and-swap
(ADR 0045); it does not send the lease snapshot.

The function commits once. A second call finds no matching `running` Attempt
and writes nothing. Startup runs it. Lifespan shutdown runs it again so
Attempts this process stamped, whose `ExecutionService` is already gone, are
settled immediately instead of waiting out the lease. The refresher runs it
after each heartbeat interval. `ExecutionService.begin` also runs it before
looking up the dispatch key or moving the Task to `ready`, so a `/run` after
the lease expires settles the orphan without another process restart.

`RuntimeController.recover` is still not called from this path.

### 3. Live versus dead

Two columns on `attempts`, both nullable (migration `0027_attempt_executor_lease`):

- `executor_id`: the process that claimed the Attempt. The id is
  `{pid}-{uuid}`, minted for this OS process. Lifespan startup remints when
  the id's pid is not this process, and `os.register_at_fork(after_in_child=...)`
  remints in a forked worker and drops the copied owner map. Workers therefore
  do not share one id with an empty owner map.
- `executor_heartbeat_at`: the last time that process proved it was still
  executing.

`ExecutionService.begin` claims the lease in the same commit that moves the
Attempt to `running`. The claim is also kept in memory on the service object,
through a weak reference. It lasts while that object lives.

An Attempt is live when any of these holds:

- This process still has the service in its owner map and `executor_id` is
  this process's id. That hold is authoritative here even when the heartbeat
  is older than the lease, so a dead refresher thread does not reap a run
  this process is still executing.
- `executor_id` names another process and `executor_heartbeat_at` is within
  `LEASE_TTL` (30 seconds).

An Attempt is dead when the heartbeat is null, when another process's
heartbeat is older than the lease, or when this process stamped it and no
longer holds the service (dead immediately, even if the heartbeat is fresh).
A null lease is how a row written before this migration, or a row inserted
without `begin`, appears: nobody is executing it.

The heartbeat refresher runs under a supervisor that starts it again if the
loop thread dies. Shutdown sets a stop flag and joins that supervisor.
Heartbeats are a conditional update: `status` is still `running` and
`executor_id` is this process. A failed row is not refreshed.

The refresher rewrites `executor_heartbeat_at` every 10 seconds for Attempts
this process still holds. It does not start on the one-connection in-memory
SQLite used by hermetic tests (`StaticPool`), because a second thread cannot
share that connection. Same-process ownership still applies there. Production
PostgreSQL runs the refresher.

A hard kill of another OS process leaves a fresh heartbeat. The next startup
in a new process (a new `EXECUTOR_ID`) treats that Attempt as live until the
lease expires. During that window a new `/run` with a different dispatch key
still sees the Task `running` and cannot enter `ready`. After the lease
expires, a synthetic in-process Attempt can be settled and a new explicit
dispatch can proceed. An Attempt that is `live`, or that still has a
registered remote effect, cannot (section 5). A graceful lifespan shutdown
settles this process's un-owned synthetic Attempts immediately. It does not
settle one whose remote effects are unconfirmed.

Crash-recovery tests mint a fresh `EXECUTOR_ID` and expire the previous
heartbeat, which is how a new process observes a dead neighbor.

### 4. Invariants

- One keyless logical request creates one Attempt and invokes the adapter once,
  including when two requests overlap.
- A repeated keyless POST returns that Attempt and does not run the adapter,
  including after the Attempt is terminal and after recovery has marked it
  `failed`/`killed`.
- An explicit new `dispatch_key` can create another Attempt. A retry of a
  finished Attempt also sends `parent_attempt_id`.
- Two tasks with the same keyless body get two Attempts.
- Startup recovery is idempotent.
- A live executor is not failed. A stale or missing lease fails only an
  Attempt whose remote effects are confirmed stopped (section 5). A heartbeat
  renewed after the SELECT is not failed.
- A forked worker remints its executor id before recovery, so a sibling's
  fresh Attempt stays `running`.
- A held local owner stays `running` when the heartbeat is older than the
  lease and the refresher is stopped. Dropping that owner lets recovery fail
  a synthetic Attempt. Dropping it does not fail a `live` Attempt or one
  whose registered effect has not confirmed stop.
- `finish` checks the owner fence before it closes the revision or captures
  artifacts. A reaped Attempt gets no Artifact row, no Evidence row, no
  artifact graph node, and no capture file.
- `_fail_attempt` uses the owner fence. If that compare-and-swap loses, it
  does not move the Task, including when another Attempt of the Task is
  still `running`.
- Coordinator cancel moves the Task with compare-and-swap
  (`running` to `evaluating` to `inconclusive`). A Task recovery already
  set to `failed` stays `failed`.
- An explicit `dispatch_key` that starts with `keyless-` is rejected with
  422. Heartbeat refresh does not change a failed Attempt.

## Consequences

- A rolling start of a new process reaps `running` Attempts that have no
  heartbeat. In-flight work from a process that heartbeats and is inside the
  lease is left alone. In-flight work from a build that does not write the
  lease looks dead and is failed.
- A hard crash of another process is retryable after the lease expires only
  when that Attempt is not `live` and has no registered remote effect still
  outstanding. A `live` Attempt stays `running` and blocks a new dispatch.
  A startup inside the lease leaves every Attempt alone. Graceful shutdown
  does not wait on synthetic in-process work. It does not declare a live
  sandbox stopped.
- A keyless retry of a recovered (`failed`/`killed`) Attempt replays that
  Attempt. Operators who want a new execution after a crash send a new
  explicit `dispatch_key` and `parent_attempt_id`.
- Migration `0027_attempt_executor_lease` revises `0026_oracle_visibility`.
  The columns are nullable `ADD COLUMN` on SQLite and PostgreSQL. Downgrade
  drops them.

## 5. Lease expiry is not quiescence

The earlier text of this ADR treated a stale lease as a dead execution and
then allowed a new explicit dispatch. That is too weak. A lease timeout does
not prove that local or remote work has stopped causing effects.

Recovery may fail the Attempt and unstick the Task only when all of these hold:

- The executor is not live under section 3, judged from the SELECT snapshot.
- The UPDATE still sees that snapshot's `executor_id` and
  `executor_heartbeat_at`.
- Remote effects are confirmed stopped. `evidence_mode` is not that proof.
  A synthetic row can already have called the provider. Confirmation is:
  - `remote_effect` is NULL (the provider create call was not entered) and
    the Attempt is not `live`, and no process-local effect is still
    outstanding; or
  - `remote_effect` is `stopped` because this process confirmed the provider
    resource is gone; or
  - a process-local `confirm_stopped` callback returns True while the durable
    value is not `uncertain`.

Otherwise the Attempt and its Task stay `running`. `ExecutionService.begin`
returns the existing Attempt for the same dispatch key and does not run the
adapter again. A new dispatch key raises, and `POST /tasks/{id}/run` returns
409. No new state-machine transition is added.

This process does not call the sandbox provider during recovery. It does not
list E2B or Daytona sandboxes, and it does not kill by id from a restarted
process. `LiveRunAdapter.confirm_live` still records that a sandbox id was
observed (`evidence_mode=live`). That write is later than the create fence. A
`live` row that is not `stopped` stays `running`.

`E2BHerdrTransport._ensure_sandbox` and `DaytonaHerdrTransport._ensure_sandbox`
commit `remote_effect=uncertain` through `before_provider_create` immediately
before the provider factory, and only on a fresh create. `LiveRunAdapter` and
a coordinator worker sandbox (`E2BAgentSandbox.bind_attempt`) supply that
callback. `uncertain` is not cleared because the factory raised: a timeout can
mean the provider created a sandbox the client never observed. `stopped` is
committed only after a teardown that both saw a sandbox id and reported
success. Until then a new dispatch stays refused. There is still no reaper, so
a crashed create blocks that Task until some later process can confirm stop.
That is the availability cost of failing closed.

A fake in-process adapter never enters `_ensure_sandbox`. Its `remote_effect`
stays NULL, so a host crash between `begin` and `run` remains retryable.

Remaining limitations, which this ADR does not claim to close:

- The coordinator lead sandbox has no Attempt, so this fence does not cover it.
- `E2BContainmentBackend` provisions through the `e2b-box` CLI, not
  `_ensure_sandbox`. An evaluator box is outside this Attempt fence.
- Any new provider create that skips `before_provider_create` is not fenced.
  Evidence mode will not catch it while the row is still synthetic.
- After a hard crash, `uncertain` stays set. This process will not list or
  kill provider sandboxes to clear it.
- Recovery does not promise exactly-once behavior for arbitrary external side
  effects.
- The lease clock is still the application clock (the open question below).
- A process-local callback exists only in the process that registered it.

## Open question: lease clock

The lease uses the application clock (`datetime.now(timezone.utc)` in the
API process), not the database clock. Two processes, or a process and the
database, can disagree. A clock that jumps forward makes a live heartbeat
look stale, and recovery can reap a neighbor that is still working. A clock
that jumps backward keeps a dead executor looking fresh until real time
catches up. One alternative is to stamp and compare `executor_heartbeat_at`
with the database clock (`now()` or `clock_timestamp()` on PostgreSQL), so
every worker shares one clock. This pass does not change the clock source
or the 30 second TTL. The owner should decide whether the database clock
replaces the application clock.

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
  drops the service after `begin`, mints a new `EXECUTOR_ID`, expires the
  previous heartbeat, and enters the FastAPI lifespan. The orphan is `failed`
  or `cancelled`, and a new dispatch key runs once.
- `tests/test_crash_recovery.py::test_startup_recovery_is_idempotent` enters
  that lifespan twice.
- `tests/test_crash_recovery.py::test_live_executor_is_not_reaped_on_startup`
  keeps a held service and a fresh foreign heartbeat, and fails a stale one.
  A simulated restart with a fresh heartbeat leaves the previous Attempt
  running until the lease expires; after expiry the next startup fails it
  and leaves the foreign one running.
- `tests/test_crash_recovery.py::test_expired_lease_is_settled_on_next_dispatch_without_restart`
  plants an expired orphan after startup; the next `/run` settles it and
  dispatches.
- `tests/test_crash_recovery.py::test_recovery_does_not_overwrite_a_completed_attempt`
  and `test_stale_finish_does_not_overwrite_recovery` lock both CAS races.
- `tests/test_crash_recovery.py::test_keyless_retry_after_recovered_failure_replays_failed_attempt`
  posts the same keyless body after recovery and requires the killed Attempt
  back, with no adapter run.
- `tests/test_crash_recovery.py::test_in_memory_database_does_not_start_the_lease_refresher`
- `tests/test_crash_recovery.py::test_forked_worker_does_not_reap_a_siblings_fresh_attempt`
  runs the after-fork remint with an empty owner map and leaves the sibling Attempt `running`.
- `tests/test_crash_recovery.py::test_held_owner_survives_a_stale_heartbeat_when_the_refresher_is_stopped`
  stops the refresher, ages the heartbeat past the lease, and still leaves a held Attempt `running`. Dropping the owner then lets recovery fail it.
- `tests/test_crash_recovery.py::test_finish_does_not_overwrite_a_reaped_attempt`
  finishes after recovery and requires no artifact row, evidence row, artifact graph node, or capture file.
- `tests/test_crash_recovery.py::test_fail_attempt_does_not_move_a_task_with_a_live_sibling`
- `tests/test_crash_recovery.py::test_stale_coordinator_cancel_leaves_a_failed_task`
- `tests/test_crash_recovery.py::test_heartbeat_refresh_does_not_touch_a_failed_attempt`
- `tests/test_crash_recovery.py::test_new_dispatch_does_not_overlap_a_sandbox_left_running_by_lease_expiry`
  keeps a fake provider sandbox executing after the host lease expires. The
  same key replays. A new key is refused while that sandbox is still executing.
- `tests/test_crash_recovery.py::test_recovery_does_not_fail_a_lease_renewed_after_the_select`
  renews the heartbeat after the SELECT and requires the Attempt to stay running.
- `tests/test_crash_recovery.py::test_live_attempt_is_not_reaped_or_retried_without_a_confirmed_stop`
- `tests/test_crash_recovery.py::test_termination_failure_leaves_the_attempt_running`
- `tests/test_crash_recovery.py::test_confirmed_stop_allows_settle_and_a_new_dispatch`
- `tests/test_crash_recovery.py::test_stale_heartbeat_with_the_owner_still_alive_refuses_a_new_dispatch`
- `tests/test_crash_recovery.py::test_crash_before_provider_create_still_allows_a_new_dispatch`
- `tests/test_crash_recovery.py::test_crash_during_provider_create_refuses_a_new_dispatch`
- `tests/test_crash_recovery.py::test_sandbox_returned_before_confirm_live_refuses_a_new_dispatch`
- `tests/test_crash_recovery.py::test_unknown_provider_outcome_refuses_a_new_dispatch`
- `tests/test_crash_recovery.py::test_confirmed_provider_stop_allows_a_new_dispatch`
- `tests/test_crash_recovery.py::test_live_run_adapter_commits_the_create_fence_before_the_transport_returns`
- `tests/test_crash_recovery.py::test_worker_sandbox_start_passes_the_create_fence`

The create-window tests drive `E2BHerdrTransport._ensure_sandbox` or
`DaytonaHerdrTransport._ensure_sandbox` with an injected factory. They do not
call E2B or Daytona.
- `tests/test_api.py::test_explicit_keyless_prefix_is_rejected` expects 422.
- `tests/test_migrations.py` upgrades an empty SQLite database to head
  `0027_attempt_executor_lease` and expects the two columns.

No live provider sandbox was killed or listed to prove section 5. The provider
boundary there is what the code refuses to do, not a claim that E2B or Daytona
was observed. PostgreSQL is not required for these hermetic tests.
