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

**Evidence**:
A recorded observation from an Artifact or evaluator that supports, weakens or contradicts an Assumption or Decision.
_Avoid_: agent opinion, status flag

**Evaluator**:
The deterministic or policy-controlled checker that turns an Attempt's outputs into Evidence; an agent cannot self-certify its own result.
_Avoid_: reviewer, judge

**Decision Graph**:
The typed, traceable relationships connecting Business Context, Objectives, Assumptions, Constraints, Decisions, Requirements, Tasks, Attempts, Artifacts and Evidence.
_Avoid_: knowledge graph, task graph

**Validation edge**:
A graph relationship in which Evidence supports or contradicts an Assumption or Decision.
_Avoid_: inference, annotation

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
