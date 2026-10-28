# Agent upgrade: OpenCode 2 and Pi 1

Status: accepted (2026-10-28). Amends [ADR 0011](0011-opencode-agent-kind-and-per-agent-provider-config.md)
(OpenCode), [ADR 0012](0012-pi-agent-kind.md) (Pi) and
[ADR 0017](0017-image-baked-pi-extensions.md) (Pi extensions).

## Context

Every pinned agent CLI was moved to its latest stable release:

| Agent | Old pin | New pin | Notes |
| --- | --- | --- | --- |
| Codex (`@openai/codex`) | `0.157.0` | `0.165.0` | flags, `-c` pins, project-config handling and `OPENAI_BASE_URL` behaviour unchanged |
| Claude Code | `2.1.282` | `2.1.308` | npm `stable` (`latest` is a newer, non-stable build) |
| OpenCode | `opencode-ai` `1.19.4` | `@opencode/cli` `2.2.0` | OpenCode 2: new package, config schema, CLI and server; this ADR |
| Pi | `@mariozechner/pi-coding-agent` `0.73.1` | `@earendil-works/pi-coding-agent` `1.4.0` | package moved scope; new project trust model; this ADR |
| Herdr | `0.9.1` | `0.9.5` | graphics API removed (unused); Codex turn state by screen detection instead of hooks |
| Qwen Code / Goose | `0.26.2` / `1.56.0` | unchanged | already the latest stable |
| Node.js | `22` | `22` | Pi 1.x needs Node >= 22.19 |

OpenCode 2 and Pi 1 change behaviour SDF depends on; the others needed only
pin, comment and test updates.

## Decision: OpenCode 2

- **Package and version.** `@opencode/cli` replaces `opencode-ai`; the binary
  prints `opencode vX.Y.Z`. Its postinstall copies the binary from the
  exact-version platform package (falling back to an npm install of the same
  version), so the Dockerfile keeps install scripts for this one package.
- **Config (V2 schema).** The seed now writes `providers.<id>` with
  `package: "aisdk:@ai-sdk/openai-compatible"` and
  `settings: {baseURL, apiKey: "{env:NAME}"}`, `agents.title.model` for the
  small model, `update: "disable"`, `share: "disabled"`, and a
  `permissions` rule list (later rules win):
  `* → ask`; `read`, `glob`, `grep → allow`; `*.env`/`*.env.*` reads `ask`
  (`*.env.example` allowed); `webfetch`, `websearch`, `execute`,
  `opencode_worktree_create`, `opencode_worktree_remove`,
  `opencode_session_move → deny`. Headless `run` rejects every `ask`, so
  without `--auto` OpenCode can read but not edit or run commands, as before
  (ADR 0008). Worktree/session tools are gated by `edit` in 2.2.0 as well;
  they are denied explicitly anyway.
- **Private server per turn.** OpenCode 2 connects MCP servers only when a
  server first boots a directory, so a one-shot run never offered the
  `sdf-msg` MCP tools (ADR 0014). Each turn now starts `opencode serve` on a
  free `127.0.0.1` port with a random per-turn password (env only), waits for
  `/api/mcp` until every configured MCP server settled (a server that never
  connects is reported as `[sdf: MCP servers not ready: ...]` and the turn
  still runs), runs `opencode run --server URL --format json ...`, and always
  kills the server. Cancel kills both (`pkill -f '[o]pencode (run|serve) '`).
- **Prompt on stdin.** 2.2.0 JSON-quotes a message argument that contains
  spaces or quotes, repeats it after `--`, and treats a leading `-` as a flag.
  The prompt is now written to stdin, which is passed verbatim; argv carries
  no prompt.
- **Usage.** OpenCode 2 emits no `step_finish` for the final text step and
  reports no token `total`. The driver sums this turn's assistant messages
  from the server's session message list into a synthetic `sdf_usage` event;
  `total_tokens` is input + output + reasoning + cache read/write.
- **Hardening.** Every inherited `OPENCODE_*` variable is dropped (config and
  permission overrides, `OPENCODE_SERVER_URL`, passwords, feature flags) and
  `OPENCODE_DISABLE_PROJECT_CONFIG=1` plus `OPENCODE_CONFIG_PROJECT_DISABLE=1`
  are forced. Verified locally against 2.2.0: a workspace `opencode.json` and
  `.opencode/opencode.json` that allow everything and redirect the provider
  to a second stub, plus `OPENCODE_CONFIG_CONTENT`, `OPENCODE_PERMISSION` and
  `OPENCODE_SERVER_URL` in the environment, sent 0 requests to the evil stub
  and could not run a command; `AGENTS.md` instructions did not execute.

## Decision: Pi 1

- **Package.** `@earendil-works/pi-coding-agent`; `pi --version` prints to
  stdout (0.73 used stderr). The version probe purges `PI_*` like the turn
  wrapper so a hostile `PI_PACKAGE_DIR` cannot change the answer.
- **API key.** Pi 1.x reads `apiKey: "$NAME"` from the environment; a bare
  name is now sent as the literal key. The seed writes `"$" + NAME` (still a
  validated `*_API_KEY` name, never a `!command`).
- **Project trust.** Pi 1.x adds `--no-approve` (ignore project-local
  settings and resources) and built-in MCP. Every turn passes `--no-approve`
  and `--no-mcp` in addition to the discovery-off flags. SDF still refuses a
  workspace containing `.pi/` on the host and in the sandbox (defence in
  depth).
- **Extensions.** Pi 1.x resolves an extension's `@earendil-works/*` imports
  to its own modules, so `link-pi-host.mjs` is removed; `verify-pins.mjs`
  fails the build if `@earendil-works` or `@mariozechner` appears under the
  extensions' `node_modules` (no second Pi). pi-steering 0.2.0 and
  apply-patch 0.3.1 keep their pins and lockfile hashes.
- **pi-fabric stays rejected.** It routes prompts and code to its own
  third-party model services and embeds a second Pi; it also needs Node >= 24.
- `--tools` stays a hard allowlist in 1.4.0: read-only turns get
  `find,grep,ls,read`; Pi's codemode tool is not exposed under the allowlist
  nor by the default tool set.

## Consequences

- One extra process (the private server) per OpenCode turn, bound to
  loopback with a per-turn password; startup adds about a second locally.
- The Docker build needs install scripts for `@opencode/cli`; the other npm
  packages are unchanged.
- The public `sdf-herdr-agents` template is rebuilt only after a private
  staging build passes the secret scan and all live checks.

## Verification

Local, against 127.0.0.1 stub models:

- OpenCode 2.2.0: multi-turn session continuation, usage, MCP tools through
  the private server, hostile workspace/env as above.
- Pi 1.4.0 with both extensions in write mode: pi-steering blocked a force
  push, `apply_patch` and `bash` created files, turn 2 continued the session,
  the hostile `PI_CODING_AGENT_DIR` was ignored. Read-only with pi-steering:
  tools `find,grep,ls,read`, bash/write failed, no file created.
- Codex 0.165.0: hostile project config ignored, `-c` pins hold, web search
  off. Claude 2.1.308 with `--setting-sources user`: a hostile project
  settings file got 0 stub requests and its hook did not run (control
  without the flag leaked).
- Full suite and PostgreSQL suite pass; ruff F unchanged from main.

Live results are recorded in the template README.
