# Verify

`sdf verify SHA --task ID` runs a declared task's checks (see
[tasks.md](tasks.md)) against one commit, and records what happened.

```bash
sdf verify "$(git rev-parse HEAD)" --task add-widget --task-ref origin/main --out verdict.json
```

Exit 0 is PASS, 1 FAIL, 3 ERROR, 2 a usage error. Only PASS is acceptance.
The verdict and every evidence record are printed as JSON (`--out` writes
the same document to a file); a short summary goes to stderr.

## Where the task comes from

From trusted copies only: `--task-ref REF` (the task file committed on a
protected branch, read as `REF:.sdf/tasks/<id>.json`), the sdf data dir
copy (`--data-dir`, `--no-data-dir`), and the ledger when one is given
(`--ledger PATH` or `$SDF_LEDGER`; there is no default ledger path).
At least one must exist and every one found must have the same hash. The
`.sdf/tasks` file in the working tree, or in the submitted commit, is never
a source.

## What runs, in order

1. **Checkout.** A new repository in a temporary directory outside the
   repository (`--work-dir` sets the parent). The submitted commit and the
   task's base are fetched from the repository, so git re-hashes every
   object it receives, then the commit is checked out detached. The commit
   does not have to be on a branch. Hooks, user and system git config, and
   `GIT_*` variables are not used. `--keep` leaves the directory.
2. **Revision identity.** HEAD is the submitted commit, the base's tree is
   the one the task recorded, the base is an ancestor of the commit, and the
   fresh checkout is clean.
3. **Designated files.** The base holds exactly the blobs the task pinned
   (else `BASE_MISMATCH`). Every designated file has the same mode and blob
   at the commit as at the base, and the same sha256 on disk, read without
   following symlinks (the on-disk type must match the git mode). A new file that
   matches a designated glob fails. So does a copy of this task's
   `.sdf/tasks/<id>.json` in the commit that differs from the trusted one.
4. **Secret scan** (when the task has `secret_scan`). Added lines of
   `git diff base..SHA`, against a small fixed rule set (`sdf.secrets/v1`:
   private keys, AWS, GitHub, GitLab, Slack, Anthropic, OpenAI, xAI, Google,
   and Stripe live keys). A finding records the rule, path, line, and the
   sha256 of the match, never the match. Binary files are listed as skipped.
   This is not a replacement for a full scanner.
5. **Criteria**, only if nothing above failed: the submitted code does not
   run otherwise. Each argv runs in the checkout root with no shell, its
   own process group, the declared timeout, and an allowlisted environment
   (`HOME`, Go cache variables, locale, temp; plus `SDF_VERIFY=1`). `PATH`
   is set explicitly from `--path` (default: the verifier's PATH) with
   relative entries dropped, and argv[0] is resolved in that PATH only, so
   a bare name never resolves to a file in the checkout. The resolved
   command, cwd, and PATH are in the evidence.
   Output is hashed (`--artifacts DIR` keeps it as `<sha256>` files). After
   each check, HEAD and the tracked files must be unchanged.

## Verdict codes

FAIL: `TASK_SNAPSHOT_MISSING`, `TASK_HASH_MISMATCH`, `TASK_FILE_MODIFIED`,
`REVISION_UNRESOLVABLE`, `REVISION_MISMATCH`, `BASE_MISMATCH`,
`BASE_NOT_ANCESTOR`, `DESIGNATED_FILE_MODIFIED`, `DESIGNATED_FILE_ADDED`,
`SECRET_IN_DIFF`, `CHECKOUT_DIRTY`, `CHECKOUT_MODIFIED`, `CHECK_FAILED`,
`CHECK_TIMEOUT`.

ERROR (the verifier could not decide): `CHECK_ERROR` (the command could not
start), `INFRA_ERROR`.

Any FAIL code makes the verdict FAIL; otherwise any ERROR code makes it
ERROR; otherwise it is PASS. The order checks ran in does not change that.

## Records

Evidence (`sdf.evidence/v1`) and the verdict (`sdf.verdict/v1`) are RFC 8785
JSON, hashed like a task: `"sha256:" + hex(sha256(kind + "\n" + canonical))`.
The verdict lists every evidence id and hash, the task hash and its
sources, the submitted commit and tree, and the secret rule set's hash.
With `--ledger PATH` or `$SDF_LEDGER`, the evidence and then the verdict are
appended to that ledger in one transaction ([docs/ledger.md](ledger.md#who-writes-it)):
all of them or none. The ledger is prepared before the checkout, so one
that cannot be written is exit 3 with nothing run. If the append itself
fails, nothing is appended, the verdict JSON is still printed, and the exit
is 3. With neither set, no ledger is opened and the printed JSON is the
record.

## What this does not guarantee

- The verifier runs as the same user as everything else on the machine. A
  process running as that user at the same time could write the temporary
  checkout. Run verify where the agent is not running: in CI, or after it
  exits.
- When a check exits, everything left in its process group is killed. A
  process that leaves the group (`setsid`, a double fork into a new
  session) survives, and as the same user it can change files the verifier
  or the CI job writes later. Running the checks as a separate OS user is
  the fix; it is not built yet.
- `--task-ref` is read from the repository given. If the agent can move that
  ref locally, it is not protected. In CI, use the protected branch as
  fetched by the runner.
- A check can be weakened without touching a designated file, for example a
  new non-designated file that changes how the tests run (a Go `TestMain` in
  another `_test.go` file, a changed `go.mod`). Designate broadly.
- A path inside a shell string (`sh -c "..."`) is not designated.
- The secret scan covers added text lines only, with eleven rules.
- No signature: the records are hashed, not signed.

The GitHub Action that runs this on a pull request is in [action.md](action.md).
