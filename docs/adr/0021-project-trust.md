# Agents trust their Attempt workspace's project config in a fresh E2B sandbox

Status: accepted (2026-10-29). Supersedes the "refuse / ignore project
config" parts of [ADR 0008](0008-claude-skips-permission-prompts-in-disposable-sandboxes.md)
(Claude `--setting-sources user`), [ADR 0011](0011-opencode-agent-kind-and-per-agent-provider-config.md)
(`OPENCODE_DISABLE_PROJECT_CONFIG`), [ADR 0012](0012-pi-agent-kind.md)
(`.pi/` refusal, `--no-approve`) and [ADR 0013](0013-openai-compatible-headless-agent-kinds.md)
(`.qwen/`, `.env`, `.goose/` refusal, Qwen `--bare`). Everything else in
those ADRs stands. Pins as in [ADR 0018](0018-opencode-2-and-pi-1-upgrade.md).

## Context

The owner drives agents only through SDF; nobody is at the sandbox to answer
a CLI's "trust this folder?" prompt. Repositories legitimately carry agent
instructions (`AGENTS.md`, `CLAUDE.md`, skills, `.pi/APPEND_SYSTEM.md` …), and
refusing or ignoring them made agents worse at the repo they were given.
The earlier ADRs refused project config because it can redirect the model
endpoint (exfiltrating the injected key), re-enable tools or run hooks.

## Decision

* `SDF_<AGENT>_PROJECT_TRUST=always|never` per kind (`sdf_core/project_trust.py`),
  default `always`; any other value fails closed with an error.
* Effective trust = setting `always` **and** the fresh-E2B permission claim
  (`_claim_fresh_e2b_permission_bypass`, exact E2B transport types, consumed
  once at start). Daytona, SSH, local and test transports stay untrusted and
  keep the old refusals. The host-side staging refusal is skipped only when the
  claim is still available; the in-sandbox wrapper refuses again if the claim
  then fails. Codex is the exception: it was already trusted on every
  transport (ADR 0011 `projects.<dir>.trust_level`), now `untrusted` under
  `never`.
* Host-controlled invocation still outranks project config, per kind:

| Kind | Trusted invocation | Endpoint / key / model pin (probe, hostile project config, in-sandbox mock) |
| --- | --- | --- |
| Claude 2.1.308 | `--setting-sources user,project,local --settings ~/.config/sdf/claude-settings.json` | project `env` **did** hijack `ANTHROPIC_BASE_URL`/key with project sources alone. The SDF flag settings file (written by the trust seed from sandbox env, names only) pins `ANTHROPIC_BASE_URL`, blanks Bedrock/Vertex/Foundry switches and unset tokens, blanks `ANTHROPIC_API_KEY` and supplies it via `apiKeyHelper` reading the 0600 pane env file. Then our endpoint and key were used, `--model` won, and the project skill/CLAUDE.md marker was honoured. |
| Codex 0.165.0 | `-c projects."<dir>".trust_level="trusted"` (unchanged) | project `openai_base_url`, `model_provider`, `model_providers.openai`, `model` ignored; `developer_instructions` marker honoured |
| OpenCode 2.2.0 | stop setting `OPENCODE_DISABLE_PROJECT_CONFIG`/`OPENCODE_CONFIG_PROJECT_DISABLE`; set `OPENCODE_CONFIG_CONTENT` to SDF's user config text | project `opencode.json` / `.opencode/opencode.json` **did** override `baseURL`/`apiKey` and re-enable `subagent`/`webfetch`; `OPENCODE_CONFIG_CONTENT` loads after project config, so SDF's provider, key, model and denials win; `AGENTS.md`/instructions marker honoured. Missing user config fails the turn (exit 96). |
| Pi 1.4.0 | `--approve` (seed also writes `defaultProjectTrust: always` to `~/.pi/agent/settings.json`), no `.pi` refusal | project `models.json` ignored; `--provider sdf --model` win; `APPEND_SYSTEM.md` marker honoured; `--no-extensions --no-skills --no-mcp`, `PI_OFFLINE` keep project extensions/packages and the third-party sub-agent tool off |
| Qwen 0.26.2 | drop `--bare`, no `.qwen`/ancestor `.env` refusal | hostile `.qwen/settings.json` (auth, model, modelProviders, env, `tools.core` incl. `agent`), `.qwen/.env`, `.env` did not move base URL/key/model; `--exclude-tools` still removed `agent`; `QWEN.md` marker honoured |
| Goose 1.56.0 | no `.goose` refusal | hostile `.goose/config.yaml`, `config.yaml`, custom providers and `.env` changed nothing (`--no-profile --provider sdf_openai --model`); `.goosehints`/`AGENTS.md` honoured; no `delegate` |

Sub-agent bans (ADR 0020) stay on the command line / in SDF's highest config
layer and were re-checked under the hostile configs above.

## Consequences

* The egress allowlist of the E2B sandbox remains the security boundary.
  Trusted project config can run hooks, plugins, MCP servers and shell
  commands with the agent's own permissions (which in a fresh sandbox already
  include arbitrary shell), so anything the agent process can read, project
  code can read, including `~/.config/sdf/agent-env.sh`. Trust adds no
  capability the agent's shell tool did not already have; it adds
  *unattended* execution of repository-supplied code.
* Provider keys in subprocess environments: Claude's flag settings blank
  `ANTHROPIC_API_KEY` in Claude's environment, so hooks/MCP servers it spawns
  do not inherit it. OpenCode plugins and MCP servers inherit the env the
  provider key is referenced from (`{env:NAME}`); Qwen and Goose subprocesses
  likewise inherit theirs. That is not avoidable without changing how the
  CLIs read keys and is accepted under the egress boundary above.
* Pi: `pi-lens` may resolve project-local linters/formatters from the
  workspace (`node_modules/.bin`), i.e. run repository code; same boundary.
* Codex `never` (`trust_level="untrusted"` beside the config.toml trust seed)
  is unit-tested only, not live-verified.
* Permission-skipping and trust remain tied to the one fresh-E2B claim; reusing
  a sandbox or any other transport falls back to the pre-0021 behaviour.
