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

From `--task-ref REF` (the task file committed on a protected branch, read
as `REF:.sdf/tasks/<id>.json`), the sdf data dir copy (`--data-dir`,
`--no-data-dir`), and the ledger when one is given (`--ledger PATH` or
`$SDF_LEDGER`; there is no default ledger path). At least one must exist
and every one found must have the same hash; `--task-hash sha256:...` pins
the hash they must have. The `.sdf/tasks` file in the working tree, or in
the submitted commit, is never a source.

**PASS needs a trusted source**: `--task-ref`, `--task-hash`, or a ledger
pinned with `--expect-prefix SEQ:sha256:<entry hash>`. The data dir and an
unpinned ledger are written by whoever runs sdf on the machine, the agent
included (any `--ledger` path or `$SDF_LEDGER` can be a ledger the agent
made), so a task found only there runs but gives at best ERROR
(`TASK_UNTRUSTED`). With `--expect-prefix`, the ledger must verify and its
head must be exactly that entry, else `TASK_HASH_MISMATCH` (FAIL); a ledger
that has grown past the pin is not trusted either, because the ledger API
has no lookup of an entry by sequence yet. A local branch the agent can
move is no better: pass the protected branch as just fetched from the
remote (for example `git fetch origin main` and `--task-ref
refs/remotes/origin/main`, or the fetched commit id). The Action does this.
`--task-ref` with a local branch, a tag, or anything but a remote-tracking
ref or a full commit id prints a warning and records the kind as
`task_ref_kind` in the evidence.

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
   matches a designated glob fails (`DESIGNATED_FILE_ADDED`). For a `go test`
   or `go vet` criterion, so does any new or changed `_test.go` file or
   `testdata` file in a package it tests, and a new or changed `go.mod`,
   `go.sum`, `go.work` or `go.work.sum`, and any new, changed or deleted file
   under `vendor/`, whether or not the task designated it: a new `TestMain`
   or a replaced dependency decides the result. A symlink that is new or changed
   since the base and points outside the checkout fails
   (`SYMLINK_OUTSIDE_CHECKOUT`). So does a copy of this task's
   `.sdf/tasks/<id>.json` in the commit that differs from the trusted one.
4. **Secret scan** (when the task has `secret_scan`). Every commit in
   `base..SHA`, not only the net diff: each whole commit object (message,
   author, committer and any extra header), every added or changed path
   name, and the new lines of every added or changed blob, read from the objects (so a `-diff` or
   textconv attribute cannot hide a file, and a secret added then removed
   is still found). Binary blobs are scanned too and listed. The rule set is
   small and fixed (`sdf.secrets/v1`: private keys, AWS, GitHub, GitLab,
   Slack, Anthropic, OpenAI, xAI, Google, and Stripe live keys). A finding
   records the rule, commit, path, line, and the sha256 of the match, never
   the match (a path that itself matches is recorded as a hash). This is not a replacement for a full scanner.
