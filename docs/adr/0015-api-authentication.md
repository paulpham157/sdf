# SDF Core API authentication: hashed bearer tokens, four roles, separation of duties

Status: accepted

Until now the FastAPI API had no authentication (ADR 0014 said "run it on
127.0.0.1 only"), and plan approvers were whatever `decided_by` string the
client sent. This ADR makes the API fail closed and takes every identity
from the credential.

## Mechanism

- **Bearer API tokens.** `Authorization: Bearer sdf_<16 hex id>.<43 urlsafe secret>`.
  The id is the `api_tokens` primary key. The secret has 256 random bits
  (`secrets.token_urlsafe(32)`) and is stored only as
  `HMAC-SHA256(SDF_AUTH_PEPPER, secret)`. The plaintext is returned once by
  `python -m sdf_core.auth_cli create`, printed to stdout, and never stored or
  logged.
- **Why HMAC with a pepper and not argon2.** A slow KDF protects
  low-entropy, human-chosen passwords. These secrets are 256-bit random
  values, so offline guessing is infeasible even with a fast hash. A keyed
  hash also means a stolen database dump without the pepper (kept in the
  environment, not the DB) cannot even be checked offline. A per-request
  argon2 would be a CPU denial-of-service lever on every API call, and
  argon2 is not a project dependency. Comparison uses `hmac.compare_digest`.
  Unknown ids and malformed tokens still compute an HMAC against a dummy
  value, so timing doesn't reveal which tokens exist.
- **Token lifecycle.**
  - Every token has an expiry: default 90 days, maximum 365. There are no
    tokens without one.
  - `last_used_at` is updated at most once per minute.
  - Revocation (`revoked_at` / `revoked_by`) is final.
  - Tokens are never deleted.
  - Principal, kind, roles, hash and expiry are immutable. This is enforced
    by an ORM `before_flush` guard and, on PostgreSQL, by the
    `trg_api_tokens_guard` trigger (migration `0015_api_auth`).
- **Fail closed.**
  - An app-level dependency (`FastAPI(dependencies=[Depends(authorize)])`)
    runs for every route, including any route added later.
  - Only `GET /healthz` is public. `/docs` and `/redoc` are disabled, and
    `/openapi.json` needs a token.
  - Without a valid `SDF_AUTH_PEPPER` (at least 32 characters), the app
    refuses to start (lifespan `RuntimeError`). If it is started anyway (for
    example, without a lifespan), every request gets 503.
  - A mutating route missing from `auth.ROUTE_ROLES` needs `admin`. A test
    asserts that every mutating route is classified explicitly.
- **Dev mode.** `SDF_AUTH_DEV_INSECURE=1` serves requests that have **no**
  Authorization header as `local-dev` (admin), but only when both the peer
  and the server socket are loopback and no `X-Forwarded-For`, `Forwarded`
  or `X-Real-IP` header is present. Any other request gets 403. Tokens still
  work in dev mode.
- **Lockout.** After `SDF_AUTH_MAX_FAILURES` (default 10) failed
  authentications from one client within `SDF_AUTH_FAILURE_WINDOW_S` (300 s),
  that client gets 429 with `Retry-After` for `SDF_AUTH_LOCKOUT_S` (300 s).
  The counter is in memory, per process.

## Roles

| Role | May |
| --- | --- |
| `viewer` | every `GET` |
| `operator` | viewer + create business contexts/objectives/assumptions/constraints/requirements/tasks, run tasks, record tool actions, metrics and observations, propose plans, start and cancel agent runs |
| `approver` | viewer + approve/reject plans, resolve decision requests, record decisions and ADRs |
| `admin` | everything |

Operator and approver are deliberately disjoint. A solo user holds both
(`--role operator --role approver`) or `admin`.

## Identity reaches the Decision Graph only from the credential

- **Plans.** `POST /plans` overwrites `proposed_by` with the principal.
  `PlanDecisionRequest` has no `decided_by` field, and
  `ResolveDecisionRequest` has no `resolved_by` field. Such keys sent by a
  client are ignored, and a test sends spoofed values to check this.
- **The decision node** records:
  - `owner` = the principal;
  - metadata `principal` = `{name, kind, token_id, method, roles}`;
  - `decided_by`, `proposed_by` and `self_approval`.
- **Runs.** `agent_runs.started_by` and `cancel_requested_by` come from the
  principal.
- **Domain fields, not identity.** `NodeRequest.owner` (the business owner of
  a node) and `ToolActionRequest.actor` remain domain data, not identity. The
  audit row records who actually sent them.
- **Approving needs all three:** the approver role, a `human` token, and a
  name that is not reserved. Reserved names include `sdf`, `host`, `lead`,
  `manager`, `worker`, `worker-N`, `agent*`, `policy*`, `sandbox*`, `sbx*`,
  `e2b*`, `system`, `root`, `dev`, `anonymous`, and every agent kind name
  alone or as a prefix (`pi`, `qwen`, `opencode:lead-1`, `pi:worker-1`, ...;
  derived from `credentials.AGENT_KINDS`).
  - Such names are refused when a token is created and checked again on
    every request.
  - `service` tokens cannot hold `approver` or `admin`.
  - Agents still get no tokens. The host relay (ADR 0014) is unchanged, and
    agent decision requests are resolved over HTTP by humans only.

## Separation of duties and single-user mode

`decide_plan` (service layer, so neither HTTP nor in-process callers can
bypass it) raises `SeparationOfDutiesError` (HTTP 403) when the approver is the
proposer (case-insensitive). Rejecting your own plan is allowed.
`SDF_AUTH_SINGLE_USER=1` allows self-approval for a solo deployment; the
decision node then records `self_approval: true` so it stays visible.

## Audit

`auth_audit` is append-only: an ORM guard, plus the PostgreSQL trigger
`trg_auth_audit_append_only`. It records:

- `token_created` and `token_revoked`;
- `auth_failed` (401) and `denied` (403, 429);
- one `mutation` row per authenticated non-GET request, with the principal,
  token id, route template, status code, client address and path parameters.

Request bodies (which could hold secrets) are not logged.

## Not done / future work

- **OIDC/JWT.** Not implemented. The natural hook is a second branch in
  `api._authenticate` that validates a JWT (issuer, audience, JWKS) and maps
  a claim to a `Principal`. The role checks, SoD and audit would then work
  unchanged.
- **Lockout state** is per process. Behind several workers, put rate
  limiting in the reverse proxy as well.
- **Malformed JSON bodies** can get a 422 before the auth dependency runs.
  Unknown methods get a 405. Neither reveals data.
- **Token rotation** is create-new-then-revoke. There is no token admin over
  HTTP; the CLI is the only path.
