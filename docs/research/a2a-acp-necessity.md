# Historical Research: A2A and ACP

> **Superseded by [ADR-0004](../adr/0004-internal-runtime-and-enforced-tool-policy.md).** This note is retained for traceability. ACP adapters and A2A gateways are not part of the active SDF Core roadmap.

## Conclusion

SDF Core uses an internal Agent Runtime and a Native Adapter first. Protocol work is deferred until a concrete interoperability requirement exists.

- **Agent Client Protocol (ACP)** is a client/editor-to-coding-agent protocol. It may be reconsidered when SDF needs a portable integration for multiple coding-agent runtimes.
- **Agent2Agent Protocol (A2A)** is an agent-system-to-agent-system protocol. It may be reconsidered when SDF delegates to an independently operated specialist agent or another SDF across a trust boundary.
- Neither protocol replaces SDF ownership of Task, Attempt, budget, Policy Decision, Evaluator, Evidence, or the Decision Graph.

## Decision rationale

Introducing a protocol before it solves a demonstrated requirement adds lifecycle, compatibility, and authorization complexity. SDF must first prove one real coding-agent loop under its own runtime, Tool Proxy, containment, and evaluator boundaries.

If either protocol is reconsidered, a new architecture decision must define the external boundary, identity and authorization model, failure and reconnect behavior, audit requirements, and the mapping from protocol state to SDF Attempts.

## References

- [A2A Protocol Specification](https://a2a-protocol.org/latest/specification/)
- [Agent Client Protocol](https://agentclientprotocol.com/get-started/introduction)
- [ADR-0004: Internal runtime and enforced tool policy](../adr/0004-internal-runtime-and-enforced-tool-policy.md)
- [Active SDF roadmap](../roadmap.md)
