# E2B Herdr agents template (`sdf-herdr-agents`)

Pinned execution image for the SDF persistent Herdr runtime. It replaced the
retired `sdf-herdr-codex` template ([ADR 0009](../../../docs/adr/0009-retire-sdf-herdr-codex-template.md))
and carries the supported agent kinds:

| Component | Pinned version |
| --- | --- |
| Herdr (Linux x86_64, SHA-256 verified) | `0.9.5` |
| Codex CLI (`@openai/codex`) | `0.165.0` |
| Claude Code (`@anthropic-ai/claude-code`, npm `stable`) | `2.1.308` |
| OpenCode 2 (`@opencode/cli`, headless `opencode run --server` against a private `opencode serve`, [ADR 0011](../../../docs/adr/0011-opencode-agent-kind-and-per-agent-provider-config.md), [ADR 0018](../../../docs/adr/0018-opencode-2-and-pi-1-upgrade.md)) | `2.2.0` |
| Pi (`@earendil-works/pi-coding-agent`, headless `pi -p`, [ADR 0012](../../../docs/adr/0012-pi-agent-kind.md)), with baked extensions ([ADR 0017](../../../docs/adr/0017-image-baked-pi-extensions.md)) | `1.4.0` |
| Qwen Code (`@qwen-code/qwen-code`, headless `qwen -o json`, [ADR 0013](../../../docs/adr/0013-openai-compatible-headless-agent-kinds.md)) | `0.26.2` |
| Goose (`goose`, musl release tarball, SHA-256 verified, ADR 0013) | `1.56.0` |
| Node.js runtime | `22` (`node:22-bookworm-slim`; Pi 1.x needs >= 22.19) |

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
| `pi-lens` | `4.4.6` | write turns only |

Pi 1.x resolves an extension's `@earendil-works/pi-*` imports to its own
modules, so no peer is installed or linked; `verify-pins.mjs` fails the build
on any version drift and if a second Pi (`@earendil-works` or
`@mariozechner`) appears under the extensions' `node_modules`. Pi loads them only by explicit
`-e` path, chosen with `SDF_PI_EXTENSIONS`; discovery stays off. `pi-lens`
(ADR 0017 update) runs with tool/LSP installs and rule-pack refresh disabled
and `PI_LENS_HOME=$HOME/.pi-lens`, set by the turn wrapper; Pi's
`--no-approve` keeps it from running binaries inside the workspace. `pi-fabric`
stays rejected: it sends prompts and code to its own third-party model
services (outside the egress lock), embeds a second Pi, and needs Node >= 24.

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
`SDF_E2B_VULTR_TEMPLATE=sdf-herdr-agents` (the Vultr agents test's default).

Second team copy, rebuilt 2026-10-30: the operator's other E2B team (the
`E2B_API_KEY` on the CI/agent box) owns its own **private**
`sdf-herdr-agents`, template id `tybptciqi1v4wnrvbhhu`. It had gone stale (a
2026-09-25 image: Herdr 0.9.1, Codex 0.157.0, Claude Code 2.1.282, Node
22.23.2, and no `pi`, `opencode`, `qwen` or `goose`, so those kinds exited
127). It was rebuilt in place from this unchanged Dockerfile (commit
`441173b`) with the build command above: build
`a66e215d-bec8-4980-a79f-2e5f5d4cbba7` (2 vCPU, 2048 MiB, E2B `diskSizeMB`
25328, envd 0.10.0), then rebuilt again the same day (same Dockerfile, E2B
layer cache) as the current build `4e213e87-8f62-46a0-b490-8cbc9d658dac`,
which passed the same version, secret and live checks. Verified in a fresh sandbox: Node v22.23.4, Herdr 0.9.5,
Codex 0.165.0, Claude Code 2.1.308, OpenCode v2.2.0, Pi 1.4.0, Qwen Code
0.26.2, Goose 1.56.0, the three Pi extensions present, Herdr server running;
no credential files, npmrc tokens or secret env names. Live on Vultr glm-5.3:
`tests/test_live_full_loop_http.py` 6 passed / 2 skipped (opencode, pi,
codex) and `tests/test_vultr_agents_live.py` 5 of 5 (codex, claude, pi,
qwen, goose). The live tests resolve the bare name to the key's own team copy; since
2026-10-30 `tests/test_vultr_agents_live.py` defaults to it
(`SDF_E2B_VULTR_TEMPLATE` unset = `sdf-herdr-agents`; only an explicit
`SDF_E2B_VULTR_BASE_TEMPLATE` bootstraps a bare base such as E2B's `codex`,
whose Node 20 cannot run Pi 1.x). Rebuild this copy whenever the public one is
rebuilt.

Current build, 2026-10-28 (ADR 0017 update: pi-lens 4.4.6 baked in for
write turns; otherwise the ADR 0018 versions below): template id
`xj8glxnh091op795c7me`, build `45ba7067-6549-48e3-8510-b69caeef0333`
(2 vCPU, 2048 MiB), still public (`public: true`), from the Dockerfile at
commit `d7d2964`; E2B `diskSizeMB` 14400 (+103 over the previous build).
It was first built as the private staging template
(`sdf-herdr-agents-staging`, build `9f463227-59fc-4ebd-aa76-2430125ee0c1`,
since deleted) and verified there:

