# ADR 0047: a same-origin console that calls only the public API

Status: proposed

Numbered 0047 because 0043 is the evidence content MAC and 0044-0046 are
the earlier human-interaction steps. Fourth step of the workstream. Depends
on [ADR 0044](0044-human-read-model-and-approver-inbox.md),
[ADR 0045](0045-human-actions.md), and
[ADR 0046](0046-live-updates.md). The console is a thin client. It does not
read the database, and it does not grow a private API.

## Context

SDF Core is an HTTP API with bearer tokens. The browser is not a client yet.

The app disables Swagger and ReDoc and sets `openapi_url` to null, then
registers `GET /openapi.json` as an ordinary route
(`sdf_core/api.py:257-258`, `sdf_core/api.py:297`). That route is not in
`PUBLIC_ROUTES`, so an anonymous GET is 401. ADR 0015 says `/openapi.json`
needs a token. `tests/test_api_auth.py` asserts the anonymous call is 401
and a viewer call succeeds (`tests/test_api_auth.py:66-71`).

Tokens are `sdf_<id>.<secret>`. The secret is shown once, stored as an HMAC,
and lives up to 365 days (`sdf_core/auth.py:12-18`, `sdf_core/auth.py:63-64`;
ADR 0015). `create_token` refuses an expiry under 1 day or over 365
(`sdf_core/auth.py:178-179`). Identity on every mutating route is the
principal from that token (`sdf_core/api.py:153-167`). Plan approval,
decision resolve, and the human actions in ADR 0045 all read that principal.
A script that can read the approver token can act as that approver until
the token is revoked or expires.

There is no `CORSMiddleware` in `sdf_core`. There is no HTML, TSX, or Vue
client in the repository. Static files are not mounted. `authorize` is an
app-level dependency (`sdf_core/api.py:257`), so a file mount added later
still passes through it unless the route is listed in `PUBLIC_ROUTES`
(`sdf_core/auth.py:302`).

The reads, actions, and streams a console needs are the public routes in
ADR 0044, ADR 0045, and ADR 0046, plus the gates that already exist:
`POST /plans/{id}/approve`, `POST /plans/{id}/reject`,
`POST /decision-requests/{id}/resolve` (`sdf_core/api.py:1346`,
`sdf_core/api.py:1352`, `sdf_core/api.py:1513`), and `GET /auth/whoami`,
which already returns `can_approve` and `single_user_mode`
(`sdf_core/api.py:291`). v1 of the live updates ADR polls the timeline and
streams one runtime-event stream (ADR 0046).

`approver` does not imply `operator` (`sdf_core/auth.py:48`). A person who
only approves cannot cancel or post a human message unless they also hold
`operator` or `admin` (ADR 0045). `can_approve` is false for a service token
and for a reserved name (`sdf_core/auth.py:95-97`). Every approve, reject,
and resolve already re-checks `can_approve` and separation of duties
(`sdf_core/api.py:165-167`, `sdf_core/plans.py:213-217`).

## Decision

The owner accepted these choices in the review of PR #124.

- v1 uses a one-day session, the shortest expiry `create_token` allows.
- Step-up is `X-SDF-Confirm` plus the fresh `can_approve` check. The person
  does not paste the long-lived token again.
- The API process serves `/console` in v1.
- Logout revokes only the session token.
- `X-SDF-Confirm` is required only when the credential is the session
  cookie. Bearer and CLI approve, reject, and resolve stay as they are
  today. The fresh `can_approve` check and the separation-of-duties check
  stay for every caller, cookie or bearer.

### 1. Where the client lives

The client is a package at `console/` in this repository. It is versioned
with the API. The package version equals the FastAPI `version` on `app`,
which is `0.1.0` today (`sdf_core/api.py:257`). A separate repository would
have to discover the contract from `/openapi.json`, and that document is
auth-gated with docs disabled (`sdf_core/api.py:258`; ADR 0015). Keeping
the client here lets the test import `app.routes` in-process. It does not
need a token, and it does not turn the docs back on.

The package builds to static files. Those files contain no database URL, no
pepper, and no token. The running API serves the built files at `/console`
as GET routes added to `PUBLIC_ROUTES` and nowhere else. A request under
`/console` that is not a GET of a built file falls through to `authorize`
and fails closed. The static handler reads the package directory only. It
does not open `SessionLocal` and it does not call a repository.

