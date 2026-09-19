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
