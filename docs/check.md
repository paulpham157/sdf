# Check the summary

`sdf check` runs a fixed set of local cases against the collector and prints
how each one came out. It does not score them, and it does not decide whether a
change may land.

```bash
./sdf check
./sdf check --cases DIR
```

Each case names one field of the report and the value that field should have.
The input is an OTLP export the collector decodes, or a summary report it
already produced. The outcome is `pass` when the field matches, `fail` when the
field was produced and does not match, and `not_checked` when the input cannot
be priced or cannot be linked. `not_checked` is reported and is not a failure.
Unknown is not zero, and it is not a pass. The command exits non-zero when any
case fails. It makes no network call.

The cases committed with the collector live in `internal/check/testdata/cases`
and are embedded in the binary. `./sdf check` runs that embedded set. It does
not look the files up next to the compiled source: `make build` and the
Goreleaser snapshot pass `-trimpath`, so the recorded path is the module path
and is not a directory on disk. `--cases` points at a different directory of
the same JSON files.