- Secret check: no credential files, npmrc tokens or secret env names; the
  only key-shaped matches are the same 3 E2B SSH host-key baseline lines; the
  operator's E2B/Vultr key values are absent (SHA-256 only). Versions match
  every pin. `/opt/sdf-pi-extensions` is 105 MiB (was 5.2 MiB; pi-lens 28 MiB,
  `@ast-grep` native 62 MiB), root-owned, not writable by `user`; no second
  Pi; pi-fabric absent.
- Extension load (Pi 1.4.0, 127.0.0.1 stub, `--no-mcp --no-approve`, the
  `PI_LENS_*` runtime env): write mode had `apply_patch`, `bash`, `read` plus
  the pi-lens tools (`lens_diagnostics`, `symbol_search`, `read_symbol`, ...);
  pi-steering blocked a force push and `apply_patch` patched the file; the
  workspace held no pi-lens files afterwards. Read-only (pi-steering only)
  had `find,grep,ls,read`, "Tool apply_patch not found", file unchanged.
- Live on Vultr glm-5.3 with egress locked: pi with all three extensions
  4 of 4 passed (24-40s each, no hangs, about 12.6k input tokens and $0.0097
  per run against 4.8k and $0.004 without pi-lens); Pi read-only live (only
  `read` ran, file hash unchanged); opencode passed; agent-comm (OpenCode
  lead, Pi workers) succeeded in 91s, $0.10.
- Public build: a pi smoke passed (58s, served glm-5.3, 0 leftover
  sandboxes).

Previous build, 2026-10-28 (ADR 0018: Herdr 0.9.5, Codex 0.165.0, Claude Code
2.1.308, OpenCode 2 2.2.0, Pi 1.4.0, Qwen Code 0.26.2, Goose 1.56.0, Node
22.23.4): template id `xj8glxnh091op795c7me`, build
`56678a3b-9c19-4547-97f2-464c332856bd` (2 vCPU, 2048 MiB), still public
(`public: true`), from the Dockerfile at commit `ff227dd` (the build context is
unchanged through `d288649`); E2B `diskSizeMB` 14297. It was first built as the
private staging template (`sdf-herdr-agents-staging`, build
`5bccc49b-3892-45cf-ab7f-013cab75848c`, since deleted) and verified there:

- Secret check: no credential files, npmrc tokens or secret env names; the
  only key-shaped matches are the 3 E2B SSH host-key baseline lines; the
  operator's E2B/Vultr key values are absent (compared by SHA-256 only). The
  same scan passed on the public build. Installed versions match every pin;
  Node >= 22.19; no second Pi under `/opt/sdf-pi-extensions` (5.2 MiB,
  root-owned, not writable by `user`); pi-lens and pi-fabric absent.
- Extension load (Pi 1.4.0, 127.0.0.1 stub, `--no-mcp --no-approve`):
  write mode had tools `apply_patch,bash,read`, pi-steering blocked a force
  push and `apply_patch` patched the file; read-only had `find,grep,ls,read`,
  "Tool apply_patch not found", file unchanged; a bad `-e` path exits 1.
- Live on Vultr glm-5.3 with egress locked (only `api.vultrinference.com`
  reachable), one kind at a time, both criteria PASS for each:
  codex (50.5s, 41.9k in / 401 out, about $0.033), claude (41.9s, served
  glm-5.3; Vultr's Anthropic endpoint reports zero usage), pi (22.2s, 4.8k
  tokens, $0.004), qwen (15.8s, 26.4k tokens, $0.021), goose (25.7s, 7.5k
  tokens, $0.011; Goose does not report the served model) and opencode
  (18.2s, 13.2k tokens, $0.010). Served model glm-5.3 wherever the agent
  reports it; no leaks, 0 leftover sandboxes.
- The first OpenCode live run failed: OpenCode 2.2.0 takes its project
  directory from `$PWD`, which in E2B is the image `WORKDIR`
  (`/home/user/project`), so the agent looked in an empty folder. Fixed in
  the runtime (`d288649`, the driver sets `PWD` to the Attempt directory);
  the image needed no change.
- Pi read-only live with both extensions loaded (`--tools read,grep,find,ls`,
  asked to edit with any tool): only `read` ran, the file hash was unchanged,
  and the model reported it had no write tools.
- Agent-comm (OpenCode lead, two Pi workers): run succeeded in 65s, $0.087,
  0 sandboxes left.
- Public build: pi (36.0s) and qwen (44.1s) smokes passed, served glm-5.3.

Previous build, 2026-10-28 (ADR 0017: Pi extensions `@cad0p/pi-steering`
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
(`45ba7067-6549-48e3-8510-b69caeef0333`); rebuilding replaces the public
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
