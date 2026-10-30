# ADR 0032: Artifacts and Evidence are bound to immutable Attempt revisions

Status: accepted (maintainer, 2026-10-31, issue #62: "do it now").
The approval scope is the issue's acceptance criteria. ADR 0028 left #62
unapproved; this record is that approval and the design.

## Context

An Attempt's diff and process log were stored with a SHA-256 of their own
bytes and tied to the Attempt id. Evaluator Evidence named its criterion and
its evaluator-output artifact. Nothing recorded which repository snapshot the
Attempt started from or which snapshot the evaluator actually checked:

- the diff was computed against the fixture *as it was at finish time*, so a
  fixture that changed mid-Attempt silently changed the meaning of the diff;
- the E2B path created an "sdf attempt baseline" Git commit
  (`herdr_e2b._ensure_git_baseline`) and threw its id away;
- two Evidence rows with the same criterion could not be told apart by the
  bytes they checked.

The roadmap (P0) asks for "evaluator artifact provenance" and a trace "from
Objective to change and evaluation" without equating a passing command with
validation of the intended outcome.

## Decision

1. **One revision row per Attempt** (`attempt_revisions`, migration
   `0023_attempt_revisions`). `ExecutionService.begin` writes it right after
   it copies the fixture into the Attempt workspace:
   - `repository`: provider-neutral identity. `fixture:<path under
     SDF_FIXTURES_ROOT>`, else `path:<absolute real path>`. No GitHub URL or
     other forge data is canonical.
   - `base_tree_sha256`: the tree id of the workspace the agent receives.
   - `tree_format`: `git-sha256-tree-v1` (below).
   `ExecutionService.finish` then sets, exactly once and before any artifact
   is written:
   - `result_tree_sha256`: the tree id of the workspace the evaluator gets;
   - `base_commit` / `base_commit_source`: a Git commit an adapter created on
     the host before the agent ran. Today only the E2B adapter does this
     (`e2b-baseline`); every other adapter leaves both `NULL`.
   - `finished_at`.

2. **Tree identity is a Git tree id in SHA-256 object format** of every
   regular file and symlink under the directory. Any `.git` component is
   skipped. `.gitignore`, attributes and filters are *not* applied, so an
   agent cannot hide a change behind an ignore rule. The execute bit gives
   mode `100755`, otherwise `100644`; symlinks are hashed, never followed.
   It is computed in-process (`sdf_core/revisions.py`): no `git` binary runs
   on an agent-written tree, so an agent's `.git/config` (hooks, fsmonitor,
   filters) cannot execute on the host. Anyone can reproduce it with
   `git init --object-format=sha256 && git add -A -f && git write-tree`. A
   test checks this against real Git. Workspace copies now keep the
   fixture's execute bit so the base tree matches the fixture.

3. **Every artifact and Evidence row names its revision.** `artifacts` and
   `evidence` get `revision_id` and `tree_sha256` (the result tree the diff
   describes or the evaluator checked). The evaluator-output artifact also
   carries `{"revision": {...}}` in its JSON, so the stored bytes name what
   they checked. Graph node metadata carries the same binding.

4. **Fences.** A new artifact/Evidence row for an Attempt that has a
   revision is refused when its binding is:
   - **missing**: no `revision_id`;
   - **mismatched**: a tree other than the revision's result tree, or no
     result tree yet;
   - **cross-Attempt**: another Attempt's revision;
   - **unknown**: a revision that does not exist.
   Revision rows are never deleted. The base identity is immutable and the
   result fields are set once. These rules are enforced by the ORM session
   guards (`sdf_core.db`) and, on PostgreSQL, by the triggers
   `trg_artifacts_revision_binding`, `trg_evidence_revision_binding` and
   `trg_attempt_revisions_guard`, so direct SQL is refused too.
   `ExecutionService.finish` also fails the Attempt, writing no artifacts,
   when:
   - the Attempt has no base revision;
   - the fixture no longer matches the base tree (the diff would describe a
     different change);
   - an adapter-reported result tree (the E2B adapter reports one after the
     pull) differs from the tree SDF computes.

5. **Read surface.** `GET /attempts/{id}/revision` returns the revision, the
   Attempt's `evidence_mode` and a re-check of every binding and stored
   artifact digest. The status is `verified`, `failed`, `incomplete` (no
   result yet) or `unbound` (no revision). `GET /tasks/{id}/trace` gains
   `revisions`; `GET /evidence/{id}` gains `revision_id`, `tree_sha256` and
   `evidence_mode`. None of these are acceptance Evidence
   (`acceptance_evidence: false`).

6. **Synthetic and live (ADR 0031).** The tree ids are real digests of the
   server-side workspace bytes in both modes. They are never invented, and
   the mode is reported next to them. A synthetic revision proves which
   bytes were evaluated, not that a live agent produced them. `base_commit`
   stays `NULL` unless an adapter really made one. The E2B adapter's dry run
   makes a real local commit, which is recorded while the Attempt stays
   synthetic.

## Consequences

- A PASS is scoped to `result_tree_sha256`: test success is evidence about
  that snapshot only. Human approval (ADR 0014) remains authority evidence,
  not behavioural proof. This ADR does not change either.
- Rows written before 0023 keep `NULL` bindings and read as `unbound`. They
  are never backfilled: the snapshot they checked was never recorded and is
  not guessed. Rows for such Attempts may still be written unbound, which
  keeps in-flight upgrades working. Every Attempt started by
  `ExecutionService` after 0023 has a revision, and `finish` refuses one
  without it.
- `escalation.escalate_attempt` still creates an Attempt row without a
  workspace or revision. It is never finished through `ExecutionService`, so
  any rows written for it directly stay unbound.
- The PostgreSQL insert trigger does one indexed lookup per artifact/Evidence
  row.
- The revision is recorded, not re-materialised: the API verifies the
  records and stored artifact bytes, not a re-run of the agent. Re-deriving
  the result tree from base fixture + diff is possible offline but is not
  automated here.
- A nested Git repository inside a workspace is hashed as files, where Git
  would record a gitlink; that is the only intended difference from
  `git write-tree`.
