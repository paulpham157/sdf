# Agent Credentials for the persistent Herdr runtime

Status: ready-for-agent
Tracker: https://github.com/paulpham157/sdf/issues/4 (source of truth)

Governing decisions: ADR-0007 (Agent Credentials injected into disposable sandboxes), ADR-0003, ADR-0004. Background: `docs/research/e2b-exec-reliability.md`. Glossary: Agent Credential, Credential Mode, Agent Runtime, Runtime Session (`CONTEXT.md`).

## Problem Statement

The operator can run a Codex Attempt end to end only through the headless `e2b-box run` path. The persistent Herdr path (the one that yields live Runtime Sessions, lifecycle events and second-agent routing) creates its sandbox through the E2B CLI with no Agent Credential at all, so any agent started there cannot call its model provider. Claude Code is not even installed in the template. Attempts to borrow host variables failed with 401 because the operator's shell `ANTHROPIC_API_KEY`/`ANTHROPIC_BASE_URL` point at a host-local proxy the cloud sandbox cannot reach. The operator wants to authenticate once on their machine (or supply a provider key) and have every sandbox work, for both Codex and Claude Code, without ever typing secrets into a pane.

## Solution

Each agent kind gets an explicit Credential Mode, `subscription` or `api-key`, configured by the operator in a repo-local `.env`. When the persistent runtime creates a sandbox, SDF resolves the Agent Credential on the host — from the herdr-e2b plugin connection for `subscription`, from dedicated `SDF_*` variables for `api-key` — validates it (presence, expiry, base URL), and passes it as sandbox-wide environment variables at creation through the E2B Python SDK. Agent config inside the box is seeded by variable name only. A new template carries pinned Codex and Claude Code. Misconfiguration fails before any sandbox is created, with a message saying exactly what to fix. The Runtime Session records which mode and connection were used, never the secret.

## User Stories

