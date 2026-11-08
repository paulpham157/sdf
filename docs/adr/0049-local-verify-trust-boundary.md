# ADR 0049: the local verify command is a narrower trust boundary

Status: proposed. This record describes the command introduced at
`862266816cfe0ecbb3723cf144f9ffdafd559734`. It does not accept that command
as an isolated or independent verification guarantee, and it does not treat
a green test suite as proof of that guarantee.

Follows [ADR 0001](0001-postgresql-as-sdf-state-source.md),
[ADR 0014](0014-plan-gate-and-agent-messaging.md),
[ADR 0015](0015-api-authentication.md),
[ADR 0019](0019-per-turn-process-containment.md),
[ADR 0025](0025-server-registered-checks.md),
[ADR 0026](0026-tool-proxy-actor-binding.md),
[ADR 0031](0031-live-evidence-mode-for-run.md),
[ADR 0032](0032-attempt-revisions.md),
[ADR 0039](0039-evidence-independence.md),
[ADR 0041](0041-keyless-dispatch-and-orphan-recovery.md),
[ADR 0042](0042-evidence-classes.md), and
[ADR 0043](0043-evidence-content-mac.md).

Numbered 0049 because 0048 is session import and history audit. This file
adds no code, no test, and no metric. It does not change the HTTP API.

`782a8f602636fc5160d418328061b95934b89852` is a later commit. It gives each
process its own turn-record directory and `SDF_TURN_ID` value for the
headless Pi and OpenCode guards in `sdf_core/turn_guard.py`. `sdf_core/verify.py`
does not call `guard_argv`. That commit is not part of the local verify path
described here.

## Context

`python -m sdf_core.verify` is the user-facing command at `8622668`. It
copies the caller's repository into a private snapshot, runs an already
logged-in Codex or Claude CLI in that snapshot, and asks
`ExecutionService` to evaluate checks that were read from the snapshot
before the CLI started.

The HTTP API still has its own gates: plan approval, bearer tokens, Tool
Proxy, and the fake-or-live `POST /tasks/{id}/run` adapter. Those gates
remain in the code. The local command does not call them, so they are not
armed for this path.

A Task reaching `succeeded` on this path means the evaluator recorded a
passing verification for the frozen checks after the CLI exited 0. That is
task completion under those checks. It is not, by itself, a claim that the
business goal was met, that a second person approved the work, or that the
verifier was isolated from the agent.

## Decision

Treat `python -m sdf_core.verify` as a convenience for one trusted user
running trusted code on their own computer. Do not describe it as the same
guarantee as API verification, where a plan can be required, a token
identifies the caller, tool actions go through the Tool Proxy, and the
evaluator's signing key is not sitting on a path the agent is allowed to
read.

The command may keep reusing the execution services below. Reuse is not
equivalence. The sections that follow state what is reused, what is not
armed, and what is still unproven.

### What the local path reuses

`run_verification` builds one dispatch key from the snapshot tree id, the
agent name, the instruction, and the frozen check argv, then calls
`ExecutionService.run` (`sdf_core/verify.py`, `run_verification`).

That call reuses the existing Attempt machinery:

- Dispatch-key uniqueness. `ExecutionService.begin` returns the Attempt that
  already holds the key instead of starting another one.
- Executor leases and orphan recovery. `begin` calls
  `_recover_orphans_independently` before it looks up the key, and it claims
  the executor when it creates a new Attempt.
- Guarded state transitions. `finish` moves the Task with
  compare-and-swap through `evaluating`.
- Revision hashes and drift detection. The Attempt revision records the
  workspace tree, and a check that changes the tree after the result hash
  does not keep a PASS.
- Deterministic evaluation. The service uses `DeterministicEvaluator` with
  `reference` set to the snapshot, so named check files are compared with
  the pre-agent copy.
- Idempotent replay of the recorded Attempt. The same key returns that row.
  A failed key stays occupied. Replay is not a retry.

`freeze_checks` resolves `sdf-checks.json`, or names the pytest files in
the snapshot, before `LocalCliAdapter.run`. The evaluator is given those
argv. It does not re-read a manifest the agent could have rewritten in the
workspace.

### What the local path does not arm

Compared with the API workflow, this command:

- does not create a Plan and does not approve one. `check_plan_gate`
  returns immediately when the Task has no plan link, because
  `require_plan` stays false.
- does not authorize an API token. The Attempt principal is the string
  `local-<user>` from `getpass`. It is not a bearer credential, and there
  is no second-person requirement.
- has no cancel operation. `main` does not catch `KeyboardInterrupt`. An
  interrupt leaves settlement to the next `begin`, which recovers a dead
  executor's Attempt and then replays that row if the key matches.
- does not enforce network denial. Landlock, as applied by
  `FixtureSandbox` through `process_containment`, has no network rule.
  `SDF_NETWORK=denied` is an environment label.
- does not mediate tool actions through the Tool Proxy. `LocalCliAdapter`
  starts the CLI with `FixtureSandbox.run`.
- does not run the ADR 0019 turn guard. A descendant that leaves the
  sandbox process group is outside `FixtureSandbox`'s timeout kill, and
  nothing in this command sweeps it afterwards.

