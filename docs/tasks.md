# Tasks

`sdf task declare` records what a piece of agent work has to pass, before
the agent starts. It runs nothing. A later `sdf verify` (not built yet)
reads the declared task and runs its checks against a submitted revision.

```bash
sdf task declare --id add-widget --title "Add the widget" \
  --check 'unit=go test ./...' --designated 'unit=internal/**/*_test.go' \
  --check 'accept=sh scripts/accept.sh' --write
sdf task show add-widget
```

Or from a JSON file (`--file PATH`, `-` for stdin):

```json
{
  "id": "add-widget",
  "title": "Add the widget",
  "objective": "optional free text",
  "base": "HEAD",
  "secret_scan": true,
  "criteria": [
    {"name": "unit", "argv": ["go", "test", "./..."], "timeout_s": 600,
     "expect_exit": 0, "designated": ["internal/**/*_test.go"]}
  ]
}
```

Flags override the file's `id`, `title`, `objective`, and `base`, and each
`--check` adds a criterion. A `--check` command is split on spaces with no
quoting and runs without a shell. Use the file for anything else.

## What is declared

A task id, and each criterion name, is 1 to 128 lowercase ASCII letters,
digits, `.`, `_`, or `-`, starting with a letter or digit. Lowercase only,
so two ids never differ by case alone. Such an id is also valid UTF-8 with
no newline or control character, which the planned ledger will require of
every id.

`--repo` may be any directory inside the work tree; the task file goes under
the repository's top level.

The task is pinned to a **base revision** (`--base`, default `HEAD`),
resolved to a full commit SHA and its tree id. Only committed content
counts: an uncommitted edit in the working tree is not read.

Each criterion has a name, an argv, a timeout (default 600 s), an expected
exit code (default 0), and **designated** globs: the files the check depends
on, usually its tests. `**` matches any number of directories. Each glob
must match at least one file at the base, or the declaration is refused.

`designated_files` lists every file those globs match at the base, plus any
argument in a criterion's argv that names a file at the base
(`sh scripts/accept.sh` designates `scripts/accept.sh`), plus the makefile
when the command is `make`. A path inside a shell string (`sh -c "..."`) is
not found. Each file is recorded with its git mode, blob id, and sha256, so
verify can tell whether the submitted revision changed the tests that judge
it.

A designated symlink records its target string, and the file it points to
is designated as well (through a chain of links). A link that points
outside the repository, to a directory, or to nothing is refused.

## Canonical form and hash

The stored task (schema `sdf.task/v1`) is RFC 8785 (JCS) JSON: keys sorted,
no whitespace, integers only. Criteria are sorted by name, globs and files
by path, so the order you declare them in does not change the hash.

```
task_hash = "sha256:" + hex(sha256("sdf.task/v1\n" + canonical))
```

The time of declaration is not in the hash, so declaring the same task twice
is a no-op. Declaring an existing id with anything different (a title, a
check, or the same flags after `HEAD` moved) is refused when a copy of that
id is found in a place this declaration reads (see below). That is a local
check, not a global one: after the data dir copy is deleted, or from a
branch or clone without the `.sdf/tasks` file, a different task can be
declared under the same id. Which declaration counts is decided later by
the copy the verifier trusts (the file on the protected branch). Declare a
new id rather than reusing one.

A stored file must be exactly the canonical bytes, plus at most one
trailing newline. A reformatted or extended file is refused rather than
re-hashed. A hand edit that keeps the canonical form (for example a changed
title, with keys still sorted and no whitespace) is accepted: it is a
different task with a different hash. Nothing in the file says who wrote
it, so protect the copy you trust (review on the protected branch).

## Where it is stored

- `--write` puts it at `.sdf/tasks/<id>.json` in the repository. Commit
  that file on a protected branch. Planned: `sdf verify` and its CI action
  (separate pull requests) read the task from that branch, not from the
  submitted revision.
- `$XDG_DATA_HOME/sdf/tasks/<id>.json` (default
  `~/.local/share/sdf/tasks/`, directory mode 0700), read-only, outside the
  repository. Files are written to a temporary name and then linked into
  place, so a copy is never half written and never replaced.
  `--data-dir DIR` moves it, `--no-data-dir` skips it.
- The SDF ledger, an append-only store for tasks, evidence, and verdicts,
  is not in this build. When it lands, declare writes there as well.

Every place is read before anything is written. If any one already holds
the id with a different hash, nothing is written. `sdf task show` reads
every place, checks each copy is canonical, and fails if they disagree.