Same origin is required. The API does not add `CORSMiddleware`. A browser
on another origin cannot call the API with the console cookie. A later
split origin is a new ADR.

### 2. Browser auth

The API keeps bearer tokens for the CLI, for tests, and for any non-browser
caller (ADR 0015). The console does not store an approver, admin, or
operator token in `localStorage`, `sessionStorage`, IndexedDB, or a cookie
the page script can read.

Login is one exchange. The page takes a bearer token in a password field
and sends it once as `Authorization` on `POST /auth/session`. Any
authenticated principal may call it. The route is listed in `ROUTE_ROLES`
as `viewer`, because every role implies `viewer`
(`sdf_core/auth.py:45-49`) and the session must not be stronger than the
token presented. This is the one mutating route a viewer is allowed to
call, together with `POST /auth/session/logout`.
`test_every_mutating_route_forbids_a_viewer` allowlists exactly those two
paths. Every other POST still returns 403 for a viewer. The handler checks
the bearer with `verify_token` (`sdf_core/auth.py:229-246`). It then calls
`create_token` for the same principal, the same roles, and the same kind,
with `expires_in_days=1`, the shortest expiry `create_token` allows
(`sdf_core/auth.py:178-179`). The plaintext of that new token is set as:

`sdf_session=<token>; HttpOnly; Secure; SameSite=Strict; Path=/; Max-Age=86400`

The response body is `whoami`'s record (`sdf_core/api.py:291`). It does
not contain the token. The long-lived token the person pasted is not
written to a table other than the usual `last_used_at` touch, and it is not
echoed. The session token is a normal `ApiTokenRow`. Revocation, expiry,
and the audit row from `create_token` already apply
(`sdf_core/auth.py:189-191`). `POST /auth/session/logout` revokes that
session token through `revoke_token` (`sdf_core/auth.py:196-207`) and
clears the cookie. Logout does not revoke the long-lived token the person
still holds outside the browser.

On later requests, `_authenticate` treats a missing `Authorization` header
plus an `sdf_session` cookie as the presented bearer
(`sdf_core/api.py:125-133`). The cookie is checked before the
`SDF_AUTH_DEV_INSECURE` branch. That branch runs only when the header and
the cookie are both absent (`sdf_core/api.py:127-133`), so a loopback dev
process cannot turn a session cookie into the `local-dev` admin principal.
A request that has both the header and the cookie uses the header and
ignores the cookie, so a CLI caller is unchanged. Cookie authentication
of a non-safe method also requires the header `X-SDF-Request: 1`. A missing
header is 403 and no domain write. Browsers do not add that header to a
cross-site form POST. Combined with `SameSite=Strict` and no CORS, a form
on another site does not send the cookie and cannot set the header.

`Secure` means a production deployment is HTTPS. A loopback development
process may set the cookie without `Secure` only when `dev_insecure()` is
already true and the request is the loopback check ADR 0015 already
requires (`sdf_core/api.py:127-133`). That exception is not the default.

### 3. Step-up for approve, reject, and resolve

`POST /plans/{id}/approve`, `POST /plans/{id}/reject`, and
`POST /decision-requests/{id}/resolve` re-check `can_approve` and the
separation-of-duties rule on every call, whether the credential is a bearer
token or the session cookie. They already do (`sdf_core/api.py:165-167`,
`sdf_core/plans.py:213-217`). This ADR does not add a header to the bearer
path. CLI and other bearer callers keep the request bodies they send today.

When the credential that authenticated the call is the session cookie, the
same three routes also require `X-SDF-Confirm`. The value is `approve`,
`reject`, or `resolve`, and it must match the route. A mismatch or a
missing header is 403. The header is a custom header, so the CSRF rule in
section 2 covers it. The handler does not ask the browser to send the
long-lived token again. The session cookie is the credential, and it is at
most one day old.

If the request carries `Authorization`, that header is the credential, the
cookie is ignored, and `X-SDF-Confirm` is not required.

The console hides approve and resolve when `whoami.can_approve` is false.
It hides cancel and the message box when the principal lacks `operator` and
lacks `admin` (ADR 0045). Hiding a control is not the authorization. The
API still returns 403.

### 4. Screens

The first client has only these screens:

- Inbox: `GET /inbox` (ADR 0044).
- Run and Attempt: `GET /runs/{id}`, `GET /attempts/{id}`,
  `GET /runs/{id}/timeline` polled, and the runtime-event stream from
  ADR 0046. v1 does not open a timeline stream.
