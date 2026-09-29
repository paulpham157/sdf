# 07 Real agent fixture evidence loop

Status: resolved
Blocked by: 05, 06

## What to build and acceptance

One real agent edits bounded fixture; independent criterion checks create artifacts/Evidence and full trace; prove positive and negative cases. Requires provider access, no fabricated live proof.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

## Follow-up implementation

Added `AgentFixtureLoop`, which drives start/send/stream/status through the
internal runtime seam and then runs independent deterministic evaluator checks.
The result exposes `runtime_completed` separately from `accepted`; a runtime
`done` state cannot certify acceptance. Fake-runtime tests cover positive and
negative criterion results and missing-workspace rejection. A real Herdr agent
session and provider-backed fixture edit remain unverified, so this ticket is
not resolved.

`AgentFixtureLoop` now accepts the `RuntimeController` seam (including durable
event sinks) rather than requiring a raw provider runtime. A local integration
test verifies lifecycle events are persisted while evaluator acceptance remains
independent. Artifact/Evidence/trace persistence still belongs to the full
ExecutionService/provider-backed loop and live agent proof remains open.

The HTTP acceptance surface now exposes normalized lifecycle events in
`GET /tasks/{task_id}/trace` alongside graph nodes/edges, while the structured
`POST /attempts/{attempt_id}/actions` boundary persists Tool Proxy events in the
same attempt trace. This closes the local trace-assembly gap; provider-backed
artifact capture and real Herdr evidence remain unverified.

## 2026-09-25 Live verification attempt (superseded — see Live resolution below)

**Status (superseded)**: Blocked on Codex authentication. The auth prompt came from running the custom `sdf-herdr-codex` template, which has no borrowed Codex connection; the shipped `codex` template with the configured `codex-personal` borrowed session authenticates non-interactively.

**Locally verified** (178 passed tests):
- `AgentFixtureLoop` positive case: FakeRuntime edits workspace, evaluator accepts
- `AgentFixtureLoop` negative case: evaluator rejects even when runtime completes
- `ExecutionService.run()` creates complete Objective→Task→Attempt→Artifact→Evidence chain
- `RuntimeController` with `SqlAlchemyRuntimeEventSink` persists lifecycle events idempotently
- `GET /tasks/{id}/trace` returns nodes, edges, and runtime_events as designed

**Live infrastructure verification (all pass):**
```
✓ e2b-box doctor: [ok] all checks
✓ E2B_API_KEY: available in process environment
✓ E2B sandbox provisioned: im41p6h0cq8rr3joitci6
✓ Herdr server started in sandbox
✓ Codex CLI 0.155.1 installed  
✓ Workspace staged into sandbox
✓ Sandbox cleanup: no dangling processes
```

**Exact blocker with captured error:**

Command:
```bash
scripts/e2b-env.sh e2b-box run -t dluzqbi870es47svmsjq \
  --task "Update app.py to print 'updated'" --timeout-ms 60000 --json
```

Agent output (captured from stderr):
```
  Welcome to Codex, OpenAI's command-line coding agent
  Sign in with ChatGPT to use Codex as part of your paid plan
  or connect an API key for usage-based billing

  1. Sign in with ChatGPT
     Usage included with Plus, Pro, Business, and Enterprise plans
  2. Sign in with Device Code
     Sign in from another device with a one-time code
  3. Provide your own API key
     Pay for what you use

  Press enter to continue
```

Error: Codex CLI halts at interactive auth prompt. No non-interactive credential injection mechanism available in E2BHerdrTransport/E2B sandbox environment.

Per docs/CONTRIBUTING.md: "The image does not contain provider credentials. Inject the Codex connection through the E2B/Herdr control plane at sandbox creation time." Current implementation does not wire Codex credentials into the sandbox.

**Why acceptance not met:**
- Codex agent never executed task commands (halted at auth)
- Artifact diff not captured (no code changes made)
- No Evidence records created (agent incomplete)
- Sandbox closed cleanly but without task result

## 2026-09-25 Live resolution (coordinator)

A real Codex agent completed the fixture loop through `ExecutionService`, with
positive and negative cases, in disposable E2B boxes.

Implementation:
- `sdf_core/herdr_e2b.py`: `HerdrE2BNativeAdapter` plugs the headless
  `e2b-box run -t codex --task ... --kill --json` path into the ExecutionService
  artifact pipeline. The Attempt workspace gets a git baseline, is synced into a
  box, the agent runs, the result is pulled back, and the box is killed. Changed
  files come from local before/after snapshots, not from agent claims. An
  adapter result counts as completed only when `status=done`, `ok`, the pull
  succeeded and cleanup succeeded.
- `HerdrE2BAdapter` plan fixed: the separate bare `sync` booted the plugin's
  default template (Muse), and the separate `pull` targeted an already-paused
  box. `run` now syncs, pulls and kills in one step; a trailing idempotent
  `kill` is kept as cleanup.
