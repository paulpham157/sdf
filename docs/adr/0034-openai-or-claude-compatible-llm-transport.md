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
(ADR 0007, ADR 0026, ADR 0002). The api-key variable names in ADR 0007 are
amended to the allow-list below; Credential Mode itself is not.

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
3. **A host is an endpoint, not a protocol.** Each kind reads one allow-listed
   triple and no other name for the same values:

   | Kind | Base URL | Key | Model |
   | --- | --- | --- | --- |
   | `claude` | `ANTHROPIC_BASE_URL` | `ANTHROPIC_API_KEY` | `ANTHROPIC_MODEL` |
   | `codex` | `OPENAI_BASE_URL` | `OPENAI_API_KEY` | `OPENAI_MODEL` |
   | `pi` | `PI_BASE_URL` | `PI_API_KEY` | `PI_MODEL` |
   | `qwen` | `QWEN_BASE_URL` | `QWEN_API_KEY` | `QWEN_MODEL` |
   | `opencode` | `OPENCODE_BASE_URL` | `OPENCODE_API_KEY` | `OPENCODE_MODEL` |
   | `goose` | `GOOSE_BASE_URL` | `GOOSE_API_KEY` | `GOOSE_MODEL` |

   `SDF_*_API_KEY`, `SDF_*_BASE_URL`, `SDF_*_MODEL` and `*_API_KEY_ENV` are
   not credentials. `VULTR_INFERENCE_API_KEY` is not read.
4. **Defaults are the official APIs, or none.** `claude` with no
   `ANTHROPIC_BASE_URL` uses `https://api.anthropic.com` and Claude Code's
   own model (`ANTHROPIC_MODEL` optional). Codex with no `OPENAI_BASE_URL`
   uses `https://api.openai.com/v1` (Responses); `OPENAI_MODEL` is optional.
   `opencode`, `pi`, `qwen` and `goose` have no default host: api-key mode
   fails closed unless that kind's `*_BASE_URL` and `*_API_KEY` are set.
   Their model defaults stay a bare `glm-5.3`, except OpenCode, which still
   needs a `<local provider id>/<model>` and defaults to `vultr/glm-5.3`.
   That prefix is the seeded provider id, not a host, and the title model
   uses the same id.
5. **Egress follows the resolved host.** An E2B sandbox allows the hostname
   of each kind's resolved base URL (explicit, else the official API for
   Claude and Codex) and denies everything else. A headless kind with no
   base URL contributes no host. `SDF_RUN_EGRESS_ALLOW` still adds hosts.
   Subscription modes that need further vendor hosts still list them there
   (ADR 0023). Daytona's unrestricted-egress opt-in is unchanged.
6. **Attempt provenance names the host and the family.** The coordinator
   writes `attempts.provider` as `{protocol}:{host}` (for example
   `openai:llm.example.com`), taken from the sandbox's configured base URL.
   It does not write `vultr`. `/run` keeps the sandbox provider as a prefix
   (ADR 0027 adapter identity) and appends the same label:
   `e2b:openai:llm.example.com`. The string `vultr` alone is not a
   provider value.
7. **Example model price is a ledger estimate, not a reported provider cost.**
   `glm-5.3` keeps `(0.75, 3.00)` USD per million tokens in
   `PRICES_PER_MTOK`. `SDF_PRICE_<MODEL>` overrides that. Pi's seeded
   `models.json` prices are `0`, so Pi does not report an SDF-chosen dollar
   amount. `usage.record_turn` then estimates from the price table
   (`cost_source=estimated`, ADR 0027). A cost the agent actually reported
   stays `reported`.
8. **Every sandbox declares `credential_environ`.** The coordinator reads that
   attribute. A sandbox that omits it fails; it does not inherit the example
   host. An empty mapping means no explicit base URL.

Credential Mode (`subscription` | `api-key`), one credential per sandbox,
seeds that store key *names*, the Tool Proxy, Attempt lifecycle and Evidence
provenance are not given a new abstraction.

## Migration

| Old | New |
| --- | --- |
| `SDF_ANTHROPIC_API_KEY`, `SDF_CLAUDE_API_KEY_ENV` | `ANTHROPIC_API_KEY` |
| `SDF_ANTHROPIC_BASE_URL` | `ANTHROPIC_BASE_URL` |
| `SDF_CLAUDE_MODEL` | `ANTHROPIC_MODEL` |
| `SDF_OPENAI_API_KEY`, `SDF_CODEX_API_KEY_ENV` | `OPENAI_API_KEY` |
| `SDF_OPENAI_BASE_URL` | `OPENAI_BASE_URL` |
| `SDF_CODEX_MODEL`, `SDF_HERDR_CODEX_MODEL` | `OPENAI_MODEL` |
| `SDF_PI_API_KEY`, `SDF_PI_API_KEY_ENV` | `PI_API_KEY` |
| `SDF_PI_BASE_URL` | `PI_BASE_URL` |
| `SDF_PI_MODEL` | `PI_MODEL` |
| `SDF_QWEN_API_KEY`, `SDF_QWEN_API_KEY_ENV` | `QWEN_API_KEY` |
| `SDF_QWEN_BASE_URL` | `QWEN_BASE_URL` |
| `SDF_QWEN_MODEL` | `QWEN_MODEL` |
| `SDF_OPENCODE_API_KEY`, `SDF_OPENCODE_API_KEY_ENV` | `OPENCODE_API_KEY` |
| `SDF_OPENCODE_BASE_URL` | `OPENCODE_BASE_URL` |
| `SDF_OPENCODE_MODEL` | `OPENCODE_MODEL` |
| `SDF_OPENCODE_SMALL_MODEL` | removed; the title model is `OPENCODE_MODEL` |
| `SDF_GOOSE_API_KEY`, `SDF_GOOSE_API_KEY_ENV` | `GOOSE_API_KEY` |
| `SDF_GOOSE_BASE_URL` | `GOOSE_BASE_URL` |
| `SDF_GOOSE_MODEL` | `GOOSE_MODEL` |
| `VULTR_INFERENCE_API_KEY` | not read |

- Headless kinds no longer default to `https://api.vultrinference.com/v1`.
  Set `*_BASE_URL` and `*_API_KEY` or credential resolution fails closed.
- Live gates `SDF_LIVE_VULTR_AGENTS` and `SDF_E2B_VULTR_TEMPLATE` keep their
  names. They are not a protocol. Each kind uses its allow-listed key and
  base URL.

## Consequences

- A non-Vultr OpenAI-compatible or Claude-compatible host is configured with
  the same knobs as the example endpoint. Egress and `attempts.provider`
  follow that host.
- Codex on a Responses host other than `api.openai.com` still disables the
  hosted `web_search` tool, because those hosts reject it. Official
  `api.openai.com` keeps the tool.
- No new client SDK is introduced. Existing OpenAI-compatible and Anthropic
  Messages clients stay pointed at whatever base URL the operator set.