1. As an operator, I want to choose `subscription` or `api-key` separately for Claude Code and for Codex, so that I can mix a subscription login for one agent with a provider key for the other.
2. As an operator, I want SDF to refuse to start an agent whose Credential Mode is not declared, so that I never run on a credential I did not intend.
3. As an operator, I want SDF never to fall back silently from one Credential Mode to the other, so that billing and account usage stay predictable.
4. As an operator, I want to authenticate a subscription once on my machine with `e2b-box auth connect`, so that every sandbox can reuse that login.
5. As an operator, I want to name which plugin connection each agent uses, so that I can keep personal and work accounts apart.
6. As an operator, I want SDF to reuse the plugin's own connection resolution, so that the connection format and refresh rules have a single owner.
7. As an operator, I want a clear failure naming the plugin path when the herdr-e2b plugin is missing, so that I know to install it rather than debug a 401.
8. As an operator, I want a subscription credential that will expire before the sandbox's lifetime ends to be refused up front with the exact reconnect command, so that an Attempt does not die halfway.
9. As an operator, I want to supply provider keys through `SDF_ANTHROPIC_API_KEY` and `SDF_OPENAI_API_KEY`, so that they never collide with the proxy-bound variables in my shell.
10. As an operator, I want SDF to ignore my shell's `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `OPENAI_API_KEY` and `OPENAI_BASE_URL`, so that a host-local proxy configuration never leaks into a sandbox.
11. As an operator, I want an optional `SDF_ANTHROPIC_BASE_URL` / `SDF_OPENAI_BASE_URL` for `api-key` mode, defaulting to the provider's own endpoint, so that I can use a company gateway.
12. As an operator, I want a base URL rejected when it is set for a `subscription` agent, so that a subscription token is never sent to an unexpected host.
13. As an operator, I want loopback, link-local and private-network base URLs rejected with an explanation, so that I do not repeat the unreachable-proxy 401.
14. As an operator, I want SDF to load `.env` at the repo root automatically, with real environment variables taking precedence, so that I do not have to source it before each run.
15. As an operator, I want `.env` git-ignored and a committed `.env.example` listing every variable name without values, so that I can configure SDF without leaking secrets.
16. As an operator, I want exactly one credential per agent kind present in a box, with conflicting provider variables removed, so that the agent cannot pick the wrong one.
17. As an operator, I want no secret to appear in any Herdr pane, command line, log line, Evidence record or error message, so that transcripts are safe to share.
18. As an operator, I want error messages to name the variable or connection at fault and never its value, so that I can fix configuration without exposing secrets.
19. As an Evaluator reader, I want each Runtime Session to record its agent's Credential Mode and connection id, so that a 401-driven FAIL can be traced to the credential used.
20. As an operator, I want Claude Code started in the sandbox to skip onboarding, theme and permission prompts, so that it reaches `idle` without human input.
21. As an operator, I want Claude Code in `api-key` mode to have my key pre-approved inside the box, so that it does not stop on the "use this API key?" prompt.
22. As an operator, I want the Attempt workspace pre-trusted for Claude Code, so that the trust dialog does not block the first prompt.
23. As an operator, I want Codex in `api-key` mode to be seeded in API-key auth mode, so that it does not ask for a ChatGPT login.
24. As an operator, I want Codex in `subscription` mode to use the forwarded session, so that it runs on my ChatGPT plan.
25. As an operator, I want a template `sdf-herdr-agents` with pinned Herdr, Codex and Claude Code versions, so that live runs are reproducible.
26. As an operator, I want the template to contain no credentials, so that it is safe to publish to my E2B team.
27. As an operator, I want the persistent transport to use the E2B Python SDK with bounded command and request timeouts, so that a stalled stream cannot hang an Attempt.
28. As an operator, I want a host-side timeout to also kill the remote sandbox, so that a stuck command does not keep running and billing (upstream e2b #1877).
29. As an operator, I want the sandbox killed when the Attempt owner closes the runtime, including after a failure, so that no sandbox is left behind.
30. As an operator, I want workspace staging and collection to keep working on the new transport, so that Attempt Artifacts are still pulled home.
31. As an operator, I want a live check that a Codex `subscription` Attempt produces a Runtime Session that reaches idle and answers a prompt, so that ticket 08's live boundary can close.
32. As an operator, I want a live check that a Claude Code `api-key` Attempt does the same, so that a second agent kind is proven.
33. As an operator, I want live checks skipped with an explicit reason when their credential is absent, so that the suite stays green without hiding what was not verified.
34. As a developer, I want credential policy testable without network or E2B, and the sandbox wiring tested against a real E2B sandbox, so that the default suite stays provider-free while wiring is proven on the real platform.
35. As a developer, I want the headless `e2b-box run` path left unchanged, so that the already verified live Codex loop does not regress.

## Implementation Decisions

- **Credential resolution module (new, deep).** One entry point takes an agent kind, an environment mapping, a sandbox lifetime and a clock, plus an injectable plugin-material reader, and returns a *credential plan*: the Credential Mode, the connection id (if any), the sandbox env vars to set, the env var names to strip, and the seed steps (by variable name only). It raises a single configuration error type whose messages name variables, never values. All policy lives here: mode required, no fallback, expiry margin, base-URL rules, ignored host variables.
- **Configuration variables.** `SDF_CREDENTIAL_MODE_CLAUDE`, `SDF_CREDENTIAL_MODE_CODEX` (`subscription` | `api-key`, required when that agent is started); `SDF_CONNECTION_CLAUDE`, `SDF_CONNECTION_CODEX` (subscription); `SDF_ANTHROPIC_API_KEY`, `SDF_OPENAI_API_KEY`, optional `SDF_ANTHROPIC_BASE_URL`, `SDF_OPENAI_BASE_URL` (api-key only). Agent kinds map to providers: `claude` → Anthropic, `codex` → OpenAI.
- **Sandbox variables produced.** Claude: `CLAUDE_CODE_OAUTH_TOKEN` (subscription) or `ANTHROPIC_API_KEY` (+ `ANTHROPIC_BASE_URL` if set). Codex: `CODEX_AUTH_JSON` (subscription) or `OPENAI_API_KEY` (+ the base-URL setting Codex 0.155.1 actually honours — verify whether env or config key during implementation). The conflicting set follows the plugin's `CONFLICTING_AUTH`.
- **Base URL rule.** Allowed only in `api-key` mode; must be `https`; host must not resolve to loopback, link-local, private or unspecified addresses (also reject `localhost` literally). No DNS resolution beyond what the stdlib address check needs; literal checks plus hostname checks are enough.
- **Expiry rule.** For `subscription`, the connection's expiry must be later than now + sandbox timeout + 10 minutes, else refuse with `e2b-box auth connect <agent>`.
- **Plugin bridge.** A small Node helper shipped with SDF imports the installed herdr-e2b plugin's connection code, selects the named connection and prints its material as JSON on stdout; SDF reads it through a pipe (stdin DEVNULL) and never writes it to disk. Plugin location is discovered under the Herdr plugins directory; absence is a configuration error. This is the only place SDF depends on plugin internals.
- **`.env` loading.** SDF loads the repo-root `.env` at process start without overriding existing environment variables. Add `.env` to `.gitignore`; commit `.env.example` with names only.
- **Persistent transport on the E2B Python SDK.** `E2BHerdrTransport` keeps its public interface (`run`, `close`, `stage_workspace`, `collect_workspace`, `sandbox_id`) but creates the sandbox with `Sandbox.create(template, envs=…, timeout=…)` and runs commands with explicit command and request timeouts; on host timeout it kills the sandbox. The CLI runner seam is replaced by an injectable sandbox factory so tests use a fake. Adds the `e2b` Python dependency.
- **Credential plans per agent.** The transport receives the credential plans for the agent kinds the runtime will start; envs are merged at creation, conflicting names stripped, and seed steps run once after creation, before any agent starts.
- **Seeding.** Template carries shared Claude first-run state (onboarding completed, theme, skip dangerous-mode prompt). Runtime seed writes: Claude key tail into `customApiKeyResponses.approved` (api-key), Attempt workspace trust; Codex `~/.codex/auth.json` from `$CODEX_AUTH_JSON` or in API-key mode from `$OPENAI_API_KEY`. Seed commands reference variable names only.
- **HerdrRuntime.** `start` launches Claude with `--dangerously-skip-permissions` (disposable sandbox only). Runtime Session gains credential metadata (mode, connection id) — extend `RuntimeSession` with an optional, secret-free metadata field and persist it wherever Runtime Sessions are stored.
- **Template `sdf-herdr-agents`.** Based on the existing herdr-codex image: Herdr 0.9.1, Codex 0.155.1, pinned `@anthropic-ai/claude-code`, Claude shared first-run state. `sdf-herdr-codex` is deprecated but kept until live checks pass on the new template.
- **Headless path unchanged.** `HerdrE2BNativeAdapter` / `e2b-box run` keep the plugin's own credential selection; document this.

## Testing Decisions

- Good tests assert externally visible behaviour: the credential plan produced, the envs a fake sandbox was created with, the commands a fake sandbox received, errors raised — never private helpers or call order beyond what the contract requires.
- **Seam 1 — credential resolution module**, tested with plain mappings, a fixed clock and a fake plugin-material reader: every mode × agent combination, missing mode, missing key, missing connection, expired/near-expiry connection, base URL in subscription mode, loopback/private/http base URLs, shell `ANTHROPIC_*`/`OPENAI_*` ignored, no secret substring in any error message.
- **Seam 2 — `HerdrRuntime` over `E2BHerdrTransport` against a real E2B sandbox** (operator decision: no fake sandbox at this seam). Gated by `SDF_LIVE_E2B=1` and `E2B_API_KEY`, skipped with an explicit reason otherwise, always kills its sandbox in teardown. Asserts from inside the box without printing values: expected credential variable names present and conflicting names absent (compare names or hashes, never echo values); seed files exist with the right shape (`~/.codex/auth.json` auth mode, Claude key approval and workspace trust) and contain no secret in any Herdr pane scrollback; Claude started with the skip-permissions flag reaches idle; Runtime Session carries mode and connection id; a deliberately short host timeout kills the sandbox (sandbox no longer listed); close kills; staging/collection round-trip works. These checks can run with dummy credential values where only wiring is asserted, so they need no model-provider key.
- Plugin bridge: one test with a fake plugin directory containing a stub connection module; one live-gated check against the real plugin.
- `.env` loader: precedence test with a temp directory.
- End-to-end live Attempts (real provider calls), gated like the existing ones (`SDF_LIVE_E2B=1` plus the needed credentials), skipping with an explicit reason: Codex `subscription` and Claude `api-key`.
- Prior art: `tests/test_e2b_herdr_transport.py` (fake runner transport), `tests/test_herdr_e2b_execution.py` (live gating and fixture Attempt), `tests/test_e2b_cli_stdin.py` (runner contract).

## Out of Scope

- A host-side credential proxy or gateway (rejected in ADR-0007).
- Changing credential selection on the headless `e2b-box run` path.
- Refreshing subscription tokens inside the sandbox.
- SDF-owned credential storage; `subscription` always goes through the plugin.
- Live runs of Claude `subscription` and Codex `api-key` (supported and unit-tested, not required live).
- Second-agent live routing (ticket sdf-runtime/09) beyond making it possible.

## Further Notes

- Operator prerequisites for live runs: `SDF_ANTHROPIC_API_KEY` with a real Anthropic key; `codex-personal` connection expires 2026-10-03 — reconnect with `e2b-box auth connect codex` if running later.
- Follow the stdin rule for every subprocess (DEVNULL).
- Closing this spec should let `.scratch/sdf-runtime/issues/08-events.md` close its live boundary.
