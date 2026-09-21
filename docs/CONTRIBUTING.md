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

## E2B and Herdr

Copy `.env.example` to `.env`, set the E2B credentials in your environment, and
use the wrapper so the local `.env` is loaded:

```bash
cp .env.example .env
scripts/e2b-env.sh e2b-box doctor
```

`E2B_DOMAIN` is a bare cluster domain such as `e2b.dev` or `e2b.app`; the SDK
adds the `api.` prefix. Do not set it to `api.e2b.app`.

Build the pinned image with:

```bash
e2b template create sdf-herdr-codex \
  --path infra/e2b/herdr-codex \
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

## Verification boundaries

- `tests/` and fake transports prove local contracts only.
- A template build proves that the image can start and pass its readiness command.
- A live E2B smoke test must record sandbox creation, command correlation,
  artifact handling, and cleanup; always kill the sandbox in a `finally` path.
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
