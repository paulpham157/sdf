# Codex base URL and API-key auth (ticket #12)

Question: does the Codex CLI pinned in `sdf-herdr-agents` take its OpenAI base
URL from an environment variable or from a config key, and what does an
API-key `~/.codex/auth.json` look like?

Verified against **Codex 0.157.0** (the version pinned in
`infra/e2b/herdr-agents/Dockerfile`), both in the source at tag `rust-v0.157.0`
and inside a live `sdf-herdr-agents` sandbox. The source at tag `rust-v0.155.1`
(the version the issue text names) was also checked, and the relevant code is
the same.

## Finding: config key `openai_base_url`, not `OPENAI_BASE_URL`

- `codex-rs/config/src/config_toml.rs` has a top-level
  `openai_base_url: Option<String>`, documented as the "Base URL override for
  the built-in `openai` model provider".
- `codex-rs/core/src/config/mod.rs` (0.157.0 around line 3772, 0.155.1 around
  line 3738) passes `cfg.openai_base_url` (with empty strings filtered out) to
  `built_in_model_providers(openai_base_url)`.
- `codex-rs/model-provider-info/src/lib.rs` `create_openai_provider(base_url)`
  (0.157.0 line 514, 0.155.1 line 443) uses that value as the provider's
  `base_url`, with `env_key: None`.
- The only use of `OPENAI_BASE_URL` in the Rust tree is
  `codex-rs/network-proxy/src/credential_broker/providers/openai.rs`. The
  network-proxy credential broker uses it as a *context* variable for
  host-binding checks, and nothing routes model requests through it.
- The TypeScript SDK (`sdk/typescript/src/exec.ts:109`) maps its `baseUrl`
  option to `--config openai_base_url=...`, which confirms the config route.

Empirical check in a live sandbox: a dummy API-key `auth.json` and two
local HTTP listeners.
- With `OPENAI_BASE_URL=http://127.0.0.1:9101/v1` set in the environment and no
  config key, `codex exec` sent **0** requests to 9101: it went to the default
  host.
- With `openai_base_url = "http://127.0.0.1:9102/v1"` in `~/.codex/config.toml`,
  `codex exec` sent `POST /v1/responses` to 9102.

**Plan:** in api-key mode, when `SDF_OPENAI_BASE_URL` is set, the Credential Plan
still sets the sandbox variable `OPENAI_BASE_URL`. That keeps it name-addressable
and lets the ticket #9 validation apply unchanged. The plan also adds a seed
step, `codex-config-base-url`, which writes `openai_base_url` into
`~/.codex/config.toml` from that variable inside the box. The host sends only
the variable name. The seed merges into any existing config and replaces an
earlier top-level `openai_base_url`.

## API-key `auth.json`

- `codex-rs/protocol/src/auth.rs`: `enum AuthMode` has
  `#[serde(rename_all = "lowercase")]`, so the API-key variant serialises as
  `"apikey"`.
- `codex-rs/login/src/auth/storage.rs` `AuthDotJson`: `auth_mode`,
  `OPENAI_API_KEY` (serde rename of `openai_api_key`), `tokens`, `last_refresh`,
  and optional others.
- `codex-rs/login/src/auth/manager.rs` `login_with_api_key` writes
  `{auth_mode: ApiKey, openai_api_key: Some(key), everything else None}`.

The seed `codex-auth-json-api-key` therefore writes
`{"auth_mode":"apikey","OPENAI_API_KEY":<value of $OPENAI_API_KEY>}` with mode
0600. With it present, the TUI starts at its prompt with no login screen: see
`tests/test_codex_api_key_live.py`.
