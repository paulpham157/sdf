# ADR 0048: import prior agent sessions as Claims; audit git history as proxies

Status: proposed
Follows [ADR 0039](0039-evidence-independence.md) and
[ADR 0042](0042-evidence-classes.md). Does not change Evidence append-only
rules ([ADR 0002](0002-evidence-closes-the-decision-loop.md)), revision
binding ([ADR 0032](0032-attempt-revisions.md)), the sealed evaluator
([ADR 0039](0039-evidence-independence.md)), or the retirement of Aider
and Cursor Agent as live kinds
([ADR 0016](0016-remove-aider-and-cursor-agent-kinds.md)).

Numbered 0048 because 0047 is the same-origin console. This file proposes
two read-only commands. It does not add a migration and it does not
change the acceptance gate.

## Context

SDF's thesis is that an agent saying it is done is not success. A Task is
accepted only by independent verification Evidence (`sdf-evaluator`).
Execution Evidence records that an Attempt ran and cannot accept a Task
(ADR 0042). A Claim is what an agent reports about its own work; it
triggers host verification and is never Evidence (`CONTEXT.md`;
`AgentClaimRow` in `sdf_core/db.py` says the same: "What an agent *says*
it did (a `report`). Never Evidence.").

A team that used coding agents for years before SDF has no Task, no
recorded acceptance criteria, and no evaluator Evidence. They do have
two residual traces:

1. Local session history on the operator's machine (Claude Code JSONL,
   Cursor Composer / transcripts, Codex CLI rollouts, Aider markdown).
   The formats are surveyed in
   [docs/research/session-history-formats.md](../research/session-history-formats.md).
