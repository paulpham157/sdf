# ADR 0035: held-out and randomized oracles

Status: proposed (implemented with this change; awaiting maintainer acceptance).
Follows [ADR 0034](0034-evidence-independence.md). Does not change Evidence
append-only rules ([ADR 0002](0002-evidence-closes-the-decision-loop.md)) or
revision binding ([ADR 0032](0032-attempt-revisions.md)).

## Context

ADR 0034 stops a subject from forging the evaluator's verdict. It does not
stop the subject from gaming a check whose inputs and expected values it can
see. A function that returns `5` for the one example in the workspace, or a
worker that patches its RPC encoder to report `5`, passes that check and
would be accepted. That is test overfitting. An agent reporting completion
is not success, and neither is a pass on examples the agent could read.

## Decision

1. **A Task declares a hidden oracle by mapping a server-registered check
   that is held-out or randomized.** The public fixture manifest
   `sdf-checks.json` is copied into the workspace, so it can only define
   `visible` checks. Held-out checks are defined in
   `.sdf-held-out/sdf-checks.json` inside the same fixture. A deployment
   registry (`SDF_EVALUATOR_CHECKS_FILE`) may define either, because that
   file is not copied. `randomized: true` is allowed on both kinds. A
   held-out check must name a file under `.sdf-held-out/`. `python -c` is
   refused there: the subject can read the oracle process command line
   (`PR_SET_DUMPABLE` 0 hides `environ`, not `cmdline`).

2. **Held-out bytes stay on the server.** `WorkspaceManager` and the plan
   coordinator's fixture copy skip `.sdf-held-out/`. The Attempt's base
   revision is the workspace. `close_revision` hashes the fixture with that
   directory skipped so the two still match. The result tree does not skip
   it: a file the agent writes under that name is part of the snapshot.
   The diff artifact does not include the fixture bytes of a held-out path.

3. **The evaluator seals held-out files from the fixture only.** They are
   not required to exist in the workspace, and a workspace file at the same
   path is not the oracle. Subject files for that check are placed under
   `.sdf-subject/` inside the sealed tree. The worker's cwd is that
   directory. On Linux x86_64 every subject worker, including one running
   a visible check, restricts its own reads with landlock to its cwd, its
   temp directory, the interpreter's library directories (`/usr/lib`,
   `/usr/lib64`, `/lib`, `/lib64`), `/dev`, `/etc`, and `/proc/self`.
   The whole of `/usr` is not allowed. A visible check's cwd is the sealed
   tree, which does not contain held-out files. The server fixture is not in the
   ruleset, so a visible check cannot open it either. The sealed tree is
   created under the default temporary directory. If that directory would
   sit on an allowed prefix, or landlock cannot be installed, the worker
   exits 86 and the check is not a pass.

4. **A randomized check draws `secrets.token_hex(32)` when the check is
   about to run**, not when the Task is created and not in the workspace.
   The seed is `SDF_ORACLE_SEED` in the oracle process. The worker's
   environment drops every `SDF_ORACLE_*` variable, and the oracle process
   is non-dumpable, so the subject cannot read the parent environment.
   The seed is stored on Evidence as `input_seed` after the run. It is
   part of the oracle digest, so the recorded digest is the oracle that
   ran and the run can be repeated from the seed plus the server files.

5. **Oracle digest** (`sdf-oracle-v2`) covers, in UTF-8: the visibility
   (`visible` or `held_out`), the seed (empty when there is none), the
   registered argv, and each sealed oracle file (path and file hash,
   sorted). Subject files are not included. Two checks that differ in
   visibility or seed do not share a digest. The empty SHA-256 still
   cannot accept (ADR 0034).

6. **Schema** (migration `0026_oracle_visibility`). Both columns are
   nullable. Rows written before this migration are not rewritten:

   | Column | Meaning |
   | --- | --- |
   | `oracle_visibility` | `visible` or `held_out`; NULL if none was recorded |
   | `input_seed` | server seed recorded after a randomized check; NULL otherwise |

   PostgreSQL checks `oracle_visibility` when it is not NULL
   (`ck_evidence_oracle_visibility`). SQLite uses the same rule in the
   ORM session guard.

7. **What is stored.** A held-out Evidence row keeps status, exit code,
   digest, visibility, and seed. Its stdout and stderr are replaced with
   a fixed withheld note (the evaluator's own `[evaluator]` / `[acceptance]`
   reason is kept). A `python -c` body, if one were ever presented as
   held-out, is stored as `<held-out>`. The command that remains is the
   argv the registry already allowed (a path under `.sdf-held-out/`).
   API responses, MCP tool results, timeline entries, and graph-node
   metadata expose visibility and the seed. They do not expose the
   withheld output. The seed is on the row after the run so the result
   is reproducible. It is not on the row before the run.

