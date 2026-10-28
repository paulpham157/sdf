# E2B Herdr agents template (`sdf-herdr-agents`)

Pinned execution image for the SDF persistent Herdr runtime. It replaced the
retired `sdf-herdr-codex` template ([ADR 0009](../../../docs/adr/0009-retire-sdf-herdr-codex-template.md))
and carries the supported agent kinds:

| Component | Pinned version |
| --- | --- |
| Herdr (Linux x86_64, SHA-256 verified) | `0.9.1` |
| Codex CLI (`@openai/codex`) | `0.157.0` |
| Claude Code (`@anthropic-ai/claude-code`) | `2.1.282` |
| OpenCode (`opencode-ai`, headless `opencode run`, [ADR 0011](../../../docs/adr/0011-opencode-agent-kind-and-per-agent-provider-config.md)) | `1.19.4` |
| Pi (`@mariozechner/pi-coding-agent`, headless `pi -p`, [ADR 0012](../../../docs/adr/0012-pi-agent-kind.md)) | `0.73.1` |
| Qwen Code (`@qwen-code/qwen-code`, headless `qwen -o json`, [ADR 0013](../../../docs/adr/0013-openai-compatible-headless-agent-kinds.md)) | `0.26.2` |
| Goose (`goose`, musl release tarball, SHA-256 verified, ADR 0013) | `1.56.0` |
| Aider (`aider-chat` in `/opt/aider` venv on Debian `python3`, ADR 0013) | `0.86.2` |
| Cursor Agent CLI (`agent` / `cursor-agent`, linux/x64 tarball, SHA-256 verified) | `2026.09.28-64d2043` |
| Node.js runtime | `22` (`node:22-bookworm-slim`) |

The build fails if any installed binary reports a different version, or if the
Cursor tarball SHA-256 does not match. `agent` and `cursor-agent` are on PATH
for `USER user` via `/usr/local/bin` (same pattern as Herdr / Codex / Claude).
No Cursor credential is baked into the image.

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
Without one, Codex and Claude still start and reach their interactive prompt;
they just cannot call a model. Cursor fails closed with a clear authentication
error (`agent login` or `CURSOR_API_KEY`) until a Credential Mode injects auth.

## Build

From the repository root, logged in with `e2b auth login` (or `E2B_API_KEY`),
with E2B CLI 2.22.3 (it needs Node.js >= 22.19):

```bash
scripts/e2b-env.sh e2b template create sdf-herdr-agents \
  --path infra/e2b/herdr-agents \
  --dockerfile Dockerfile \
  --cpu-count 2 \
  --memory-mb 2048 \
  --ready-cmd 'herdr status server --json'
```

`.dockerignore` allowlists the build context to the files the Dockerfile
copies, so a stray `.env` or credential file here never reaches the build.
Dockerfile `ARG`/`ENV` hold only versions, SHA-256 sums and non-secret flags.

The template is **public** (published 2026-10-28 with
`e2b template publish sdf-herdr-agents -y`). Other E2B teams reference it as
`paul-pham-157/sdf-herdr-agents`; the owning team can also use the bare name
`sdf-herdr-agents`. Select it for the live tests with `SDF_E2B_HERDR_TEMPLATE=sdf-herdr-agents`,
`SDF_E2B_OPENCODE_TEMPLATE=sdf-herdr-agents` or
`SDF_E2B_VULTR_TEMPLATE=sdf-herdr-agents`.

> **Rebuild pending.** The Dockerfile now also pins Qwen Code, Goose and Aider
> (ADR 0013). The published build below predates them and does not contain
> those CLIs; rebuilding replaces the public image, so repeat the secret check
> and the licensing review first. Until then the live tests install the three
> CLIs at runtime (`tests/test_vultr_agents_live.py`).

Built 2026-10-28: template id `xj8glxnh091op795c7me`, build
`172d9021-919b-4de1-8391-35aaed26cad2` (2 vCPU, 2048 MiB, ~110 s build). A
boot check found no credential files, no key-shaped strings and no secret env
names in the image.

Licensing caveat: publishing redistributes Claude Code (proprietary,
"SEE LICENSE IN README.md") and the Cursor Agent CLI tarball to every E2B team.
Codex (Apache-2.0), OpenCode (MIT), Pi (MIT), Qwen Code, Goose and Aider
(all Apache-2.0) carry no such restriction. The
operator published it knowingly, accepting that risk; if Anthropic or Cursor
terms require it, withdraw it with `e2b template unpublish sdf-herdr-agents -y`
(or rebuild without those CLIs). Users of the public template remain bound by
each CLI's own license and need their own credentials; the image holds none.
The published build is the secret-checked one above
(`172d9021-919b-4de1-8391-35aaed26cad2`); rebuilding replaces the public
image, so repeat the secret check before any rebuild.

Dockerfile `ENV` reaches the image entrypoint (and so the Herdr server), but
not commands run through the E2B SDK, which start with a clean environment.

## Herdr bridge

The image contains a deployment-owned Herdr bridge at `POST /v1/command` on
port `8787`. Set `HERDR_ENDPOINT_TOKEN` or mount a secret at
`HERDR_ENDPOINT_TOKEN_FILE` (default `/run/secrets/herdr_endpoint_token`) when
creating the sandbox; the bridge rejects unauthenticated requests, shell
commands, oversized bodies, and timeouts over two minutes. The SDF client uses
`HerdrEndpointTransport` against the HTTPS-forwarded endpoint.

An E2B CLI `--env` value only applies to the terminal session used by that CLI
command; it does not configure the already started image entrypoint. Use the
E2B SDK or the deployment/orchestrator secret mechanism to provide
`HERDR_ENDPOINT_TOKEN` before exposing the endpoint.
