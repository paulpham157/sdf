# ADR 0034: verification evidence is independent of the agent workspace

Status: proposed (implemented with this change; awaiting maintainer acceptance).
Narrows the residual stated in [ADR 0025](0025-server-registered-checks.md).
Does not change Evidence append-only rules ([ADR 0002](0002-evidence-closes-the-decision-loop.md))
or revision binding ([ADR 0032](0032-attempt-revisions.md)).

## Context

ADR 0025 stopped the workspace from choosing the interpreter, the pytest
configuration and the named test file. Two holes remained, and both are
visible in the code that landed with that ADR:

1. **The oracle was not sealed.** `_verify_targets` hashes only the pytest
   files and python scripts named in the check. A module those files import
   (`helper.py`, a planted `conftest.py` the test imports, fixture data next
   to the tests) is taken from the agent-writable workspace. ADR 0025 names
   this residual explicitly.
2. **The verdict was the child's stdout.** `pytest_verdict` accepts exit 0
   when the output contains a summary with at least one passed test. A helper
   can print `1 passed in 0.01s` and call `os._exit(0)` before pytest writes
   anything. That is a pass. JUnit was rejected in ADR 0025 because a same-uid
   process can rewrite a file; stdout is easier to forge than a report the
   evaluator reads back itself.

Evidence rows made the same gap durable. `kind=evaluation` is used both for a
real check and for a coverage gap ("no deterministic check mapped"). A row
does not name its producer or verifier, does not bind the artifact bytes it
claims to have checked, and does not say when it was collected or when it
expires. An evaluator cannot show that the verdict is independent of the
Attempt's agent, or that it is still current.

## Decision

1. **Three Evidence classes**, stored separately from `kind` (which stays
   `evaluation` for existing readers):
   - `execution` — the Attempt ran (reserved; not written by this change).
   - `verification` — an Evaluator verdict about a check.
   - `outcome` — a business result (reserved; outcome metrics stay on their
     own table).

   A coverage gap, an unmapped criterion and a run with no commands are not
   verification. Their `evidence_class` stays NULL. Only `verification` can
   accept an artifact.

2. **Schema** (migration `0025_evidence_independence`). All of the following
   are nullable. Rows written before this migration are not rewritten and are
   not backfilled:

   | Column | Meaning |
   | --- | --- |
   | `evidence_class` | `execution`, `verification` or `outcome`; NULL if none was recorded |
   | `producer_identity`, `producer_role` | who wrote the row |
   | `verifier_identity`, `verifier_role` | who decided the verdict |
   | `subject_artifact_sha256` | sha256 of the artifact the verdict is about |
   | `oracle_digest` | sha256 of the sealed oracle manifest (path and file hash, sorted) |
   | `environment_digest` | sha256 of command, runner, oracle digest, Python and platform |
   | `collected_at` | when the evaluator read the verdict |
   | `expires_at` | when the verdict stops being acceptable |

   PostgreSQL checks `evidence_class` when it is not NULL
   (`ck_evidence_evidence_class`). SQLite uses the same rule in the ORM
   session guard. NULL stays legal so old rows insert and read.

3. **The check runs from a sealed oracle**, outside the workspace:
   - Oracle files are the named test or script, every `conftest.py` in the
     fixture, support modules (`helper` / `helpers`, anything under
     `tests/`, `test/`, `checks/` or `fixtures/`) imported by those files,
     and non-Python files in those directories.
   - Each oracle file must be a regular file in the server fixture and
     byte-identical in the workspace. A difference, a symlink, a deletion,
     or a support module that exists only in the workspace is
     `INCONCLUSIVE` and the check does not run.
   - The code under test (a module such as `calc.py` that the tests import
     and that is not support) is copied from the workspace. Workspace
     `conftest.py`, pytest/ruff config and files under support directories
     are not.
   - The sealed directory is the child cwd. `--confcutdir` is that directory,
     so a planted workspace `conftest.py` is not loaded. `-c /dev/null` and
     plugin autoload stay off (ADR 0025).

