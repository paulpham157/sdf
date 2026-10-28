# Qwen Code, Goose and Aider are headless OpenAI-compatible agent kinds

Status: accepted

SDF adds three popular open-source coding-agent CLIs as agent kinds on par
with OpenCode (ADR 0011) and Pi (ADR 0012). None runs in a Herdr pane: each is
a `HeadlessAgentRuntime` (`sdf_core.headless_runtime`) that runs one
non-interactive turn per `send` through the persistent sandbox transport (E2B
or Daytona), which still owns creation, credential seeding, workspace
staging/collection and teardown. Each runtime refuses any other installed
version, because it depends on that release's flags, output shape and
project-config layout.

| Agent | Pin | Runtime | Headless call | Base URL | Model |
| --- | --- | --- | --- | --- | --- |
| `qwen` | npm `@qwen-code/qwen-code` `0.26.2` (needs Node ≥ 22) | `sdf_core.qwen_runtime.QwenHeadlessRuntime` | `qwen -o json --bare --auth-type openai`, prompt on stdin | `SDF_QWEN_BASE_URL` → `--openai-base-url` | `SDF_QWEN_MODEL` → `-m` |
| `goose` | GitHub release `1.56.0`, musl tarball, sha256-pinned | `sdf_core.goose_runtime.GooseHeadlessRuntime` | `goose run -i - --output-format json --no-profile` | `SDF_GOOSE_BASE_URL` → seeded custom provider | `SDF_GOOSE_MODEL` → `--model` |
| `aider` | PyPI `aider-chat` `0.86.2` (own venv) | `sdf_core.aider_runtime.AiderHeadlessRuntime` | `aider --message-file F` | `SDF_AIDER_BASE_URL` → `--openai-api-base` | `SDF_AIDER_MODEL` → `--model openai/<id>` |

Defaults: `https://api.vultrinference.com/v1` and `glm-5.3` (a bare id, no
provider prefix). All three are Apache-2.0, so bundling them in the
`sdf-herdr-agents` image is fine.

## Research (2026-10-28)

| Agent | Headless | OpenAI-compatible base URL + key by env name | License | Workspace config | Decision |
| --- | --- | --- | --- | --- | --- |
| Qwen Code | yes (`-p`/stdin, `-o json`) | yes (`--openai-base-url`, `OPENAI_API_KEY`) | Apache-2.0 | `--bare` skips settings/hooks/MCP/extensions, but still loads an upward `.env` / `.qwen/.env` | **implemented**; refuse `.env`/`.qwen` |
| Goose | yes (`run -i -`, JSON output) | yes (declarative custom provider, `api_key_env` = name) | Apache-2.0 | no endpoint/key/mode read from the workspace | **implemented**; `.goose` refused as defense in depth |
| Aider | yes (`--message-file`, exits) | yes (`--openai-api-base`, LiteLLM `openai/<id>`) | Apache-2.0 | merges `.aider*.yml`, `.env` from cwd, git root and `$HOME`; GitPython runs repo-configured programs | **implemented**; refuse `.aider*`/`.env`/`.git`, private per-turn repo |
| Gemini CLI | yes | no custom OpenAI-compatible endpoint (Gemini API only) | Apache-2.0 | — | deferred (Qwen Code is its OpenAI-compatible fork) |
| GitHub Copilot CLI | yes | no; GitHub-hosted models, needs a GitHub token | proprietary | — | deferred |
| Amp | yes | no; Amp account/backend | proprietary | — | deferred |
| Crush | yes | yes | FSL-1.1 (not open source until conversion) | — | deferred (license) |
| Cursor CLI | yes | no custom base URL | proprietary | — | already a kind (subscription); no change |
| Kilo Code CLI | yes | yes | MIT | — | deferred (OpenCode fork; redundant with `opencode`) |

## Credentials

- `api-key` mode only (`SDF_CREDENTIAL_MODE_{QWEN,GOOSE,AIDER}=api-key`);
  `subscription` is a configuration error and a missing mode fails closed
  (ADR 0007).
- Default key variables `SDF_QWEN_API_KEY`, `SDF_GOOSE_API_KEY`,
  `SDF_AIDER_API_KEY` (ADR 0011 rule). `SDF_<AGENT>_API_KEY_ENV` *replaces*
  the default with another name (e.g. `VULTR_INFERENCE_API_KEY`); it never adds
  a fallback, and an unset named variable fails closed. The ADR 0007 / 0011
  name rules apply, plus per-kind reserved prefixes, because the turn wrapper
  unsets them and the agent would never see the key: `QWEN_`/`OPENAI_`/
  `GEMINI_` (qwen), `GOOSE_`/`OPENAI_` (goose), `AIDER_`/`OPENAI_`/`LITELLM_`
  (aider).