5. **Criteria**, only if nothing above failed: the submitted code does not
   run otherwise. Before they run, `.git` is moved out of the checkout, so
   checks see a plain tree (it stays writable by the same user; the
   integrity checks do not read it again). Each argv runs in the checkout
   root with no shell, in its own namespaces (below), the declared
   timeout, and a small environment: locale, temp, CI and Go proxy
   settings are inherited; `HOME` and `XDG_*` point at a new empty
   directory; `GOENV=off`, `GOFLAGS=` and `GOTOOLCHAIN=local`; `GOPATH`,
   `GOCACHE` and `GOMODCACHE` are new (`--go-cache`/`--go-mod-cache` reuse
   caches the agent cannot write); plus `SDF_VERIFY=1`. Nothing under the
   real home (a `go env -w GOFLAGS=-exec=...`, a git or shell config, a
   poisoned build cache) reaches a check. `PATH` is set explicitly from
   `--path` (default: the verifier's PATH) with relative entries dropped,
   and argv[0] is resolved in that PATH only, so a bare name never resolves
   to a file in the checkout. The resolved command, cwd, and PATH are in
   the evidence.
   **Isolation.** Each check runs as pid 1's child in a new user, pid and
   mount namespace (`CLONE_NEWUSER|CLONE_NEWPID|CLONE_NEWNS`, uid and gid
   mapped to the same user, a private `/proc`; the check itself runs with
   no capabilities, and a namespace where the init cannot make those mounts,
   such as one confined by Ubuntu's AppArmor restriction, counts as no
   isolation). When the check exits, the
   namespace's init exits and the kernel kills every process left in it,
   including ones that called `setsid` or double-forked, before the
   checkout is re-hashed and before the next check starts. Without
   unprivileged user namespaces (for example Ubuntu 23.10+ with
   `kernel.apparmor_restrict_unprivileged_userns=1`) the verdict is ERROR
   `ISOLATION_UNAVAILABLE` and nothing runs. `--allow-unisolated` runs the
   checks anyway with only process-group cleanup; the verdict then records
   `"isolation": "none"` and a warning is printed. A run with isolation
   records `"isolation": "namespace"`. The Action never allows unisolated
   runs.
   **Go test results.** `go test` stdout is not trusted: code under test can
   print a fake `--- PASS`. Instead `go test` runs with `-count=1` and an
   `-overlay` the verifier writes (the task's argv may not set `-overlay`,
   `-exec` or `-toolexec`). The overlay adds a package with a random name,
   imported from every package of the module, so its `init` runs before
   any of the module's own; it connects to a unix socket the verifier owns
   (`$SDF_VERIFY_HOOK`) and receives a fresh random key. The verifier only
   answers a peer (checked with `SO_PEERCRED`) whose executable is a `.test`
   binary started by the `go` command it launched, once per process. Each
   designated top-level test is wrapped in place so that, when it ends, the
   hook sends `pass`, `fail` or `skip` with an HMAC under that key. Every
   designated test must report only `pass`, else `DESIGNATED_TEST_NOT_PASSED`
   (named as `TestX (package import/path)`; a `go test` criterion with no
   designated test fails the same way). Any other connection, a second one
   from a process, or a frame with a bad HMAC is `TEST_REPORT_INVALID`
   (FAIL). A `TestMain` or `init` that exits 0, a `-run` filter or a skipped
   designated test reports no pass and fails. `CGO_ENABLED=0` is set for
   these runs, so no C constructor runs before the hook.
   Output is hashed (`--artifacts DIR` keeps it as `<sha256>` files).
   Before and after each check, every tracked file on disk is re-hashed
   (without git, its index, or following symlinks) against the submitted
   tree, and no designated or Go test input file may appear
   (`CHECKOUT_MODIFIED`). A check is not run on a checkout an earlier check
   changed.

## Verdict codes

FAIL: `TASK_SNAPSHOT_MISSING`, `TASK_HASH_MISMATCH`, `TASK_FILE_MODIFIED`,
`DESIGNATED_TEST_NOT_PASSED`, `SYMLINK_OUTSIDE_CHECKOUT`,
`REVISION_UNRESOLVABLE`, `REVISION_MISMATCH`, `BASE_MISMATCH`,
`BASE_NOT_ANCESTOR`, `DESIGNATED_FILE_MODIFIED`, `DESIGNATED_FILE_ADDED`,
`SECRET_IN_DIFF`, `CHECKOUT_DIRTY`, `CHECKOUT_MODIFIED`, `CHECK_FAILED`,
`CHECK_TIMEOUT`, `TEST_REPORT_INVALID`.

ERROR (the verifier could not decide): `TASK_UNTRUSTED` (the task came only
from the data dir or an unpinned ledger), `ISOLATION_UNAVAILABLE` (no
namespaces and no `--allow-unisolated`), `CHECK_ERROR` (the command could
not start), `INFRA_ERROR`.

Any FAIL code makes the verdict FAIL; otherwise any ERROR code makes it
ERROR; otherwise it is PASS. The order checks ran in does not change that.

## Records

Evidence (`sdf.evidence/v1`) and the verdict (`sdf.verdict/v1`) are RFC 8785
JSON, hashed like a task: `"sha256:" + hex(sha256(kind + "\n" + canonical))`.
The verdict lists every evidence id and hash, the task hash and its
sources, the submitted commit and tree, and the secret rule set's hash.
The verdict also records `isolation`. With `--ledger PATH` or `$SDF_LEDGER`, the evidence and then the verdict are
appended to that ledger in one transaction ([docs/ledger.md](ledger.md#who-writes-it)):
all of them or none. The ledger is prepared before the checkout, so one
that cannot be written is exit 3 with nothing run. If the append itself
fails, nothing is appended, the verdict JSON is still printed, and the exit
is 3. With neither set, no ledger is opened and the printed JSON is the
record.

## What this does not guarantee

- Only a verdict produced in CI (or another place the agent never ran)
  counts. Locally the verifier runs as the same user as the agent, and a
  process of that user running at the same time can write the checkout,
  the moved `.git`, or the verdict file. "After the agent exits" is not
  enough on its own: a daemon it left behind is still that user.
- The namespaces end every process a check started, but only those. A
  process of the same user that was already running (an agent still
  running locally) is outside them and can still write the checkout. With
  `--allow-unisolated`, a process that leaves the check's process group
  (`setsid`, a double fork) survives the check and can change files later
  checks read; such a verdict says `"isolation": "none"`.
- Checks are not otherwise sandboxed: the user namespace maps to the same
  user, so they can read and write anything that user can (for example
  `~/.ssh`, or the moved `.git` next to the checkout) and use the network.
  A separate OS user is not built. Run verify on a machine or runner without credentials worth
  stealing; in the Action there are none.
- `--task-ref` is read from the repository given. If the agent can move that
  ref locally, it is not protected: use the protected branch as just
  fetched from the remote, or `--task-hash`.
- Code under test runs inside the test binary, in the same process as the
  hook. The key arrives before any of the module's own `init` runs, so it
  cannot be read from the socket first, and printing to stdout no longer
  matters. But code that runs later in that process can still read the key
  from memory (`unsafe`, reflection on the hook's variables), call the
  hook's report function directly, or patch the `testing` package, and so
  forge a pass. Closing that needs the tests to run in a process the code
  under test cannot inspect; it is not built. Packages in nested modules or
  `replace` directories outside the module are not hooked. Non-Go checks
  have no equivalent: a script check reports only its exit code.
- A check can still be weakened through a file that is neither designated
  nor a Go test input, for example a helper a script sources. `sdf task
  declare` warns for interpreter, `-c` and `make` criteria with no glob;
  designate broadly.
- The secret scan has eleven fixed-prefix rules. An encoded (base64, hex)
  or split token is not found.
- No signature: the records are hashed, not signed. A verdict JSON shows
  what a verifier claimed; trust it only from a run you control, such as
  the CI job's log and artifact.
