# ADR 0025: server-registered evaluator checks

Status: proposed (branch `sec/52-registered-checks`, issue #52)

## Context

`POST /tasks/{id}/run` accepted `commands` and `criterion_checks` as raw argv
lists. The evaluator checked only the executable's basename against an
allowlist and then ran the argv with `subprocess.run` on the API host, in the
server's full environment. Two consequences (issue #52):

1. Any operator-token holder could run `python -c ...` on the host and read
   the server's environment (database URL, auth pepper, provider keys); the
   output landed in Evidence and Artifacts.
2. `/any/dir/bin/python` passed the basename check, so an arbitrary script ran
   on the host.

It also let the caller choose the command that decides PASS, so Evidence
could be self-forged (`python -c pass` for every criterion).

## Decision

1. **No caller-supplied commands on the public API.** `RunTaskRequest` drops
   `commands`; `criterion_checks` maps each acceptance criterion to a list of
   **check ids** (`[A-Za-z0-9][A-Za-z0-9._:-]{0,119}`). Unknown request
   fields are rejected (`extra="forbid"`), so the old shapes return 422.
2. **Server-controlled registry** (`sdf_core/checks.py`). Ids resolve to argv
   from:
   - `sdf-checks.json` in the *original* fixture directory under
     `SDF_FIXTURES_ROOT` (never the agent's workspace copy; a symlinked
     manifest is refused), and/or
   - the file named by `SDF_EVALUATOR_CHECKS_FILE`.

   Format: `{"checks": {"<id>": ["pytest", "-q", "tests/test_calc.py"]}}` or
   `{"checks": {"<id>": {"argv": [...], "timeout_seconds": 60}}}`. Every argv
   must start with a bare allowlisted name (`python`, `python3`, `pytest`,
   `ruff`), with no path separators, NUL bytes or oversized arguments; the
   timeout is 1-600 s. A pytest check must name its test files explicitly
   (`path.py` or `path.py::node`, no directories, globs or `..`) and may only
   use a small option allowlist (`-q`, `-v`, `-x`, `-k`, `-m`, `-r…`,
   `--tb=…`, `--maxfail=N`, ...; no `-p`, `-c`, `-o`, `--rootdir`,
   `--pyargs`, `-qq`), so pytest never collects a file the agent added.
   Unknown keys and an id defined in both sources are
   configuration errors. Unknown ids or a bad registry → 400 before any
   Attempt or sandbox exists.
3. **Hardened evaluator for every caller** (public runs, plan runs, coordinator):
   - argv[0] must be a bare allowlisted name; the allowlist can only be
     narrowed by callers;
   - the executable is resolved on a safe `PATH` (absolute entries outside the
     workspace, world-writable directories such as `/tmp` dropped) and must not
     resolve into the workspace, so an agent cannot plant `./bin/python`;
   - checks run through `FixtureSandbox`: an environment built from scratch
     (no server variables; `PYTHONPATH`, `PYTHONSTARTUP`, `PYTEST_ADDOPTS`,
     `PYTEST_PLUGINS`, ... are never passed, which a helper asserts), `HOME` =
     workspace, new session with process-group kill on timeout, bounded output;
   - the argv is rewritten so files in the workspace cannot change *how* the
     check runs (`sdf_core.evaluator.harden_argv`):
     - `python`/`python3` run with `-I` (no `PYTHON*` variables, no user site,
       no cwd or script directory first on `sys.path`) through a small
       bootstrap that **appends** the workspace to `sys.path` and then runs the
       original `-c CODE`, `-m MODULE` or script. A script check first gets its
       own directory appended (`python3 checks/run.py` can import
       `checks/helper.py`), after the stdlib and site-packages, so a sibling
       `fractions.py` still cannot shadow anything. The code under test stays
       importable, but a workspace `fractions.py`, `pytest.py`,
       `sitecustomize.py` or user-site `.pth` cannot shadow the stdlib or an
       installed package. Only `-c`, `-m`, a script path and the harmless flags
       `-B -u -O -OO -b -bb -q -W <arg>` are accepted (validated at load time);
     - `pytest` (bare or `python -m pytest`) gets `--noconftest -c /dev/null
       --rootdir / -p no:cacheprovider --import-mode=append` and
       `PYTEST_DISABLE_PLUGIN_AUTOLOAD=1`: no workspace `conftest.py`,
       `pytest.ini`/`pyproject.toml`/`tox.ini`/`setup.cfg` (`addopts`, `-p`,
       `pythonpath`), cache, or entry-point plugin from a planted `*.dist-info`;
       test modules are appended to `sys.path`. Checks therefore cannot rely on
       a `conftest.py` fixture either. On the host, bare `pytest` runs as
       `<its interpreter> -I -c BOOTSTRAP -m pytest ...` (interpreter from the
       console script's shebang, outside the workspace), so it imports
       root-level code like `python -m pytest`. Inside a containment backend
       bare `pytest` keeps the plain form (only the flags above); prefer
       `python -m pytest` there. For `python3 -m pytest` the venv python must be
       first on the server's `PATH` (the safe `PATH` is derived from it);
   - **the check's own files are verified against the server fixture.** Every
     pytest test file and python script a check names by a relative path must
     be a regular file in the original fixture, and the workspace copy must be
     byte-identical and not a symlink, or the check is `INCONCLUSIVE` without
     running. Absolute paths must lie outside the workspace;
   - **the pytest summary must agree with the exit status.** Imported agent
     code can still end pytest with `0` (`os._exit(0)`, an `atexit` hook). An
     exit-0 pytest check is `PASS` only if pytest's final summary reports at
     least one passed test and no failures or errors; otherwise it is `FAIL`
     (`INCONCLUSIVE` if the output was truncated before the summary). This
     can only downgrade a result, so a printed fake summary gains nothing.
     JUnit XML was not used: a same-uid check can rewrite the file, and a
     containment backend cannot write a host path;
     - `ruff` gets `--isolated --no-cache`, so a workspace `ruff.toml` /
       `pyproject.toml` cannot exclude every file. Select rules in the argv;
   - on the host, the server process is made non-dumpable
     (`prctl(PR_SET_DUMPABLE, 0)`) before a check starts. A check runs as the
     server's uid, and without this it could read `/proc/<server pid>/environ`
     (the server's initial environment: API keys) or `/proc/<pid>/mem`. Checks
     are `execve`d and therefore dumpable again; they are unaffected. If
     `prctl` fails on Linux the evaluator logs `server_process_not_protected`.
     **This protects only the server process itself**, not other same-uid
     processes that hold the same environment (see Consequences);
   - timeouts, errors, refusals and unsupported forms are `INCONCLUSIVE`,
     never `PASS`;
   - every Evidence records where its check ran (`runner`: `host` or the
     containment backend's name) in the evaluator-output artifact and the
     artifact node's metadata.
4. **Containment** via `SDF_EVALUATOR_CONTAINMENT`: `off`, `prefer` (use the
   promoted containment backend, `SDF_CONTAINMENT_BACKEND` +
   `SDF_CONTAINMENT_SMOKE=passed`, when present; otherwise run on the host and
   log `evaluator_runs_on_host`) or `require` (no backend → every check is
   `INCONCLUSIVE`). Any other value → 400. **Unset, the mode is `require`
   whenever the workspace was written by a live agent** (`SDF_RUN_ADAPTER=live`
   and every plan run) and `off` only for the deterministic fake adapter.
   Running live-agent checks on the host needs an explicit `off`/`prefer`, which
   logs `evaluator_runs_on_host`. The mode applies to plan runs too: the
   coordinator (synchronous and background) uses the same configured
   evaluator as `/run`.
5. **Plans keep argv checks.** Plan subtasks are human-approved (ADR 0014) and
   the approver sees the exact argv. At proposal time each argv goes through
   the same `checks.validate_argv` as a registered check (bare allowlisted name,
   no path, hardenable python form), and at run time through the hardened
   evaluator above.
6. **Check sources must be server-controlled.** A `fixture_dir` (and therefore
   its `sdf-checks.json`) or `SDF_EVALUATOR_CHECKS_FILE` that resolves under a
   workspace or artifact root (`SDF_WORKSPACE_ROOT`, `SDF_ARTIFACT_ROOT`, the
   default `.sdf-workspaces`/`.sdf-artifacts`) is refused with 400. Registry
   files are opened with `O_NOFOLLOW` and must be regular files (`fstat`), at
   most 1 MiB. Timeouts are looked up by check id, so two ids with the same
   argv keep their own limits.

## Consequences

- **Breaking API change:** `commands` is gone and `criterion_checks` takes
  ids. Callers must register checks in the fixture's `sdf-checks.json` or the
  server checks file.
- **Breaking default for live runs:** with no containment backend promoted,
  live `/run` and plan-run checks are `INCONCLUSIVE` until the operator
  promotes a backend or sets `SDF_EVALUATOR_CONTAINMENT=off` explicitly.
- Checks that relied on a workspace `conftest.py`, pytest ini options or ruff
  config must move those options into the registered argv.
- **What remains (inherent).** A check exists to run the agent's code, so the
  agent controls whatever the check imports: an imported module can call
  `sys.exit(0)`/`os._exit(0)` and end the check with status 0, or patch the
  assertion it is imported by. The hardening above stops the workspace from
  changing the interpreter, the test runner and its configuration; it cannot
  make imported code honest. Prefer checks whose verdict does not depend on
  code the agent wrote besides the function under test, and use containment.
- With `off` (or `prefer` without a backend), checks still run on the host
  with the server user's filesystem privileges: a check can read any file the
  server user can (e.g. a host `.env`, the database file) and print it into
  Evidence. Only the server process's own `/proc` entries are protected
  (above): the `environ` of every other process running as the same user
  stays readable, e.g. the shell or supervisor that launched the server with
  the keys exported, a `uv run` parent, or a multi-worker master. Files are
  not protected either. Host mode is therefore not a secret boundary.
  Production deployments should use `require` with a promoted backend, or at
  least run checks as a separate uid that can read neither the server's files
  nor its processes.
- Non-dumpable also means no core dumps and no same-uid `ptrace` of the
  server (e.g. `py-spy`) - acceptable for a server. It does not protect
  against a root-owned check, and it is a no-op off Linux.
- An agent can still edit test files in its workspace copy, but a check
  whose named test file or script no longer matches the fixture is
  `INCONCLUSIVE`. Helper modules those files import (and `-c`/`-m` checks)
  are not verified: they are "imported code" in the sense of the inherent
  limit above. Test files under a server directory passed by absolute path
  avoid the fixture comparison entirely.
