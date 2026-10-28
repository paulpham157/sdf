# Remove the Aider agent kind and the Cursor Agent CLI

Status: accepted (2026-10-28). Supersedes the Aider parts of
[ADR 0013](0013-openai-compatible-headless-agent-kinds.md) and the Cursor pin
recorded in `docs/research/cursor-*.md`.

SDF supports exactly six agent kinds: `codex`, `claude`, `opencode`, `pi`,
`qwen` and `goose` (`sdf_core.credentials.AGENT_KINDS`). Aider and the Cursor
Agent CLI are gone from the code, the tests, the docs that describe current
behaviour, and the `sdf-herdr-agents` image.

## Why Aider goes

- **No shell, so no agent messaging.** Aider has no shell tool; it cannot run
  the `sdf-msg` shim, so it can never take part in an ADR 0014 multi-agent run
  (lead or worker).
- **A single-shot edit flow.** One turn is one model exchange plus its edits:
  a pair programmer, not an autonomous agent that plans, runs checks and
  iterates.
- **It reads too much of the workspace.** It merges `.aider*.yml` and `.env`
  from the working directory, the git root and `$HOME`, and GitPython runs
  repository-configured programs. SDF had to refuse `.aider*`, `.env` and
  `.git`, build a private per-turn repository and clean up a pickle cache to
  make it safe. That is a lot of hardening for an agent of limited use here.

## Why the Cursor Agent CLI goes

- **No custom base URL.** It cannot be pointed at Vultr or any other
  OpenAI-compatible endpoint, so it cannot follow SDF's per-agent provider
  configuration (ADR 0011).
- **Proprietary, with no overrides.** It is a closed binary with no model,
  endpoint or config override SDF could pin, and publishing it in a public
  template redistributes a proprietary tarball.

## History: Cursor was part of the original design

The Cursor track was part of the project's original design (commits by Paul
Pham): `072e4a5` "Pin Cursor Agent CLI in sdf-herdr-agents (#31)", `6beabf3` (#32, path spike recommending Herdr
`--kind cursor`) and `3588fb7` (#33, subscription forward blocked, auth
alternatives), all 2026-09-30. Subscription forward was blocked (Keychain-only
login, no herdr-e2b harness) and api-key mode was deferred, so Cursor was
**never wired**: it was never in `AGENT_KINDS`, had no credential spec, seed,
runtime or reserved name, and only its binary was pinned in the image. ADR
0013's "already a kind (subscription)" was therefore inaccurate. The research
documents stay, marked superseded, as the record of that decision.

## What changed

- Code: `sdf_core/aider_runtime.py` deleted; the Aider credential spec,
  conflicting variables (`AIDER_`/`OPENAI_`/`LITELLM_`), `aider-key-ref` seed,
  model defaults and flags removed; the Aider-only `cleanup_entries` step left
  `sdf_core.headless_runtime`. Nothing referenced Cursor in `sdf_core`.
- Reserved principal names are derived from `AGENT_KINDS` (ADR 0015), so
  `aider` is no longer reserved; `cursor` never was.
- Tests: Aider cases and the Cursor pin assertions removed;
  `test_removed_kinds_are_unknown_everywhere` and
  `test_removed_agent_clis_are_not_in_the_image` guard the removal.
- Image: no `aider-chat` venv (and no `python3-venv`) and no Cursor tarball.
  `python3` stays because the `sdf-msg` shim, the OpenCode MCP config merge
  and the verification checks run it. The `AIDER_*` telemetry switches left
  the image `ENV`.
- Docs: `.env.example`, README, CONTEXT and the template README no longer list
  Aider or Cursor; the template's licensing caveat now names only Claude Code.

## Rollout

Verified on a private staging template before the public image was replaced:
boot scan clean (no secrets; only E2B's SSH host keys), `aider`, `agent`,
`cursor-agent`, `/opt/aider` and `/opt/cursor-agent` absent, and all six kinds
plus an agent-comm run pass on Vultr glm-5.3 with egress locked. Public
`sdf-herdr-agents` is now build `a8d59540-a08d-4352-8c5a-60d90987ad21`
(still public; rootfs 2.2 GiB, down from 4.1 GiB). Details are in
`infra/e2b/herdr-agents/README.md` and `docs/research/vultr-agents.md`.

## Data

No migration. The agent kind is free text in the database (no enum or CHECK
constraint), so historical rows naming `aider` stay valid and readable. New
Attempts cannot use it: `resolve_credential_plan("aider", ...)` fails with
"unknown agent kind", as for any other unknown kind.

## Consequences

- Claude stays: it is proprietary too, but it runs on an Anthropic
  subscription or API key, and Vultr serves its Messages API.
- `SDF_AIDER_*` / `SDF_CREDENTIAL_MODE_AIDER` variables are now ignored.
- Re-adding Aider or Cursor needs a new ADR that answers the reasons above.
