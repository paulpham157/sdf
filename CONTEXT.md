# SDF Core

SDF Core is the decision-and-evidence system that traces business intent through engineering work and back to evidence that validates or contradicts the original assumptions.

## Business and decision language

**Business Context**:
The bounded description of the customer, problem, operating conditions, value, alternatives and economic constraints for a product or initiative.
_Avoid_: background, customer profile

**Objective**:
A desired business or engineering outcome that gives a piece of work its purpose.
_Avoid_: task, goal statement

**Assumption**:
A claim about the world or the system that influences a decision and must remain explicitly testable.
_Avoid_: fact, requirement

**Constraint**:
A limit or rule that narrows which decisions or implementations are acceptable.
_Avoid_: preference, suggestion

**Decision**:
An accepted choice made in response to an objective, assumption and constraints.
_Avoid_: conclusion, implementation

**Requirement**:
A verifiable statement of what the system or artifact must provide.
_Avoid_: task, feature request

## Work and evidence language

**Task**:
A logical unit of work owned by SDF, with acceptance criteria, dependencies and budget.
_Avoid_: attempt, prompt, job

**Attempt**:
A single execution of a Task by an agent or execution backend.
_Avoid_: task, retry

**Artifact**:
A captured immutable output of an Attempt, such as a diff, test log, build result or release package.
_Avoid_: evidence, output

**Attempt Revision**:
The immutable record of which repository snapshot an Attempt started from (base tree) and produced (result tree), to which its Artifacts and Evidence are bound (ADR 0032). A PASS holds for that result tree only.
_Avoid_: version, commit (a Git commit is only an optional name for the base)

**Evidence**:
A recorded observation from an Artifact or evaluator that supports, weakens or contradicts an Assumption or Decision.
_Avoid_: agent opinion, status flag

**Evidence class**:
Which kind of observation an Evidence row is: execution (the Attempt ran), verification (an Evaluator verdict) or outcome (a business result). A coverage gap is not verification. The row's `kind` stays a separate label (ADR 0037).
_Avoid_: kind, status

**Verification Evidence**:
Evidence an Evaluator records from a sealed oracle. It names its verifier, the artifact bytes it checked, the oracle and environment digests, and when it expires. The Attempt's agent is never the verifier, and expired verification cannot accept an artifact (ADR 0037).
_Avoid_: test log, agent report

**Evaluator**:
The deterministic or policy-controlled checker that turns an Attempt's outputs into Evidence; an agent cannot self-certify its own result.
_Avoid_: reviewer, judge

**Decision Graph**:
The typed, traceable relationships connecting Business Context, Objectives, Assumptions, Constraints, Decisions, Requirements, Tasks, Attempts, Artifacts and Evidence.
_Avoid_: knowledge graph, task graph

**Validation edge**:
A graph relationship in which Evidence supports or contradicts an Assumption or Decision.
_Avoid_: inference, annotation

**Plan**:
An immutable, hashed proposal that splits work into subtasks, each an SDF Task with host-run criterion checks and the files it may change. No Attempt of a plan task starts until a human approves the plan (ADR 0014).
_Avoid_: todo list, agent plan

**Decision Request**:
A pending question recorded in the Decision Graph, raised for a plan approval or by an agent. Only a human approves; a policy may reject or answer; nothing is auto-approved. A resolved request is final.
_Avoid_: prompt, confirmation

**Material Decision Request**:
A Decision Request the agent marks material (with the consequence and at least two options), or one that a deterministic materiality rule matches (deleting data, external sends, spending, credentials, destructive git, shared-schema migrations, network egress, merging). Until a human resolves it, it holds dispatch, turns, verification, Attempt start/finish and run completion for its scope: the worker's subtask, or the whole Agent Run when the lead asks. No policy resolves it. Time spent only waiting for the human does not count toward the run's wall-time cap, and provider sandbox timeouts are extended meanwhile. Its materiality and its link to the asking message cannot be changed afterwards (ADR 0033).
_Avoid_: blocking question, urgent request

**Agent Run**:
One execution of an approved Plan by a lead agent and worker agents, one sandbox each. It succeeds only when every subtask has PASS Evidence.
_Avoid_: swarm, crew

**Lead / Worker**:
The coordinating agent of an Agent Run (assigns work by message, writes no code) and the agents bound by the host to exactly one subtask each.
_Avoid_: orchestrator agent, sub-agent

**Agent Message**:
An append-only, sanitized message between agents, relayed by the SDF host, whose sender is the sandbox it came from. Agent text is untrusted data.
_Avoid_: chat, instruction

**Claim**:
What an agent reports about its own work. A Claim triggers host verification and is never Evidence.
_Avoid_: result, evidence

## Access language

**Principal**:
The authenticated identity behind an API request, taken only from its credential (an API token today). Proposers, approvers, run starters and audit rows record the Principal, never a name sent in the request (ADR 0015).
_Avoid_: user field, decided_by

**API Token**:
A bearer credential `sdf_<id>.<secret>` for one Principal with roles and an expiry, stored only as a peppered hash, shown once at creation, revoked but never deleted. Agents never get one.
_Avoid_: API key, password

**Role**:
What a Principal may do: viewer (read), operator (create and run work), approver (decide), admin (all).
_Avoid_: permission level, scope

**Separation of Duties**:
The rule that the Principal who proposed a Plan cannot approve it, and that agent, sandbox and service identities can never approve.
_Avoid_: four-eyes check

**Single-user Mode**:
`SDF_AUTH_SINGLE_USER=1`: a solo deployment where the proposer may approve their own Plan; each such Decision is marked `self_approval`.
_Avoid_: admin mode

**Auth Audit**:
The append-only log of token creation and revocation, failed or denied requests, and every authenticated mutating request with its Principal.
_Avoid_: access log

