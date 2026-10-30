# ADR 0033: a pending material decision request holds its scope until a human resolves it

Status: accepted 2026-10-31 (maintainer decision on #60, narrow scope; refines ADR 0014)

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

1. **Materiality is declared by the agent, on the request.**
   - Requests are non-material by default.
   - `sdf-msg request --material --consequence TEXT ...` (or the MCP `request`
     tool's `material`/`consequence` fields) marks a request material.
   - A material request needs a non-empty consequence (at most 1000
     characters) and at least two distinct options.
   - Materiality, the consequence and `authority: human` are stored in the
     append-only `AgentMessageRow.meta` of the asking message, so they cannot
     be changed after the fact. **No schema change or migration is needed.**

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
- Materiality depends on the agent declaring it. An agent that does not mark a
  consequential question as material is not gated. Classifying requests
  automatically was out of the approved scope.

### Deferred (not part of the narrow scope)

- **No database trigger.** The gate is enforced in the ORM and the services
  (coordinator, `ExecutionService`, `PlanService`), not by a PostgreSQL
  trigger.
- **No preemption.** A turn that is already running when the request is
  created is not interrupted. Its result is held at verification, Attempt
  finish and completion.
- **Wall-time cap still applies.** The run's wall-time cap still runs while
  the run waits for a human, so a long wait can end the run as failed
  (unchanged behaviour). Pausing the cap needs a separate decision.
- **Not adopted from #60:** the broader authority model, delegated approvers,
  and materiality chosen by a policy.