- `tests/test_herdr_e2b_execution.py`: two deterministic fake-runner tests
  (pulled edit → DIFF artifact + `validates`; completion without meeting the
  criterion → FAIL Evidence + `contradicts`) plus a live test gated on
  `SDF_LIVE_E2B=1`.

Live command (key from the operator environment, redacted):
`E2B_API_KEY=<REDACTED> SDF_LIVE_E2B=1 uv run pytest -s -q tests/test_herdr_e2b_execution.py -k live`
→ 2 passed. Connection: `codex-personal` (borrowed-session); agent Codex v0.153.4.

| Case | Attempt | Sandbox | Box | Task | Edge to ASSUMPTION-CALC |
| --- | --- | --- | --- | --- | --- |
| Positive: fix `add` | ATTEMPT-4d1c2ceafac3 | iba42paezsp7j6n3qq9ma | killed | succeeded | validates |
| Negative: docstring only | ATTEMPT-6da6bc7c74dd | iv0bt559idr11wkhgl5hn | killed | failed | contradicts |

The independent criterion check (`python3 -c "from calc import add; assert add(2, 3) == 5"`)
ran locally on the pulled workspace. The negative case shows the runtime
reporting success (agent exit 0) while the evaluator contradicts the
Assumption. Afterwards `e2b-box list --json` returned `[]` and `e2b sandbox list`
reported no sandboxes. Full suite: all tests pass, 7 skipped (5 PostgreSQL-gated, 2 live-gated).

Boundary: this path is the Herdr-E2B plugin's headless `codex` template. The
persistent `HerdrRuntime` + custom `sdf-herdr-codex` template still cannot
authenticate Codex, because the image has no connection injection. Multi-turn
prompt/reconnect against a live agent on that path is follow-up work, not part
of this acceptance.

## 2026-09-26 GitHub #2: both cases on the persistent HerdrRuntime path

`tests/test_real_agent_loop_live.py` runs a real Codex Attempt (`subscription`
mode, `codex-personal`, model `gpt-6-luna` pinned test-side) through
`ExecutionService` + `RuntimeAgentAdapter` + `RuntimeController` on the
`sdf-herdr-agents` template, for a positive and a negative case. The fixture
has `add` (buggy) and `sub` (correct); the Task has two criteria checked by the
independent evaluator in the Attempt workspace:

- `add returns the sum`: `add(2, 3) == 5`;
- `sub unchanged` (guard): `sub(5, 3) == 2` and `calc.py` is the only file
  added, removed or changed relative to the fixture.

Live command (key from the operator environment, redacted):
`E2B_API_KEY=<REDACTED> SDF_LIVE_E2B=1 uv run pytest -s -q tests/test_real_agent_loop_live.py`
→ 2 passed.

| Case | Attempt | Sandbox | Task | add | guard | Edge |
| --- | --- | --- | --- | --- | --- | --- |
| Positive: fix `add` | ATTEMPT-801d8ed55765 | i3msakazz5ek8la1c910f | succeeded | PASS | PASS | validates |
| Negative: docstring only | ATTEMPT-bcfd66ab6aca | ix928f9y5x9cc6ovhgrx3 | failed | FAIL | PASS | contradicts |

Both cases also assert:
- runtime events `runtime_started(running)`, `runtime_input_sent(completed)`,
  `runtime_output_observed(completed)`, `runtime_terminated(terminated)`,
  source `herdr-e2b`, one session id; in the negative case the runtime reports
  completion while the evaluator contradicts the Assumption;
- the pane output is kept only as the `-LOG` Artifact; no Evidence row
  references it;
- `GET /tasks/{id}/trace`, `GET /evidence/{id}` and
  `GET /attempts/{id}/runtime-events` return the Attempt, the DIFF, LOG and
  evaluator Artifacts, the Evidence and the runtime events;
- network: agent egress is allowed (the Codex turn reached its API from the
  sandbox), while a structured `network.request` sent through the real Tool
  Proxy (`ExecutionService.execute_tool`, mid-Attempt, E2B containment backend)
  fails with "network.request is disabled" even when the allowlist names it,
  and is denied by the server-configured policy in E2B mode. Both leave
  `source="tool-proxy"` events; no `herdr-e2b` event has a `tool_*` kind;
- cleanup: every session ends `terminated`, each sandbox is gone, and
  afterwards `e2b sandbox list` and `e2b-box list --json` were empty;
  `leaked: []`.

Bug found on the way: a `network.request` carries a `bytes` body, which made
`SqlAlchemyAuditSink` / `SqlAlchemyToolEventSink` raise `TypeError: Object of
type bytes is not JSON serializable`, so the Tool Proxy (and
`POST /attempts/{id}/actions`) crashed on it. Durable audit/event context now
stores such values as `<bytes len=N sha256=...>`; covered by
`test_sqlalchemy_audit_persists_a_network_request_with_a_bytes_body`.
