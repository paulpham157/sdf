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
and JSON result correlation. Live E2B provisioning, command execution, diff
pull and teardown now have a disposable CLI smoke; full SDF adapter
correlation and network policy remain unverified.

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
machine has Herdr 0.9.1, E2B CLI 2.16.1 and e2b-box 0.5.0; a disposable
provider lifecycle smoke has passed, while full SDF adapter correlation
remains unverified.

Live execution now attempts `kill` in a `finally` block after sync/run/pull
failures, preserving the primary provider error while preventing an avoidable
leaked sandbox. A regression test covers malformed run JSON and verifies the
cleanup command is still issued. This is lifecycle contract evidence only;
provider teardown remains unverified without a live disposable sandbox.

Successful results now expose `cleanup_attempted` and `cleanup_succeeded`, so a
caller can gate downstream artifact acceptance on explicit teardown evidence
instead of inferring it from provider output.

Clarification: `e2b-box` is the installed Herdr-E2B plugin (`herdr-e2b 0.5.0`);
it is not the same thing as a generic E2B template containing a `herdr`
binary. The plugin's configured headless template is `muse` and invokes
`muse exec` inside the E2B box. A generic template such as `openclaw-agent`
does not provide Herdr automatically.

Live plugin smoke (2026-09-21): `e2b-box run --task ... --kill --json`
provisioned sandbox `iy58nvph9ujq1y7xykzg0` with template `muse`, wrote the
task, attempted the agent, pulled without overwriting the dirty local tree, and
killed the box. The agent exited 1 because the sandbox lacked Meta credentials
(`muse login`/`META_API_KEY`); therefore this proves plugin provisioning and
cleanup, not successful agent output or the full Herdr lifecycle.

Live smoke evidence (2026-09-21): with the installed `e2b-box` 0.5.0 plugin
and configured E2B key, a disposable git fixture completed `sync -> exec ->
pull -> kill`. The remote command returned JSON `ok=true`, `exitCode=0`, the
pulled marker was verified locally, and the sandbox was explicitly killed.
This proves the provider CLI lifecycle, not yet the full SDF Attempt-bound
adapter or E2B network-deny policy.

SDF-bound live evidence (2026-09-21): `ExecutionService.execute_tool()` ran a
real `process.run` through `E2BContainmentBackend` for
`ATTEMPT-LIVE-E2B-20260921`. The action received ID
`ACTION-374413d6e6198a51adc3d75ca1f289e0ffedff1837d21b6d3f9f48150bc7bb4b`,
the E2B sandbox was `irjxx6neqsa85eo4v5ym5`, exit code was `0`, and the pulled
process artifact was `ATTEMPT-LIVE-E2B-20260921-PROCESS` with SHA-256
`a53c0909b9d0b8a5d37081e1871be081a8c898ee3fcce2f07b8f0547e6758554`.
Durable audit events (`policy_decided`, `action_claimed`, `action_executed`)
all carried the same Attempt ID. Post-cleanup `e2b-box status --json` returned
`tracked:false`.