8. **Acceptance** (`check_hidden_oracle_acceptance`), applied in
   `ExecutionService.finish` after the ADR 0034 per-row gate:
   - A Task whose mapped checks include no held-out check and no
     randomized check is unchanged. A passing, unexpired, artifact-bound
     verification row can still accept it.
   - A Task that maps at least one held-out or randomized check requires
     one PASS row that also passes the ADR 0034 gate and is held-out
     (`oracle_visibility=held_out`) or carries an `input_seed`.
   - Visible PASS rows are stored as PASS. They do not validate that
     Task. A `validates` edge is written only for a row that meets the
     rule above. If every mapped check passed but none of them is an
     acceptable hidden oracle, the Task is `INCONCLUSIVE`. If a hidden
     check failed, the Task is `FAIL`.

9. **Plans that inline argv** (ADR 0014) do not go through the check
   registry. A path under `.sdf-held-out/` is still sealed as held-out,
   and it is still omitted from the workspace copy. A seed is drawn only
   when the registry marks the check `randomized`. An inlined `python -c`
   string in plan content is visible to `get_plan` because the plan
   content is the declaration the lead is shown. That string is not a
   held-out oracle.

## Consequences

- A subject that hard-codes the visible example, or that patches the
  worker's `encode` to report that example, can pass the visible check.
  The held-out or randomized check fails, the visible PASS is stored, and
  the Task is not accepted.
- An honest subject that implements the behaviour passes both, and the
  accepting row names its visibility and, when randomized, its seed.
- Held-out source is not in the workspace, the prompt, MCP tool results,
  or API responses. The seed is not readable from the subject process
  before or during the run. After the run it is on the Evidence row.
- Verification Evidence from before migration 0026 has NULL visibility
  and a NULL seed. It cannot satisfy a Task that declares a hidden
  oracle. Tasks that declare none still use the ADR 0034 gate.
- A failing held-out check does not publish the assertion text. The
  status, the exit code, and the seed (when there is one) are what the
  row retains. Repeating the check means running the server-side files
  again with that seed.

## What remains

- A subject that implements the real behaviour, including by patching
  the encoder to compute the right answer for every input, passes a
  property that is that behaviour. Held-out cases catch a hard-coded
  visible example. They do not catch a correct implementation.
- The oracle source still shares the oracle process with the completion
  stub (ADR 0034). The worker cannot read those files when the check is
  held-out; the oracle process can, because it is the check.
- Landlock allows the interpreter's library directories (`/usr/lib`,
  `/usr/lib64`, `/lib`, `/lib64`), `/etc`, `/dev`, `/proc/self`, the
  worker's cwd, and the worker temp directory. It does not allow the
  whole of `/usr`. It is installed for every subject worker. A visible
  check's cwd is the sealed tree of that check, which has no held-out
  files. The worker can still write outside the frozen tree where the
  mode allows it, as in ADR 0034. It cannot read the held-out files or
  the server fixture unless that fixture itself sits inside one of those
  allowed directories.
- The worker can still `kill` the oracle child's pid. The verdict is
  not a pass (ADR 0034).
- A Plan that inlines a check argv does not get a randomized seed unless
  that check is also a registered check with `randomized: true`. The
  inlined argv is part of the plan content agents can read.

## Example

A Task maps a visible example and a held-out property. The subject
returns `5` for `(2, 3)` and the wrong value otherwise. The visible row
is a pass and is not acceptance. The held-out row fails. The Task is
not accepted.

```json
{"id": "EVIDENCE-ATTEMPT-1", "kind": "evaluation", "status": "PASS",
 "evidence_class": "verification",
 "verifier_identity": "sdf-evaluator", "verifier_role": "evaluator",
 "subject_artifact_sha256": "<diff artifact sha256>",
 "oracle_digest": "<sdf-oracle-v2 of the visible argv and files>",
 "oracle_visibility": "visible", "input_seed": null,
 "command": "pytest -q tests/test_visible.py",
 "criterion": "visible example"}
```

```json
{"id": "EVIDENCE-ATTEMPT-2", "kind": "evaluation", "status": "FAIL",
 "evidence_class": "verification",
 "verifier_identity": "sdf-evaluator", "verifier_role": "evaluator",
 "subject_artifact_sha256": "<diff artifact sha256>",
 "oracle_digest": "<sdf-oracle-v2 of the held-out argv, files, and seed>",
 "oracle_visibility": "held_out",
 "input_seed": "<64 hex chars, recorded after the run>",
 "command": "pytest -q .sdf-held-out/test_property.py",
 "stdout": "[held-out] evaluator output withheld (ADR 0035)",
 "criterion": "held-out property"}
```

An honest subject on the same Task produces a PASS for the second row
with the same shape, `status` `PASS`, and that row is what accepts the
artifact. A Task with only visible checks still stores
`"oracle_visibility": "visible", "input_seed": null` and is accepted
under ADR 0034, with no extra requirement.
