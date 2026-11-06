# ADR 0041: execution, verification, and outcome are different Evidence

Status: proposed
Follows [ADR 0039](0039-evidence-independence.md) and
[ADR 0040](0040-held-out-oracles.md). Does not change Evidence append-only
rules ([ADR 0002](0002-evidence-closes-the-decision-loop.md)), revision
binding for Attempt-scoped Evidence ([ADR 0032](0032-attempt-revisions.md)),
or the fail-closed gates in ADR 0039 and ADR 0040.

Numbered 0041 because 0039 is evidence independence and 0040 is held-out
oracles. Issue #88 was filed when independence was drafted as 0034; 0034 on
`main` is the LLM transport ADR. This file is a new proposal rather than an
edit of ADR 0039: that ADR is accepted, and it still says `execution` and
`outcome` are reserved and a coverage gap stays NULL. Those sentences stay
in the accepted text. Accepting this ADR is what supersedes them for new
writes.

## Context

ADR 0039 stored three class names and only ever wrote `verification`. A
coverage gap, an unmapped criterion, and a run with no commands were
`evidence_class` NULL. The acceptance gate already refuses anything that is
not independent, unexpired, artifact-bound verification. The scorecard did
not. A historical `PASS` with a NULL class, and a `PASS` an agent could
attach to its own run, still counted as a verified success next to a real
check.

An agent reporting completion is not success. The record that the agent ran,
the record that an independent check passed, and the record that an
Objective was met are three observations. One of them cannot stand in for
another.

## Decision

1. **Every new Evidence row has a class.** The class is `execution`,
   `verification`, or `outcome`. The ORM refuses a new row whose class is
   missing or outside that set. The database column stays nullable.
   Evidence written before this rule, including rows with
   `evidence_class` NULL, is not rewritten and is not backfilled
   (ADR 0002). Migration `0027_evidence_classes` does not add `NOT NULL`.

2. **Execution Evidence** is what the Attempt did: the diff and the process
   log, summarized when the run is finished. `evidence_class` is
   `execution`. `producer_identity` is the Attempt's `agent` and
   `producer_role` is `agent`. `kind` is `execution`. It may carry the diff
   digest and status `PASS` when the adapter exited 0. That is the
   observation that the process finished. It is not a check. The acceptance
   gate rejects it. It does not create a `validates` edge. It does not
   satisfy an acceptance criterion. A scorecard does not count it as a
   verified pass.

3. **Verification Evidence** stays the ADR 0039 row: an Evaluator verdict,
   `verifier_identity` `sdf-evaluator`, bound to the diff, with an oracle
   digest and an expiry. ADR 0040 still decides when a visible pass is not
   enough. A coverage gap, an unmapped criterion, and a run with no
   commands are verification with status `INCONCLUSIVE` and a
   `coverage_gap` reason. They name the evaluator. They do not carry an
   oracle digest or a subject digest, so they cannot accept an artifact.

4. **Outcome Evidence** is an Objective-level result: a measured metric or
   a human sign-off. `evidence_class` is `outcome`. `kind` is `outcome`.
   The row's `objective_id` is the Objective. `attempt_id` is NULL, and
   the row has no Attempt revision, so it is not Attempt Evidence and it
   is not Task verification. It carries `verifier_identity`,
   `verifier_role` (not `agent`), and an optional `expires_at`. The API
   records it on the Objective and reads it back from the Objective and
   from `GET /evidence/{id}`. The decision-graph edge is `measures` from
   the Evidence node to the Objective. It is not a `validates` edge to a
   Task. The acceptance gate rejects it.

5. **Schema** (migration `0027_evidence_classes`). Existing rows are not
   rewritten. `ADD COLUMN` does not fire the Evidence append-only trigger.

   | Column | Meaning |
   | --- | --- |
   | `objective_id` | the Objective an `outcome` row is about; NULL on execution, verification, and historical rows |
   | `attempt_id` | nullable. NULL only for outcome. Execution and verification still name an Attempt |

   PostgreSQL checks the link when the class is not NULL
   (`ck_evidence_class_subject`) and the evidence insert guard allows an
   outcome row with no Attempt revision. SQLite applies the same link
   rule. A historical NULL class is still legal on both.

6. **A verified pass is only a verification row that passes the ADR 0039
   gate** against the Attempt's agent and that Attempt's diff digest, and
   whose status is `PASS`. The local scorecard (`/scorecard`) and the
   impact scorecard (`/impact-scorecard`) count those rows as verified
   passes. Every other `PASS` — legacy NULL class, execution, outcome,
   expired verification, a verifier who is the Attempt's agent, a digest
   that is not the diff — is reported separately as an unverified legacy
   pass and does not accept the Task. Routing (ADR 0030) still treats
   evaluator `PASS` rows, including a historical row with no class, as the
   sample's success, and it ignores execution and outcome so "the agent
   ran" is not "the check passed".

## Consequences

- A new run records execution Evidence for the agent that ran and
  verification Evidence for each check, including a gap. Only the
  verification row that passes ADR 0039 (and ADR 0040, when the Task
  declares a hidden oracle) can accept the Task.
- A database created before this migration still opens. Its NULL-class
  `PASS` rows remain. They show up as unverified, not as verified success,
  and they still cannot accept an artifact.
- Outcome Evidence is queryable on the Objective. It does not move a Task
  to succeeded and it does not appear as an Attempt's verification.
- Accepting this ADR accepts the scorecard split and the non-null rule for
  new writes. It does not accept deleting or rewriting historical Evidence.

## Example

```json
{"id": "EVIDENCE-ATTEMPT-1-EXEC", "kind": "execution", "status": "PASS",
 "evidence_class": "execution", "attempt_id": "ATTEMPT-1",
 "producer_identity": "native", "producer_role": "agent",
 "subject_artifact_sha256": "<diff sha256>",
 "criterion": null}
```

```json
{"id": "EVIDENCE-ATTEMPT-1-1", "kind": "evaluation", "status": "INCONCLUSIVE",
 "evidence_class": "verification",
 "verifier_identity": "sdf-evaluator", "verifier_role": "evaluator",
 "command": "<missing check>",
 "stderr": "coverage_gap: no deterministic check mapped to criterion: handles empty input",
 "criterion": "handles empty input"}
```

```json
{"id": "EVIDENCE-OUTCOME-1", "kind": "outcome", "status": "PASS",
 "evidence_class": "outcome", "attempt_id": null,
 "objective_id": "OBJ-1",
 "verifier_identity": "alex", "verifier_role": "stakeholder",
 "expires_at": "2026-12-01T00:00:00+00:00",
 "criterion": "incident_minutes"}
```

The first row does not accept the artifact. The second does not either.
The third is the Objective's outcome, not the Task's verification. A
historical row `{"status": "PASS", "evidence_class": null}` is an
unverified legacy pass on the scorecard.
