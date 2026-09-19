# 05 Verify Herdr identity and contract

Status: needs-info
Blocked by: None

## What to build and acceptance

Find authoritative repository/version/license and verify actual session APIs, event semantics, cancellation, reconnect and sandbox compatibility. Cite primary evidence and mark unsupported or ambiguous claims. No provider installs or credentials required for research.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

## Findings

Primary-source report: docs/research/herdr-runtime-verification.md. Identity/license and documented APIs investigated; no real session executed. Pin and test the intended binary in a disposable environment. Per-agent cancel/terminate and descendant cleanup remain unverified, so ticket 06 is still gated. This is a runtime verification gap, not a completed integration.
