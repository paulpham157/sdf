# Remote-effect recovery

Lease expiry does not mean a provider sandbox has stopped. This note is the
operator view of the contract in ADR 0041 section 5. It is not a claim that
every external side effect is exactly-once.

## What you can observe

`GET /attempts/{attempt_id}/remote-effects` (operator) returns:

- `remote_effect`: `null` (no provider create was entered), `uncertain`, or `stopped`
- `sandboxes`: one row per create that was entered
  - `phase=creating` and `sandbox_id=null`: the create call started and no id was stored. This cannot be marked gone.
  - `phase=identified`: an id was stored. It is not gone until a separate lookup says so.
  - `phase=absent`: that id was looked up and was not found after reconcile tried to kill it.

A new explicit `dispatch_key` is refused while any row for that Task is not `absent`, even if the Attempt is already `failed`, `cancelled`, or `completed`. The same `dispatch_key` still replays the existing Attempt.

A worker retry that already holds a sandbox id records that id as `identified`. It does not open a `creating` row with a null id. Reconnect of an existing id does not count as another create.

After a kill that this process believes succeeded, a separate lookup may set `absent`. That lookup does not run in the hermetic suite and does not run when the provider key is unset. The row then stays `identified` until reconcile. A kill or delete return value is not the check.

The lease that decides whether a foreign executor is still live is the database clock (`clock_timestamp()` on PostgreSQL, `CURRENT_TIMESTAMP` on SQLite). One API process being ahead or behind does not reap or spare that executor. The 30 second TTL is unchanged. Other timestamps in the product still use the application clock.

## Reconcile

`POST /attempts/{attempt_id}/remote-effects/reconcile` (admin) takes no "mark stopped" flag.

For each `identified` row it asks the configured provider to kill that id, then calls a separate lookup. Only a lookup that reports the id missing sets `phase=absent`. `Sandbox.kill` or `Daytona.delete` returning is not that lookup.

E2B absence is `Sandbox.get_info` raising not-found. Any other error leaves the row in place.

Daytona absence is `Daytona.get` raising not-found (HTTP 404) or returning state `destroyed`. `stopped` and `paused` can be started again, so they stay `identified`. `unknown`, `error`, `destroying`, an authentication failure, and any other error also stay `identified`. Delete is requested with `wait=False`; acceptance of that request is not absence.

When every row is `absent`, `remote_effect` becomes `stopped` and lease recovery may settle a still-`running` Attempt. A second reconcile is a no-op.

Hermetic tests set `SDF_TEST_HERMETIC=1` and do not call a provider. A deployment with neither `E2B_API_KEY` nor `DAYTONA_API_KEY` has no lookup: reconcile returns `blocked=probe_unavailable` and changes nothing. A key for one provider does not prove the other provider's id is gone.

## Unknown create

If a row is `creating` with a null id, reconcile does not delete it and does not set `stopped`. A timeout can leave a sandbox that this process never named. An empty provider list is not treated as proof, because a sandbox can exist without the metadata we would have filtered on.

Optional body:

```json
{"observe_sandbox_id": "sbx_...", "observe_provider": "e2b"}
```

This adds another `identified` row for an id you found in the provider console. Reconcile then kills and looks up that id. The original `creating` row stays. The Attempt stays blocked. Do not `UPDATE` the row to `absent` or set `remote_effect` to `stopped` by hand.

## When to stop

Stop reconciling an Attempt when:

- every sandbox row is `absent` and `remote_effect` is `stopped`, or
- a `creating` row still has a null `sandbox_id` (`blocked=unknown_create`), or
- the lookup is unavailable or still reports the id present (`probe_unavailable` or `still_present`).

Repeating reconcile is safe. It does not create sandboxes. It does not clear an unknown create.

## Rollout

Apply migrations through `0032_attempt_remote_sandboxes` (it revises `0031_evidence_mac_v2`) before serving traffic that creates provider sandboxes. Rows written before that table existed have no sandbox inventory. A `live` Attempt with no sandbox rows still stays `running` on lease expiry. It is not marked `stopped` for lack of rows.

In-process fake adapters never open a sandbox row. A crash before any provider call remains retryable.

## Not solved

- A sandbox created and then lost with no id. Reconcile does not list E2B or Daytona to guess which sandbox it was. An empty list is not proof.
- The coordinator lead sandbox, which has no Attempt.
- No live E2B or Daytona sandbox was created for this behavior. The Daytona rules above are the SDK's documented `get` and state values, executed here against those result objects, not against a Daytona account. Kill or delete success is not proof.