- Seeds record only the key variable's **name** (mode `0600`):
  `qwen-key-ref` and `aider-key-ref` write a name file that the wrapper reads
  and validates (`[A-Z][A-Z0-9_]*_API_KEY`) before exporting
  `OPENAI_API_KEY="$NAME_VALUE"` for that turn only; `goose-provider` writes
  `~/.config/goose/custom_providers/sdf_openai.json` (engine `openai`) whose
  `api_key_env` is the name. The value never reaches argv, a file or a log.
- The variables that would move each agent's config or widen its permissions
  (`QWEN_HOME`, `QWEN_CODE_SYSTEM_SETTINGS_PATH`, `GOOSE_PATH_ROOT`,
  `GOOSE_MODE`, `AIDER_CONFIG`, `AIDER_YES_ALWAYS`, …) are stripped at sandbox
  creation; the shared `OPENAI_*` names stay with Codex so the kinds can share
  a box, and are unset per turn instead.

## Hardening (the Attempt workspace is untrusted)

Shared wrapper, every turn, before the agent starts:

- unset every variable with the kind's prefixes, then export fixed
  telemetry/update switches (`QWEN_USAGE_STATISTICS_ENABLED=false`,
  `GOOSE_TELEMETRY_ENABLED=false`, `GOOSE_DISABLE_KEYRING=1`,
  `AIDER_ANALYTICS_DISABLE=true`, `AIDER_CHECK_UPDATE=false`, …);
- refuse (exit marker 96, agent never started) when the workspace holds a
  project-config entry, or an ancestor holds a config file the agent searches
  upward for. The same check runs on the host in `bind_workspace` before
  anything is staged, and again in the sandbox because the agent may create one;
- fail closed when the endpoint, model or key variable is missing (95/94);
- run under `timeout --kill-after=10` with the prompt on stdin or in a private
  file; stdout and an explicit exit marker are parsed, and the stderr tail is
  kept for errors. `cancel` runs `pkill -f` on the kind's command line and
  closes the environment.

Per kind:

- **Qwen Code**: `--bare` does not stop `.env` loading; a planted `.env` was
  verified to redirect the endpoint and key, so `.env` and `.qwen` are refused
  in the workspace and any ancestor `.env` / `.qwen/.env` /
  `.qwen/settings.json` too. The CLI's own sandbox relaunch is disabled.
- **Goose**: verified to read nothing from a workspace `.goose/`, `config.yaml`
  or `.env`; `.goose` is still refused. `XDG_*` are unset so the seeded
  provider cannot be moved; `--no-profile` loads no configured extensions.
- **Aider**: refuses `.aider*`, `.env` and `.git` (GitPython would run
  `core.fsmonitor`, filters, …). The wrapper creates a private repository per
  turn (`git init`, `git add -A`, no commit) for the repo map and removes it,
  together with `.aider.tags.cache.v*` (a pickle cache), afterwards. Every
  security-relevant flag is set explicitly (command line wins); chat/input
  history go to `/tmp`; `--no-gitignore`.

## Permissions (ADR 0008)

Write-capable modes are enabled only after the one fresh production E2B
claim (`_claim_fresh_e2b_permission_bypass`); everywhere else (Daytona, a
reused sandbox, tests) each kind runs read-only:

| Agent | Read-only (default) | After the fresh E2B claim |
| --- | --- | --- |
| qwen | `--approval-mode plan` | `--approval-mode yolo` |
| goose | `GOOSE_MODE=chat` (no tools) | `GOOSE_MODE=auto` + builtin `developer` |
| aider | `--chat-mode ask --no-suggest-shell-commands` | default edit format + `--yes-always` |

## Usage and served model

- Qwen's JSON reports tokens and the served model per assistant message.
- Goose reports cumulative tokens and its own `cost_usd` estimate (from its
  pricing table, not the provider; not authoritative) and the requested model
  only.
- Aider prints `Tokens: N sent, M received` per exchange; no served model.
- `served_model_reported` is `False` for goose and aider; SDF does not claim a
  served model it never saw. Costs are computed only for the default Vultr
  `glm-5.3` price (USD 0.75 / 3.00 per 1M in/out).

## Live verification (E2B, Vultr glm-5.3, egress locked to `api.vultrinference.com`)

Installed at runtime on the `codex` template (the published
`sdf-herdr-agents` image does not contain these agents yet; Qwen also needed
Node 22 there). Every run: Attempt completed, both acceptance criteria PASS,
1 sandbox, 0 left, no key value in output or reports.

| Kind | Tokens in / out | LLM est. | Duration (bootstrap) | Served model |
| --- | --- | --- | --- | --- |
| qwen | 26,496 / 371 | $0.021 | 62.1 s (41 s) | `glm-5.3` |
| goose | 7,515 / 221 | $0.0063 | 34.1 s (28.7 s) | not reported |
| aider | 785 / 65 | $0.0008 | 78.2 s (70.9 s) | not reported |

## Consequences

- The Dockerfile installs all three (pins in ARGs, `--version` checks), but
  the public `sdf-herdr-agents` template must be rebuilt before they are
  available without a runtime install.
- Aider is a pair programmer, not an autonomous agent: one turn is one model
  exchange plus its edits.