## Ownership language

**SDF Task**:
The canonical logical work unit owned by SDF. External protocol tasks or sessions are attempts or delegations linked to it, not replacements for it.
_Avoid_: A2A Task as source of truth, ACP session as task

**Native Adapter**:
An execution adapter for a coding agent that does not require a portable protocol contract.
_Avoid_: fallback hack

**ACP**:
Agent Client Protocol, a client/editor-to-coding-agent protocol referenced in historical architecture discussions.
_Avoid_: unqualified ACP, Agent Communication Protocol

**A2A**:
Agent2Agent Protocol, an agent-system-to-agent-system protocol referenced in historical architecture discussions.
_Avoid_: coding-session protocol

**Agent Runtime**:
The execution boundary through which SDF controls an agent session associated with an Attempt.
_Avoid_: domain core, Task owner

**Runtime Session**:
An execution resource linked to an Attempt whose connection and process lifecycle are distinct from the Task outcome.
_Avoid_: Task, Evidence

**Runtime Event**:
One normalized, append-only observation correlated to an Attempt and a session. It is a runtime observation from an Agent Runtime or, for `tool_*` kinds, an action-boundary event that only the Tool Proxy emits (ADR 0029). It is telemetry, never Evidence.
_Avoid_: log line, status, proof of success

**Agent Credential**:
The right to call a model provider that an Agent Runtime receives when its sandbox is created; it belongs to the runtime, not to the Attempt.
_Avoid_: auth, token

**Credential Mode**:
How an Agent Credential is obtained for one agent kind: `subscription` (a login made once on the operator's host and forwarded) or `api-key` (an operator-supplied provider key).
_Avoid_: auth method, fallback

**Agent Provider Config**:
The per-agent-kind base URL, key variable name and model(s) an `api-key` Agent Credential uses; the key itself only ever comes from the environment.
_Avoid_: hard-coded provider, plaintext key

**LLM Transport**:
The one HTTP API family an agent kind uses to call a model: `openai` (an OpenAI-compatible API; Codex requires the Responses API, not Chat Completions alone) or `anthropic` (the Claude Messages API). A host such as the example Vultr endpoint is a base URL on one of those families, not a third transport (ADR 0034).
_Avoid_: vendor protocol, Vultr API

**Project Agent Config**:
Agent configuration an Attempt workspace carries (`.claude/`, `.codex/config.toml`, `opencode.json`, `.opencode/`, `.pi/`, `.qwen/`, `.goose/`, `.env`); untrusted, so it is ignored (Claude, OpenCode), outranked by pinned invocation overrides (Codex approval/sandbox policy) or the workspace is refused (Pi, Qwen Code, Goose).
_Avoid_: repo settings, trusted config

**Headless Agent Runtime**:
An Agent Runtime that runs one non-interactive agent turn per input in the sandbox (OpenCode `run`, Pi `-p`, Qwen Code `-o json`, Goose `run`), instead of driving an agent in a Herdr pane.
_Avoid_: Herdr pane, interactive session

**Baked Pi Extension**:
A Pi extension package installed into the `sdf-herdr-agents` image at an exact, integrity-checked version and loaded only by explicit `-e` path from SDF's allowlist (`SDF_PI_EXTENSIONS`, ADR 0017); never discovered, never installed at runtime, and in read-only turns limited to the ones that add no write capability.
_Avoid_: Pi package, plugin, project extension (`.pi/extensions` is refused)

**Tool Proxy**:
The SDF action boundary that obtains a Policy Decision before executing a requested tool action.
_Avoid_: terminal output parser, sandbox

**Policy Decision**:
An SDF authorization result for an action, resource and execution identity in a specific context.
_Avoid_: agent consent, successful execution

**Model Tier**:
An ordered cost/capability class for model selection: `basic`, `medium` or `high`.
_Avoid_: provider, model name

**Model Profile**:
A configured description of a model's capabilities, cost limits, latency expectations and provider identity.
_Avoid_: model tier, agent

**Routing Qualification**:
An agent kind's routing inputs (demonstrated capability, success, latency, cost, availability and load), measured only from server-written live Attempt records: the usage ledger, Evidence and trusted Runtime Events. An agent without them is not routable (ADR 0030).
_Avoid_: profile claim, agent score, benchmark

**Routing Decision**:
The append-only record of which agent kind a live Attempt ran with and why: either explicit configuration, or a measured choice that lists each candidate's inputs or rejection reason.
_Avoid_: dispatch log, agent preference

**Escalation Policy**:
The SDF rule that maps verified Attempt outcomes and budget state to the next Model Tier.
_Avoid_: agent fallback, retry prompt

**Attempt Lineage**:
The parent-child chain linking a later Attempt to the earlier Attempt whose Evidence caused a retry or escalation.
_Avoid_: duplicated task, retry count only

**Escalation Exhausted**:
A terminal Task outcome meaning the maximum Attempts, Model Tier or hard cost ceiling was reached without an accepted result.
_Avoid_: agent failed, infinite retry

**Accepted Outcome**:
A Task result supported by independent Evidence that satisfies its explicit acceptance criteria and, where defined, its Objective outcome metric.
_Avoid_: agent completed, evaluator ran

**Impact Measurement**:
The attributable change in an Objective's outcome compared with its declared baseline over a defined measurement window.
_Avoid_: task count, token count, dashboard activity

## Repository language

**Repository Protocol**:
The rules for working in this repository (where authority lives, when work stays read-only, when a work plan is warranted, when to stop for a decision, how to report proof), kept only in `docs/agents/workflow.md` (ADR 0028). It governs contributors and coding agents working on SDF's code, not SDF's runtime Tasks or Attempts.
_Avoid_: harness, Plan (the ADR 0014 product term), second task tracker
