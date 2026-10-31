# Contributing to SDF Core

SDF keeps the control plane on the host and runs coding agents in an explicitly
selected runtime boundary. A green local test is evidence for a change; it is
not proof that E2B, Herdr, PostgreSQL, or a provider account is configured for a
deployment.

## Start here

Install the supported toolchain and run the deterministic suite:

```bash
uv sync --all-groups
uv run pytest
```

Use PostgreSQL for persistence work. SQLite is reserved for isolated tests and
local demonstrations:

```bash
docker compose -f docker-compose.test.yml up -d postgres
export SDF_DATABASE_URL='postgresql+psycopg://postgres:postgres@localhost:55432/sdf_core'
uv run alembic upgrade head
```

Keep changes small and commit one coherent slice at a time. Do not commit
`.env`, API keys, endpoint tokens, provider credentials, generated graph output,
or live sandbox identifiers.

## Continuous integration

`.github/workflows/ci.yml` runs on every pull request and on pushes to `main`.
It is hermetic: no secrets, no `SDF_LIVE_*` flags, no provider keys.

- `pytest (SQLite, py3.11 / py3.13)`: the full suite, `uv sync --locked
  --all-groups` then `uv run pytest -rs`, on the minimum supported Python and
  the local dev version.
- `pytest (PostgreSQL 16, py3.13)`: a `postgres:16-alpine` service (same
  credentials as `docker-compose.test.yml`), `alembic upgrade head`, then the
  full suite with only `SDF_POSTGRES_TEST_URL` set (`SDF_DATABASE_URL` is for the
  migration step only), so the PostgreSQL integration
  tests run instead of skipping.

`.github/workflows/security.yml` runs on the same triggers plus weekly:

- `CodeQL (python)`: `security-extended` queries; results go to GitHub code
  scanning (the only job with `security-events: write`).
- `pip-audit (uv.lock)`: audits the exact hashed set from `uv export --locked
  --all-groups`; any known vulnerability fails. Fix by upgrading the package
  (`uv lock --upgrade-package <name>`), not by ignoring the advisory.
- `gitleaks (full history)`: pinned, checksum-verified binary, `--redact`.
  `.gitleaksignore` holds fingerprints of verified dummy test fixtures only;
  never paste a value there. Locally: `gitleaks git . --redact`.

Keep `uv.lock` in sync (`uv lock`); CI fails on a stale lock. A green CI run
proves only these hermetic contracts. Live tests are skipped there and remain
a separate, recorded verification step (see below). No lint or type-check
gate exists yet.

## Paseo workspace scripts

Root `paseo.json` only wraps the same long-lived / supervised commands documented
above and under `scripts/`. It is not a second runbook.

```bash
paseo script ls --workspace <workspace-id>
# or: paseo script ls --cwd /path/to/this/worktree
paseo script start api --workspace <workspace-id>
paseo script stop api --workspace <workspace-id>
```

Configured names: `api` (uvicorn on `$PASEO_PORT`), `postgres` (compose up only;
not `scripts/postgres-test.sh`, which also migrates, runs pytest, and tears down
on EXIT), and `e2b-doctor`. Prefer `scripts/e2b-env.sh e2b-box up …` in an
interactive terminal when you need a box attach — it is not wrapped here.

## Live full loop (public run path)

See [live-full-loop.md](live-full-loop.md) for `SDF_RUN_ADAPTER=herdr`, Credential
Mode, template, and the live-gated HTTP proof for issue #46. Default CI remains
`FakeNativeAdapter`. That note also lists what is **not** claimed.

## E2B and Herdr

Copy `.env.example` to `.env`, set the E2B credentials in your environment, and
use the wrapper so the local `.env` is loaded:

```bash
cp .env.example .env
scripts/e2b-env.sh e2b-box doctor
```

`e2b-box` is provided by the herdr-e2b plugin (id `e2b-dev.herdr-e2b`, v0.5.0).
Install it only for subscription Credential Mode or for the `e2b-box` commands
in this section. Api-key mode does not use the plugin.

```bash
herdr plugin install --yes --ref v0.5.0 e2b-dev/herdr-e2b-sandbox
```

The GitHub repository is `e2b-dev/herdr-e2b-sandbox`. Passing the plugin id as
`e2b-dev/herdr-e2b` fails with "Repository not found".

`E2B_DOMAIN` is a bare cluster domain such as `e2b.dev` or `e2b.app`; the SDK
adds the `api.` prefix. Do not set it to `api.e2b.app`.

Build the pinned image with:

```bash
e2b template create sdf-herdr-agents \
  --path infra/e2b/herdr-agents \
  --dockerfile Dockerfile \
  --ready-cmd 'herdr status server --json'
```

