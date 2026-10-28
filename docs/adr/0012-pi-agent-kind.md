# Pi is a headless agent kind with a key-by-name model registry

Status: accepted

SDF adds `pi` (npm `@mariozechner/pi-coding-agent`, pinned `0.73.1`) as an
agent kind on par with OpenCode (ADR 0011). Like OpenCode it does not run in a
Herdr pane: `sdf_core.pi_runtime.PiHeadlessRuntime` runs one
`pi -p --mode json` turn per `send` through the persistent sandbox transport
(E2B or Daytona), which still owns creation, credential seeding, workspace
staging/collection and teardown. Pi's JSON events give per-message token,
cost and served-model data without screen scraping. The runtime refuses any
other installed version (`pi --version`, printed on stderr), because it
depends on that release's flags, event shapes, `models.json` schema and
project-config layout.

| Agent | Base URL | Key variable name | Model |
| --- | --- | --- | --- |
| `pi` | `SDF_PI_BASE_URL` (default `https://api.vultrinference.com/v1`) → provider `baseUrl` | `SDF_PI_API_KEY_ENV` (default `VULTR_INFERENCE_API_KEY`) → same name, written as the provider `apiKey` | `SDF_PI_MODEL` (default `glm-5.3`) → `--model` and the registered model id |

## Credentials

- `api-key` mode only (`SDF_CREDENTIAL_MODE_PI=api-key`); `subscription` is a
  configuration error, and a missing mode fails closed (ADR 0007).
- The default key variable is `VULTR_INFERENCE_API_KEY`, not an `SDF_*` name.
  This is a deliberate, documented exception: Vultr is the default provider and
  the operator already holds that variable. `SDF_PI_API_KEY_ENV` *replaces* it,
  never adds a fallback; an unset named variable fails closed. The ADR 0007 /
  0011 name rules apply (`[A-Z][A-Z0-9_]*_API_KEY`, not a provider variable,
  not an `E2B_`/`DAYTONA_`/`HERDR_`/`SDF_CREDENTIAL_MODE_`/`SDF_CONNECTION_`
  name); for Pi a `PI_*` name is also refused, because the turn wrapper unsets
  every `PI_*` variable and Pi would never see the key.
- `SDF_PI_MODEL` must be a bare model id without `:`; Pi parses `id:level` as a
  thinking level.
- The `pi-models` seed writes `~/.pi/agent/models.json` (dir `0700`, file
  `0600`) at sandbox creation with one provider `sdf`
  (`api: openai-completions`) whose `apiKey` is the key variable's **name**.
  Pi 0.73.1 resolves `apiKey` as: a value starting with `!` runs as a shell
  command; otherwise the environment variable of that name if set; otherwise
  the literal string. The seed only ever writes a validated `*_API_KEY` name,
  so it can never be a `!command`; the key value never reaches a file or a
  command line. Residual: if the variable were unset in the sandbox, Pi would
  send the name itself as a bearer token (no secret involved). The plan fails
  closed when the variable is unset on the host, and the transport injects it
  into the sandbox environment.
- Token prices are registered only for the default Vultr `glm-5.3`
  (USD 0.75 / 3.00 per 1M input/output); any other endpoint or model reports
  tokens with cost 0. URL and model are matched by escaped regexes because a
  seed command may not contain a plan variable's value.

## Hardening (the Attempt workspace is untrusted)

- Pi 0.73.1 always merges `<cwd>/.pi/settings.json` (packages, `shellPath`,
  …) and loads `<cwd>/.pi/extensions` (code), skills, prompts, themes,
  `SYSTEM.md` and `APPEND_SYSTEM.md`; there is no switch to ignore project
  settings. SDF therefore refuses a workspace that contains `.pi` (directory,
  file or symlink, dangling included): on the host in `bind_workspace` before
  anything is staged, and again inside the sandbox before every turn, because
  the agent itself may create one (exit marker 96).
