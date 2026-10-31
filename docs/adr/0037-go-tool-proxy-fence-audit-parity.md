# ADR 0037: Go Tool Proxy fence keeps a local event log

Status: proposed (2026-10-31). Records why the Go Tool Proxy idempotency
fence does not move onto the Python `tool_audits` table in this change,
and the audit-parity step that is safe without that migration. It becomes
accepted when a maintainer accepts the implementing pull request. It does
not change Credential Mode, Attempt lifecycle, or Evidence provenance
(ADR 0002, ADR 0007, ADR 0026).

[ADR 0038](0038-go-attempt-evidence-mode.md) later stores `evidence_mode`
on this local fence, copied from the Go Attempt record at insert time.
The Consequences bullet that described the column as absent is the #109
rule. A shared PostgreSQL `tool_audits` writer is still not decided here.

## Context

[ADR 0026](0026-tool-proxy-actor-binding.md) (proposed) defines the Python
Tool Proxy audit trail on `tool_audits`:

- one `policy_decided` row per delivery, not unique on the action;
- `action_claimed`, `action_executed`, and `action_failed` unique per
  `(attempt_id, action_id, event)` via the partial index
  `WHERE event <> 'policy_decided'`;
- `principal` and `evidence_mode` on every row;
- the executor return value is not stored. A replay reports the terminal
  status without that value.

The Go fence shipped in #106 and #107 is a separate SQLite file
(`tool_outcomes`, one row per action, `value_json` kept so a restarted
process can return `Result.Value`). It recorded no `policy_decided` row
and no principal. [ADR 0035](0035-port-orchestration-core-to-go.md)
(proposed) says Go and Python meet through explicit interfaces and that
one writer owns Attempt state until cutover. No accepted ADR requires the
Go process to write Python `tool_audits`.

## Decision

1. **Do not share Postgres `tool_audits` in this change.** A shared fence
   would make the Go orchestrator a second writer of the Python audit
   table. That needs a schema both sides can use (executor value versus
   omission, `evidence_mode` read from `attempts`, the append-only
   trigger) and a cutover rule so two processes do not both decide the
   same action. Those choices are not settled by an accepted ADR.

2. **Bring the local fence closer to ADR 0026's event shape.**
   `MemoryStore` and `SQLiteStore` append observations:
   - `policy_decided` once per delivery, including denials and replays;
   - `action_claimed`, `action_executed`, and `action_failed` at most
     once per action.
   The same action still cannot run twice. A second partial unique index
   allows only one terminal event (`action_executed` or `action_failed`)
   per action. `principal` is stored on each row and is empty when the
   caller has none. An existing `tool_outcomes` file is copied into the
   event log on open and that table is then dropped.

3. **A shared Postgres fence is the next architecture leap, not this
   one.** Accept it only in a later ADR that picks one writer, whether
   `tool_audits` gains a replay value or Go stops replaying `value_json`,
   and how Go learns `evidence_mode` without inventing it.

## Consequences

- Operators still point `-outcome-db` at a local SQLite file. It is not
  `tool_audits`. Empty `OutcomeDBPath` stays `MemoryStore`.
- Restart replay still returns the executor value from `value_json` on
  the terminal event. That remains a Go-only deviation from Python.
- `evidence_mode` on each event is [ADR 0038](0038-go-attempt-evidence-mode.md).
  This ADR left the column off: the Go Attempt did not store a mode, and
  a hardcoded `synthetic` would mislabel a live attempt. ADR 0038 stores
  the mode on the Attempt and copies it onto rows at insert. It still
  does not write PostgreSQL `tool_audits`.
- The action context document is not copied onto the row. It already
  feeds `action_id`.
- There is no admin bypass. A principal that does not own the Attempt is
  denied and does not receive the owner's replay. The denial is its own
  `policy_decided` row.
- Downgrade to the #107 `tool_outcomes` table is not automatic. The
  terminal event is the fence. Removing the file and starting empty is
  the recovery.

## Alternatives considered

1. **Migrate the Go fence onto Postgres `tool_audits` now.** Rejected
   for this change. The shared schema and the second writer are a larger
   decision than the audit gap this ADR closes locally.
2. **Drop `value_json` to match Python replay.** Rejected. The
   orchestrator contract already returns the stored value after restart
   (`TestOutcomeDBPathReplaysStoredResult`). Removing it is a behaviour
   change, not a parity fix that is safe on its own.