Deleting `scripts/demo.py` and `fixtures/offline-evidence` removed a
synthetic demo. It did not remove the API gates, and it did not add the
missing guarantees above. `tests/test_demo_script.py` was the automated
walk of that demo. Other evaluator tests remain. They are not runs of
`python -m sdf_core.verify`.

### Trust and evidence authenticity

`LocalCliAdapter` promotes `evidence_mode` to `live` after the confined
child returns without a Landlock failure. The child is confined by
Landlock: read of `/`, write only under the workspace
(`sdf_core/process_containment.py`, the ruleset that adds `/` for read and
the workspace for write). `HOME` is the user's home so the CLI can open
its own login file. `login_present` only stats that file.

The local signing material for `SDF_AUTH_PEPPER` and
`SDF_EVIDENCE_MAC_KEY` is stored in `runtime-secrets` under the data
directory (`sdf_core/local_state.py`, `ensure_local_secrets`). That file
is not in the workspace write root, so this ruleset does not grant the
child write there. The ruleset does grant read of `/`, and the file lives
on the same machine. A content MAC made with a key the agent is allowed to
read does not show that the signer was isolated from the agent. ADR 0043
already states that the MAC binds the stored verifier string and does not
prove the named verifier is an independent party.

`check_verification_acceptance` refuses verification whose
`verifier_identity` equals the Attempt's agent, or whose subject hash does
not match the diff artifact. On this path the verifier string is
`sdf-evaluator` and the agent string is `codex` or `claude`. Those are
different strings in one process. The check does not place the evaluator
under another user, another host, or a key the agent cannot read.

The local path is therefore a trusted-user, trusted-code convenience. It
is not the isolated, independent verification guarantee of the API path.

### Shared SQLite default

When `SDF_DATABASE_URL` is unset and the process is not hermetic, the API
import, `python -m sdf_core.auth_cli`, and this command share one SQLite
file created with `create_all` (`sdf_core/api.py` at import,
`sdf_core/local_state.py`, `ensure_local_database`). Sharing the file
means they see the same rows. It does not add migrations, and it does not
by itself make Evidence harder to forge.

`create_all` is not Alembic. An explicit PostgreSQL URL is left as given.
That URL does not generate the local secrets and does not turn HTTP
authentication off. The verify command still does not present a token.

`O_EXCL` makes one creator win the new database file. Two processes
generating `runtime-secrets` at the same time are not shown to be safe.
No test in this repository demonstrates that race.

## Validation

The proofs below are not implemented by this ADR. Where a test already
exists, it is named with the limit of what it shows. A green full suite
does not close these gaps and does not prove a real Codex or Claude run.

Pending, not implemented:

- A check file changed in the workspace must not produce an accepted PASS
  on this command.
- A failed dispatch key must be returned unchanged and must not start a
  second live CLI.
- An interrupt during `LocalCliAdapter.run` must be recovered by the next
  `begin` without starting a second live CLI.
- The confined child must fail to read `runtime-secrets`.
- Two processes initializing the SQLite file and `runtime-secrets`
  together must have one defined result, without a split pepper.

Proposed metrics, not implemented. No counter, log line, or dashboard in
the current code reports them:

- Orphan recoveries: Attempts settled by `_recover_orphans_independently`
  on this command.
- Duplicate dispatch suppression: calls that returned an existing Attempt
  for the same key.
- Rejected evidence: verification rows that `check_verification_acceptance`
  refused on this command.

Existing tests, and what they do not show:

- `tests/test_verify_own_repo.py::test_own_repo_checks_are_evaluator_evidence_and_replay`
  uses a stand-in `codex` executable. It records pytest as the verification
  command and `sdf-evaluator` as the verifier string, and a second call
  does not run the stand-in again. It does not run a real CLI, does not
  tamper with a check file, and does not cover a failed key.
- `tests/test_verify_own_repo.py::test_landlock_blocks_a_write_outside_the_workspace`
  shows a write outside the workspace is denied. It does not show that a
  read of `runtime-secrets` is denied.
- `tests/test_verify_own_repo.py::test_confinement_failure_does_not_run_unconfined`
  and `test_main_does_not_fall_back_to_fake` show the command refuses to
  start when Landlock is unavailable and does not switch to the fake
  adapter.
- `tests/test_verify_own_repo.py::test_user_repository_is_not_a_fixture_dir`
  shows `resolve_fixture` still rejects a path outside `SDF_FIXTURES_ROOT`.
- `tests/test_local_state.py::test_local_default_permissions_and_stable_secrets`,
  `test_api_and_token_cli_share_the_local_database`,
  `test_hermetic_resolve_does_not_create_files`,
  `test_explicit_url_does_not_create_local_files_or_secrets`, and
  `test_short_pepper_is_not_replaced` cover the private file mode, the
  shared URL, the hermetic import, an explicit URL, and a short pepper.
  They do not cover two creators at once.

## Consequences

- Readers should not cite this command as proof of plan approval,
  separation of duties, network isolation, Tool Proxy mediation, or an
  independent signer.
- The API gates listed above stay where they are. Hardening this command
  up to those gates would be a separate decision.
- `782a8f6` remains the headless turn-guard fix. It does not shrink the
  read granted to the local verify child, and it does not add the pending
  proofs above.
