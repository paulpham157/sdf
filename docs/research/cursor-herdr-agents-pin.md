# Cursor CLI pin in `sdf-herdr-agents` (Slice 3)

Date: 2026-09-30. Track: `cursor-e2b`. Peer: template pin + publish + no-cred smoke.
Path B. No `sdf_core/` credential wiring. No credentials baked into the image.
No `CURSOR_API_KEY` in Evidence.

## What changed

`infra/e2b/herdr-agents/Dockerfile` pins Cursor Agent CLI for linux/x64:

| Field | Value |
| --- | --- |
| Version | `2026.09.28-64d2043` (Slice 1 observed) |
| URL | `https://downloads.cursor.com/lab/<version>/linux/x64/agent-cli-package.tar.gz` |
| Integrity | SHA-256 of tarball verified at build; build fails on mismatch |
| PATH | `/usr/local/bin/agent` and `cursor-agent` → `/opt/cursor-agent/cursor-agent` for `USER user` |
| Version gate | `test "$(agent --version)" = "${CURSOR_AGENT_VERSION}"` (and `cursor-agent`) |

## Publish

| Field | Value |
| --- | --- |
| Template name | `sdf-herdr-agents` (same name; rebuild) |
| Template id | `tybptciqi1v4wnrvbhhu` |
| Build id | `1e1ce389-2c00-4570-8c4c-e154f8515d8f` |
| Spec | `--cpu-count 2 --memory-mb 2048 --ready-cmd 'herdr status server --json'` |
| Ready | Template ready after Herdr server status check |

## Smoke (disposable sandbox)

| Field | Value |
| --- | --- |
| Sandbox id | `ilawksr9gkcsu77uyixx5` |
| `agent --version` | `2026.09.28-64d2043` |
| PATH | `/usr/local/bin/agent`, `/usr/local/bin/cursor-agent` present for `user` |
| `agent status` | `Not logged in` (exit 0) |
| `agent --print` (no cred) | Clear auth failure: `Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY environment variable.` (exit 1) |
| Auth outcome class | **no-cred auth failure** (expected success for this slice) |
| Kill | `e2b sandbox kill` succeeded; sandbox not in running list |
| Secrets in Evidence | None |

## Out of scope (this slice)

- Subscription-forward design (parallel Peer)
- `sdf_core/credentials.py` / api-key Credential Mode
- Baking or inventing Cursor subscription tokens
