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

The first append creates the directory (mode 0700), the file (mode 0600), the
table, and the genesis entry. `sdf audit` on a path that does not exist
reports an empty ledger and creates nothing.

### Journal mode, and what a read may touch

The ledger uses SQLite's WAL mode, set by the first append. Reads open the
file read-only (`mode=ro`), and they never change the ledger file.

- **Why WAL.** In the older rollback-journal mode, a writer that crashed
  mid-append leaves a *hot journal*. The next reader has to roll it back,
  which is a write, so a read-only open fails. In WAL mode a reader skips an
  unfinished transaction without writing. `verify` works after a crash.
- **The trade-off.** A WAL reader may create the `ledger.db-wal` and
  `ledger.db-shm` sidecar files next to the ledger if they are missing. That
  changes the directory, not the ledger file. In a read-only directory with no
  sidecars, and with no `-wal` file (so every committed entry is in the main
  file), the reader opens the file with `immutable=1` instead.
- **Older ledgers.** A ledger an earlier build wrote in rollback-journal mode
  stays readable. The next append switches it to WAL. If such a file holds a
  hot journal, a read reports an I/O error (`sdf audit` exits 3) and leaves
  the journal in place. The next append rolls it back and switches the file
  to WAL.
- **Concurrency.** Appends take `BEGIN IMMEDIATE`, so two writers are
  serialized. Readers do not block writers. Every connection waits up to 5
  seconds on `SQLITE_BUSY` (`busy_timeout`) before failing. Commits use
  `synchronous=FULL`.

## What is stored

One table, `ledger_entries`. Each row is one entry, payload included:

| Column | Meaning |
| --- | --- |
| `seq` | 0 for genesis, then 1, 2, 3 with no gaps |
| `kind` | Payload schema name, such as `sdf.task/v1` |
| `id` | Any non-empty, valid UTF-8 string: a ULID, or a task id a person chose |
| `payload` | The canonical bytes, exactly as the writer passed them |
| `hash` | Payload hash |
| `prev_hash` | `entry_hash` of the entry before |
| `entry_hash` | Hash of this entry's own fields |
| `chained_at` | UTC time the entry was written |

`(kind, id)` is unique. Appending the same `(kind, id)` with the same hash
again returns the stored entry. A different hash for a stored `(kind, id)` is
`ErrConflict`. Nothing updates or deletes a row.

`Append` returns `ErrInvalid` for:

- a `kind` that is empty, is not valid UTF-8, or contains a control character.
  A kind with `\n` could share a payload hash with a different kind/payload
  split (`"a"` + `"b\n{}"` versus `"a\nb"` + `"{}"`);
- a `kind` that starts with `sdf.ledger.`. That prefix is reserved, and only
  the genesis entry uses it;
- an `id` that is empty or not valid UTF-8.

`Get` checks the row it reads: its fields, its payload hash, and its entry
hash. A row that fails is `ErrCorrupt`, wrapped with the kind and id. `Get`
does not walk the chain; `sdf audit verify` does.

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

- Keys are in that (sorted) order, with no whitespace.
- `seq` is a JSON integer.
- `chained_at` is UTC with exactly nine fraction digits and a `Z`.
- Strings escape only `"`, `\`, and control characters: `\b \f \n \r \t`, and
  otherwise `\u00xx` in lowercase hex.
- JSON cannot carry invalid UTF-8. `"a\xffb"` and `"a\uFFFDb"` would encode
  the same, so the ledger never writes an invalid-UTF-8 field, and it treats a
  stored one as corrupt rather than hashing it.

Append rejects a hash that is not the hash of `kind` and the payload
(`ErrHashMismatch`).

Genesis is seq 0: kind `sdf.ledger.genesis/v1`, id `genesis`, payload
`{"schema":"sdf.ledger/v1"}`, and a `prev_hash` of `sha256:` followed by 64
zeros.

`internal/ledger/ledger_test.go` has known-answer values computed outside Go,
for checking a writer in another language.

## sdf audit

```bash
sdf audit verify [--ledger PATH] [--json] [--require-nonempty]
                 [--expect-head HASH | --expect-head-file PATH]
