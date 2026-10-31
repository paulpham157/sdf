# ADR 0034: agent kinds use only an OpenAI-compatible or Claude Messages API

Status: proposed (2026-10-31). Written to record the product owner's
LLM-transport contract for this change. It becomes accepted when a maintainer
accepts the pull request. It amends the Vultr-as-default wording in
[ADR 0011](0011-opencode-agent-kind-and-per-agent-provider-config.md),
[ADR 0012](0012-pi-agent-kind.md),
[ADR 0013](0013-openai-compatible-headless-agent-kinds.md),
[ADR 0023](0023-run-observability.md) and
[ADR 0024](0024-otel-tracing.md). Credential Mode, the Tool Proxy, sandbox
credential scope, the Attempt lifecycle and Evidence provenance are unchanged
(ADR 0007, ADR 0026, ADR 0002).

## Context

Every coding-agent kind already speaks one HTTP API:

| Kind | Transport | What the host must implement |
| --- | --- | --- |
| `claude` | `anthropic` | Claude / Anthropic Messages API |
| `codex` | `openai` | OpenAI **Responses** API (`POST /v1/responses`), not Chat Completions alone. Codex reads `openai_base_url` from `~/.codex/config.toml` ([research](../research/codex-base-url.md)). |
| `opencode`, `pi`, `qwen`, `goose` | `openai` | An OpenAI-compatible HTTP API (Chat Completions / the agent's documented OpenAI-compatible surface) |

Vultr Serverless Inference was the example default (base URL
`https://api.vultrinference.com/v1`, model `glm-5.3`, and for OpenCode the
local provider id `vultr/glm-5.3`). It is an OpenAI-compatible host. Two
places treated it as if it were the protocol: E2B egress was locked to
`api.vultrinference.com` no matter which base URL was configured, and the
coordinator wrote `attempts.provider = "vultr"` for every Attempt. The `/run`
path also filled a Vultr base URL and model only when the key variable was
named `VULTR_INFERENCE_API_KEY`.

## Decision

1. **Two transports only.** Each agent kind maps to exactly one family:
   `openai` or `anthropic` (`sdf_core.credentials.protocol_of`). A new kind
   has to pick one. There is no third family and no Vultr protocol.
2. **Codex's OpenAI transport is the Responses API.** A Chat Completions-only
   host is not enough for `codex`. The other OpenAI-family kinds keep the
   OpenAI-compatible surface they already use (`@ai-sdk/openai-compatible`,
   Pi `openai-completions`, Qwen `--auth-type openai`, Goose engine `openai`).
3. **A host is an endpoint, not a protocol.** Base URL, key variable name and
   model stay the ADR 0007 / ADR 0011 knobs: `SDF_*_BASE_URL` (or
   `SDF_OPENAI_BASE_URL` / `SDF_ANTHROPIC_BASE_URL`), `SDF_*_API_KEY` or
   `SDF_*_API_KEY_ENV`, and `SDF_*_MODEL`. Any `*_API_KEY` name that passes
   the existing name rules works. `VULTR_INFERENCE_API_KEY` is only an
   example name, used by the `/run` and coordinator DX paths when no `SDF_*`
   key is set. It does not select an API.
4. **Example defaults stay.** Unset OpenAI-family base URLs on the `/run`
   path, and the headless kinds' `default_base_url`, remain the example
   OpenAI-compatible endpoint (`https://api.vultrinference.com/v1`, model
   `glm-5.3`; OpenCode `vultr/glm-5.3` and `vultr/glm-5.3-flash`). The
   `vultr/` prefix is OpenCode's seeded provider id, not an API family.
   Removing those defaults would fail closed; this ADR does not.
   `claude` with no `SDF_ANTHROPIC_BASE_URL` uses `https://api.anthropic.com`
   and Claude Code's own model. It is not pointed at the example host just
   because the key variable is `VULTR_INFERENCE_API_KEY`.
   Codex with no base URL *outside* `/run` stays the official OpenAI API
   (`https://api.openai.com/v1`). `/run` fills the example endpoint when
   `SDF_OPENAI_BASE_URL` is unset, for every key name.
5. **Egress follows the resolved host.** An E2B sandbox allows the hostname
   of each kind's resolved base URL (explicit, else example, else official)
   and denies everything else. `SDF_RUN_EGRESS_ALLOW` still adds hosts.
   Subscription modes that need further vendor hosts still list them there
   (ADR 0023). Daytona's unrestricted-egress opt-in is unchanged.
6. **Attempt provenance names the host and the family.** The coordinator
   writes `attempts.provider` as `{protocol}:{host}` (for example
   `openai:llm.example.com`), taken from the sandbox's configured base URL.
   It does not write `vultr`. `/run` keeps the sandbox provider as a prefix
   (ADR 0027 adapter identity) and appends the same label:
   `e2b:openai:api.vultrinference.com`. The string `vultr` alone is not a
   provider value.
7. **Example model price is not a host invoice.** `glm-5.3` keeps the
   example per-million price `(0.75, 3.00)` in the usage ledger and in Pi's
   seeded cost, for that model id on any host. Any other model is `0` in
   Pi's seed and is priced on the ledger only via `SDF_PRICE_<MODEL>`.

Credential Mode (`subscription` | `api-key`), one credential per sandbox,
seeds that store key *names*, the Tool Proxy, Attempt lifecycle and Evidence
provenance are not given a new abstraction.

## Migration

- To call official OpenAI from `/run` Codex, set
  `SDF_OPENAI_BASE_URL=https://api.openai.com/v1` and `SDF_CODEX_MODEL` to a
  model that host serves. The host must implement Responses.
- To keep Claude on a Claude-compatible proxy, including Vultr's
  Anthropic-compatible endpoint, set `SDF_ANTHROPIC_BASE_URL` (Vultr's is
  `https://api.vultrinference.com`, no `/v1`) and `SDF_CLAUDE_MODEL`. Unset
  now means `https://api.anthropic.com`.
- If both `SDF_<AGENT>_API_KEY` and `VULTR_INFERENCE_API_KEY` are set and
  `SDF_<AGENT>_API_KEY_ENV` is unset, the `SDF_*` key is used. Name the
  example variable explicitly to keep the old coordinator fallback.
- Live gates `SDF_LIVE_VULTR_AGENTS` and `SDF_E2B_VULTR_TEMPLATE` are
  unchanged. They exercise the example endpoint; they are not a protocol.

## Consequences

- A non-Vultr OpenAI-compatible or Claude-compatible host is configured with
  the same knobs as the example endpoint. Egress and `attempts.provider`
  follow that host.
- Codex on a Responses host other than `api.openai.com` still disables the
  hosted `web_search` tool, because those hosts reject it. Official
  `api.openai.com` keeps the tool.
- No new client SDK is introduced. Existing OpenAI-compatible and Anthropic
  Messages clients stay pointed at whatever base URL the operator set.
