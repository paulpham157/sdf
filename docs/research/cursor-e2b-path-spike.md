# Cursor in disposable E2B for SDF — path spike (Slice 1)

Date: 2026-09-30. Track: `cursor-e2b`. Peer slice: path comparison + install/auth findings + one disposable live probe. No production `sdf_core/` edits; no template publish in this slice.

Human decisions folded in: **Path B is the proof vehicle**; **subscription Credential Mode first**; **api-key deferred** until subscription is attempted or documented blocked; **template publish allowed in a later slice** (not done here).

## Verdict

**Recommend Path B** (install Cursor CLI into `sdf-herdr-agents` / approved fork + Herdr `--kind cursor`) for the SDF disposable-sandbox proof.

Path A (public `cursor-agents` Self-Hosted Machines) is a different architecture (Cursor Cloud pool workers + Enterprise service-account key) and does not prove “agent turn under SDF/Herdr control.” Path C (`e2b-box run` headless) has **no Cursor harness** today.

**Slice 2: conditional GO** — binary + Herdr kind are unblocked; the critical next work is a **subscription-forward / herdr-e2b plugin-gap spike**. Do **not** silently fall back to api-key. Template pin/publish is allowed after (or parallel to) that spike, then a live Herdr subscription proof.

## Path comparison

| Path | What it is | Fits “disposable sandbox + agent turn under SDF control”? | Tradeoffs |
| --- | --- | --- | --- |
| **A. Public E2B `cursor-agents` / `cursor-agents-worker`** | Dispatcher sandbox watches a Cursor Self-Hosted Machines **pool**; per-request worker sandboxes run tool calls while Cursor Cloud runs the agent loop ([docs.e2b.dev/agents/cursor](https://docs.e2b.dev/agents/cursor)). | **No** (unless only possible proof). Control plane is Cursor pool/dispatcher, not HerdrRuntime / SDF Attempt panes. | Requires Cursor **Enterprise** + **service-account** API key; personal/team-admin keys rejected. Separate from `sdf-herdr-agents` and ADR-0007 injection. |
| **B. Custom / `sdf-herdr-agents` + Herdr `--kind cursor`** | Pin `agent`/`cursor-agent` in the SDF Herdr template; create disposable box; inject Credential Mode envs (ADR-0007); `herdr agent start --kind cursor` → prompt → read → kill. | **Yes** — same shape as Claude/Codex today. | Template rebuild/publish (allowed later); SDF `_AGENTS` has no `cursor` yet; **plugin has no Cursor connection**; subscription forward is the open problem. |
| **C. Headless `e2b-box run` if/when supported** | Plugin `e2b-box run -t <template>` path used for Codex/etc. | **Not today.** | `e2b-box auth` / harness catalog: Claude, Codex, Grok, OpenCode, Amp, Droid, Prime, Muse — **no Cursor**. No `auth connect cursor`. |

## Recommended path for SDF proof

**Path B.**

Rationale: overall acceptance is start disposable E2B → start Cursor agent → prompt → model response **or** clear auth failure → terminate, with Credential Mode recorded without secrets. That matches HerdrRuntime + `sdf-herdr-agents`, not Cursor Cloud workers. Host Herdr already lists `--kind cursor`. Live probe shows the Linux x86_64 CLI installs into a disposable `sdf-herdr-agents` box and fails closed without credentials.

Credential order (human): **subscription first** (host already has `agent login` on Free tier). **Api-key later**; do not start Slice 2 with `SDF_CURSOR_API_KEY` / api-key mode. If subscription cannot be forwarded with the current plugin, record an explicit **human/plugin blocker** — never silent fallback to api-key.

## Install / auth findings

### Install on Linux x86_64 E2B

- Official installer: `curl -fsS https://cursor.com/install | bash` (also documented for CI: [cursor.com/docs/cli/github-actions](https://cursor.com/docs/cli/github-actions)).
- Live probe on `sdf-herdr-agents`: detected `linux/x64`, downloaded `https://downloads.cursor.com/lab/<version>/linux/x64/agent-cli-package.tar.gz`, installed under `~/.local/share/cursor-agent/versions/…`, symlinked `~/.local/bin/agent` and `cursor-agent`.
- Version observed in box: `2026.09.28-64d2043`.
- Host (darwin/arm64) CLI: `agent` / `cursor-agent` present; `agent about` reports Subscription Tier **Free**; logged in via `agent login` (email redacted).

### No credential — exact failure

Isolated host `HOME` and live E2B box (no `CURSOR_API_KEY`, no forwarded login):

```text
Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY environment variable.
```

- `agent status` → `Not logged in` (exit 0).
- `agent --print --trust …` → error above (exit 1).

### Subscription (host login) — can it be forwarded?

**Critical unknown for Path B; currently blocked by plugin + storage shape.**

| Fact | Evidence |
| --- | --- |
| Host is logged in (subscription / Free) | `agent status` / `agent about` (PII redacted). |
| `e2b-box auth` has **no Cursor** | Auth help + harness catalog keys: `claude`, `codex`, `grok`, `opencode`, `amp`, `droid`, `prime`, `muse` only. Titles in `harness-auth.js` match that set. |
| No portable `~/.cursor/auth.json` on host | File absent; `cli-config.json` `authInfo` holds display fields only (`authId`, `displayName`, `email`, `userId`) — **not** tokens. |
| Login material is in macOS Keychain | Keychain items named `cursor-access-token` and `cursor-refresh-token` (service labels only; values not read or copied). |
| CLI supports store kinds | `AGENT_CLI_CREDENTIAL_STORE` ∈ `{file, memory, default}`; darwin default uses keychain (SSH+locked keychain error path exists). File-store path logic exists (`~/.…/auth.json` style) but was **not** populated on this host. |
| Free-tier login ≠ copy-into-box | Unlike Codex `CODEX_AUTH_JSON` borrowing, there is **no** herdr-e2b connection format for Cursor. Forwarding would need a new plugin harness (or approved file/env seed) — Slice 2 spike. |

**Forbidden:** inventing or recommending api-key as the default next step. Api-key via `CURSOR_API_KEY` / `--api-key` is documented for non-interactive CI, but SDF order is subscription-first.

### Herdr / SDF gaps (product; not edited this slice)

- Host + in-box Herdr `0.9.1`: `--kind cursor` is a possible value.
- `infra/e2b/herdr-agents/Dockerfile`: Herdr + Codex + Claude only — **no** Cursor binary pinned.
- `sdf_core/credentials.py` `_AGENTS`: `claude`, `codex` only (ADR-0007).

## Live probe result (redacted Evidence)

| Field | Value |
| --- | --- |
| Sandbox id | `intuuexsd4p4tqasbmerq` |
| Template name / id | `sdf-herdr-agents` / `tybptciqi1v4wnrvbhhu` |
| Sequence | create (SDK, interrupted) → reuse via CLI → install/reinstall Cursor CLI → `agent status` / `--print` without credential → kill |
| Install | Success (`linux/x64`, version `2026.09.28-64d2043`) |
| Auth outcome | **Clear auth failure** (no subscription forwarded; no API key). Exact string above. Exit 1 on print. |
| Herdr in box | `herdr 0.9.1`; `--kind cursor` listed in help |
| Kill | `e2b sandbox kill` succeeded; no running sandboxes afterward |
| Credential Mode | N/A (no injection). Mode for subscription proof remains **subscription** when wired. |
| Secrets in Evidence | None: no API keys, no tokens, no auth.json bodies, no emails. |

Not attempted in Slice 1 (intentionally): forwarding host Keychain material; `herdr agent start --kind cursor` interactive turn; template rebuild/publish; api-key injection.

## Human blockers

1. **Plugin gap (primary):** herdr-e2b has no Cursor auth harness / `e2b-box auth connect cursor` / connection material. Subscription forward cannot use the Claude/Codex plugin path as-is.
2. **Subscription storage:** host Free-tier login lives in **macOS Keychain**, not a ready-to-seed JSON env like Codex. Need a safe, ADR-0007-compatible forward design (file store? named connection? explicit export?).
3. **Template pin/publish:** Cursor CLI not in Dockerfile; Lead **allows** Peers to rebuild/publish `sdf-herdr-agents` (or approved fork) in a **later** slice — not done here.
4. **SDF credentials map:** no `cursor` in `_AGENTS` / Credential Mode vars (production change deferred).
5. **Api-key:** available as env/`--api-key` per Cursor docs, but **deferred** by human decision until subscription is attempted or **documented blocked**. Not a silent fallback.
6. **Path A Enterprise/ToS/billing:** only relevant if Path B is abandoned; needs Enterprise + service-account key — out of preferred proof path.
7. **ToS / tier:** Free-tier host login may or may not be licensed for automated disposable sandboxes — human confirmation if subscription forward is designed.

## Slice 2 go / no-go

**Conditional GO for Path B.**

| Item | Status |
| --- | --- |
| Path B as proof vehicle | **GO** |
| Install Cursor into disposable E2B Linux x86_64 | **Proven** |
| Herdr `--kind cursor` available in box | **Proven** (binary detection / full turn not yet) |
| Clear no-cred failure | **Proven** |
| Subscription forward via current plugin | **NO-GO until plugin-gap spike** — explicit blocker |
| Start with api-key Credential Mode | **NO-GO** (human order; deferred) |
| Template pin/publish | **Allowed later** (Lead); not a Slice 1 action |
| Full live Herdr subscription proof | **Blocked on** subscription-forward design + then template pin |

Suggested Slice 2 order:

1. **Plugin-gap / subscription-forward spike** (primary): how to resolve a Cursor **subscription** connection into create-time envs / seed-by-name without secrets in Evidence; or document hard block (Keychain-only / ToS / no export).
2. **Template pin** of `agent`/`cursor-agent` into `sdf-herdr-agents` (or fork) and **publish** (allowed).
3. **Live Herdr proof:** disposable box → `--kind cursor` → prompt → response **or** clear auth failure with Credential Mode=`subscription` recorded secret-free.

If (1) documents that subscription cannot be forwarded, escalate as **human/plugin blocker** and only then consider api-key as a separate, explicit decision — never silent fallback.

## Sources

- `infra/e2b/herdr-agents/Dockerfile`, `README.md`
- `docs/adr/0007-agent-credentials-injected-into-disposable-sandboxes.md`
- `sdf_core/credentials.py` (`_AGENTS`), `sdf_core/herdr_runtime.py` (kinds used today)
- Host `herdr agent start --help`; host `agent --help` / `agent status` / `agent about`
- herdr-e2b plugin `src/harnesses.js`, `src/harness-catalog.generated.js`, `src/harness-auth.js`, `e2b-box auth`
- [E2B Cursor Self-Hosted Machines](https://docs.e2b.dev/agents/cursor)
- [Cursor CLI GitHub Actions / `CURSOR_API_KEY`](https://cursor.com/docs/cli/github-actions)
- Live probe sandbox `intuuexsd4p4tqasbmerq` (killed)