2. Git, pull request, CI, and revert history. A read-only trial of that
   reverse-audit on `paulpham157/sdf` (main at `0fa0a6a`, 7 November 2026)
   found proxy signals, not acceptance: no post-merge revert on `main`;
   one in-branch `git revert` 2.7 minutes after the original commit
   inside PR #127; six strong fix-after-merge pairs, most within an hour
   (`#70→#72`, `#73→#74`, `#89→#97/#101`, `#114→#120`, `#112→#121`);
   32 of 87 merged PRs landed before GitHub Actions pytest existed
   (PR #75, 30 October 2026); PR #84 merged while `gitleaks (full history)`
   was red; PRs #99 and #125 merged while pytest was still running;
   two flakes seen in logs (`KeyError: 'forged'` in
   `tests/test_core_mcp.py` and `test_material_decision_gate`, issue
   #140). Zero merged PRs had a human GitHub approval. Producer bodies
   often claimed `uv run pytest` had passed. None of those traces named
   an acceptance criterion.

Without a deliberate mapping, either leftover history is ignored (the
team cannot feel a difference quickly) or someone treats a session
"done" or a green CI run as if it were verification Evidence. This ADR
chooses a third path: import and audit as labeled, self-reported,
non-accepting records.

## Decision

1. **Two commands, both read-only toward their sources.** `sdf import`
   reads local session files the operator names. `sdf audit` reads git
   history, pull requests, CI check-runs, and revert commits. Neither
   command pushes, comments, opens issues or pull requests, rebases, or
   force-pushes. `sdf audit` does not write to the audited repository.

2. **Import never auto-accepts a Task.** A session that claims "done",
   "all tests pass", Codex `task_complete`, or Cursor `turn_ended` with
   `status: "success"` becomes a Claim (`claimed_status` plus a redacted
   body), optionally attached to a newly created or existing Attempt.
   If execution Evidence is written at all, its class is `execution`,
   its producer is the imported agent identity (for example
   `claude-code` plus the session id), and its `producer_role` is
   `agent`. The acceptance gate in ADR 0039 and ADR 0042 already
   rejects that row. The imported Task, if one is created, stays in a
   state the UI and API can label `self-reported` / `no evidence yet`.
   It does not become `succeeded`.

3. **Mapping to existing entities, without new Evidence classes.**

   | Source | SDF record | Notes |
   | --- | --- | --- |
   | One session file (or one Aider session boundary) | Attempt | Distinct from a live `/run`. Identity records the tool and session id. |
   | User's first request / session title | optional Task stub | Criteria unknown unless the operator supplies them. A stub cannot be accepted. |
   | Assistant "done", `task_complete`, `turn_ended`, "tests pass" | `AgentClaimRow` | A report. Never Evidence. |
   | Tool names, exit codes if structured | Attempt metadata or Claim text | Default: store names and statuses, not stdout. |
   | Session `cwd`, `gitBranch`, Codex `git.commit_hash` | Attempt revision hint | Bind a revision only when the source actually stored a SHA (ADR 0032). Do not invent one. |
   | `sdf audit` finding (revert, fix-after-merge, merged-red-CI, flake, claim-vs-CI) | report row, not Evidence | A proxy signal with a kind, pointers, and a caveat. It does not accept or reject a Task. |

   Execution Evidence still cannot accept. Verification Evidence is
   written only when `sdf-evaluator` later runs a server-registered
   check against a bound revision. Outcome Evidence remains a human
   Objective-level record (ADR 0042) and is out of scope here.

4. **Privacy, redaction, consent, and no upload.** `sdf import` is
   opt-in: it reads only paths the operator passes (a file, a project
   slug directory, or an explicit home subdirectory such as
   `~/.claude/projects/<slug>`). It does not scan the whole home
   directory by default. It does not upload session files, send them to
   a model, or share them with SDF's servers. Default persistence keeps
   identifiers (session id, timestamps, tool name, `cwd`, branch,
   commit if present) and a redacted claim summary. Raw prompts, file
   bytes, tool arguments, and command output stay on disk in the
   original file unless the operator asks for a local, explicitly
   flagged raw archive. Secret-shaped strings are redacted. Aider,
   Claude, Cursor, and Codex histories all contain prompts and often
   contain secrets; the survey lists those risks per tool.

5. **Audit findings are proxies and must say so.** The reverse-audit
   trial showed the false-positive paths an implementation must
   label, not hide: conventional `fix:` titles that are not
   regressions; issue numbers that collide with pull request numbers;
   a test-gap PR followed by a fix (`#126` then `#141`) which is not
   "accepted work that later broke"; in-branch reverts that never
   landed on `main`; CI failures on `main` caused by a known flake
   (issue #140, `KeyError: 'forged'`) rather than the merged patch;
   demo text that contains "done" / "all tests pass" (PR #149);
   producer pytest counts in a PR body; agent work authored under the
   operator's login. `sdf audit` reports confidence and the matching
   rule next to each finding.

6. **Minimal first slice (no migration).**

   - `sdf audit` against a local clone plus optional `gh` read APIs:
     revert commits, explicit fix-after-merge pairs, check conclusions
     compared with `mergedAt`, workflow `run_attempt` greater than 1,
     and claim language in PR bodies. Emit JSON and a readable report.
     This is the shape already prototyped against `paulpham157/sdf`.
   - `sdf import` with one adapter (Claude Code parent-session JSONL
     under `~/.claude/projects/*/*.jsonl`) that reads `sessionId`,
     `cwd`, `gitBranch`, timestamps, line `type`s, and tool *names*
     when present, and writes a local import report of Attempts and
     Claims. Cursor, Codex, and Aider adapters follow behind the same
     interface once the Claude path is proven.
   - No database migration in this slice. Persisting into a running
     SDF instance (Attempt + `AgentClaimRow`) is a later slice and
     still cannot accept a Task.

7. **Non-goals.** This ADR does not: accept a Task from imported
   history; treat CI green, gitleaks, or Sourcery as verification
   Evidence; revive Aider or Cursor Agent as live execution kinds
   (ADR 0016); upload or train on session files; become a general
   session viewer or analytics product; parse every undocumented extra
   field a vendor may add; claim a commit binding the source did not
   store; or change merge policy on `paulpham157/sdf`.

## Consequences

- A team can see, in one local report, what their agents claimed and
  where git history later disagreed, without SDF pretending those
  traces were evaluator Evidence.
- The first slice is offline and fail-closed: unknown file families are
  skipped with a warning (Cursor in particular has at least three
  on-disk families that have already drifted).
- Operators must point import at a path. Session files remain the
  operator's; SDF stores redacted projections unless asked otherwise.
- Until criteria exist for a stub Task, the only honest label is
  `no evidence yet`. Filling criteria and running `sdf-evaluator` is a
  separate, later step.

## Open questions

- Should slice 2 persist imported Attempts into the SDF database by
  default, or keep reports as the only output until an operator
  attaches them to an Objective?
- When Codex stored `git.commit_hash` and the tree has moved on, should
  import pin that SHA as `base` even if the working tree now differs?
- Which additional tools (OpenCode, Pi, Copilot Chat) are worth an
  adapter after Claude, Cursor, Codex, and Aider?

## Sequencing

Survey of formats (this proposal's companion doc) and a trial
`sdf audit` against this repository come first. Implementation of the
two commands follows this ADR and does not land in the same change
unless a later slice explicitly says so.
