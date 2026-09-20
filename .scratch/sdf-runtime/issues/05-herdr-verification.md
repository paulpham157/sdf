# 05 Verify Herdr identity and contract

Status: needs-info
Blocked by: None

## What to build and acceptance

Find authoritative repository/version/license and verify actual session APIs, event semantics, cancellation, reconnect and sandbox compatibility. Cite primary evidence and mark unsupported or ambiguous claims. No provider installs or credentials required for research.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

## Findings

Primary-source report: docs/research/herdr-runtime-verification.md. Identity/license and documented APIs investigated. A disposable Herdr 0.9.1 session has now been exercised; per-agent prompt/reconnect/cancel/terminate and descendant cleanup remain unverified, so ticket 06 is still gated. This remains a partial runtime verification, not a completed integration.

The local adapter now exposes `HerdrRuntime.probe()`. The installed binary is
`herdr 0.9.1`; a default non-running session is still reported outside the
disposable smoke session. This is useful binary identity evidence, but does
not prove cancellation, termination or containment.

`HerdrRuntime(expected_version=...)` now provides an explicit compatibility
gate and fails closed on the observed `0.7.3` versus pinned `0.9.1` mismatch.

`HerdrProbeResult.as_dict()` now provides JSON-safe version/session/error
evidence for deployment records without treating CLI availability as a live
agent lifecycle pass.

Live smoke evidence (2026-09-21): Herdr 0.9.1 created a disposable named
session, workspace, and Codex agent (`interactive_ready=true`); a pane command
returned observable output. The workspace and session were then closed/stopped
cleanly. Full prompt/reconnect/termination semantics remain unverified.
