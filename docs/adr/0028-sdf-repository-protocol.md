# ADR 0028: SDF Repository Protocol v0.1

Status: accepted (maintainer, 2026-10-05, issue #59). Implemented by issue #61.

The acceptance comment on #59 named this record
"0011-sdf-repository-protocol"; that file was never committed and
0011 is taken by the OpenCode ADR, so the record uses the next free number.

## Context

A read-only study of `hoangnb24/repository-harness` at commit
`e765792b635b4d5e3e5fc0578f82f9ca5dea2681` found useful repository practices:
a compact `AGENTS.md` router, a plan threshold based on the shape of the work,
a rule to stop on material ambiguity, calibrated reporting of evidence and
enforcement, and positive/negative checks for accepted invariants. Repository
Harness is not an orchestrator or an evaluator, and its installer/updater and
onboarding evidence-capsule machinery do not fit SDF.

SDF already owns Task and Attempt state, Policy, evaluation, Evidence and the
Decision Graph (`docs/architecture.md`, ADR 0001, 0002, 0004). Before this
decision the repository had `AGENTS.md` (skill routing), `docs/agents/`
(issue tracker, triage labels, domain docs), `CONTEXT.md`, the ADRs and
`docs/CONTRIBUTING.md` ("Verification boundaries"). It had no written rule for
where authority lives, when a plan is warranted, when to stop for a decision,
or how to name evidence classes; a project-local workspace protocol
(WORKSPACE_PROTOCOL, PR #40) was later removed.

## Decision

Adopt a repository-level agent workflow guide at `docs/agents/workflow.md`,
with root `AGENTS.md` as a concise router to it. Per candidate principle:

| Principle | Decision | Where |
| --- | --- | --- |
| Compact root router | **Adopt.** `AGENTS.md` stays a short map. | `AGENTS.md` |
| Authority vs. observed implementation | **Adopt.** ADRs, `CONTEXT.md`, roadmap and recorded maintainer decisions are normative; code, tests and defaults are observations. | workflow §1 |
| Read-only requests stay read-only | **Adopt.** | workflow §2 |
| Plan threshold by work shape | **Adapt.** Persistent plans live in the GitHub issue or PR, not in `docs/plans/`; "Plan" stays the ADR 0014 product term. | workflow §3 |
| Stop on material ambiguity | **Adopt.** Ask for the smallest decision; minor choices made under instruction go under "Decisions to confirm". | workflow §4 |
| Evidence and enforcement calibration | **Adopt.** Reports distinguish available validation, executed local result, CI run/result, branch protection, runtime observation and human approval. | workflow §5 |
| Positive/negative invariant checks | **Adopt** for accepted invariants, with native tooling; the protocol's own check is `tests/test_repository_protocol.py`. | workflow §6 |
| Operate-the-application runbooks | **Defer.** `docs/CONTRIBUTING.md` and `docs/live-full-loop.md` remain the runbooks. | — |
| Harness self-improvement loop | **Defer.** | — |
| Installer/updater, CLI, evidence capsules, `.sdf/policies/`, `.sdf/evidence/` | **Reject.** They would create a second policy or Evidence store. | — |
| Automatic agent orchestration | **Reject** here; it is SDF product or external tooling scope. | — |

SDF keeps Task, Attempt, Policy, Evaluator, Evidence and Decision Graph
ownership. This decision adds no runtime state, schema or parallel Evidence
store.

## Consequences

- One canonical location for repository workflow rules; other docs link to it
  instead of restating it.
- The minimal validation is a pytest check that the router and its links stay
  intact and that no parallel `.sdf/` store is tracked. It does not enforce
  agent behaviour; reports still depend on the agent following the guide.
- Product proposals #60 (unresolved decisions as an SDF gate) and #62
  (binding artifacts to immutable revisions) remain separate and are not
  approved by this decision. The maintainer later approved both on
  2026-10-31: #62 as written ([ADR 0032](0032-attempt-revisions.md)) and #60
  in a narrow scope (a pending *material* agent decision request blocks
  dispatch and consequential mutation until resolved), recorded in its own
  ADR with that change.
- No GitHub branch-protection, CI or agent-permission change is made.
