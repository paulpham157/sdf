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
(`TASK_UNTRUSTED`). A local branch the agent can move is no better: pass
the protected branch as just fetched from the remote (for example `git
fetch origin main` and `--task-ref refs/remotes/origin/main`, or the
fetched commit id). The Action does this. `--task-ref` with a local branch,
a tag, or anything but a remote-tracking ref or a full commit id prints a
warning and records the kind as `task_ref_kind` in the evidence.

**Ledger pins.** With `--expect-prefix`, the ledger must verify and its
head must be exactly that entry, else `TASK_HASH_MISMATCH` (FAIL).
- **Pins are single-use today.** Every `sdf verify` that writes to the
  ledger appends its evidence and verdict (about 20 entries), so the head
  moves and the old pin no longer matches: a ledger that has grown past the
  pin is `TASK_UNTRUSTED`. Take a new pin for each run: `sdf audit head` prints `seq N entry_hash HASH`, which is `--expect-prefix N:HASH`.
- **Take the pin before the agent can write the ledger**, from a copy you
  trust. A pin read after the agent had write access only pins what the
  agent wrote.
- A follow-up will switch to the ledger's `VerifyPrefix` (pull request
  #220: the pinned entry exists with that hash and the whole chain
  verifies, later appends allowed), so a pin survives later appends.

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
   run otherwise. Criteria run one at a time in **name order** (the task
   file sorts them by name, and the hash covers that order, not the order
   they were declared in): name them `a-…`, `b-…` when order matters.
   Before they run, `.git` is moved out of the checkout (an isolated check
   does not see it; the integrity checks do not read it again, and a `.git`
   a check creates in the checkout is `CHECKOUT_MODIFIED`), and every
   criterion's argv[0] is resolved and its file hashed (evidence
   `command_pins`). Each criterion re-resolves and re-hashes its command
   before it starts; a different file is `COMMAND_CHANGED` (FAIL), and the
   criterion does not run, so a check that drops an `sh` into a `PATH`
   directory or rewrites `go` cannot change what a later one runs.

   Each argv runs in the checkout root with no shell, in its own namespaces
   (below), the declared timeout, and a small environment: locale, CI and
   Go proxy settings are inherited; `GOENV=off`, `GOFLAGS=` and
   `GOTOOLCHAIN=local`; `SDF_VERIFY=1`; and `HOME`, `XDG_*`,
   `TMPDIR`/`TEMP`/`TMP`, `GOPATH`, `GOCACHE`, `GOMODCACHE` and `GOTMPDIR`
   point at **new directories for that criterion alone**, removed when it
   ends. Nothing one check writes there (a patched module source, a
   poisoned build-cache entry, a shell or git config) reaches another
   criterion or a later verification, and nothing under the real home (a
   `go env -w GOFLAGS=-exec=...`) reaches a check.

   `--go-cache`/`--go-mod-cache` name trusted caches to start from. An
   isolated check gets each as the read-only lower layer of an overlay
   whose writes land in its own directory: it reads the cache and cannot
   change it (evidence `read_only_caches`). An unisolated check uses the
   cache directly and can write it. Without them every criterion starts
   with empty caches, so a repository with dependencies and no `vendor/`
   must download them: give that criterion `--allow-network NAME`, or an
   honest commit FAILs (`go: ... dial tcp: ... network is unreachable`).

   `PATH` is set explicitly from `--path` (default: the verifier's PATH)
   with relative entries dropped, and argv[0] is resolved in that PATH
   only, so a bare name never resolves to a file in the checkout. The
   resolved command, its sha256, cwd, and PATH are in the evidence.

   **Isolation.** Each check runs as the child of a pid 1 in new user, pid,
   mount, IPC, UTS and network namespaces (`CLONE_NEWUSER|CLONE_NEWPID|
   CLONE_NEWNS|CLONE_NEWIPC|CLONE_NEWUTS|CLONE_NEWNET`, uid and gid mapped
   to the same user). Inside:
   - a private `/proc`, so the verifier's and the host's processes are not
     visible; the host's System V IPC objects and POSIX message queues are
     not visible either (a new `mqueue` is mounted on `/dev/mqueue`);
   - **every mount is read-only**: pid 1 remounts each one it inherited
     (submounts included) with `MS_RDONLY`. The Go toolchain (GOROOT),
     every `PATH` directory, trusted caches, the verifier's binary and any
     other host directory (`/workspace`, `/opt`, a mounted volume) cannot be
     written by a check (atk_goroot, atk_pathjack, atk_modcache). The hook
     is added to GOROOT's `testing` package through `go test -overlay`,
     which is read by the go command and compiled into the criterion's own
     GOCACHE: GOROOT itself is never written;
   - empty, writable tmpfs over `/tmp`, `/var/tmp`, `/run` (and
     `/var/run`), `/dev/shm`, the verifier's `TMPDIR` and
     `XDG_RUNTIME_DIR`, the real `HOME` and this verification's work
     directory: where the sockets of processes outside the namespace live
     (a tmux or screen server, docker and containerd, ssh-agent, a D-Bus or
     systemd user session), and where the moved `.git`, other criteria's
     output and the harness are. Bound back **writable**: only the checkout
     and the criterion's own directory (HOME, TMPDIR, GOPATH, the Go caches;
     evidence `writable`). Bound back **read-only**, where a mask covers
     them: the harness socket's directory, `PATH` entries and the Go
     toolchain. The evidence lists `masked` (and `unmasked`, a mask a bound
     directory equals or contains);
   - only loopback: no route to the host network, its listening ports or
     abstract unix sockets, and no downloads (use `--go-mod-cache` or
     vendor). `--allow-network NAME` (repeatable) keeps the host network
     for that criterion; the evidence records `"network": "loopback"` or
     `"host"`. The task cannot ask for it;
   - the check runs with no capabilities (pid 1 holds `CAP_SYS_ADMIN`,
     `CAP_NET_ADMIN` and `CAP_DAC_OVERRIDE` in its own user namespace only,
     for the mounts, loopback and overlayfs, and drops them first). A
     namespace where pid 1 cannot do all of the above (such as one confined
     by Ubuntu's AppArmor restriction, or a kernel without unprivileged
     overlayfs when a trusted cache is given) counts as no isolation or
     fails the criterion.

   When the check exits, pid 1 exits and the kernel kills every process
   left in the namespace, including ones that called `setsid` or
   double-forked, before the checkout is re-hashed and before the next
   check starts. Without unprivileged user namespaces (for example Ubuntu
   23.10+ with `kernel.apparmor_restrict_unprivileged_userns=1`) the
   verdict is ERROR `ISOLATION_UNAVAILABLE` and nothing runs.
   `--allow-unisolated` runs the checks anyway with only process-group
   cleanup; the verdict then records `"isolation": "none"` and a warning is
   printed. A run with isolation records `"isolation": "pid-namespace"`.
   The Action never allows unisolated runs.

   **Go test results.** `go test` stdout is not trusted: code under test can
   print a fake `--- PASS`. Instead `go test` runs with `-count=1` and an
   `-overlay` the verifier writes, placed after `-C dir` when the task uses
   it (the task's argv may not set `-overlay`, `-exec` or `-toolexec`):
   - the overlay adds the hook, under random names, to the standard
     library's `testing` package in the criterion's GOROOT (nothing is
     written to the checkout), and a file importing `testing` to every
     package directory in the checkout: the module, nested modules,
     `replace` directories and `vendor/`. Go initializes a package's imports
     first, so the hook's `init` runs before any `init` or package variable
     of the repository's code, including a `replace`d local module's;
   - the hook connects to a unix socket the verifier owns, whose path is
     compiled into it, and receives a fresh random key. The verifier only
     answers a peer (checked with `SO_PEERCRED`) whose executable is a
     `.test` binary started by the `go` command it launched, once per
     process;
   - each designated top-level test is wrapped in place so that, when it
     ends, the hook sends `pass`, `fail` or `skip` with an HMAC under that
     key. Package names come from the nearest `go.mod`;
   - right after the key exchange, still before any code of the module
     runs, the hook makes the test process non-dumpable
     (`PR_SET_DUMPABLE 0`), so its `/proc/self/mem` (and `fd/`, `environ`)
     belong to root and cannot be opened, by it or by a child: code under
     test cannot patch the `testing` package's machine code that way
     (Review's atk_tw5). On amd64 and arm64 a seccomp filter (all threads,
     inherited by children, with `no_new_privs`) then refuses
     `prctl(PR_SET_DUMPABLE)` and system calls of another ABI, so this
     cannot be undone. If any of it fails the test binary exits 3. Side
     effects for tests: `/proc/self/fd` is not listable, and set-uid
     programs run by a test do not gain privileges.

   **This resists only naive cheating. It is not a security boundary
   against an adversarial agent**: code in the test binary that uses
   `unsafe`, `//go:linkname` or assembly can still read the key or write
   memory and forge a pass (see the tripwire, and "What this does not
   guarantee").

   Every designated test must report only `pass`, else
   `DESIGNATED_TEST_NOT_PASSED` (named as `TestX (package import/path)`; a
   `go test` criterion with no designated test fails the same way). Any
   other connection, a second one from a process, or a frame with a bad
   HMAC is `TEST_REPORT_INVALID` (FAIL). A `TestMain` or `init` that exits
   0, a `-run` filter or a skipped designated test reports no pass and
   fails. `CGO_ENABLED=0` is set for these runs, so no C constructor runs
   before the hook. A designated `_test.go` that no `go test` criterion
   runs (a script that runs `go test`, for example in a nested module)
   gets none of this; `sdf task declare` and `sdf verify` warn.

   **Tripwire (known gap).** Code in the test binary can still forge a pass
   without the key, for example by calling the hook's report function
   through its own symbol table (`debug/elf` + `debug/gosym` + `unsafe`,
   about 40 lines; Review's atk_b1mem). The harness cannot prevent that, so
   for a task with a `go test` criterion, a file added or changed since the
   base that looks able to is ERROR `TAMPER_CAPABLE_CODE`:
   - assembly, C, C++, Objective-C, Fortran or SWIG source, or a `.syso`;
   - a Go file go/parser cannot read (it could not be checked);
   - a Go file (tests included) importing `unsafe` (which `//go:linkname`
     also needs), `C`, `runtime/cgo`, `plugin` or any `debug/...` package;
     dot-importing `syscall`, `golang.org/x/sys/unix`, `reflect`, `runtime`
     or `unsafe`; using `//go:linkname`; using raw system calls, `Mmap`,
     `Mprotect`, `ProcessVM*` or ptrace through `syscall` or
     `golang.org/x/sys/unix` (called or taken as a value);
     using `runtime.FuncForPC` or a reflect `Pointer`/`UnsafePointer`/
     `UnsafeAddr` method; or with a string constant (after folding `+` of
     literals) containing `/proc` or `proc/`, or equal to `proc`.

   **The tripwire catches known patterns only.** A path or name can be
   built at run time in ways it does not follow, and code that already had
   one of these at the base is flagged only when it changes. ERROR, not
   FAIL, because legitimate code uses them too: `--allow-tamper-capable`
   (after a person has read the change) turns the findings into a
   warning. atk_tw5 (FuncForPC + `/proc/self/mem`) is flagged, and fails
   anyway with `--allow-tamper-capable` because of the non-dumpable hook;
   atk_b1mem (unsafe) is flagged and, with `--allow-tamper-capable`, still
   forges a pass.

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
`CHECK_TIMEOUT`, `TEST_REPORT_INVALID`, `COMMAND_CHANGED`.

ERROR (the verifier could not decide): `TASK_UNTRUSTED` (the task came only
from the data dir or an unpinned ledger), `ISOLATION_UNAVAILABLE` (no
namespaces and no `--allow-unisolated`), `TAMPER_CAPABLE_CODE` (the
tripwire), `CHECK_ERROR` (the command could not start), `INFRA_ERROR`.

Any FAIL code makes the verdict FAIL; otherwise any ERROR code makes it
ERROR; otherwise it is PASS. The order checks ran in does not change that.

## Records

Evidence (`sdf.evidence/v1`) and the verdict (`sdf.verdict/v1`) are RFC 8785
JSON, hashed like a task: `"sha256:" + hex(sha256(kind + "\n" + canonical))`.
The verdict lists every evidence id and hash, the task hash and its
sources, the submitted commit and tree, and the secret rule set's hash.
The verdict also records `isolation`; `command_pins` lists each criterion's
resolved argv[0] and its sha256; each criterion's evidence records
`command_sha256`, `network`, `masked`, `unmasked`, `writable`,
`read_only_caches` and `goroot`. With `--ledger PATH` or `$SDF_LEDGER`, the evidence and then the verdict are
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
- The namespaces end the processes a check started, hide the host's
  sockets under the masked directories and the host network, and make
  every host directory read-only. They do not stop a process that was
  already running outside (an agent still running locally) from writing
  the checkout, GOROOT, a `PATH` directory or a cache: command pins catch a
  changed argv[0] between criteria, nothing else is re-hashed. Read-only
  is not invisible: a check can read whatever that user can read outside
  the masks (files under `/workspace`, `/etc`, a mounted volume), and can
  connect to a socket in a directory that is not masked (read-only does
  not stop `connect`), for example under `/var/lib` or a bound-back `PATH`
  entry. With `--allow-unisolated` none of this applies; such a verdict
  says `"isolation": "none"`.
- Checks run as the same user, mapped into the user namespace, with no
  capabilities. A separate OS user is not built. Run verify on a machine or
  runner without credentials worth stealing: a check can read what that
  user can read outside the masks. The Action keeps the token out of
  `.git/config` and the checks' environment and asks for `contents: read`
  only, but the runner's own files are not hidden further (see
  [action.md](action.md)).
- Untracked files a check leaves in the checkout stay there for later
  criteria (only designated and Go test input paths, and `.git`, are
  checked): criteria share one checkout.
- `--task-ref` is read from the repository given. If the agent can move that
  ref locally, it is not protected: use the protected branch as just
  fetched from the remote, or `--task-hash`.
- **Known gap: the go test harness is not a security boundary.** Code under
  test runs in the same process as the hook. With `unsafe`,
  `//go:linkname` or assembly it can read the key or call the hook's
  report function (Review's atk_b1mem), or patch code or data in memory,
  and forge a pass. The non-dumpable test process closes `/proc/self/mem`
  (atk_tw5), and the tripwire turns the usual ways into
  `TAMPER_CAPABLE_CODE`, but it catches known patterns only. Closing the
  gap needs the tests to run in a process the code under test cannot
  inspect; it is not built. Packages from the module cache (not vendored)
  are not hooked, so a dependency's `init` may run before the hook. Which
  dependencies are used cannot change without a `go.mod`/`go.sum` change,
  which fails, and their sources come from a fresh download (checked
  against `go.sum`) or a trusted cache a check cannot write; but go does
  not re-verify a trusted cache's extracted files, so `--go-mod-cache`
  must point at a cache no agent can write. Non-Go checks have no
  equivalent: a script check reports only its exit code.
- A check can still be weakened through a file that is neither designated
  nor a Go test input, for example a helper a script sources. `sdf task
  declare` warns for interpreter, `-c` and `make` criteria with no glob;
  designate broadly.
- The secret scan has eleven fixed-prefix rules. An encoded (base64, hex)
  or split token is not found.
- No signature: the records are hashed, not signed. A verdict JSON shows
  what a verifier claimed; trust it only from a run you control, such as
  the CI job's log and artifact.
