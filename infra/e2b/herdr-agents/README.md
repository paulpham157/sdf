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
| Pi (`@mariozechner/pi-coding-agent`, headless `pi -p`, [ADR 0012](../../../docs/adr/0012-pi-agent-kind.md)), with baked extensions ([ADR 0017](../../../docs/adr/0017-image-baked-pi-extensions.md)) | `0.73.1` |
| Qwen Code (`@qwen-code/qwen-code`, headless `qwen -o json`, [ADR 0013](../../../docs/adr/0013-openai-compatible-headless-agent-kinds.md)) | `0.26.2` |
| Goose (`goose`, musl release tarball, SHA-256 verified, ADR 0013) | `1.56.0` |
| Node.js runtime | `22` (`node:22-bookworm-slim`) |

The build fails if any installed binary reports a different version, or if the
Herdr or Goose SHA-256 does not match. Debian `python3` is installed for the
`sdf-msg` shim (ADR 0014), the OpenCode MCP config merge and verification
checks; it is not tied to an agent kind.

Aider and the Cursor Agent CLI were removed from the image by
[ADR 0016](../../../docs/adr/0016-remove-aider-and-cursor-agent-kinds.md)
(2026-10-28).

## Pi extensions (ADR 0017)

`pi-extensions/` is installed to `/opt/sdf-pi-extensions` with
`npm ci --ignore-scripts --legacy-peer-deps`: exact versions, every tarball
checked against the committed lockfile's sha512 integrity, and no install
scripts.

| Package | Pinned version | Loaded in |
| --- | --- | --- |
| `@cad0p/pi-steering` | `0.2.0` | write and read-only turns |
| `@paulpham157/apply-patch` | `0.3.1` | write turns only |

`link-pi-host.mjs` points their `@earendil-works/pi-*` peer imports at the
global `@mariozechner` Pi 0.73.1, so no second Pi is installed.
`verify-pins.mjs` fails the build on any drift. Pi loads them only by explicit
`-e` path, chosen with `SDF_PI_EXTENSIONS`; discovery stays off. Two
requested packages were vetted and rejected: `pi-fabric` needs Pi >= 1.0 and
Node >= 24, and `pi-lens` 4.4.6 intermittently hung live Pi turns after the
final answer.

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
they just cannot call a model.

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

Current build, 2026-10-28 (ADR 0017: Pi extensions `@cad0p/pi-steering`
0.2.0 and `@paulpham157/apply-patch` 0.3.1 baked in): template id
`xj8glxnh091op795c7me`, build `dfc0c53e-e0b6-4c02-a14e-d7904a5cb2c6`
(2 vCPU, 2048 MiB), still public (`public: true`), from the Dockerfile at
commit `848c038`; E2B `diskSizeMB` 13645. It was first built as the private
staging template (`sdf-herdr-agents-staging`, build
`fda6c1cf-08cf-4f87-ad3b-098f60ea6338`, since deleted) and verified there:

- Secret check: the same boot scan as below. No credential files, and none
  of the operator's E2B/Vultr key values (compared by SHA-256 only); the
  key-shaped matches are the same baseline as before. `/opt/sdf-pi-extensions`
  (5.2 MiB) is root-owned and not writable by `user`. The `@earendil-works`
  peers link to the global `@mariozechner` Pi 0.73.1.
- Extension load (Pi 0.73.1 against a 127.0.0.1 stub model):
  - write mode: pi-steering blocked `git push --force`, and `apply_patch`
    patched the file;
  - read-only (`--tools read,grep,find,ls` with pi-steering only): the tools
    were `find,grep,ls,read`, "Tool apply_patch not found", and the file was
    unchanged;
  - a missing `-e` path: exit 1, "Failed to load extension".
- Live on Vultr glm-5.3 with egress locked:
  - pi with both extensions: 2 of 2 passed, both criteria PASS, tools
    `read` and `apply_patch`, 16–29s;
  - opencode, qwen, and agent-comm (OpenCode lead, Pi workers) all passed.
  - A pi smoke on the public build passed (20s).

Previous build, ADR 0016 (Aider and the Cursor Agent CLI removed): template
id `xj8glxnh091op795c7me`, build `a8d59540-a08d-4352-8c5a-60d90987ad21`
(2 vCPU, 2048 MiB), still public (`public: true`), from the Dockerfile at
commit `48c131e`. Contains every CLI in the table above: Herdr, Codex, Claude
Code, OpenCode, Pi, Qwen Code and Goose. Root filesystem 2.2 GiB (was
4.1 GiB; `/opt/aider` was 679 MiB and `/opt/cursor-agent` 556 MiB); E2B
`diskSizeMB` 14581 (was 15944). It was first built as a private staging
template (`sdf-herdr-agents-staging`, since deleted) and verified there
before the public image was replaced:

- Secret check: the build context is the `.dockerignore` allowlist; Dockerfile
  `ARG`/`ENV` hold only versions, SHA-256 pins and telemetry flags. A boot
  scan found no credential files (`auth.json`, `.credentials.json`, `.env`,
  `.netrc`, `.npmrc` tokens, ...), no key-shaped strings, no secret env names,
  and none of the operator's E2B/Vultr key values (compared by SHA-256 only).
  The only private keys are `/etc/ssh/ssh_host_*_key`, which E2B's own
  provisioning layer (`openssh-server`) adds to every template; this
  Dockerfile installs no SSH.
- Removal check: `aider`, `agent` and `cursor-agent` are not on `PATH`, and
  `/opt/aider` and `/opt/cursor-agent` do not exist.
- Live: all six kinds (codex, claude, opencode, pi, qwen, goose) pass on
  Vultr glm-5.3 with egress locked and no runtime install, plus an ADR 0014
  agent-comm run (OpenCode lead, Pi workers). A Pi + Qwen smoke passed on
  the public build.

Before that, without ADR 0016 (it still contained Aider and the Cursor
Agent CLI): `60e6172b-fe69-4c5e-b81a-b60b28a59c9b`, from commit `535318b`.
Before that: `172d9021-919b-4de1-8391-35aaed26cad2` (without Qwen Code,
Goose and Aider).

Licensing caveat: publishing redistributes Claude Code (proprietary,
"SEE LICENSE IN README.md") to every E2B team. Codex (Apache-2.0), OpenCode
(MIT), Pi (MIT), Qwen Code and Goose (both Apache-2.0) carry no such
restriction. The operator published it knowingly, accepting that risk; if
Anthropic's terms require it, withdraw it with
`e2b template unpublish sdf-herdr-agents -y` (or rebuild without Claude Code). Users of the public template remain bound by
each CLI's own license and need their own credentials; the image holds none.
The published build is the secret-checked one above
(`dfc0c53e-e0b6-4c02-a14e-d7904a5cb2c6`); rebuilding replaces the public
image, so build a private staging template first and repeat the secret check
there before any rebuild.

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
