# ADR 0033: a pending material decision request holds its scope until a human resolves it

Status: accepted 2026-10-31 (maintainer decision on #60, narrow scope; refines ADR 0014).
Amended 2026-10-31 by the maintainer's review decisions: rule-based materiality
(§1b) and a paused wall clock while held (§3b); rejection semantics unchanged.

## Context

ADR 0014 lets an agent ask a question with `sdf-msg request`, which creates a
`DecisionRequestRow` linked to the agent's message. Before this change, that
request never blocked anything (checked in `sdf_core/coordinator.py`):

- The coordinator kept delivering messages, waking agents, verifying reported
  subtasks and finishing Attempts while the request was pending.
- A run could end `succeeded` with the request still pending.
- The only effect was `waiting_human`, which pauses stall detection.
- A configured policy could also `answer` or `reject` the request without a
  human.

So an agent that asked "may I delete these tests?" and went ahead anyway in the
same turn had its work verified and accepted before anyone had answered.

Issue #60 proposed a broader authority model. The maintainer approved only a
narrow part of it on 2026-10-31 (recorded in ADR 0028).

## Decision

1. **The agent can declare materiality on the request.**
   - Without a declaration, a request is material only if a rule matches (§1b).
   - `sdf-msg request --material --consequence TEXT ...` (or the MCP `request`
     tool's `material`/`consequence` fields) marks a request material.
   - A material request needs a non-empty consequence (at most 1000
     characters) and at least two distinct options.
   - Materiality, the consequence and `authority: human` are stored in the
     append-only `AgentMessageRow.meta` of the asking message, so they cannot
     be changed after the fact. **No schema change or migration is needed.**

1b. **Deterministic rules can also make a request material** (`sdf_core/materiality.py`).
   - A request is material if the agent declares it **or** any rule matches.
     Rules can only upgrade a request; they never downgrade an agent's
     declaration.
   - Rules are case-insensitive regular expressions, with no model and no
     network. Each is matched separately against the question, each option,
     the consequence and an optional declared `action`
     (`sdf-msg request-decision --action TEXT`, at most 200 characters
     stored). Matching uses the full text the agent sent, not the truncated
     stored copy.
   - Default rules (`DEFAULT_RULES`), with id and category:

     | Rule id | Category |
     |---|---|
     | `data.delete` | deleting or dropping data, files, tests or history; `rm -rf`; `DROP`/`TRUNCATE`/`DELETE FROM` |
     | `external.send` | sending email or messages, posting or publishing outside the sandbox (`npm publish`, `twine upload`) |
     | `money.spend` | spending, purchases, payments, paid plans, provisioning GPUs or instances, amounts in $/€/£ |
     | `credentials.secret` | credentials, secrets, passwords, API, access or private keys, named tokens, `.env`, vaults |
     | `git.destructive` | force-push, pushing to main/master/production/release, `reset --hard`, history rewrites, rebasing shared branches, deleting remote branches or tags |
     | `schema.shared` | Alembic, `ALTER TABLE`, DDL, schema or database migrations, shared/production/staging databases |
     | `network.egress` | egress, outbound traffic, firewalls, security groups, opening ports, network allowlists |
     | `merge` | merging a PR or a branch (`gh pr merge`, `git merge`) |

   - "Conservative" means erring towards asking a human. A false positive
     costs one human answer; a false negative lets an agent decide something
     consequential alone. Words that are common in ordinary coding questions
     ("token budget", "AllowlistPolicy", "migrate this function", "pay
     attention") are deliberately excluded.
   - Extending: append a `MaterialityRule` to `DEFAULT_RULES`, or pass a tuple
     to `MessageHub(materiality_rules=...)`. Rule ids must be unique.
   - Recorded on the message: `material`, `materiality_source` (`agent` or
     `rule`) and `matched_rules`. When both apply, the source is `agent` and
     the matches are still listed. A rule-material request without a
     consequence gets "matched materiality rules: …" as its consequence.
   - The host tells the agent when its undeclared request became material by
     rule, and why its work now waits.

2. **Scope.**
   - A worker's material request blocks that worker's subtask.
   - A lead's material request (or one from an unknown sender) blocks the
     whole run (`RUN_SCOPE`).

3. **What is held while a material request is pending** (`sdf_core/decision_gate.py`):
   - **Coordinator:**
     - No turns for agents in the blocked scope. Their wake flag and unread
       mail are kept.
     - No verification of a blocked subtask. Its `verify_requested` is kept,
       so verification runs after resolution.
     - No run success while anything is pending.
   - Each hold is audited once per item in the run summary under
     `material_decisions`, together with the final status of each request.
   - **Execution service:** `ExecutionService.begin` and `.finish` raise
     `DecisionGateError` (a `PlanGateError`) for a plan task whose runs have a
     pending material request for that task or for the whole run. A refused
     `finish` leaves the Attempt `running`, so nothing is lost.
   - **Policies:** they never resolve a material request.
     `PlanService.resolve_request` refuses a policy outcome for one.

3b. **The wall clock pauses while the run is only waiting on a human.**
   - In a coordinator poll interval where a material request is pending and
     nothing else is happening (no agent turn in flight, no host
     verification), that time is not counted toward `max_wall_s`.
   - Time outside holds still counts. So does a turn still running after its
     agent asked: that turn is work, not waiting.
   - Held time has its own cap, `max_material_hold_s` (default 3600 s, range
     (0, 86400]). Exceeding it fails the run with "material decision hold cap
     … reached". Sandboxes stay alive while held, so their time still counts
     toward `max_cost_usd`.
   - Stall detection was already suspended while any decision request is
     pending, and still is.
   - Audit: the run summary's `timings` gains `material_hold_s` and
     `counted_wall_s` (`wall_s` stays the total). Each `material_decisions`
     entry records `pending_s`, `materiality_source` and `matched_rules`.

4. **Resolution.**
   - Any human outcome lifts the gate: `approved`, `answered` or `rejected`.
   - The agent is told the outcome and the resolution text through the
     existing resolved-decision delivery, and must act on it.
   - A rejection does not fail the run. Withheld verification then runs on
     whatever the workspace holds. If the agent had already acted against the
     rejected option, the criterion checks and the human reviewing the
     Evidence decide, not the gate.

5. **Non-material requests are unchanged.** They still do not block, and a
   policy may still answer or reject them.

6. API responses for decision requests include `material`, `consequence`,
   `authority` and `blocks`.

## Consequences

- An agent that declares a decision material cannot have work in that scope
  verified, accepted or completed before a human answers.
- An agent that neither declares a consequential question material nor uses
  any of the rule vocabulary is not gated. The rules are a floor, not a
  guarantee.
- The rules are English-only. Their false positives cost a human answer.

### Deferred (not part of the narrow scope)

- **No database trigger.** The gate is enforced in the ORM and the services
  (coordinator, `ExecutionService`, `PlanService`), not by a PostgreSQL
  trigger.
- **No preemption.** A turn that is already running when the request is
  created is not interrupted. Its result is held at verification, Attempt
  finish and completion.
- **Sandbox lifetime while held.** A provider-side sandbox timeout (for
  example E2B's) is not extended during a long hold. A sandbox that expires
  then fails the run.
- **Not adopted from #60:** the broader authority model, delegated approvers,
  and LLM-based or policy-chosen materiality.