4. **Pytest's verdict is a result frame the parent reads, not stdout and not
   a file the child was told to write.** The process the sandbox starts is a
   notary. It forks the pytest child and writes the result file only after
   that child has exited. The child is not given `SDF_ORACLE_TOKEN`,
   `SDF_ORACLE_MARKER`, or the result path.
   - The plugin's `pytest_sessionfinish` writes one JSON frame (counts and
     exit status) through a pipe, using a small `write` stub mapped for that
     purpose.
   - On Linux x86_64 the child installs a seccomp filter before pytest
     imports anything: `write` and the other write-family syscalls to that
     pipe succeed only when the instruction pointer is in the stub. `os.write`
     and a forged JUnit file do not.
   - The parent accepts `PASS` only when the frame's counts agree, the exit
     code and the frame's exit status are both 0, at least one test passed,
     the count is at least the number of test functions the oracle declares,
     and the sealed oracle bytes still match the fixture.

   Zero collected tests, and tests that were only skipped, are `FAIL`. A
   missing frame after exit 0 is `FAIL` (unexpected early exit). A frame whose
   counts disagree is `INCONCLUSIVE`. None of these is a pass. The stdout
   summary parser remains in the tree and is not the accepting signal. A bare
   `pytest` executable, which does not run this bootstrap, is `INCONCLUSIVE`.

5. **Acceptance gate** (`check_verification_acceptance`), applied in
   `ExecutionService.finish` before a `validates` edge or a succeeded Task:
   - the row's class is `verification`;
   - `verifier_role` is not `agent`, and `verifier_identity` is set and is
     not the Attempt's `agent` (the evaluator writes `sdf-evaluator` /
     `evaluator`);
   - `subject_artifact_sha256` equals the diff artifact being accepted;
   - `expires_at` is in the future. NULL or past means the verdict requires
     re-validation and cannot accept.

   A `PASS` that fails the gate is stored as `INCONCLUSIVE`. The evaluator
   sets `expires_at` to 24 hours after `collected_at`.

6. **The pytest verdict process does not execute subject code.** Before
   collection, the pytest process installs an import hook for every
   top-level module in the sealed tree that is not an oracle file and not
   the stdlib or pytest. Those modules load in a fresh interpreter (the
   subject worker) started with `close_fds` so it does not inherit the
   result pipe or the result file. The test's assertions run in the pytest
   process on the values the worker returns. `os._exit`, a closure walk,
   `gc.get_objects`, and `pluginmanager.register` in the subject therefore
   run where `sdf_oracle_plugin` and the report stub do not exist. Seccomp
   is still installed in the pytest process and still fails closed when it
   cannot be installed.

