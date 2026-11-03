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
     SDF_FIXTURES_ROOT>`, else `path-sha256:<16 hex of the real path digest>` (no host path is exposed). No GitHub URL or
     other forge data is canonical.
   - `base_tree_sha256`: the tree id of the workspace the agent receives.
   - `tree_format`: `git-sha256-tree-v1` (below).
   `ExecutionService.finish` then sets, exactly once and before any artifact
   is written:
   - `result_tree_sha256`: the tree id of the workspace when the evaluator
     starts (see §4b for checks that change it);
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
   fixture's owner-execute bit (owner only) so the base tree matches the fixture.

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
   Unbound rows are allowed only for an Attempt that existed before 0023.
   That is decided by a durable marker, not by a missing revision row:
   - `attempts.revision_required` is `NULL` on every Attempt that existed
     when 0023 ran;
   - it is forced to `TRUE` on every Attempt inserted afterwards, by the ORM
     guard and, on PostgreSQL, by `trg_attempts_revision_required`, whatever
     the writer says;
   - it is immutable.

   An Attempt created outside `ExecutionService.begin` after 0023 (for
   example by `escalation.create_escalated_attempt`, or by direct SQL on
   PostgreSQL) therefore cannot get unbound artifacts or Evidence. Neither
   can an unknown Attempt.

   Revision rows are never deleted. The base identity is immutable and the
   result fields are set once. An artifact's `revision_id`/`tree_sha256`
   never change after insert; Evidence is append-only already.

   These rules are enforced by the ORM session guards (`sdf_core.db`) and,
   on PostgreSQL, by triggers, so direct SQL is refused too:
   - `trg_artifacts_revision_binding` and `trg_evidence_revision_binding`
     (insert);
   - `trg_artifacts_binding_immutable` (update);
   - `trg_attempt_revisions_guard`;
   - `trg_attempts_revision_required`.

   On SQLite only the ORM path is fenced: direct SQL there is not, as for
   the other SQLite guards.
   `ExecutionService.finish` also fails the Attempt, writing no artifacts,
   when:
   - the Attempt has no base revision;
   - the fixture no longer matches the base tree (the diff would describe a
     different change);
   - an adapter-reported result tree (the E2B adapter reports one after the
     pull) differs from the tree SDF computes.

4b. **Drift during evaluation.** Checks run in the Attempt workspace one
   after another, and a check can change files.
   - `ExecutionService` hashes the workspace again after every check
     (`DeterministicEvaluator.evaluate(after_check=...)`), and once more
     after the whole evaluation for an evaluator that does not call the
     hook.
   - If the tree differs from `result_tree_sha256`, a PASS becomes
     `INCONCLUSIVE` (confidence at most 0.2) with an explanatory stderr
     note. A FAIL stays FAIL. All later checks see the drift too.
   - The evaluator-output artifact records `tree_after_check_sha256` and
     `tree_drift`, as does the Evidence node metadata (`tree_drift`).
   - So a PASS always describes the recorded result tree. Hardened
     `python`/`pytest` checks run with `-B`, so bytecode caches no longer
     count as drift.
   - The hook hashes the Attempt workspace, not the check's cwd. Since
     [ADR 0039](0039-evidence-independence.md), a sealed check's cwd is the
     sealed tree and that process's `TMPDIR` is that tree; the subject
     worker's `TMPDIR` is a private directory under `/tmp`. A relative write
     or pytest `tmp_path` there does not change `result_tree_sha256`. A
     write that does change the Attempt workspace still makes a PASS
     `INCONCLUSIVE`. A FAIL stays FAIL.

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
   bytes were evaluated (unchanged, §4b), not that a live agent produced them. `base_commit`
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
- `escalation.create_escalated_attempt` still creates an Attempt row without
  a workspace or revision. It is never finished through `ExecutionService`.
  Because of `revision_required`, rows written for it directly are refused
  rather than accepted unbound.
- The PostgreSQL insert trigger does one indexed lookup per artifact/Evidence
  row.
- The revision is recorded, not re-materialised: the API verifies the
  records and stored artifact bytes, not a re-run of the agent. Re-deriving
  the result tree from base fixture + diff is possible offline but is not
  automated here.
- A nested Git repository inside a workspace is hashed as files, where Git
  would record a gitlink; that is the only intended difference from
  `git write-tree`.
