# Image-baked Pi extensions

Status: accepted (2026-10-28). Amends [ADR 0012](0012-pi-agent-kind.md);
respects [ADR 0008](0008-claude-skips-permission-prompts-in-disposable-sandboxes.md).

The `pi` agent kind ships with two third-party Pi extensions, baked into the
`sdf-herdr-agents` image and loaded from an allowlist. Two more were requested
and rejected: `pi-fabric` (incompatible with Pi 0.73.1) and `pi-lens`
(intermittently hangs headless turns).

## Vetting (Pi 0.73.1, Node 22)

Each package was fetched with `npm pack` and its code was read. It was then
run under Pi 0.73.1 against a 127.0.0.1 stub model, with network, file and
process tracing.

| Package | Version | License | What it does | Decision |
| --- | --- | --- | --- | --- |
| `@cad0p/pi-steering` | 0.2.0 | MIT | `tool_call` hooks that block `git push --force`, `git reset --hard`, `rm -rf /` and long-running dev servers. No network, no new tools; its optional git plugin is off. | Keep; loads in read-only turns too |
| `@paulpham157/apply-patch` | 0.3.1 | MIT | An `apply_patch` tool (Codex patch format) that replaces `edit`/`write`, confined to the workspace. No network, no shell. | Keep; write mode only |
| `pi-lens` | 4.4.6 | MIT | Lint, type-check and LSP feedback after writes, plus symbol and report tools. It spawns analysers found on `PATH`, `git` and the bundled `ast-grep`. | **Rejected** (hangs) |
| `pi-fabric` | 0.128.0 | MIT | A multi-agent and kernel fabric. | **Rejected** |

Why `pi-fabric` was rejected:

- It refuses hosts older than Pi 1.0 (`MINIMUM_PI_HOST_VERSION`) and declares
  Node >= 24.
- It bundles its own Pi 1.3 SDK and a third-party LLM provider extension
  (`inference.coralbricks.ai`) that fetches a model catalogue and writes to the
  agent directory.
- It also brings an MCP client and QuickJS, Python and unsafe Node or Bun
  process kernels. That attack surface does not belong in an Attempt sandbox.

`SDF_PI_EXTENSIONS=pi-fabric` fails with that reason. Revisit it only together
with a Pi 1.x upgrade.

Why `pi-lens` was rejected: it loaded and worked (its 8 tools registered, and
it ran analysers after `apply_patch`), but in live Vultr glm-5.3 Attempts with
egress locked, Pi intermittently never exited after the final answer. Pi then
sat idle with no child process and no open outbound socket, its event stream
ending at the last `turn_end` with no `agent_end`. That points to a promise in
pi-lens's `turn_end`/`agent_end` handlers (debounced flush, bootstrap clients,
`handleTurnEnd`) that never settles. Results on the same staging image:

- with pi-lens: 2 of 3 runs hung until the turn timeout (420s and 240s);
- without it: 3 of 3 runs passed in 13–34s, with about a third of the input
  tokens (its tool schemas add about 8k tokens per request).

A turn that hangs until the timeout fails the Attempt, so it is not usable
headless with Pi 0.73.1. pi-lens is also by far the largest package: a
155k-line bundle, LSP and MCP clients, and about 100 MiB with `ast-grep` and
tree-sitter WASM. `SDF_PI_EXTENSIONS=pi-lens` fails with that reason. Revisit
it only with a pi-lens release that bounds its turn-end work, re-tested live.

Both kept packages import the renamed `@earendil-works/pi-*` packages and
declare them as peers, while SDF pins `@mariozechner/pi-coding-agent` 0.73.1.
Pi's loader aliases only `@mariozechner/*`. The image therefore links
`node_modules/@earendil-works/{pi-coding-agent,pi-tui,pi-ai,pi-agent-core}` to
the globally installed `@mariozechner` packages (`link-pi-host.mjs`). Same
files means same module instances, and no second Pi is installed.

## Decision

**Image.** `infra/e2b/herdr-agents/pi-extensions/` holds:

- a `package.json` with exact versions and no scripts;
- the committed `package-lock.json` (5 packages, each with a sha512
  integrity, all from registry.npmjs.org);
- the link script and `verify-pins.mjs`.