The image contains a deployment-owned bridge at port `8787`. Its credential is
`HERDR_ENDPOINT_TOKEN`, a secret chosen by the deployment operator. It is not
the E2B API key and is not a Codex or model-provider key. The bridge also
accepts a mounted secret file configured with `HERDR_ENDPOINT_TOKEN_FILE` and
defaults to `/run/secrets/herdr_endpoint_token`.

The E2B CLI's `sandbox create --env` option applies to the terminal session. It
does not inject an environment variable into the entrypoint that started with
the image. For a real endpoint, use the E2B SDK or deployment/orchestrator
secret mechanism to inject the token before exposing the HTTPS-forwarded port.

## Sandbox provider choice (E2B default, Daytona alternative)

The persistent Herdr transport defaults to E2B. Operators may select Daytona
beside it without removing the E2B path:

```bash
# default
SDF_SANDBOX_PROVIDER=e2b

# alternative
SDF_SANDBOX_PROVIDER=daytona
DAYTONA_API_KEY=…          # required for Daytona
# DAYTONA_API_URL=…        # optional
# DAYTONA_TARGET=us        # optional
SDF_DAYTONA_SNAPSHOT=daytona-small   # snapshot/image name
```

`create_credentialed_transport` reads `SDF_SANDBOX_PROVIDER` (or an explicit
`provider=` argument). E2B still uses `template=` / `SDF_E2B_*` / `E2B_*`.
Daytona uses `snapshot=` (or `template=` as an alias) / `SDF_DAYTONA_SNAPSHOT`
/ `DAYTONA_*`. Agent Credentials are still injected at sandbox create
(ADR-0007) for either provider; missing mode/credential fails closed before
provisioning.

The Claude `--dangerously-skip-permissions` flag is enabled only by the E2B
transport under ADR-0008. Daytona shares Herdr, not that permission-bypass
boundary; its agent start command omits the flag.

**Not claimed:** Daytona containment for public process actions
(`SDF_CONTAINMENT_BACKEND=daytona`) is deferred. Unit tests with fakes do not
prove Daytona production readiness. Live Daytona smoke requires
`SDF_LIVE_DAYTONA=1` and `DAYTONA_API_KEY` (env or `.env` via `load_dotenv` into
a private mapping). A green unit suite without that gate is not live evidence.

## Verification boundaries

- `tests/` and fake transports prove local contracts only.
- A green CI run is the same hermetic evidence on a clean runner (SQLite on
  py3.11/3.13, PostgreSQL 16 on py3.13); it is not live-provider evidence.
- A template build proves that the image can start and pass its readiness command.
- A live E2B smoke test must record sandbox creation, command correlation,
  artifact handling, and cleanup; always kill the sandbox in a `finally` path.
- A live Daytona smoke (`SDF_LIVE_DAYTONA=1` + `DAYTONA_API_KEY`) must record
  the same create/exec/workspace/delete cycle against a real Daytona sandbox;
  always delete the sandbox in a `finally` path. Skipped without the gate.
- A live OpenCode loop (`SDF_LIVE_E2B=1` + `SDF_LIVE_OPENCODE=1` +
  `E2B_API_KEY` + the configured OpenCode key) must record Task → Attempt →
  Evidence, token/cost totals, the egress allowlist and sandbox cleanup.
  Skipped without the gates.
- A live agents loop on the example OpenAI-compatible endpoint
  (`SDF_LIVE_E2B=1` + `SDF_LIVE_VULTR_AGENTS=1` or a subset such as `pi` +
  `E2B_API_KEY` + that kind's allow-listed API key and base URL) must record the same, plus the
  served model (Pi: `served_models == ["glm-5.3"]`). Those gate names are
  historical; the endpoint is whatever `*_BASE_URL` names, not a Vultr protocol
  (ADR 0034). Codex on a non-OpenAI host must be a Responses API. The test uses the prebuilt
  `SDF_E2B_VULTR_TEMPLATE` (default `sdf-herdr-agents`); only an explicit
  `SDF_E2B_VULTR_BASE_TEMPLATE` (e.g. E2B's public `codex`, Node 20, too old
  for Pi 1.x) bootstraps a bare base. Skipped without the gates.
- A remote Herdr milestone requires authenticated SDF → HTTPS bridge → Herdr
  evidence, including start, input, output, cancellation, termination,
  reconnect, and no duplicate execution.

Never report a static test, mocked provider, or template build as proof of a
production deployment. Record the exact live boundary and remaining blocker.

## Pull requests and review

Describe the domain behavior changed, the verification run, and the boundary
that remains unverified. Preserve unrelated worktree changes. Use the canonical
domain vocabulary in `CONTEXT.md` and record architectural changes in
`docs/adr/` when they alter an ownership or trust boundary.