- Approve, reject, and resolve: the three existing posts. The page sends
  `X-SDF-Confirm` because it authenticates with the cookie.
- Cancel: `POST /attempts/{id}/cancel` (ADR 0045).
- Message: `POST /runs/{id}/messages` (ADR 0045), including an
  `Idempotency-Key` the page generates for that submit.

The client does not build plans, does not start runs, does not record
outcome Evidence, does not administer tokens beyond logout, and does not
draw the scorecard. It does not call a URL that `app.routes` does not
register. It does not import `sdf_core.db` or open a database driver.

A stream uses the cookie on the same origin. The browser's `EventSource`
cannot set `Authorization`. That is why the cookie session exists. On
`event: goodbye` or a dropped connection the page calls the matching GET
and replaces what it showed (ADR 0046: polling is the source of truth).

### 5. Contract test

`tests/test_console_contract.py` parses the path strings in `console/` and
fails if any path is missing from `app.routes`, if the package version
differs from `app.version`, or if a source file mentions `localStorage` or
`sessionStorage`. It does not fetch `/openapi.json`. Docs stay disabled.

## Invariants

- The console process, if it is just the static files, has no database
  credentials. The API process does not gain a route that queries rows for
  the console outside `authorize`.
- An approver token is not written to `localStorage`, `sessionStorage`, or
  a script-readable cookie.
- The session cookie is `HttpOnly`, `SameSite=Strict`, and, except the
  loopback dev exception, `Secure`. Its token expires in one day and is a
  normal revocable `ApiTokenRow`.
- Cookie-authenticated mutations require `X-SDF-Request: 1`.
- `X-SDF-Confirm` is required only when the session cookie is the
  credential for approve, reject, or resolve. A bearer request to those
  routes does not need the header. Every caller, cookie or bearer, still
  passes the fresh `can_approve` check and the separation-of-duties check.
- No CORS middleware is added. The console and the API share an origin.
  The API process serves `/console`.
- The static GET routes under `/console` serve built files only. They are
  the only new public routes.
- The client calls only routes registered on `app`.
- Logout revokes the session token and leaves the long-lived token in place.

## Proof

Tests to add. No browser is required for the API tests. A browser pass is
part of implementing ADR 0047, after acceptance, and is not part of this
document's merge.

- `tests/test_console_contract.py::test_console_paths_are_registered_routes`
- `tests/test_console_contract.py::test_console_version_matches_app_version`
- `tests/test_console_contract.py::test_console_source_does_not_touch_web_storage`
- `tests/test_console_auth.py::test_session_cookie_is_httponly_and_hides_the_token`
  Posts a real bearer to `/auth/session`. The JSON body has no token. The
  `Set-Cookie` header has `HttpOnly`, `SameSite=Strict`, and `Secure`
  (or omits `Secure` only in the loopback dev case). `Max-Age` is 86400.
  A following GET with the cookie and without `Authorization` returns
  `whoami`. With `SDF_AUTH_DEV_INSECURE=1` on loopback, that GET still
  returns the session principal, not `local-dev`.
- `tests/test_console_auth.py::test_cookie_mutation_requires_csrf_header`
  A POST with the cookie and without `X-SDF-Request` is 403 and inserts no
  domain row beyond the denial audit ADR 0015 already writes.
- `tests/test_console_auth.py::test_cookie_approve_requires_confirm_header`
  A cookie-authenticated approve without `X-SDF-Confirm`, or with a
  mismatched value, is 403. The same call with `X-SDF-Confirm: approve`
  from a different human approver succeeds through `decide_plan`. A
  proposer approving their own plan is still 403. A service session is
  still 403.
- `tests/test_console_auth.py::test_bearer_approve_does_not_require_confirm_header`
  An `Authorization` bearer approve, reject, and resolve succeed without
  `X-SDF-Confirm`, including when an `sdf_session` cookie is also present.
  The bearer path still enforces `can_approve` and separation of duties.
- `tests/test_console_auth.py::test_logout_revokes_the_session_token_only`
  After logout the cookie token is revoked and the original bearer still
  calls `whoami`.
- `tests/test_console_auth.py::test_static_console_routes_do_not_read_the_database`
  GET `/console/` and a file under it do not require a token. A path that
  is not a built file is 401. The handler does not query `tasks` or `plans`.
