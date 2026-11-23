# Ledger

The ledger is an append-only, hash-chained record of canonical payloads:
tasks, attempts, evidence, verdicts. A verifier writes to it through
`internal/ledger`. `sdf audit verify` and `sdf audit head` read it.

The collector does not write to the ledger. Telemetry stays in
`sdf-telemetry.sqlite`. A payload may name a session id or a commit SHA, but
the ledger never copies telemetry.

## Where it lives

One SQLite file, separate from the telemetry store. The path is the first of:

1. `--ledger PATH` on the command,
2. `$SDF_LEDGER`,
3. `$XDG_DATA_HOME/sdf/ledger.db` (when `XDG_DATA_HOME` is an absolute path),
4. `~/.local/share/sdf/ledger.db`.

Opening the ledger and reading it never writes. `sdf audit` on a path that
does not exist reports an empty ledger and creates nothing. The first append
creates the directory (mode 0700), the file (mode 0600), the table, and the
genesis entry.

## What is stored

One table, `ledger_entries`. Each row is one entry, payload included:

| Column | Meaning |
| --- | --- |
| `seq` | 0 for genesis, then 1, 2, 3 with no gaps |
| `kind` | Payload schema name, such as `sdf.task/v1` |
| `id` | Any non-empty UTF-8 string: a ULID, or a task id a person chose |
| `payload` | The canonical bytes, exactly as the writer passed them |
| `hash` | Payload hash |
| `prev_hash` | `entry_hash` of the entry before |
| `entry_hash` | Hash of this entry's own fields |
| `chained_at` | UTC time the entry was written |

`(kind, id)` is unique. Appending the same `(kind, id)` with the same hash
again returns the stored entry. A different hash for a stored `(kind, id)` is
`ErrConflict`. Nothing updates or deletes a row.

## Encoding

The writer canonicalizes its payload. The verifier uses JCS (RFC 8785). The
ledger treats the payload as opaque bytes and never reserializes it.

```
hash       = "sha256:" + hex(sha256(kind + "\n" + payload))
entry_hash = "sha256:" + hex(sha256("sdf.ledger.v1.entry\n" + J))
```

`hex` is lowercase. `J` is the RFC 8785 serialization of this object:

```json
{"chained_at":"2026-11-23T05:40:00.000000000Z","hash":"sha256:…","id":"…","kind":"sdf.task/v1","prev_hash":"sha256:…","seq":1}
```

Keys are in that (sorted) order with no whitespace. `seq` is a JSON integer.
`chained_at` is UTC with exactly nine fraction digits and a `Z`. Strings
escape only `"`, `\`, and control characters (`\b \f \n \r \t`, otherwise
`\u00xx` in lowercase hex).

Append rejects a hash that is not the hash of `kind` and the payload
(`ErrHashMismatch`).

Genesis is seq 0: kind `sdf.ledger.genesis/v1`, id `genesis`, payload
`{"schema":"sdf.ledger/v1"}`, and a `prev_hash` of `sha256:` followed by 64
zeros.

`internal/ledger/ledger_test.go` has known-answer values computed outside Go,
for checking a writer in another language.

## sdf audit

```bash
sdf audit verify [--ledger PATH] [--json]
sdf audit head   [--ledger PATH] [--json]
```

`verify` reads every entry in seq order. It recomputes each payload hash and
each entry hash, and checks that each `prev_hash` links to the entry before.
It reports the first failure:

- `modified`: a payload or an entry's own field no longer hashes to what was
  chained.
- `missing`: a seq is absent (a deleted row), or genesis is gone.
- `gap-reordered`: a `prev_hash` does not link to the entry before. A row was
  moved or inserted, or an earlier entry was rewritten along with its hashes.

Exit 0 means intact (an empty or absent ledger is intact). Exit 1 means broken,
or the file could not be read. Exit 2 means a usage error. An intact
ledger prints the head seq and its `entry_hash`.

`head` verifies first. It prints `seq N entry_hash sha256:…` only for an
intact, non-empty ledger. Otherwise it exits 1 and prints nothing to stdout.
Use it to copy the head somewhere else.

## What this does not protect against

The chain makes tampering **detectable, not prevented**, and only within
limits:

- The agent runs as the same user as sdf, so it can write this file. It can
  rewrite any entry and recompute every hash after it. It can also delete the
  last entries. The result is a valid chain, and `sdf audit verify` passes.
- That is only caught by comparing against a head hash stored **somewhere the
  agent cannot write**. Without such a copy, a rewritten ledger cannot be told
  apart from the real one.
- The real trust anchor is CI: the verifier's GitHub Action runs outside the
  agent's machine and records the head there. The local file is a working
  copy, not proof.
- There is no signature and no HMAC. Signing entries or exported heads with a
  key outside the agent's reach is future work.
