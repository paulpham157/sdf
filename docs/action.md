# The sdf verify GitHub Action

`.github/actions/sdf-verify` runs [`sdf verify`](verify.md) on a pull
request's head commit and fails the job unless the verdict is PASS.

```yaml
on:
  pull_request:
    types: [opened, synchronize, reopened, labeled]
permissions:
  contents: read
jobs:
  verify:
    if: contains(join(github.event.pull_request.labels.*.name, ' '), 'sdf-task:')
    runs-on: ubuntu-latest
    steps:
      - uses: paulpham157/sdf/.github/actions/sdf-verify@<full commit sha>
```

For this repository, [examples/sdf-verify.yml](examples/sdf-verify.yml) is
a workflow that runs it on labelled pull requests, loading the action from a
checkout of the base commit. It is not installed: copy it to
`.github/workflows/` to turn it on. Without an `sdf-task:` label the job is
skipped, so it cannot block the existing checks.

## What it does

1. Builds `sdf` from the action's own source (the pinned commit of this
   repository), with Go from that source's `go.mod` and no build cache,
   before any pull request code is on disk.
2. Checks out the pull request **head** (not the merge commit) with full
   history and `persist-credentials: false`.
3. Picks the task id: the `task` input, else exactly one label
   `sdf-task:<id>` (`task-source: label`, the default). `trailer` or
   `label-or-trailer` also accept an `SDF-Task: <id>` trailer on the head
   commit. Zero or several candidates, or an invalid id, is ERROR.
4. Runs `sdf verify <head sha> --task <id> --task-ref origin/<base branch>
   --no-data-dir`, so the task comes only from `.sdf/tasks/<id>.json` on the
   base branch as GitHub served it. An id that is not declared there is
   `TASK_SNAPSHOT_MISSING`, a FAIL.
5. Writes the status, task, commit, verdict hash, and every reason code to
   the job summary, and uploads `verdict.json` plus check output (named by
   sha256) as the `sdf-verdict` artifact.
6. Fails the job unless the status is PASS. A missing verdict, a verdict
   file that disagrees with `sdf verify`'s exit code, or a script error is
   ERROR, which fails too.

Outputs: `status`, `task`, `verdict-hash`.

## Threat model

The party to guard against is the author of the pull request, typically a
coding agent, who wants a PASS without doing the work: weakening or
deleting tests, editing the task, pointing at another commit, or slipping a
credential into the diff. `sdf verify` catches those (see
[verify.md](verify.md)). This action's job is to make sure the verifier and
the task it reads are not under that author's control.

**Why plain `pull_request`.** The checks are the pull request's own code:
`go test` runs whatever the pull request put in the package. Under
`pull_request_target` that code would run with the base repository's
context: a token that can be given write access, any secrets the workflow
references, and a cache scope shared with the default branch, which lets
pull request code poison caches that later runs on `main` restore. Under
`pull_request` from a fork, GitHub gives a read-only token and no secrets,
and caches written by the run are scoped to the pull request. This action
asks for nothing more: `contents: read`, no secrets, no cache.

**Where trust comes from.**
- The verifier: built from a pinned commit of this repository (or the base
  commit, in this repository), never from the pull request.
- The task: read from the base branch through `--task-ref`, never from the
  working tree or the head commit. A head commit that changes
  `.sdf/tasks/<id>.json` fails with `TASK_FILE_MODIFIED`.
- The task id: a label, which only people with triage access can set. A
  trailer is written by the pull request author, who could then pick an
  easier declared task, so it is off by default.
- The credentials: not written to `.git/config`, and the checks get an
  allowlisted environment without the runner's `ACTIONS_*`/`GITHUB_*`
  variables.

**What it does not protect against.**
- *The workflow file itself.* With `pull_request`, GitHub runs the workflow
  as it is in the pull request's merge commit, so a same-repository pull
  request can edit `.github/workflows/sdf-verify.yml` to skip the step or to
  run something else under the same job name. Making the result binding
  needs one of: a CODEOWNERS rule plus required review for `.github/**`; an
  organization ruleset that requires a workflow from a fixed repository
  and ref (organization-owned repositories only; the definition then does
  not come from the pull request); or a designated glob covering `.github/**` in the task,
  so such a change fails verification when the job does run.
- *Code that attacks the job it runs in.* The checks run as the runner's
  user. `sdf verify` kills each check's process group when it exits, but a
  process that detaches into its own session can outlive it and, as the
  same user, rewrite the verdict file, the step output file, or write to
  the job log. The exit code of the step that ran `sdf verify` and the
  verdict hash it printed to the log are the record to trust, not the
  artifact. Running the checks as a separate unprivileged OS user (hosted
  runners allow creating one with sudo) would close this; it is not built.
- *A dishonest base branch.* Whoever can push to the base branch decides
  the task. Protect it.
- No signature: the verdict is hashed, not signed. A ledger prefix or head
  check (below) can detect a rewritten chain.

## Optional ledger check

`sdf verify` can append evidence and the verdict to a hash-chained ledger
(`--ledger PATH` or `$SDF_LEDGER`; see [ledger.md](ledger.md) and
[verify.md](verify.md)). This action does not open a ledger today: it runs
`sdf verify ... --no-data-dir` only.

A later step in the workflow can audit a ledger the job produced:

```bash
sdf audit verify --require-nonempty --expect-prefix-file pin.txt
```

`--expect-prefix` and `--expect-prefix-file` allow later legitimate appends.
`--expect-head` and `--expect-head-file` require the current head to match
exactly. Exit codes are 0 intact, 1 corrupt or mismatch, 2 usage, 3 I/O
error. A step that uses it must map 1 to FAIL and 2 or 3 to ERROR, and must
never turn either into PASS. It is not part of this action yet.
