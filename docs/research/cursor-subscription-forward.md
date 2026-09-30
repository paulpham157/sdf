# Cursor subscription forward into disposable E2B (plugin-gap spike)

Date: 2026-09-30. Track: `cursor-e2b` / subscription-forward. Peer: research + optional
redacted live probes only. No production `sdf_core/` edits. No template publish.
No silent api-key fallback.

Human decisions (binding): Path B is the proof vehicle; **subscription first**;
**api-key deferred** until subscription is attempted or documented blocked;
Supervisor must re-authorize before any api-key path; template publish allowed
later (not this spike).

Path comparison is **out of scope here** — Slice 1 accepted Path B. Cite
`cursor-e2b-path-spike` worktree → [`docs/research/cursor-e2b-path-spike.md`](../../../cursor-e2b-path-spike/docs/research/cursor-e2b-path-spike.md)
(peer `f92cbdea`). Parked Peer `a77648bf` and Judge track remain out of scope.

## Verdict

**blocked (human/plugin)** — subscription material on this host is **macOS
Keychain–only**, with **no** portable file and **no** Cursor equivalent of
Claude’s `setup-token` / Codex’s borrowable `auth.json`. The installed
herdr-e2b plugin has **no Cursor harness** (not disabled — absent), and its
ADR 0009 forbids Keychain reads. Current plugin cannot discover or inject a
Cursor subscription connection.

**Api-key is deferred**, not recommended. If Lead wants a proof before any
plugin/human export path exists, that is a **separate explicit decision**, not
this spike’s next step.

**Template-pin slice:** **not blocked** on this gap for binary install into
`sdf-herdr-agents` (may run in parallel). **Live Herdr subscription proof**
remains blocked until an approved export or plugin exception exists.

### Next concrete step for Lead (`f0e5bc2b`)

1. Treat Cursor subscription forward as a **human/plugin blocker**: either
   (H1) Cursor documents/ships a subscription export usable like
   `claude setup-token` / a file session, or (H2) herdr-e2b amends ADR 0009 for
   a Cursor-specific, operator-approved Keychain→connection path, or (H3)
   Supervisor explicitly re-authorizes **api-key** as a separate Credential Mode
   experiment (out of this spike).
2. Do **not** unblock Path B subscription proof on SDF-only wiring of
   `CURSOR_API_KEY`.
3. Template-pin Peer may proceed on binary/`--kind cursor` install; wire
   `sdf_core/credentials.py` only after (H1)/(H2)/(H3).

---

## 1. How Claude/Codex subscription is discovered and injected today

### Plugin (herdr-e2b)

Installed tree: `~/.config/herdr/plugins/github/e2b-dev.herdr-e2b-*`
(this host: `…-d2bf3bfd054c`).

