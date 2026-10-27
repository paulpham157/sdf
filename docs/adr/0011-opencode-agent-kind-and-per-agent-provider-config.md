# OpenCode is a headless agent kind, and each agent's provider is configurable

Status: accepted

SDF adds `opencode` as an agent kind that talks to any OpenAI-compatible
endpoint, with Vultr Serverless Inference (`https://api.vultrinference.com/v1`,
`vultr/glm-5.3`, small model `vultr/glm-5.3-flash`) only as the default. Unlike
Claude Code and Codex it does not run in a Herdr pane: `OpenCodeHeadlessRuntime`
runs one `opencode run --format json` turn per `send` through the persistent
sandbox transport, which still owns creation, credential seeding, workspace
staging/collection and teardown. OpenCode needs no terminal to stay
unattended, and its JSON events give SDF token and cost totals without screen
scraping. OpenCode is pinned to `1.19.4` (npm `latest` on 2026-10-22) and the
runtime refuses any other installed version, because it depends on the
`run` flags and event shapes of that release. A future OpenCode 2.x was reported
to change its server API; as of this ADR no 2.x is published on npm, so the
pin is a guard, not a measured incompatibility, and the headless `run` path
does not use the server API at all.

Each agent kind can also be pointed at its own provider, under the ADR 0007
rules (explicit `api-key` mode, no fallback, key only via the environment):

| Agent | Base URL | Key variable name | Model |
| --- | --- | --- | --- |
| `claude` | `SDF_ANTHROPIC_BASE_URL` → `ANTHROPIC_BASE_URL` | `SDF_CLAUDE_API_KEY_ENV` (default `SDF_ANTHROPIC_API_KEY`) → `ANTHROPIC_API_KEY` | `SDF_CLAUDE_MODEL` → `claude --model` |
| `codex` | `SDF_OPENAI_BASE_URL` → `OPENAI_BASE_URL` + `config.toml` | `SDF_CODEX_API_KEY_ENV` (default `SDF_OPENAI_API_KEY`) → `OPENAI_API_KEY` + `auth.json` | `SDF_CODEX_MODEL` → `codex -m` |
| `opencode` | `SDF_OPENCODE_BASE_URL` (default Vultr) → provider `baseURL` | `SDF_OPENCODE_API_KEY_ENV` (default `SDF_OPENCODE_API_KEY`) → same name, referenced as `{env:NAME}` | `SDF_OPENCODE_MODEL`, `SDF_OPENCODE_SMALL_MODEL` → `model` / `small_model` |

## Consequences

- `opencode` supports only `api-key` mode; `SDF_CREDENTIAL_MODE_OPENCODE=subscription`
  is a configuration error, never a silent switch.
- A `*_API_KEY_ENV` name is read instead of the default variable, never in
  addition to it: an unset named variable fails closed. The name must end in
  `_API_KEY` and may not be a shell provider variable (`OPENAI_API_KEY`, ...),
  nor an `E2B_`, `DAYTONA_`, `HERDR_`, `SDF_CREDENTIAL_MODE_` or `SDF_CONNECTION_`
  variable. Naming a key variable in `subscription` mode is an error.
- The OpenCode config is seeded at sandbox creation with one custom
  `@ai-sdk/openai-compatible` provider whose `apiKey` is the literal
  `{env:NAME}`; the key value is never written to a file or command line.
  `OPENCODE_CONFIG*` and `OPENCODE_PERMISSION` are stripped at creation and
  unset again by the turn wrapper so they cannot replace or widen it.
- The Attempt workspace is untrusted content. Every turn runs with
  `OPENCODE_DISABLE_PROJECT_CONFIG=1`: on 1.19.4 a workspace `opencode.json` or
  `.opencode/` could otherwise set `permission` to `allow` (permission skipping
  outside ADR 0008), point the provider `baseURL` that receives the key at
  another host, or load plugins that run code at startup (all verified locally
  with `opencode debug config` / `debug agent`, no model call).
- OpenCode 1.19.4 allows `bash` and `edit` by default. The seed sets both to
  `ask` (headless runs auto-reject them) and denies `webfetch`. `--auto` is
  passed only after the one fresh production E2B claim of ADR 0008; elsewhere
  (Daytona, reused or injected sandboxes) OpenCode cannot edit or run commands.
- Main and small model must share one provider, so one sandbox holds one
  OpenCode base URL and key.
- Model overrides for Claude and Codex are argv for `HerdrRuntime(agent_args=)`
  (`model_agent_args`); unset keeps each CLI's default. Cursor gets none.
- The `sdf-herdr-agents` Dockerfile installs the pinned `opencode-ai`; the live
  test may instead use the public E2B `opencode` template, which carries the
  same release.

## Verification (2026-10-27)

- Deterministic: `tests/test_opencode_agent.py` (credential plans, no-fallback
  key naming, seed output, model args, transport `network`/`exec_argv`, the
  headless runtime against a fake `opencode`, and a full ExecutionService
  Attempt). Full suite: 531 passed, 28 skipped (532 after the
  project-config guard and its test were added on review).
- Live (`tests/test_opencode_vultr_live.py`, `SDF_LIVE_E2B=1 SDF_LIVE_OPENCODE=1`):
  one fresh sandbox from E2B's public `opencode` template (OpenCode 1.19.4
  matched the pin), egress `allow_out=[api.vultrinference.com]`,
  `deny_out=[0.0.0.0/0]`, timeout 900 s. The Attempt completed, both criteria
  produced PASS Evidence, and the Assumption got a `validates` edge.
  `--auto` was applied through the fresh-E2B claim. OpenCode reported 3 steps,
  20,864 tokens, $0.016062. Wall time was 72.5 s. Egress probes before teardown:
  example.com and registry.npmjs.org unreachable (000), the inference host
  returned 200. No secret appeared in the output, artifacts, or seed commands,
  and zero sandboxes were left running. This live run predates the
  project-config guard; it has not been re-run live since.
- Herdr was not involved. The `opencode` kind through `HerdrRuntime` panes is
  not verified live.
- `cancel` uses `pkill -f '[o]pencode run'` so pkill cannot match its own
  `sh -c` and fail the cancel.