- Extension, skill, prompt-template and theme discovery are disabled by flag
  (`--no-extensions --no-skills --no-prompt-templates --no-themes`).
- `PI_CODING_AGENT_DIR`, `PI_CODING_AGENT_SESSION_DIR`, `PI_PACKAGE_DIR`,
  `PI_SHARE_VIEWER_URL` and `PI_OAUTH_CALLBACK_HOST` are stripped at sandbox
  creation; the turn wrapper then unsets every `PI_*` variable and sets
  `PI_OFFLINE=1`, `PI_SKIP_VERSION_CHECK=1`, `PI_TELEMETRY=0`.
- The prompt goes in on stdin: Pi has no `--`, and a leading `-` or `@` in an
  argument would be read as a flag or a file to attach.
- Sessions live outside the workspace (`/tmp/sdf-pi-<attempt>/sessions`,
  umask 077); later turns use `--continue`.
- Each turn runs under `timeout --kill-after=10`; the transport watchdog sits
  after that. `cancel` runs `pkill -f '[p]i -p --mode json'` and closes the
  environment.
- `AGENTS.md` / `CLAUDE.md` context files are still loaded: they are prompt
  text, the same as the Task instructions, not configuration.

## Permissions (ADR 0008)

Pi has no permission prompts: every enabled tool runs. Its write tools
(`bash`, `edit`, `write`) are enabled only after the one fresh production E2B
claim (`_claim_fresh_e2b_permission_bypass`). Everywhere else (Daytona, reused
or injected sandboxes) Pi runs with `--tools read,grep,find,ls` and cannot edit
or run commands.

## Alternatives considered

- Driving Pi through a small SDK script with `SettingsManager.inMemory()` would
  ignore project settings without refusing `.pi`, but couples SDF to Pi's
  internal API; rejected while the CLI path is sufficient.
- Passing the prompt as argv: unsafe without `--` (see above).

## Consequences

- A repository that ships a `.pi/` directory cannot be an Attempt workspace
  for `pi`; remove it from the fixture or use another agent kind.
- One sandbox holds one Pi base URL and key.
- `HTTP(S)_PROXY` are not stripped (not Pi-specific); containment is the
  sandbox and its egress allowlist.
- The `sdf-herdr-agents` Dockerfile installs the pinned package (template not
  rebuilt here); the live test bootstraps E2B's public `codex` template
  instead.

## Verification (2026-10-28)

- Deterministic: `tests/test_pi_agent.py` (credential plans, no fallback,
  key-name rules, model validation, `pi-models` seed run with node, variable
  stripping, the runtime against a fake `pi`: argv, stdin prompt, read-only vs
  claimed tools, `--continue`, `.pi` refusal on host and in-sandbox, missing
  model, version pin, timeout, cancel, error events, served-model parsing,
  Daytona wiring, and a full ExecutionService Attempt) plus the template pin
  test.
- Local: the real `pi` 0.73.1 CLI against a 127.0.0.1 stub OpenAI endpoint
  with the seeded `models.json`: the key was resolved from the named variable,
  only `read,grep,find,ls` were offered, the second turn carried the first
  turn's history, and a `.pi/` created between turns blocked the next turn.
- Live (`tests/test_vultr_agents_live.py`, `SDF_LIVE_E2B=1
  SDF_LIVE_VULTR_AGENTS=pi`): one fresh E2B sandbox (public `codex` template
  + pinned Pi 0.73.1), egress locked to `api.vultrinference.com` before seeds.
  The Attempt completed, both criteria PASS, the Assumption got `validates`;
  the fresh-E2B claim enabled write tools (`read`, `edit`). Requested and
  served model `glm-5.3`; 4,180 input / 91 output tokens, $0.0034 reported by
  Pi; 37.4 s Attempt (27.2 s bootstrap). Egress probes: only the inference
  host reachable. No secret in output, artifacts or seed commands; zero
  sandboxes left running.
