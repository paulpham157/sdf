# File permissions

sdf's SQLite files hold session ids, token and cost data, tool names and,
with `sdf serve --store-content`, prompt, response and tool text. They are
kept readable by their owner only. This covers the telemetry store
(`sdf-telemetry.sqlite`, or `--db`) and the ledger (see
[ledger.md](ledger.md)).

!!! warning "Owner-only"
    sdf creates the store and the ledger at mode 0600 and strips group
    and other access from an existing file. When the mode cannot be
    changed, it warns and continues, so a read-only command still runs.

- **New files.** sdf creates the database file with mode 0600 before SQLite
  opens it. On unix SQLite creates the `-wal`, `-shm` and `-journal` files
  beside it with the main file's mode, whatever the umask, so they are 0600
  too.
- **New directories.** A missing parent directory of the database is created
  with mode 0700.
- **Existing files.** When sdf opens an existing database (`sdf serve`,
  `sdf summary`, `sdf redact`, the decision commands, the ledger commands)
  that group or others can access, it removes those bits from the file and
  from any existing `-wal`, `-shm` and `-journal` (0644 becomes 0600) and
  prints one line with the old mode. If the mode cannot be changed (a
  read-only mount, a file owned by another user) sdf prints a warning and
  goes on, so read-only commands still work.
- **Existing directories.** sdf never changes the mode of a directory it did
  not create. It warns when group or others can write the directory (they
  could replace the files in it), except for a sticky directory such as
  `/tmp`.

The `<db>.lock` file `sdf serve` and `sdf redact` take is created 0600. sdf
makes no backup copies of a database.

## Receiver limits

`sdf serve` caps connections, header and body time, and bytes buffered
for a request on the OTLP/HTTP port, the OTLP/gRPC port, and the UI.
Past a cap, the receiver refuses the work instead of reading it into
memory. The numbers, the flags, and which status a client should retry
are in [receiver.md](receiver.md#resource-limits). A bad value refuses
to start; it does not leave the listener open without the cap.
