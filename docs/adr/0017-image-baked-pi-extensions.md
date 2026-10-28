# Image-baked Pi extensions

Status: accepted (2026-10-28). Amends [ADR 0012](0012-pi-agent-kind.md);
respects [ADR 0008](0008-claude-skips-permission-prompts-in-disposable-sandboxes.md).

The `pi` agent kind ships with three third-party Pi extensions, baked into the
`sdf-herdr-agents` image and loaded from an allowlist. A fourth that was
requested, `pi-fabric`, was rejected.

## Vetting (Pi 0.73.1, Node 22)

Each package was fetched with `npm pack` and its code was read. It was then
run under Pi 0.73.1 against a 127.0.0.1 stub model, with network, file and
process tracing.

| Package | Version | License | What it does | Decision |
| --- | --- | --- | --- | --- |
| `@cad0p/pi-steering` | 0.2.0 | MIT | `tool_call` hooks that block `git push --force`, `git reset --hard`, `rm -rf /` and long-running dev servers. No network, no new tools; its optional git plugin is off. | Keep; loads in read-only turns too |
| `@paulpham157/apply-patch` | 0.3.1 | MIT | An `apply_patch` tool (Codex patch format) that replaces `edit`/`write`, confined to the workspace. No network, no shell. | Keep; write mode only |
| `pi-lens` | 4.4.6 | MIT | Lint, type-check and LSP feedback after writes, plus symbol and report tools. It spawns analysers found on `PATH`, `git` and the bundled `ast-grep`. | Keep, hardened; write mode only |
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

All three kept packages import the renamed `@earendil-works/pi-*` packages and
declare them as peers, while SDF pins `@mariozechner/pi-coding-agent` 0.73.1.
Pi's loader aliases only `@mariozechner/*`. The image therefore links
`node_modules/@earendil-works/{pi-coding-agent,pi-tui,pi-ai,pi-agent-core}` to
the globally installed `@mariozechner` packages (`link-pi-host.mjs`). Same
files means same module instances, and no second Pi is installed.

## Decision

**Image.** `infra/e2b/herdr-agents/pi-extensions/` holds:

- a `package.json` with exact versions and no scripts;
- the committed `package-lock.json` (34 entries, each with a sha512
  integrity);
- the link script and `verify-pins.mjs`.

The Dockerfile copies them to `/opt/sdf-pi-extensions` and runs
`npm ci --ignore-scripts --legacy-peer-deps`. `npm ci` checks every tarball
against the lockfile; `--ignore-scripts` means no install scripts run. The only
package with a script is `@ast-grep/cli`, whose postinstall merely copies the
platform binary that pi-lens resolves directly. The build fails if:

- a version differs from its pin;
- a peer link is a real install rather than a link;
- the `ast-grep` binary is missing.

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

- Unset: all three.
- Empty: none.
- Otherwise: a comma-separated list of allowlisted package names. Anything
  else, including paths, `npm:` specs, version ranges and `pi-fabric`, is
  refused before a sandbox is created.

Nothing is ever installed at runtime. Before the ADR 0008 claim, `start()`
reads each selected package's `package.json` in the sandbox and refuses a
missing package or a version mismatch. On a template without the extensions,
set `SDF_PI_EXTENSIONS=`.

**pi-lens hardening.** The turn wrapper first unsets every `PI_*` variable,
then exports:

- `PI_LENS_DISABLE_TOOL_INSTALL=1`, `PI_LENS_DISABLE_LSP_INSTALL=1` and
  `PI_LENS_DISABLE_TOOL_REFRESH=1`: no npm, pip or GitHub-release installs;
- `PI_LENS_HOME=$HOME/.pi-lens`: its state stays out of the workspace.

Because the wrapper sets these, callers cannot override them. Pi 0.73.1 has no
project-trust API, so pi-lens treats the workspace as untrusted. It then
ignores project `.pi-lens.json` LSP commands and project-local
`node_modules/.bin` binaries (verified).

## ADR 0008 (read-only stays read-only)

Write tools still require the one fresh production E2B claim. Without it, Pi
keeps `--tools read,grep,find,ls`. In Pi 0.73.1 that flag is a hard allowlist:
a tool outside it is "not found" even when an extension calls
`setActiveTools`. Verified: a forced `apply_patch` call failed with
"Tool apply_patch not found" and the file was unchanged.

Read-only turns also load only extensions marked read-only safe. Today that is
`@cad0p/pi-steering` alone, which adds blocking hooks and nothing else. pi-lens
and apply-patch, which spawn processes or write, never load in read-only mode.

## Residual risks

- In write mode pi-lens runs analysers on edited files. It can fall back to
  `npx madge` without `--no`, fetch opengrep packs (semgrep.dev) or
  tree-sitter grammars, and corepack may fetch `pnpm` if a shim is on `PATH`.
  The sandbox egress allowlist is the backstop: `E2BAgentSandbox` and the
  live tests lock egress to the inference host. Callers that create a
  transport with `network=None` keep E2B's open default.
- pi-steering also reads `~/.pi/agent/steering`. A write-mode agent could
  weaken its own guardrails there; it already has a shell, so this is no
  escalation. The project layer (`.pi/steering`) is refused with `.pi/`.
- A write-mode agent can edit files under `$HOME` (pi-lens state, npm cache),
  as before. The extension code itself is root-owned.

## Consequences

- The image is larger: the extension tree is about 106 MiB, mostly
  TypeScript, `ast-grep` and tree-sitter WASM.
- Bumping a package means updating `package.json`, regenerating the lockfile
  (`npm install --package-lock-only --ignore-scripts --legacy-peer-deps`),
  re-vetting it, updating `PI_EXTENSIONS`, and rebuilding through private
  staging first.
- Templates other than `sdf-herdr-agents` need `SDF_PI_EXTENSIONS=`.