- `tests/test_api_auth.py::test_route_table_is_classified` includes
  `POST /auth/session` and `POST /auth/session/logout`.
  `test_every_mutating_route_forbids_a_viewer` allowlists only those two
  paths. A viewer session that calls `POST /plans/{id}/approve` is still 403.

Metrics. `auth_session` log records on the existing `sdf` logger:
`created`, `rejected`, `logout`. The record has the principal and not the
token. ADR 0044 states the OTEL-attribute rule.

## Alternatives rejected

- **The long-lived bearer in `localStorage`.** Any script on the page can
  read it. The secret is valid for up to a year (ADR 0015). HttpOnly keeps
  the page script from reading the session secret.
- **A short-lived bearer held only in a JavaScript variable.** A refresh
  drops it, and an XSS can still read the variable. It also cannot ride on
  `EventSource`, which has no header API. The cookie is what makes ADR 0046
  usable from the browser without putting the token in script.
- **Requiring `X-SDF-Confirm` on bearer calls.** That would change the CLI
  contract for approve, reject, and resolve. The header exists to stop a
  browser form that is holding the cookie. A bearer caller already presented
  the secret on that request.
- **A new session table.** `create_token` already stores an HMAC, an expiry,
  roles, a kind, and a revocation column (`sdf_core/auth.py:158-194`). A
  one-day token is the shortest value that function accepts. A parallel
  session store would duplicate revocation.
- **A session shorter than one day in v1.** `create_token` refuses
  `expires_in_days` below 1 (`sdf_core/auth.py:178-179`). Changing that
  floor is a later ADR.
- **Credentialed CORS to a console on another origin.** There is no CORS
  middleware today. Allowing one origin to send the cookie means that
  origin is part of the approver's trust boundary. This ADR keeps a single
  origin instead of writing that allowlist.
- **Re-pasting the long-lived token on every approve.** That puts the
  year-long secret back in page memory on the action we most want to
  protect. The confirmation header, only for the cookie, plus `can_approve`
  and separation of duties, is the step-up.
- **A reverse proxy as the only way to serve the files in v1.** The API
  process serves `/console`. A proxy that shares the origin can sit in
  front later. The cookie rules do not depend on which process reads the
  files, but v1's public routes are on this API.
- **Logout that also revokes the long-lived token.** The pasted token
  remains the CLI credential. Logout revokes the session token only.
- **A backend-for-frontend with private routes.** The console would then
  have a second authorization surface. ADR 0015's app-level `authorize`
  exists so new routes fail closed. Private routes would be a hole next to
  that. The console calls the same handlers a CLI calls.
- **Enabling anonymous `/docs` so a separate repo can generate a client.**
  ADR 0015 turned the docs off and required a token for the schema. This
  ADR does not reverse that. The contract test reads `app.routes` in-process.

## Consequences

- A person can approve, resolve, cancel, and send a message without a
  second API. They install nothing but a browser pointed at the API origin.
- The browser holds a one-day token it cannot read. Stealing it requires a
  bug that can make the browser send the cookie, not a call to
  `localStorage`. Logout revokes it. The pasted token remains the person's
  CLI credential until they revoke that one too.
- Bearer approve, reject, and resolve do not grow a new required header.
  Cookie calls to those routes do.
- `Secure` cookies do not stick on plain HTTP, except the loopback dev
  exception. A production console without TLS cannot log in. That matches
  the way bearer tokens should already be sent.
- Serving `/console` from this process makes those GET paths public. The
  files must stay free of secrets. The contract test checks the storage
  APIs. It does not prove the built bundle is free of a token someone
  hardcoded. Review of the package is still required when the code exists.
- `create_token` cannot mint a token shorter than one day
  (`sdf_core/auth.py:178-179`). v1 does not change that floor.
- The console is useless for its screens until ADR 0044, ADR 0045, and
  ADR 0046 are implemented. Shipping the package before those routes exist
  fails `test_console_paths_are_registered_routes`. That failure is the
  point of the sequencing rule.

## Open questions

None. The owner closed the list in the review of PR #124. The choices are
in the Decision above.

## Sequencing

No console code, and no `/auth/session` route, is added until ADR 0044,
ADR 0045, ADR 0046, and this ADR are all accepted. See the sequencing
section in ADR 0044. Accepting this ADR accepts the package location, the
cookie session, the CSRF header, the cookie-only confirmation header, the
screen list, and the decision to leave CORS, anonymous docs, and the
bearer approve contract unchanged. It does not by itself authorize the
implementation while the earlier ADRs are still proposed.