| Stage | Mechanism | Sources |
| --- | --- | --- |
| Catalog | `src/harnesses.js` `HARNESSES` keys: `claude`, `codex`, `grok`, `opencode`, `amp`, `droid`, `prime`, `muse`. **No `cursor`.** | Live `Object.keys(HARNESSES)` |
| Discovery | `e2b-box auth discover` / `src/harness-auth.js` `buildPlan`: spawn probe; keep **file sessions** or **env names**; never open Keychain (plugin ADR 0009). | `harness-auth.js`, ADR 0009 |
| Managed connect | `e2b-box auth connect` supports **claude** (`setup-token`), **codex** (`borrowed-session` / `--oauth`), **muse**/**amp** (plugin OAuth). Explicit error: managed connections are those four only. | `auth-cli.js` |
| Material | `connectionMaterial` → Claude: env `CLAUDE_CODE_OAUTH_TOKEN`; Codex: env `CODEX_AUTH_JSON` (session with refresh placeholder). | `connections.js` |
| Box create | Selected connection injected as create-time envs; seeds write by **variable name** (e.g. Codex `~/.codex/auth.json`). | Plugin fleet-seed; SDF research `e2b-exec-reliability.md` |

Claude parallel (important for Cursor): subscription login lives in Keychain;
plugin **does not** borrow it. Operator runs `claude setup-token` and stores
`CLAUDE_CODE_OAUTH_TOKEN` as a named connection. Codex parallel: session is a
**file** (`~/.codex/auth.json`) → ADR 0010 makes it borrowable.

### SDF (ADR-0007)

| Piece | Behavior |
| --- | --- |
| `sdf_core/credentials.py` | `_AGENTS`: **`claude`**, **`codex` only**. Mode `subscription` requires `SDF_CONNECTION_<AGENT>` + `PluginConnectionBridge`. |
| `CONFLICTING_VARIABLES` | Claude OAuth/API names; Codex `CODEX_AUTH_JSON` / API key names. |
| `subscription_seeds` | Claude: none (token is env). Codex: `CODEX_AUTH_JSON` → seed `codex-auth-json`. |
| `sdf_core/plugin_bridge.py` | Imports plugin `src/connections.js`; returns material; never logs values. |
| `sdf_core/credential_injection.py` | `Sandbox.create(envs=…)`; seeds name variables only; pane env file `~/.config/sdf/agent-env.sh`. |

Headless `e2b-box run` keeps plugin-owned selection; persistent Herdr path uses
SDF Credential Mode (see `docs/research/e2b-exec-reliability.md`).

---

## 2. Cursor subscription state on the host (names / classes only)

Observed: `agent about` → Subscription Tier **Free**; `agent status` → logged in
(email redacted). CLI version host `2026.09.26-dd393fe` (box probe used
`2026.09.28-64d2043` per path spike).

| Class | What exists | Notes |
| --- | --- | --- |
| **Env (auth-bearing, names)** | `CURSOR_API_KEY`, `CURSOR_AUTH_TOKEN`, `CURSOR_AUTH_TOKEN_EXPIRES_AT` appear in CLI bundles; also `CURSOR_API_ENDPOINT` / `CURSOR_API_BASE_URL`. | Host process env for this spike did **not** expose subscription tokens as env vars. `CURSOR_API_KEY` is the documented CI/api-key path — **deferred**. |
| **Config files** | `~/.cursor/cli-config.json` has `authInfo` keys: `email`, `displayName`, `userId`, `authId` — **display/identity only**, not tokens. `agent-cli-state.json` holds non-auth CLI state. | Confirmed path spike + this spike. |
| **Portable auth file** | **`~/.cursor/auth.json` absent.** Other candidate `auth.json` paths under cursor-agent dirs also absent. | No Codex-like borrowable session file on this Free login. |
| **Credential store kinds** | `AGENT_CLI_CREDENTIAL_STORE` ∈ `{file, memory, default}`. Darwin default uses Keychain; SSH+locked keychain surfaces an unlock hint. File-store path logic exists (`…/auth.json` style) but was **empty / unused** on this host. | Path spike + launcher `AGENT_CLI_CREDENTIAL_STORE=file` branch. |
| **macOS Keychain (service names only)** | `cursor-access-token` (acct `cursor-user`), `cursor-refresh-token` (acct `cursor-user`), `Cursor Safe Storage` (acct `Cursor Key`). | **Do not read or copy values into Evidence, docs, or replies.** |
| **App Support** | `~/Library/Application Support/Cursor/` (IDE cookies/storage) — separate from CLI login; not treated as a forwardable Agent Credential surface here. | Names only. |

Contrast for operators: `--api-key` / `CURSOR_API_KEY` authenticates without
`agent login`, but is **out of scope** for this spike except as contrast to
subscription.

---

## 3. `e2b-box auth` / harness catalog gap

| Question | Answer |
| --- | --- |
| Is Cursor in `HARNESSES`? | **No.** Eight rows only (see §1). |
| Disabled / stub Cursor path? | **No.** Research doc `docs/research/0001-local-harness-auth-discovery.md` discusses Cursor in **t3code/orca**, not a shipped disabled harness in this plugin. |
| `e2b-box auth list` / help | Agents: Claude, Codex, Grok, OpenCode, Amp, Droid, Prime, Muse. Connect: claude, codex, muse, amp. |
| `auth connect cursor` | Would fail: managed connections do not include cursor (`auth-cli.js`). |
| Discovery if binary present | Without a table row, Cursor is **not detected** (“a harness that is not in this table is simply not detected” — `harnesses.js`). |

**Exact gap:** no harness row, no session/file/env mapping, no connection
method, no `connectionMaterial` branch, no fleet seed for Cursor. Plugin
extension is required before SDF can resolve `SDF_CONNECTION_CURSOR`.

SDF gap (product, not edited): `_AGENTS` has no `cursor`; no
`SDF_CREDENTIAL_MODE_CURSOR` / `SDF_CONNECTION_CURSOR`. Template
`infra/e2b/herdr-agents` has no Cursor binary yet (path spike / template-pin).

---

## 4. Feasible forward mechanisms (ranked)

### (a) Plugin auth discovery + named connection — **preferred shape, currently impossible without human/plugin change**

**Idea:** Mirror Claude/Codex: `e2b-box auth connect cursor` → connection id →
`connectionMaterial` → create-time envs → optional seed-by-name; SDF
`subscription` mode reads via `PluginConnectionBridge`.

| Sub-path | Feasibility | ToS / risk | Evidence redaction |
| --- | --- | --- | --- |
| Borrow Keychain tokens into a connection | **Blocked by plugin ADR 0009** (Keychain never opened). Same class as Claude Keychain refusal. | Reading/exporting refresh tokens into cloud sandboxes may conflict with Cursor account terms (vendor docs silent in path spike; treat as human review). Refresh dual-use risk like Codex (would need placeholder policy). | Never put Keychain **values** in Evidence; only service names + error strings. |
| First-party long-lived subscription export (Claude `setup-token` analogue) | **Not found** for Free `agent login`. No documented Cursor CLI command that mints a pasteable subscription token for sandboxes. | If Cursor later documents one, ToS follows their doc (like Anthropic setup-token). | Store only in plugin secret files; SDF records connection id. |
| Force `AGENT_CLI_CREDENTIAL_STORE=file`, re-login, borrow `auth.json` | Speculative: file store exists but was empty after normal login. Would need proven writable auth.json shape + multi-use/refresh rules (ADR 0010-style). Still a **plugin change** + operator re-auth. | Same account-portability questions as Codex file copy; OpenAI documents headless copy, Cursor does not (as of this spike). | Point at path in auth.toml; never commit file body. |

**Rank:** Correct architecture under ADR-0007, but **blocked today**.

### (b) SDF-only seed of subscription material by variable name — **insufficient alone**

**Idea:** Skip plugin connections; SDF sets known env names at `Sandbox.create`
and seeds files by name (like extending `_AGENTS`).

| Issue | Detail |
| --- | --- |
| No subscription variable on host | Login does not export `CURSOR_AUTH_TOKEN` / similar into the shell. |
| Getting a value implies Keychain or api-key | Scraping Keychain from SDF would bypass plugin ownership (ADR-0007 wants plugin as format owner) and violates herdr-e2b ADR 0009 spirit. |
| `CURSOR_API_KEY` | Documented non-interactive path — **api-key mode**, deferred; not a subscription forward. |

**Rank:** Useful **after** a connection/export exists; **not** a standalone
subscription solution. Do not invent a fake `CURSOR_API_KEY` “subscription.”

### (c) Other

| Option | Notes |
| --- | --- |
| Path A Cursor Cloud / Enterprise service account | Rejected as Path B proof vehicle (Slice 1). Different control plane. |
| Interactive `agent login` inside each box | Requires browser/device flow in disposable cloud; not ADR-0007 “authenticate once on host.” |
| Human pastes a token into `e2b-box auth connect` once Cursor documents one | Same as Claude setup-token workflow; **await vendor or Lead**. |

---

## 5. Live probe (optional; redacted)

This Peer did **not** open a second E2B box: Slice 1 already proved the
no-forward failure on disposable `sdf-herdr-agents`.

| Field | Value |
| --- | --- |
| Sandbox id | `intuuexsd4p4tqasbmerq` (path spike Peer) |
| Template | `sdf-herdr-agents` |
| Sequence | install Linux Cursor CLI → run without forwarded subscription → kill |
| Auth outcome | `Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY environment variable.` |
| Host corroboration (this spike) | Isolated `HOME`, no key env → same error string on `agent -p …`; `agent about` → “Not logged in” |
| Secrets in Evidence | None |

That establishes the negative control for Path B: Cursor starts (when
installed) and fails closed when nothing is forwarded. Positive subscription
forward remains unproven because no forward mechanism exists under current
plugin policy.

---

## 6. Verdict line (for Lead reply)

| Choice | Selection |
| --- | --- |
| subscription forward possible with current plugin | **No** |
| possible with plugin change only | **No** (plugin change necessary but **not sufficient** without an approved non-Keychain export **or** an explicit Keychain-read ADR exception + ToS sign-off) |
| **blocked (human/plugin)** | **Yes** — Keychain-only subscription, no approved export, no Cursor harness |

**Api-key:** deferred; Supervisor re-authorization required before any api-key
path. Not recommended by this spike.

**Template-pin:** may run **in parallel**; not gated on resolving this blocker
for binary presence. Subscription Credential Mode live proof **is** gated.

---

## Sources

- SDF ADR-0007; `sdf_core/credentials.py`, `plugin_bridge.py`, `credential_injection.py`
- `docs/research/e2b-exec-reliability.md`
- Slice 1: `cursor-e2b-path-spike` → `docs/research/cursor-e2b-path-spike.md` (sandbox `intuuexsd4p4tqasbmerq`)
- herdr-e2b: `src/harnesses.js`, `connections.js`, `auth-cli.js`, `harness-auth.js`; ADRs 0009, 0010, 0013, 0015; research 0001/0002
- Host: `agent about` / `agent status` / `agent --help` / `agent login --help`; Keychain **service names** only; `e2b-box auth --help` / `auth list`
- Cursor CLI install/auth docs cited in path spike (GitHub Actions / `CURSOR_API_KEY` as contrast only)