7. **The worker cannot reach the pytest process or the result file.** The
   worker is the same uid as pytest. `close_fds` only stops inheritance, so
   without the steps below a worker can `ptrace` the pytest process and
   open `/proc/<pid>/fd`.
   - The notary calls `PR_SET_DUMPABLE` 0 before it forks, and the pytest
     child calls it again before it starts the worker. A same-uid peer
     then cannot `ptrace` that process or open `/proc/<pid>/fd` (the kernel
     returns `EPERM` / `EACCES`). If `prctl` fails, that process exits 86
     and there is no result frame.
   - Before it reads any request, the worker installs its own seccomp
     filter. The filter denies `ptrace`, `process_vm_readv`,
     `process_vm_writev`, `pidfd_open`, `pidfd_getfd`, and the `chmod`
     family (`chmod`, `fchmod`, `fchmodat`, `fchmodat2`), and it sets
     `PR_SET_NO_NEW_PRIVS`, `PR_SET_DUMPABLE` 0, and `PR_SET_PDEATHSIG`
     to `SIGKILL`. If the platform is not Linux x86_64, or `prctl` or
     seccomp fails, the worker exits 86 before subject code runs. The
     import then fails and the verdict is not a pass.
   - The parent caps a worker response at 1_000_000 bytes, the same cap the
     worker applies to requests. A larger length, a body that is not UTF-8
     JSON, or a JSON value that is not an object closes the socket and
     raises `ImportError`. That is a pytest error, so it is not a pass.
   - The notary opens the result file before the fork and writes it through
     that fd after the child has been reaped. The child closes the fd
     before it starts the worker. When the result path is inside the
     check cwd (the sealed tree), every file in that tree is mode `0444`
     and every directory is mode `0555` before the worker exists. A
     private report outside the cwd (absolute test paths) freezes only
     its own file and directory, not the workspace. The worker's `TMPDIR`
     is a fresh directory under `/tmp`, so `tempfile` still works. A
     failed `chmod` exits 86. The worker cannot undo the modes because
     `chmod` is denied. The evaluator restores the modes only when it
     deletes the tree, after it has read the result.

   A separate uid or user namespace is not what this uses. Mapping the
   host uid inside a user namespace would still own the files, and
   `setuid` to an unprivileged account needs `CAP_SETUID`, which a host
   check does not have. Treating that failure as fatal would make every
   pytest check a non-pass. Dumpable plus the worker filter does not need
   extra privileges, and both fail closed when they cannot be applied.

   **What remains.**
   - Oracle code (the fixture's tests, `conftest.py`, and hashed helpers)
     shares the pytest process with the reporter. It is byte-checked
     against the server fixture before the run. Subject code does not.
   - A subject call returns a value. If that value satisfies the oracle's
     assertion, the test passes. That is the code under test, not a forged
     runner. A subject that patches its own RPC encoder so the worker
     reports the values the test expects is the same thing as hardcoding
     those answers. That is test overfitting, not evidence forgery. Held-out
     or property-based oracles are what catch it, and they are tracked
     separately from this ADR.
   - The worker can still write to its private temp directory and to other
     paths outside the frozen tree that the uid can open. It cannot replace
     the sealed tree or the result file.
   - `python -c` and script checks still run the subject in the process
     whose exit code is the verdict. `sys.exit(0)` from an imported module
     can still force that exit to 0. That is the ADR 0025 residual; it is
     not a pytest verification frame.

## Consequences

- A planted conftest, a replaced helper, a helper that exists only in the
  workspace, a subject that writes the old marker and JUnit paths and
  `os._exit(0)`, a libc write of a passing frame, a modified oracle file, a
  run that collects nothing, and a run whose tests are all skipped cannot
  accept an Attempt. ADR 0025 already rejected exit 0 when the summary was
  missing, reported failures, or reported no passes. A summary that parser
  accepts as N passed was still a pass on main, and so was a helper the
  named test imports, because helpers were not hashed. The token-and-marker
  forge was a pass only on the first cut of this change. Calling the report
  stub from the subject's closure, from `gc`, or from a
  `pluginmanager.register` hook was a pass at d216e1f; those calls now run
  in the subject worker, where the stub is not present. From that worker,
  `ptrace` of the pytest process, opening `/proc/<ppid>/fd`, a response
  longer than 1_000_000 bytes, a response that is not a JSON object, and a
  write into the sealed tree are not passes. The worker can still write a
  file under the temp directory it was given.
- Checks that import a project helper must ship that helper in the server
  fixture. A helper that exists only in the workspace is treated as planted.
- Verification Evidence expires after 24 hours. Accepting it again means
  running the check again.
- Evidence written before migration 0025 has NULL independence fields and
  cannot accept an artifact through the gate. `kind` and the scorecard's
  reading of historical `PASS` rows are unchanged.
- A check's cwd is the sealed tree, not the Attempt workspace. A write such
  as `open("dropped.txt")` does not move the revision, so it does not trip
  the ADR 0032 drift hook. That hook still downgrades a PASS when the
  workspace itself changes during evaluation.
- Absolute test paths that already live outside the workspace are not copied.
  They keep `--noconftest` and write the report to a private directory.
- Containment still works: the report paths are relative to the sealed root,
  which is what a backend syncs and pulls. A report that does not come back
  is not a pass.

## Example

Before, a passing check and a coverage gap were the same shape:

```json
{"id": "EVIDENCE-ATTEMPT-1", "kind": "evaluation", "status": "PASS",
 "command": "pytest -q tests/test_calc.py", "exit_code": 0, "criterion": "adds"}
```

```json
{"id": "EVIDENCE-ATTEMPT-2", "kind": "evaluation", "status": "INCONCLUSIVE",
 "command": "<missing check>", "criterion": "handles empty input"}
```

After, only the check is verification, and it is bound to the diff it checked:

```json
{"id": "EVIDENCE-ATTEMPT-1", "kind": "evaluation", "status": "PASS",
 "evidence_class": "verification",
 "producer_identity": "sdf-evaluator", "producer_role": "evaluator",
 "verifier_identity": "sdf-evaluator", "verifier_role": "evaluator",
 "subject_artifact_sha256": "<diff artifact sha256>",
 "oracle_digest": "<sha256 of sealed oracle manifest>",
 "environment_digest": "<sha256 of command, runner, oracle, interpreter>",
 "collected_at": "2026-10-31T12:00:00+00:00",
 "expires_at": "2026-11-01T12:00:00+00:00",
 "command": "pytest -q tests/test_calc.py", "exit_code": 0, "criterion": "adds"}
```

```json
{"id": "EVIDENCE-ATTEMPT-2", "kind": "evaluation", "status": "INCONCLUSIVE",
 "evidence_class": null, "command": "<missing check>",
 "criterion": "handles empty input"}
```
