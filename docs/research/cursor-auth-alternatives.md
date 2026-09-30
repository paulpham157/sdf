# Cursor auth alternatives for disposable E2B (after subscription-forward blocked)

Date: 2026-09-30. Track: `cursor-e2b`. Research only — no production `sdf_core/`
credential wiring, no template publish, no PR unless Lead asks.

## Lead thesis (human pushback)

> “There must be a workable direction — Cursor has a CLI. Otherwise how do people
> use it in CI/CD?”

**Answer:** people use **API key auth**, not forwarded `agent login` Keychain
sessions. Cursor’s own docs put browser login as the **desktop** path and
`CURSOR_API_KEY` as the **automation / CI** path. SDF disposable sandboxes are
CI-shaped (ephemeral Linux, no operator browser, kill after Attempt). The first
spike correctly blocked **subscription forward**; the workable Path B direction
is **CI-parity api-key injection** (H3), not inventing a Keychain export.

| Concern | Status |
| --- | --- |
| Host `agent login` **subscription forward** into the box | Still **blocked** — [PR #33](https://github.com/paulpham157/sdf/pull/33) / [`cursor-subscription-forward.md`](cursor-subscription-forward.md) |
| **CI-style API key** into the box (`CURSOR_API_KEY` at create) | **Primary alternate** — matches official headless/CI; maps to ADR-0007 `api-key` Credential Mode |
| Path B binary / Herdr kind | Unblocked by path spike + template pin ([PR #32](https://github.com/paulpham157/sdf/pull/32), [PR #31](https://github.com/paulpham157/sdf/pull/31)) |

No-cred failure (negative control, already proven):

```text
Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY environment variable.
```

That error string **names both** paths; CI/docs use the second.

---

## 1. What CI/CD actually uses today (primary sources)

Cursor CLI documents **two** auth methods
([Authentication](https://cursor.com/docs/cli/reference/authentication)):

1. **Browser authentication (recommended)** — `agent login` / `agent status` /
   `agent logout`. “Credentials are securely stored locally.” This is the
   interactive desktop path (on this host: macOS Keychain — see #33).
2. **API key authentication** — “For automation, scripts, or CI environments”:
   generate a user API key from [Dashboard → API Keys](https://cursor.com/dashboard/api),
   then `export CURSOR_API_KEY=…` or `agent --api-key …`.

**GitHub Actions / other CI** ([GitHub Actions](https://cursor.com/docs/cli/github-actions)):

- Install CLI (`curl https://cursor.com/install -fsS | bash`).
- Run with env `CURSOR_API_KEY: ${{ secrets.CURSOR_API_KEY }}` and
  `agent -p "…"`.
- “Other CI systems” need shell execution, **environment variables for API key
  configuration**, and internet to Cursor’s API — **no** Keychain, **no**
  `agent login` in the workflow examples.

**Headless / scripts** ([Using Headless CLI](https://cursor.com/docs/cli/headless)):

- Print mode `-p` / `--print` for non-interactive automation.
- Examples set `CURSOR_API_KEY` the same way.

**Enterprise service accounts** ([Service Accounts](https://cursor.com/docs/account/enterprise/service-accounts)):

- Enterprise-only non-human accounts; also authenticate CLI via
  `CURSOR_API_KEY`.
- Explicit: “This is the **recommended** way to run the CLI in CI/CD pipelines,
  cron jobs, and other non-interactive environments where browser login isn't
  possible.”
- Same env var name as user keys; different key provenance and billing pool.

**Implication:** CI does **not** scrape or forward a desktop `agent login`
session. It mints a **dashboard API key** (user or Enterprise service account)
and injects it as `CURSOR_API_KEY`. That is the existence proof the human asked
for.

Invalid-key probe (this spike, throwaway HOME, fake value only): CLI reports
that the key was loaded from `CURSOR_API_KEY` and rejects it — confirming the
env path is live without Keychain.

---

## 2. Map CI-style auth onto SDF Path B (ADR-0007)

Path B = disposable `sdf-herdr-agents` (+ Cursor pin) + `herdr agent start
--kind cursor` under SDF control ([path spike](https://github.com/paulpham157/sdf/pull/32)).

| ADR-0007 piece | Claude `api-key` today | Cursor CI-parity (proposed H3) |
| --- | --- | --- |
| Mode | `SDF_CREDENTIAL_MODE_CLAUDE=api-key` | `SDF_CREDENTIAL_MODE_CURSOR=api-key` (name TBD when wired) |
| Host secret | `SDF_ANTHROPIC_API_KEY` (not shell `ANTHROPIC_API_KEY`) | `SDF_CURSOR_API_KEY` (or similar) — **not** shell ambient key by accident |
| Box env | `ANTHROPIC_API_KEY` | `CURSOR_API_KEY` (documented CLI name) |
| Inject | `Sandbox.create(envs=…)` + seed-by-name | Same create-time inject; Cursor CLI reads env — **no** auth.json seed required for api-key |
| Evidence | mode + connection id null; never values | Same |
| Strip conflicts | Claude conflicting vars | Strip other Cursor auth names if present (`CURSOR_AUTH_TOKEN`, etc. — names only) |

This is **not** subscription mode and must **not** be recorded as
`credential_mode=subscription`. It is explicitly **api-key**, matching Cursor’s
CI story and Claude’s existing SDF path.

**Disposable constraints:** timeout + kill OK; no TTY/browser needed; Linux
x86_64 CLI already proven installable (#32). Template pin (#31) supplies the
binary; H3 only adds credential wiring + live proof — **after** Supervisor
re-authorizes.

---

## 3. Summary table (all paths)

| # | Path | Path B fit | Secrets / ADR-0007 | Disposable constraints | Verdict |
| --- | --- | --- | --- | --- | --- |
| **1** | **CI-style user `CURSOR_API_KEY`** (Dashboard user API key) | **Yes** — same shape as Claude api-key | Mode `api-key`; inject at create; names only in Evidence | No TTY; kill OK; needs outbound to Cursor API | **go (conditional on H3 + key availability/billing)** |
| **1b** | **Enterprise service-account `CURSOR_API_KEY`** | Yes for CLI in box | Same inject; Enterprise plan + admin mint | Same as 1 | **conditional** — better for teams; **not required** for first Path B proof if user key works |
| **2** | Host `agent login` subscription forward | Preferred earlier; **blocked** | Would need plugin connection; Keychain scrape forbidden (plugin ADR 0009) | N/A until export | **no-go** until H1/H2 ([#33](https://github.com/paulpham157/sdf/pull/33)) |
| **3** | Device/browser login **inside** the box | Poor | Operator interactivity; secrets in pane risk | No reliable TTY/browser; box timeout; fails ADR “login once on host” | **no-go** |
| **4** | `AGENT_CLI_CREDENTIAL_STORE=file` / portable `auth.json` | Speculative | File store exists in CLI; host Free login left **no** auth.json; producing one without Keychain scrape or re-login unproven | Would still need seed-by-name + refresh policy | **no-go** for now (no portable session on host) |
| **5** | Path A — E2B `cursor-agents` Self-Hosted Machines | **Wrong proof vehicle** | Enterprise **service-account** key; personal/team-admin/org keys **rejected** for pool workers ([E2B Cursor](https://docs.e2b.dev/agents/cursor)) | Dispatcher + worker sandboxes; Cursor Cloud runs the agent loop | **no-go for Path B proof**; existence proof of Cursor+E2B only |
| **6** | Host-side proxy for Cursor traffic | Conflicts ADR-0007 | ADR-0007 rejects loopback/private base URLs for cloud boxes; secrets still enter some boundary | Cloud sandbox cannot reach host loopback (same class as failed Anthropic proxy) | **no-go** as substitute for inject-at-create |
| **7** | Wait H1 (Cursor subscription export) / H2 (plugin Keychain exception) | Long-term subscription purity | Would restore `subscription` mode | Product/plugin calendar unknown | **conditional wait** — parallel track, not first Path B unlock |
| **8** | Personal My Machines `agent worker` (no `--pool`) | Different architecture | Worker auth token file; not Herdr Attempt pane | Host/long-lived worker, not disposable SDF box | **no-go** for disposable Path B |

---

## 4. Ranked recommendations for human decision

1. **Authorize H3 — CI-parity `api-key` on Path B (top).** Mint a Dashboard
   user API key (or Enterprise service-account key if the team already has
   Enterprise). Inject as `CURSOR_API_KEY` at sandbox create under ADR-0007
   `api-key` mode. Goal: disposable box → `herdr agent start --kind cursor` →
   prompt → model response **or** clear auth failure with
   `credential_mode=api-key` recorded secret-free. Mirror
   [GitHub Actions](https://cursor.com/docs/cli/github-actions) + Claude’s
   existing SDF api-key wiring. **Do not implement until Supervisor re-authorizes.**

2. **Keep H1/H2 as the subscription track (parallel, not blocking live Path B).**
   Subscription forward remains the wrong first bet for disposable sandboxes;
   revisit if Cursor ships a setup-token-like export or plugin ADR changes.

3. **Do not pivot the SDF proof to Path A.** Public `cursor-agents` proves
   Cursor Cloud + E2B workers with Enterprise service accounts; it does **not**
   prove HerdrRuntime / SDF Attempt control ([path spike](https://github.com/paulpham157/sdf/pull/32)).

4. **Reject** in-box browser login, host Keychain scrape, and host-loopback
   proxy for this track.

### Should Supervisor re-authorize H3 as the next experiment?

**Yes — conditional.**

- **Yes**, because subscription-forward is documented blocked, Cursor’s
  **supported** headless/CI path is `CURSOR_API_KEY`, Path B binary+kind are
  ready (or pinning), and ADR-0007 already has an `api-key` mode shape.
- **Conditions:** (a) explicit Supervisor/Lead H3 re-authorization (no silent
  fallback); (b) operator obtains a Dashboard API key and confirms billing/usage
  implications (Free subscription login ≠ “free API forever” — usage is
  product-billed; human confirms plan); (c) Evidence records `api-key`, never
  pretends subscription; (d) no production wiring until that authorization.

Enterprise service-account keys are optional upgrade for team automation; user
API keys are enough to start Path B live proof per CLI auth docs.

---

## 5. Remaining human blockers

| Blocker | Owner |
| --- | --- |
| **H3 re-authorization** (this doc’s ask) | Supervisor / Lead `f0e5bc2b` |
| Dashboard API key mint + billing/usage acceptance | Operator |
| Enterprise service accounts (if chosen over user key) | Enterprise plan + admin |
| SDF credential map for `cursor` (`_AGENTS`, `SDF_*` vars) | Engineering **after** H3 |
| Plugin ownership of Cursor api-key (optional later) vs SDF-only `SDF_CURSOR_API_KEY` | Lead — first live proof can mirror Claude’s SDF-owned api-key path without waiting on herdr-e2b Cursor harness |
| ToS / acceptable automation use | Human review (docs endorse CI; still confirm for disposable cloud sandboxes) |
| H1/H2 subscription purity | Cursor product / herdr-e2b maintainers — parallel |

---

## 6. Explicit non-goals of this note

- No `sdf_core/` Credential Mode implementation in this spike.
- No template republish.
- No claiming “subscription works via api-key.”
- No Keychain values or API key bodies in Evidence.

---

## Sources

- [Cursor CLI Authentication](https://cursor.com/docs/cli/reference/authentication) — browser vs API key
- [Cursor CLI GitHub Actions](https://cursor.com/docs/cli/github-actions) — `CURSOR_API_KEY` secret
- [Cursor Headless CLI](https://cursor.com/docs/cli/headless)
- [Cursor Enterprise Service Accounts](https://cursor.com/docs/account/enterprise/service-accounts) — CI recommendation; same env var
- [Cursor APIs Overview](https://cursor.com/docs/api) — creating API keys
- [E2B Cursor Self-Hosted Machines](https://docs.e2b.dev/agents/cursor) — Path A; service-account only for pools
- SDF ADR-0007; prior research: [`cursor-subscription-forward.md`](cursor-subscription-forward.md) ([PR #33](https://github.com/paulpham157/sdf/pull/33)), path spike ([PR #32](https://github.com/paulpham157/sdf/pull/32)), template pin ([PR #31](https://github.com/paulpham157/sdf/pull/31))
- Host probes: isolated HOME no-cred error string; invalid `CURSOR_API_KEY` rejection message (no real secrets)
