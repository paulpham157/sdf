# Active SDF roadmap

This roadmap follows accepted ADR-0004. Historical ACP/A2A research is retained only for traceability and must not create active protocol implementation tasks.

## P0 Core correctness

The deterministic fixture demonstration exists, but Core v0 is not complete. Close acceptance-criteria coverage, graph reference integrity, append-only Evidence, evaluator artifact provenance, dispatch/retry correctness and failure recovery gaps. Verify behavior against PostgreSQL and trace from Objective to change and evaluation; do not equate an arbitrary passing command with validation of a business assumption.

## P1 Tool Proxy and Policy

Prove an allowed action executes and a denied action has no side effects. Bind authorization and audit records to the Attempt. Enforce filesystem, process and network limits outside agent cooperation; test a direct bypass attempt. No execution against a real repository until containment is demonstrated in disposable fixtures.

## P2 Herdr runtime integration

First identify the authoritative Herdr repository, version, license and actual API. Verify session control and sandbox compatibility. The intended design is SDF Control Plane → internal Agent Runtime → HerdrRuntime → Herdr → one coding agent in an enforced sandbox. Tool actions pass through Tool Proxy authorization before execution.

Track session identity, agent/model, workspace, process identity where available, lifecycle timestamps, last event, status, exit code and timeout. Prove start, input, output, cancellation, termination and reconnect without duplicate execution. Retain fake runtime tests. Missing operations must be recorded explicitly; do not invent a Herdr contract.

## P3 Event stream and observability

Normalize verified runtime observations into SDF events with Attempt/session correlation, ordering and replay handling. Runtime start, output and exit events do not certify task success. TOOL_REQUESTED and POLICY_DECIDED originate from trusted SDF action boundaries, not interpreted terminal text. Feed state, evaluation, audit and metrics; a UI may consume these later.

## P4 Multi-agent routing

After one real agent completes the verified fixture loop, add other agents through the same runtime boundary. Route by demonstrated capability, historical success, latency, cost, availability and load. Multiple agents alone are not a reason to introduce another protocol.

## Excluded from the active roadmap

ACP adapters and A2A gateways have no planned phase or automatic activation gate. A future external-client or independent-SDF integration requirement requires a separate architecture decision. Herdr is the intended execution provider, not the owner of SDF domain state.

## Next implementation slices

1. Close one full acceptance-criteria → evaluation artifact → Evidence → trace loop, including a negative case.
2. Execute and deny a tool action with Attempt-bound policy audit.
3. Demonstrate sandbox enforcement against proxy bypass in a disposable fixture.
4. Verify the Herdr contract and run one agent session through cancellation and reconnect. Blocked by containment and provider verification.
5. Complete a real agent change through independent evaluation and trace. Blocked by slices 1 and 4.
6. Normalize and replay lifecycle events; then qualify additional agents for routing.

The remote endpoint bridge and its HTTPS client transport are now implemented.
The authenticated deployment proof is still open: a real orchestrator must
inject the endpoint secret, expose port `8787` through HTTPS, and demonstrate a
full SDF → bridge → Herdr request before this becomes a completed P2 gate.

These slices are a proposed implementation breakdown, not completed work. Historical resolved Core tickets do not establish production readiness.

## Current local evidence boundary

The fixture/evaluator loop, append-only Evidence and graph-edge guards, durable
Tool Proxy audit/events, measured routing, Herdr/E2B seams, workspace binding,
and fail-closed public process actions are implemented and covered by local
tests. Public process actions can now select an explicit E2B containment backend
(`SDF_CONTAINMENT_BACKEND=e2b`), which syncs, executes, pulls, and kills a
disposable box; the structured SDF `network.request` action is explicitly
disabled in that mode while the coding agent retains E2B network egress. The
live E2B process path has also verified an HTTPS request to the E2B endpoint;
this is provider egress evidence, not a general outbound allowlist. The
remaining milestone claims require external evidence: a disposable live Herdr
session, verified cancellation/termination, and PostgreSQL concurrency/trigger
proof. Until those gates pass, no real repository is authorized and local green
tests must not be reported as production proof.
