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

   Format: `{"checks": {"<id>": ["pytest", "-q"]}}` or
   `{"checks": {"<id>": {"argv": [...], "timeout_seconds": 60}}}`. Every argv
   must start with a bare allowlisted name (`python`, `python3`, `pytest`,
   `ruff`), with no path separators, NUL bytes or oversized arguments; the
   timeout is 1-600 s. Unknown keys and an id defined in both sources are
   configuration errors. Unknown ids or a bad registry → 400 before any
   Attempt or sandbox exists.
3. **Hardened evaluator for every caller** (public runs, plans, coordinator):
   - argv[0] must be a bare allowlisted name; the allowlist can only be
     narrowed by callers;
   - the executable is resolved on a safe `PATH` (absolute entries outside the
     workspace) and must not resolve into the workspace, so an agent cannot
     plant `./bin/python`;
   - checks run through `FixtureSandbox`: reduced environment (no server
     variables), `HOME` = workspace, new session with process-group kill on
     timeout, bounded output;
   - timeouts, errors and refusals are `INCONCLUSIVE`, never `PASS`.
4. **Optional containment** via `SDF_EVALUATOR_CONTAINMENT`:
   `off` (default), `prefer` (use the promoted containment backend,
   `SDF_CONTAINMENT_BACKEND` + `SDF_CONTAINMENT_SMOKE=passed`, when present)
   or `require` (no backend → every check is `INCONCLUSIVE`). Any other value
   → 400.
5. **Plans keep argv checks.** Plan subtasks are human-approved (ADR 0014) and
   the approver sees the exact argv; they now run through the hardened
   evaluator above.

## Consequences

- **Breaking API change:** `commands` is gone and `criterion_checks` takes
  ids. Callers must register checks in the fixture's `sdf-checks.json` or the
  server checks file.
- With `SDF_EVALUATOR_CONTAINMENT=off`, checks still execute
  agent-modified workspace code (e.g. `pytest` imports the agent's modules) on
  the host with the server user's filesystem privileges, but without its
  environment. Production deployments should set `require` with a promoted
  backend.
- An agent can still edit test files in its workspace copy; for plans
  `protected_paths` mitigates this. Registering checks that live outside the
  workspace (e.g. `python -c ...` assertions) avoids it.