sdf audit head   [--ledger PATH] [--json]
```

`verify` reads every entry in seq order. It checks that genesis is exactly
the entry above, and that each row's fields are ones the ledger would write
(TEXT columns, valid UTF-8, no control characters in `kind`, the reserved
prefix, the `chained_at` form). It recomputes each payload hash and entry
hash, checks that each `prev_hash` links to the entry before, and checks that
no `(kind, id)` appears twice. It reports the first failure:

- `modified`: a payload or an entry's own field no longer hashes to what was
  chained, a field holds something the ledger would never write, or genesis
  is not exact.
- `missing`: a seq is absent (a deleted row), or genesis is gone.
- `gap-reordered`: a `prev_hash` does not link to the entry before. A row was
  moved or inserted, or an earlier entry was rewritten along with its hashes.
- `duplicate`: a `(kind, id)` is stored twice.

An intact ledger prints the head seq and its `entry_hash`.

An empty or absent ledger is intact, so a mistyped path passes. Add
`--require-nonempty` to make an absent or empty ledger exit 1.

A chain with its newest entries deleted is still a valid chain. To catch that,
pass the head you pinned earlier:

- `--expect-head sha256:…` takes the hash itself.
- `--expect-head-file PATH` reads it from a file. The file can hold the bare
  hash, the line `sdf audit head` prints, or `sdf audit head --json` output.

The two flags cannot be used together. An exact match is required: after a
legitimate append, pin the new head. Verify exits 1 in each of these cases:

- The current head's `entry_hash` differs from the expected one. It prints
  both values.
- The ledger is empty. It prints `actual none`.
- The chain is broken. It says the stored head proves nothing.

`--json` reports `ok` (every check), `chain_ok` (the chain alone),
`require_nonempty`, `expected_head`, and `head_match`.

`head` verifies first. It prints `seq N entry_hash sha256:…` only for an
intact, non-empty ledger. Otherwise it prints nothing to stdout. Use it to
copy the head somewhere else.

### Exit codes

| Code | `verify` | `head` |
| --- | --- | --- |
| 0 | Intact, and every requested check passed | Head printed |
| 1 | Broken chain, `--require-nonempty` on an empty ledger, or `--expect-head` mismatch | Broken or empty ledger |
| 2 | Usage: unknown flag, missing value, both `--expect-head` flags, or an expected head that is not `sha256:` + 64 lowercase hex | Usage |
| 3 | I/O: the ledger cannot be opened or read (not a SQLite file, a directory, permissions, a hot journal in an old rollback-mode file), no default path, or an unreadable `--expect-head-file` | I/O, as for `verify` |

### Pinning the head in CI (planned)

The verifier's GitHub Action is not part of this repository yet. This is how
it is meant to use these commands.

Keep the head somewhere the agent cannot write. After the CI job appends, it
runs `sdf audit head --json` and stores the output as a CI artifact or on a
protected ref. The next job, or anyone auditing later, runs:

```bash
sdf audit verify --require-nonempty --expect-head-file pinned-head.json
```

This passes only if the local file still ends at the entry CI recorded.

## What this does not protect against

The chain makes tampering **detectable, not prevented**, and only within
limits:

- The agent runs as the same user as sdf, so it can write this file. It can
  rewrite any entry and recompute every hash after it. It can also delete the
  last entries. The result is a valid chain, and a plain `sdf audit verify`
  passes.
- That is only caught by comparing against a head hash stored **somewhere the
  agent cannot write** (`--expect-head`). Without such a copy, a rewritten
  ledger cannot be told apart from the real one.
- The intended trust anchor is CI (planned): the verifier's GitHub Action
  runs outside the agent's machine and records the head there. Until that
  exists, the local file is a working copy, not proof.
- There is no signature and no HMAC. Signing entries or exported heads with a
  key outside the agent's reach is future work.
