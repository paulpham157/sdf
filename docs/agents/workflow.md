# Repository workflow (SDF Repository Protocol v0.1)

This is the one canonical guide for how an agent or contributor works **in this
repository**. Root `AGENTS.md` only routes here. The decision that adopted it is
[ADR 0028](../adr/0028-sdf-repository-protocol.md) (issue #59, accepted
2026-10-05).

The protocol is about repository work: reading, changing and reporting on this
codebase. It does not describe or replace SDF's product behaviour. Task,
Attempt, Policy, Evaluator, Evidence and the Decision Graph are owned by SDF at
runtime (`CONTEXT.md`, ADR 0001, ADR 0002, ADR 0004); nothing here copies their
lifecycle into Markdown, and no file in this repository is a second task,
decision or Evidence store.

## 1. Where authority lives

Separate what the project has **decided** from what the code **happens to do**.

| Kind | Sources | Use |
| --- | --- | --- |
| Normative authority | Accepted ADRs in `docs/adr/`; `CONTEXT.md` (vocabulary); `docs/roadmap.md` (active phases and gates); explicit maintainer decisions recorded on a GitHub issue (for example "Accepted by maintainer on …"); the issue's own acceptance criteria | Defines required behaviour, boundaries and policy. Cite it. |
| Observed implementation | Code, tests, default values, configuration, CI files, existing patterns | Shows what the system does today. It is evidence, not policy. |
| Unaccepted material | Proposed ADRs, research notes in `docs/research/`, triage notes, open proposal issues that no ADR or recorded maintainer decision has accepted (#60 and #62 were accepted on 2026-10-31, see ADR 0028), agent Claims | Context only. Never treat as approval. |

Rules:

- A default, a convention or a passing test does not establish policy. If a
  change needs a rule that no normative source states, see section 4.
- If your work contradicts an accepted ADR, say so explicitly and either stay
  within it or propose a new ADR (`docs/agents/domain.md`).
- Use the domain vocabulary from `CONTEXT.md`. In particular, a **Plan** is the
  SDF product concept (ADR 0014); a repository work plan is a different thing
  (section 3).

## 2. Read-only work stays read-only

A request to answer, review, diagnose, triage or report status is read-only.
Read only what the answer needs, do not edit files, branches, issues, labels or
pull requests, and do not start sandboxes or spend provider credit unless the
request asks for that. Finding a defect while reading does not grant authority
to fix it: report it (with file and line), and let the owner decide.

## 3. Choose the work shape

**Bounded change** (the default). One coherent change that can be resumed from
its diff: restate the outcome, read the authority (section 1) and the code it
touches, make the smallest coherent change, run focused and repository checks,
and report (section 5). No separate plan record is needed; the issue and the
pull request are the record.

**Durable planned change.** Write a persistent work plan only when the work
spans several sessions or contributors, has real dependencies or ordering,
needs a recovery path, or cannot safely be resumed from its diff. Put the plan
where the work is already tracked: the GitHub issue (a spec issue with
sub-issues and native `blocked_by` edges, see `docs/agents/issue-tracker.md`)
or the pull request description. Keep outcome, approach, risks, progress and
decisions current there. Do not create `docs/plans/`, `.scratch/` trackers or
any other parallel task list; `.scratch/` is historical.

A plan, checklist or completion message never proves behaviour on its own.

## 4. Stop on material decisions

Before a consequential mutation (code, schema, policy, API contract, security
or trust boundary, cost, data deletion, external side effect), check that a
normative source covers it. Stop and ask for the **smallest** decision when:

- materially different choices remain and the authority does not pick one;
- product intent, compatibility or a security/trust boundary is ambiguous;
- the change would weaken validation, loosen a policy or widen a credential or
  egress boundary;
- recovery would be difficult (destructive migration, history rewrite, data
  loss); or
- you lack the authority (for example: merging, changing branch protection,
  agent permissions, CI gates, or closing an issue someone else owns).

Ask on the issue or pull request, state the alternatives and their
consequences, and keep going with unaffected work. Example: "add rate limiting"
without a quota, key or response contract must stop; "enforce the 429 +
`Retry-After` contract in ADR 0023" may proceed.

When a task explicitly says to proceed despite a minor ambiguity, choose the
option most consistent with the accepted ADRs and list it under **Decisions to
confirm** in the pull request. That is a disclosure, not an approval.

## 5. Report proof and its limits

A completion report states the outcome (or the explicit blocker), what
changed, the evidence, and what was not verified. Name each piece of evidence
by its class; never let one stand in for another:

| Class | Meaning | Example wording |
| --- | --- | --- |
| Available validation | A check exists that could be run | "`uv run pytest tests/test_x.py` covers this" |
| Executed local result | You ran it here and saw the result | "ran `uv run pytest`: N passed, M skipped" |
| CI run / result | A CI job actually ran on this revision | "CI run <link> passed on <sha>"; a workflow file alone is not a run |
| Branch protection | A rule enforced by the forge | "externally configured" or "unverified"; CI presence does not prove merge blocking |
| Runtime observation | A live system was exercised | "live E2B sandbox created, ran, killed"; record the gate variables |
| Human approval | A maintainer accepted it | link the comment or review; an agent cannot self-approve |

Also report skipped tests (a live test skipped for lack of credentials is not a
pass), and the boundary that remains, as `docs/CONTRIBUTING.md`
("Verification boundaries") requires. Local green tests are never production
proof.

## 6. Encoding invariants

When an accepted authority states a boundary worth enforcing mechanically, add
the smallest check with the repository's native tool (`pytest`, or an
Alembic/PostgreSQL trigger for data invariants) and prove both directions: a
positive case that allowed behaviour passes and a negative case that the
forbidden behaviour fails for the intended reason, with a diagnostic naming the
rule. Without an accepted authority, do not invent the invariant; report the
gap. Adding hooks, CI jobs or branch-protection rules needs separate approval.

`tests/test_repository_protocol.py` is the check for this protocol itself: the
root router points here, the links it relies on resolve, and no parallel
`.sdf/policies/` or `.sdf/evidence/` store is tracked.

## 7. Ownership

| Responsibility | Owner |
| --- | --- |
| How to work in the repo, report and pause | this file |
| Vocabulary | `CONTEXT.md` |
| Architecture and trust decisions | `docs/adr/` |
| Phases and gates | `docs/roadmap.md`, GitHub issues |
| Setup and verification boundaries | `docs/CONTRIBUTING.md` |
| Task, Attempt, Policy Decision, Evaluator, Evidence, Decision Graph | SDF at runtime (PostgreSQL), never repository Markdown |
| Agent-specific behaviour (permissions, sandbox, credentials) | agent adapters and their ADRs |
| Multi-session orchestration of contributors | external tooling (e.g. Paseo); not specified here |

Out of scope: Repository Harness installers or updaters, evidence capsules,
`.sdf/policies/`, `.sdf/evidence/`, automatic agent orchestration, and any
change to branch protection or agent permissions. Each needs its own decision.
