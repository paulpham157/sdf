# ADR 0033: a pending material decision request holds its scope until a human resolves it

Status: accepted 2026-10-31 (maintainer decision on #60, narrow scope; refines ADR 0014).
Amended 2026-10-31 by the maintainer's review decisions: rule-based materiality
(§1b) and a paused wall clock while held (§3b); rejection semantics unchanged.
Amended again 2026-10-31 by the maintainer's approval of the review fixes: the
materiality record is fenced in PostgreSQL (§1c, migration 0024) and provider
sandbox timeouts are extended during a hold (§3c).

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
     be changed after the fact (§1c). There is no schema change; migration
     0024 only adds a PostgreSQL trigger.

1b. **Deterministic rules can also make a request material** (`sdf_core/materiality.py`).
   - A request is material if the agent declares it **or** any rule matches.
     Rules can only upgrade a request; they never downgrade an agent's
     declaration.
   - Rules are case-insensitive regular expressions, with no model and no
     network. Each is matched separately against the question, each option,
     the consequence and an optional declared `action`
     (`sdf-msg request-decision --action TEXT`, at most 200 characters
     stored). Matching uses the full text the agent sent, not the truncated
     stored copy, after NFKC normalisation with invisible format characters
     (zero-width, bidi) removed. Cross-script look-alikes (for example a
     Cyrillic "о") are not mapped.
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

1c. **The materiality record cannot be downgraded.**
   - `agent_messages` is append-only: no UPDATE or DELETE, in the ORM session
     guard and, on PostgreSQL, `trg_agent_messages_append_only` (migration
     0014). So a message's `material` / `materiality_source` /
     `matched_rules` stay as recorded.
   - A decision request's identity never changes after insert: `subject_kind`,
     `subject_id`, `run_id`, `requested_by`, `message_id`, `question`,
     `options` and `created_at`. A request therefore cannot be re-pointed at a
     non-material message.
   - A request whose message is material is resolved only with
     `resolver_kind = 'human'`, also by a writer that bypasses
     `PlanService.resolve_request`.
   - Both rules are in the ORM session guard (SQLite and PostgreSQL) and, on
     PostgreSQL, in `trg_decision_requests_material_fence` (BEFORE UPDATE,
     migration `0024_decision_material_fence`). Downgrade drops the trigger.

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

3c. **Sandboxes are kept alive while held** (`Coordinator._keep_alive`).
   - The wall clock pauses during a hold, but a provider-side sandbox timeout
     does not. E2B sandboxes are created with `sandbox_timeout_s` (default
     1800 s) and would otherwise be killed by E2B during a long hold.
   - While a material request is pending, every sandbox that reports a
     provider lifetime (`lifetime_left_s`) shorter than the run's remaining
     counted wall time plus 120 s is extended to that plus 600 s. The
     extension uses E2B's `Sandbox.set_timeout` (e2b 2.51.0), which resets
     the timeout from now, up to the account's maximum sandbox lifetime.
   - E2B does not refuse a timeout past the account maximum: it silently
     caps the end time (seen in the live test of 2026-10-31: a 200 000 s
     request ended at `started_at` + 24 h). So the end time is read back with
     `get_info`, and the coordinator trusts what the provider granted, not
     what it asked for. A capped extension is recorded (`capped`,
     `granted_s`) and is treated like a failed one.
   - If the extension fails (for example past that maximum), it is retried
     at most every 30 s. Once a sandbox has 60 s or less left, the run fails
     with "sandbox of … would expire during a material decision hold …" and
     is cleaned up normally. The run does not lose a sandbox mid-hold without
     saying so.
   - Every extension and every failure is recorded in the run summary under
     `sandbox_keepalive`.
   - Daytona sandboxes are created with auto-stop disabled, so they report no
     provider lifetime and nothing is extended. Local and test sandboxes that
     do not implement the optional methods are left alone in the same way.
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
- The rules are English-only. Paraphrase, another language or cross-script
  look-alike characters can avoid them. Their false positives cost a human
  answer.

### Deferred (not part of the narrow scope)

- **The gate itself has no database trigger.** Holding turns, verification,
  Attempt start/finish and run success is enforced in the coordinator,
  `ExecutionService` and `PlanService`. A writer that inserts Attempts or
  Evidence directly is not stopped by the gate. Only the materiality record
  and human-only resolution are fenced in the database (§1c).
- **No preemption.** A turn that is already running when the request is
  created is not interrupted, and the agent can keep changing its workspace
  until that turn ends. Its messages are still relayed. Its result is held at
  verification, Attempt finish and completion. A human reviewing the request
  may therefore find the workspace already changed.
- **Sandbox cost while held.** A kept-alive sandbox keeps costing money and
  counts toward `max_cost_usd`. A long hold can therefore end the run on the
  cost cap.
- **Not adopted from #60:** the broader authority model, delegated approvers,
  and LLM-based or policy-chosen materiality.
