# 06b Herdr-E2B sandbox adapter

Status: needs-info
Blocked by: 04, 05

## What to build and acceptance

Add a provider-neutral adapter that can prepare an E2B-backed Herdr execution
without putting E2B credentials or network calls in SDF domain core. The
adapter must produce a dry-run plan containing checkout/workspace, template,
agent, timeout, sync/pull and cleanup operations; live execution is opt-in and
must fail closed when the required CLI/API key is absent. Correlate
`attempt_id`, Herdr session/workspace identifiers and E2B sandbox ID when a
provider returns them. Never treat terminal output as Evidence.

## Verification

Local tests cover deterministic plan generation, missing-credential failure,
and JSON result correlation. Live E2B provisioning, agent execution, diff
pull and teardown remain unverified until disposable credentials are provided.

Follow ADR-0004 and preserve ACP/A2A exclusion.

## Answer

Implemented `sdf_core/herdr_e2b.py` with deterministic plan generation and a
live-gated command seam. Dry-run returns the planned `sync`, `e2b-box run`,
`pull` and `kill` lifecycle without network access. Live mode runs from the
validated checkout with a sanitized environment and timeout/process cleanup;
it requires `E2B_API_KEY` and the installed `e2b-box` plugin, then correlates
`attempt_id`, `sandboxId`, `herdrSessionId`, `workspaceId` and provider status
from JSON.

Verification: focused adapter tests and the full local suite pass. The local
machine has `herdr 0.7.3` and `e2b 2.16.1`, but no `e2b-box` command or live
credentials; therefore provisioning, agent execution, pull and teardown remain
unverified.
