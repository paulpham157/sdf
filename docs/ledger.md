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
  seconds on `SQLITE_BUSY` (`busy_timeout`). An append that still gets
  `SQLITE_BUSY` or `SQLITE_LOCKED` has rolled back, and it is retried with
  backoff for up to 10 seconds. The handle's lock is released between
  attempts, so other calls on the same handle are not blocked while it waits.
  Commits use `synchronous=FULL`.

## What is stored

One table, `ledger_entries`. Each row is one entry, payload included:

| Column | Meaning |
| --- | --- |
| `seq` | 0 for genesis, then 1, 2, 3 with no gaps |
| `kind` | Payload schema name, such as `sdf.task/v1` |
| `id` | A ULID, or a task id a person chose, under the id rules below |
| `payload` | The canonical bytes, exactly as the writer passed them |
| `hash` | Payload hash |
| `prev_hash` | `entry_hash` of the entry before |
| `entry_hash` | Hash of this entry's own fields |
| `chained_at` | UTC time the entry was written |

`(kind, id)` is unique, and so is `(kind, id)` under ASCII case-folding (see
the id rules). Appending the same `(kind, id)` with the same hash again
returns the stored entry. A different hash for a stored `(kind, id)` is
`ErrConflict`. Nothing updates or deletes a row.

The case rule is checked inside the append transaction, using a plain
(non-unique) index on `(kind, id COLLATE NOCASE)`. It is not a UNIQUE
index, because a ledger written by an earlier build may already hold two
such ids, and a unique index could not be built there. Such a ledger still
opens and takes appends, and `verify` reports the pair as `duplicate`.

`Append` returns `ErrInvalid` for:

- a `kind` that is empty, is not valid UTF-8, or contains a control character.
  A kind with `\n` could share a payload hash with a different kind/payload
  split (`"a"` + `"b\n{}"` versus `"a\nb"` + `"{}"`);
- a `kind` that starts with `sdf.ledger.`. That prefix is reserved, and only
  the genesis entry uses it;
- an `id` that breaks the id rules below. That error is `ErrInvalidID`, which
  wraps `ErrInvalid`.

### Id rules

An id matches `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$` (`ledger.IDPattern`,
checked by `ledger.ValidID`). This is the same rule as the verifier's
`task.ValidID`, and a test pins the two together. In words:

- 1 to 128 bytes (`ledger.MaxIDLen`);
- ASCII letters, digits, `.`, `_`, and `-` only. That excludes whitespace,
  control characters, path separators (`/`, `\`), `:`, and anything
  non-ASCII, so an id is always valid UTF-8;
- it starts with a letter or digit. So no leading `-` (it can't be read as a
  flag), no leading `.` (not hidden, and never `.` or `..` on its own);
- `..` inside an id, as in `a..b`, is allowed. With no separator it cannot
  leave a directory;
- case is preserved. A canonical ULID is uppercase
  (`01JD5Z9X8Y7W6V5T4S3R2Q1P0N`) and is stored and returned as it is, and
  `Get` matches an id exactly;
- within one kind, two ids may not differ only in ASCII case. After
  `TASK-1`, appending `task-1` or `Task-1` is `ErrConflict`, and the error
  names both ids. That holds whatever the payload is. Appending `TASK-1`
  again with the same hash is still idempotent. Different kinds do not
  collide. This keeps ids safe as file names on case-insensitive file systems
  (macOS and Windows defaults).

`Get` matches an id exactly, case included: after appending `TASK-1`,
`Get(kind, "task-1")` is `ErrNotFound`. Only `Append` folds case, and only
to refuse a case variant. `Get` returns `ErrInvalidID` for a bad id, and
`ErrInvalid` for a kind that is empty, not UTF-8, or has a control
character, in both cases without a lookup. `Verify` reports a stored row
whose id or kind breaks these rules as `modified`.

`Get` checks the row it reads: its fields, its payload hash, and its entry
hash. A row that fails is `ErrCorrupt`, wrapped with the kind and id. `Get`
does not walk the chain; `sdf audit verify` does.

A file that is not a readable SQLite database (garbage, truncated, damaged:
`SQLITE_NOTADB` or `SQLITE_CORRUPT`) gives an error wrapping
`ErrCorruptFile`. Other failures to open or read (permissions, a missing
directory, EIO) are returned as they are.

Every kind, id, and other stored string in an error message or in `sdf
audit` output is quoted (`%q`), or passed through `ledger.Safe`, which
quotes a string with any non-printable character. A stored row cannot write
a terminal escape sequence to the screen (for example `\x1b[2K\r`, which
erases a line). `--json` output escapes control characters as JSON does.

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
zeros. Its `chained_at` is set by the first append, so no fixed value can be
checked. Genesis and seq 1 are written in one transaction with the same
timestamp. `verify` checks that genesis has the `chained_at` form and is not
later than seq 1. (A ledger from an earlier build has a genesis a moment
earlier than seq 1, which also passes.)

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
- `duplicate`: a `(kind, id)` is stored twice, or two ids of one kind differ
  only in ASCII case.

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
| 1 | Broken chain, a file that is not a readable ledger database (garbage, truncated, damaged), `--require-nonempty` on an empty ledger, or `--expect-head` mismatch | Broken, unreadable-as-a-database, or empty ledger |
| 2 | Usage: unknown flag, missing value, both `--expect-head` flags, or an expected head that is not `sha256:` + 64 lowercase hex | Usage |
| 3 | I/O: the ledger cannot be opened or read (a directory, permissions, EIO, a hot journal in an old rollback-mode file), no default path, or an unreadable `--expect-head-file` | I/O, as for `verify` |

For a file that is not a ledger database, and for exit 3, the message goes
to stderr and nothing goes to stdout, `--json` included.

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
