# E2B Herdr agents template (`sdf-herdr-agents`)

Pinned execution image for the SDF persistent Herdr runtime. It supersedes
`sdf-herdr-codex` and carries both supported agent kinds:

| Component | Pinned version |
| --- | --- |
| Herdr (Linux x86_64, SHA-256 verified) | `0.9.1` |
| Codex CLI (`@openai/codex`) | `0.157.0` |
| Claude Code (`@anthropic-ai/claude-code`) | `2.1.282` |
| Node.js runtime | `22` (`node:22-bookworm-slim`) |

The build fails if any installed binary reports a different version.

## Claude first-run state

`claude/claude.json` and `claude/settings.json` are copied to
`/home/user/.claude.json` and `/home/user/.claude/settings.json`. They hold only
shared, non-secret first-run state:

- `hasCompletedOnboarding: true` — no onboarding wizard
- `theme: "dark"` — no theme picker
- `bypassPermissionsModeAccepted: true` and
  `skipDangerousModePermissionPrompt: true` — no dangerous-mode disclaimer
- folder trust accepted for the agent working directories (`/home/user`,
  `/home/user/project`, `/workspace`, `/tmp/sdf`); auto-updates off

`HerdrRuntime.start` launches Claude with `--dangerously-skip-permissions`.
That is acceptable only because every sandbox from this template is disposable
and killed at the end of its Attempt; do not reuse the flag elsewhere.

## Credentials

The image contains no Agent Credential: no API key, OAuth token, `auth.json`
or `.credentials.json`. Credentials are injected as sandbox-wide envs at
sandbox creation according to the agent kind's Credential Mode (ADR 0007).
The Herdr server starts from the image entrypoint, before those envs reach
later commands, so a pane does not inherit them. A create-time seed therefore
writes the plan's variables, by name, from the sandbox's own environment to
`~/.config/sdf/agent-env.sh` (mode `0600`), and the first line of `~/.bashrc`
sources it. In `api-key` mode Claude's `~/.claude.json` also gets the key's last
20 characters in `customApiKeyResponses.approved` (computed inside the box), and
each Attempt workspace is marked trusted just before Claude starts there.
Without one, both agents still start and reach their interactive prompt;
they just cannot call a model.

## Build and publish

From the repository root, logged in with `e2b auth login` (or `E2B_API_KEY`):

```bash
e2b template create sdf-herdr-agents \
  --path infra/e2b/herdr-agents \
  --dockerfile Dockerfile \
  --ready-cmd 'herdr status server --json'
```

The template is private to the operator's E2B team. Select it for the live
tests with `SDF_E2B_HERDR_TEMPLATE=sdf-herdr-agents`.

## Herdr bridge

The image contains the same deployment-owned Herdr bridge as `sdf-herdr-codex`
(`POST /v1/command` on port `8787`, bearer token from `HERDR_ENDPOINT_TOKEN`
or `HERDR_ENDPOINT_TOKEN_FILE`); see that template's README for its limits.
