---
status: accepted
---

# Internal runtime and enforced tool policy

SDF removes ACP and A2A from the active roadmap and uses a small internal Agent Runtime interface, with HerdrRuntime as the intended first real implementation subject to verification of the Herdr project, license and supported operations. SDF retains ownership of Task, Attempt, State, Budget, Policy, Evaluator, Evidence and Decision Graph; runtime sessions represent execution resources and never replace these domain records. This supersedes ADR-0003: a second coding agent does not automatically trigger protocol work; reconsideration requires a new concrete requirement and a new decision.

Tool Proxy evaluates policy before performing an action, while an enforced sandbox constrains filesystem, process and network access even when an agent bypasses the proxy. Until that containment is demonstrated, real agents run only against bounded disposable fixtures with restricted authority; a copied workspace is not a sandbox, and terminal output is not a trusted tool request or evidence of policy enforcement.

The first milestone integrates one coding agent and retains a fake runtime for deterministic tests. Required lifecycle behavior includes dispatch, output, timeout, cancellation, reconnect and verified termination; those are SDF requirements to validate against Herdr, not claims about existing Herdr support. An independent Evaluator records Evidence and determines Task outcome; agent completion alone cannot certify success.