The Dockerfile copies them to `/opt/sdf-pi-extensions` and runs
`npm ci --ignore-scripts --legacy-peer-deps`. `npm ci` checks every tarball
against the lockfile; `--ignore-scripts` means no install scripts run (none of
the five packages declares one). The build fails if:

- a version differs from its pin;
- a peer link is a real install rather than a link.

The tree is root-owned and not writable by the agent user.

**Loading.** `sdf_core.pi_runtime.PI_EXTENSIONS` is the allowlist. Each entry
records the version, the integrity, the entry file and whether the extension
is read-only safe. Unit tests require it to equal the manifest and the
lockfile. Pi still runs with `--no-extensions --no-skills
--no-prompt-templates --no-themes`, and `.pi/` is still refused (ADR 0012).
Each selected extension is added as `-e /opt/sdf-pi-extensions/node_modules/<name>/<entry>`.
Pi exits non-zero if an `-e` path is missing or fails to import, so a broken
extension fails the turn rather than being silently skipped.

**Selection: `SDF_PI_EXTENSIONS`.**

- Unset: both.
- Empty: none.
- Otherwise: a comma-separated list of allowlisted package names. Anything
  else, including paths, `npm:` specs, version ranges, `pi-fabric` and
  `pi-lens`, is refused before a sandbox is created.

Nothing is ever installed at runtime. Before the ADR 0008 claim, `start()`
reads each selected package's `package.json` in the sandbox and refuses a
missing package or a version mismatch. On a template without the extensions,
set `SDF_PI_EXTENSIONS=`.

The turn wrapper still unsets every `PI_*` variable before running Pi, so no
caller-supplied variable can change how Pi or an extension behaves.

## ADR 0008 (read-only stays read-only)

Write tools still require the one fresh production E2B claim. Without it, Pi
keeps `--tools read,grep,find,ls`. In Pi 0.73.1 that flag is a hard allowlist:
a tool outside it is "not found" even when an extension calls
`setActiveTools`. Verified: a forced `apply_patch` call failed with
"Tool apply_patch not found" and the file was unchanged.

Read-only turns also load only extensions marked read-only safe. Today that is
`@cad0p/pi-steering` alone, which adds blocking hooks and nothing else.
apply-patch, a write tool, never loads in read-only mode.

## Residual risks

- Neither kept extension makes network calls. The sandbox egress allowlist
  remains the backstop: `E2BAgentSandbox` and the live tests lock egress to
  the inference host. Callers that create a transport with `network=None`
  keep E2B's open default.
- pi-steering also reads `~/.pi/agent/steering`. A write-mode agent could
  weaken its own guardrails there; it already has a shell, so this is no
  escalation. The project layer (`.pi/steering`) is refused with `.pi/`.
- A write-mode agent can edit files under `$HOME`, as before. The extension
  code itself is root-owned.

## Consequences

- The extension tree adds about 5 MiB to the image.
- Bumping a package means updating `package.json`, regenerating the lockfile
  (`npm install --package-lock-only --ignore-scripts --legacy-peer-deps`),
  re-vetting it, updating `PI_EXTENSIONS`, and rebuilding through private
  staging first.
- Templates other than `sdf-herdr-agents` need `SDF_PI_EXTENSIONS=`.

## Verification (2026-10-28)

- Staging build `fda6c1cf-08cf-4f87-ad3b-098f60ea6338`:
  - boot secret scan clean;
  - extension load test: write mode, read-only and a bad `-e` path;
  - live pi passed 2 of 2;
  - opencode, qwen and agent-comm regression passed.
- Public `sdf-herdr-agents` (`xj8glxnh091op795c7me`) rebuilt as
  `dfc0c53e-e0b6-4c02-a14e-d7904a5cb2c6`, still public, and the pi smoke test
  passed. Details are in the template README.

## Update (2026-10-28, ADR 0018)

With Pi 1.4.0 (`@earendil-works`) the extensions' peer imports resolve to
Pi's own modules, so `link-pi-host.mjs` is gone and `verify-pins.mjs` instead
fails if any `@earendil-works`/`@mariozechner` package appears under the
extensions' `node_modules`. Extension pins and lockfile hashes are unchanged.
pi-fabric was re-vetted and stays rejected (it sends prompts and code to its
own third-party model services, embeds a second Pi, and needs Node >= 24).
pi-lens: see the template README for the re-test under Pi 1.4.0.
