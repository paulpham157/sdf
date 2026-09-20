# 04 Enforced fixture sandbox

Status: needs-info
Blocked by: 03

## What to build and acceptance

Demonstrate filesystem/process/network containment even when bypassing proxy; bounded resource use and process cleanup; no real repository authority before this passes.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

## Answer

Implemented a disposable `FixtureSandbox` in `sdf_core/sandbox.py` with
path/symlink escape checks, bounded subprocess output, process-group timeout
cleanup, and fail-closed network access with an injectable test seam.

Verification: focused `7 passed`; full `.venv/bin/pytest -q` `41 passed, 1
skipped`; compile and `git diff --check` passed. This proves the Python
boundary locally, not OS-level containment for arbitrary child code. A child
process can still access host resources unless a deployment-specific sandbox
(for example a container or OS sandbox backend) is supplied. Ticket remains
`needs-info` until that live containment proof exists.

Added optional `MacOSSandboxBackend` and
`FixtureSandbox(containment_backend=...)` integration. The backend probes
`sandbox-exec`, generates a fixture-scoped profile, and fails closed when
unavailable. Local tests verify command wrapping and profile intent; the host
currently returns `Operation not permitted`, so direct-child bypass containment
remains unverified.

Public Tool Proxy process actions now default to `require_containment=True`;
without an explicitly configured OS backend they are recorded as failed before
the child starts. Filesystem actions remain available inside the bounded
fixture root. This closes the fail-closed control-plane boundary, but does not
claim host-level enforcement until a disposable smoke test passes.

`MacOSSandboxBackend.smoke()` now executes that disposable boundary check and
returns an explicit `ContainmentProbe` result, distinguishing binary presence
from actual enforcement. The host smoke result remains unverified until run in
the deployment environment.

Observed locally on 2026-09-20: `sandbox-exec` is present at
`/usr/bin/sandbox-exec`, but the disposable write-boundary smoke returned
`available=False` with `disposable write-boundary smoke test failed`; no
containment claim is made.

The public API now requires an explicit `SDF_CONTAINMENT_SMOKE=passed`
promotion in addition to `SDF_CONTAINMENT_BACKEND=macos`; merely installing or
selecting a backend cannot silently enable child execution.

`ExecutionService.execute_tool()` now defaults to requiring containment as
well, so non-HTTP callers cannot accidentally enable arbitrary process actions
by omitting the safety flag.

The public seam also supports an explicit E2B backend via
`SDF_CONTAINMENT_BACKEND=e2b`: it syncs the disposable Attempt workspace,
executes through `e2b-box exec`, pulls changes, and kills the box. This does
not claim live E2B enforcement or network denial until a disposable provider
smoke and an independent network policy check pass.

The provider lifecycle smoke passed on 2026-09-21 using a disposable fixture;
the box was killed after the marker was pulled back. This is evidence for the
E2B lifecycle seam, not a claim that E2B network egress is denied.

Network boundary decision (2026-09-21): E2B mode now removes
`network.request` from the configured server allowlist and the direct
`SandboxToolExecutor` path rejects it as well. This deliberately disables
the structured SDF network action; it does not disable the coding agent's
network egress inside the E2B box.

Live network-egress evidence (2026-09-21): an Attempt-bound process executed
inside E2B fetched `https://e2b.dev` and returned `E2B_NET_OK:200`; the box was
cleaned up by the same containment lifecycle. This confirms agent egress is
available, not that arbitrary outbound destinations are allowlisted.
