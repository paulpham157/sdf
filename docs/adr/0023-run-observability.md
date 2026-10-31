# ADR 0023: the live `/tasks/{id}/run` path and its observability

Status: accepted (branch `core/live-full-loop`, PR #53, pending review)

> **Amended by [ADR 0034](0034-openai-or-claude-compatible-llm-transport.md).**
> `/run` reads the allow-listed key, base URL and model for the selected
> kind. Claude with no base URL uses `api.anthropic.com`. Codex with no base
> URL uses `api.openai.com`. Headless kinds have no default host.
> `VULTR_INFERENCE_API_KEY` is not a credential. E2B egress allows the
> resolved host, plus `SDF_RUN_EGRESS_ALLOW`. `SDF_HERDR_CODEX_MODEL` is not
> read; the model variable is `OPENAI_MODEL`.

## Context

PR #53 let the public `POST /tasks/{id}/run` drive a real agent in a sandbox.
Review found it built a parallel path: its own env allowlist (which forwarded
`SDF_DOTENV`), Codex-only, E2B-only, sandboxes with open egress, and teardown
that swallowed a failed kill. Nothing recorded what a run cost or why a turn
failed, and there was no one place to see what happened in a run.

## Decision

1. **One path, main's building blocks.** `SDF_RUN_ADAPTER=live` (alias
   `herdr`) builds the transport with `create_credentialed_transport` from the
   agent's ADR-0007 configuration and runs `HerdrRuntime` (codex, claude) or
   the headless runtimes (opencode, pi, qwen, goose) behind
   `RuntimeController`, so the turn guard, the sub-agent ban and project trust
   (ADR 0019-0021) apply unchanged. All six kinds are supported. Credentials
   are the allow-listed names in ADR 0034; `VULTR_INFERENCE_API_KEY` is not
   read. Only agent configuration reaches credential resolution:
   `SDF_DOTENV`, `SDF_AUTH_*`, `SDF_DATABASE_URL` and server `SDF_RUN_*`
   settings never do. The default template is `agent_sandbox.DEFAULT_TEMPLATE`.
2. **Egress posture.** E2B sandboxes allow the hostname of the kind's resolved
   base URL (official API when Claude or Codex leave it unset) and deny
   everything else, with no public inbound traffic. `SDF_RUN_EGRESS_ALLOW`
   adds named hosts (an open CIDR is refused). Daytona cannot apply that lock
   at creation, so `/run` refuses it unless the operator sets
   `SDF_RUN_DAYTONA_UNRESTRICTED_EGRESS=1`, which accepts open egress.
   Subscription credential modes need their vendor's hosts in
   `SDF_RUN_EGRESS_ALLOW`.
3. **Teardown fails loudly.** The sandbox id is captured before `close()`. A
   failed close is retried as a kill by id (3 times, backoff). If it still
   fails, the run records `cleanup_failed` on the ledger, the Attempt, the log
   and a `run-<provider>-cleanup` runtime event, and the Attempt fails. Every
   E2B sandbox carries the metadata `sdf_attempt_id`, so leftovers can be found.
4. **Inputs and load.** `fixture_dir` (on `/tasks/{id}/run` and `/plans`) must
   resolve, symlinks included, to a directory strictly inside `SDF_FIXTURES_ROOT`
   (default `./fixtures`; the root itself is refused); anything else is a 400.
   A relative `fixture_dir` is relative to that root, never to the server's
   working directory (`sdf_core/fixtures.py`). `POST /plans` stores the
   resolved absolute path, and the Coordinator re-checks it when a run is
   created and again when it executes (a refusal is a 409, or a failed run
   before any sandbox exists); a legacy Plan row holding a relative path is
   resolved against the root the same way. A fixture containing a symlink is
   refused when it is copied into an Attempt workspace. Runs are synchronous; at
   most `SDF_RUN_MAX_CONCURRENT` (default 2) live runs exist per process, and a
   request past that gets 429 with `Retry-After`. An async job API was not
   added. Every configuration error (adapter, provider, credentials) is a
   `CredentialConfigError` and a 400 before any sandbox exists.
5. **Structured logs.** `sdf_core.observability` configures the `sdf` logger
   with the stdlib only: `SDF_LOG_FORMAT=json|text`, `SDF_LOG_LEVEL`. Context
   ids (request, principal, run, task, attempt, agent, turn, sandbox) are
   contextvars added to every record. The API echoes or mints `X-Request-ID`.
   Every message and field is redacted: values of secret-named env vars, bearer
   tokens, and known key prefixes.
6. **Usage ledger.** An append-only `usage_ledger` table (migration
   `0016_usage_ledger`, ORM guard plus a PostgreSQL trigger) has one row per
   turn and one per sandbox lifetime. `source` is `reported`, `estimated` or
   `missing`:
   - headless runtimes report their usage deltas;
   - Codex and Claude usage is parsed from their session files in the sandbox;
   - 0/0 tokens are recorded as `missing`, never as a free turn.

   Cost is the reported cost, or an estimate from a price table
   (`SDF_PRICE_<MODEL>=in,out` per Mtok) and the sandbox rate
   (`SDF_<PROVIDER>_USD_PER_SECOND`). The totals go on the Attempt (tokens,
   cost, provider, latency).
7. **Error taxonomy.** `classify_error` maps a turn's outcome to one of
   `timeout`, `killed`, `settled_grace` (Pi's settled-output grace kill, an
   informational success), `provider_4xx`, `provider_429`, `provider_5xx`,
   `sandbox_error`, `cleanup_failed`, `config_error` or `agent_error`. The
   class is stored on the ledger row and on `attempts.error_class`, and is
   returned by `/run` (and in the 502 detail when the agent run raised).
8. **Timeline.** `GET /runs/{id}/timeline` (viewer) and
   `python -m sdf_core.cli runs show <id> [--json]` give one ordered, redacted
   view of an agent run or an Attempt. They merge:
   - runtime events and agent messages;
   - the coordinator's turn log;
   - tool and auth audits;
   - decision requests and Evidence;
   - ledger rows.

## Consequences

- The `/run` sandbox reaches only Vultr unless an operator widens it.
  *Amended by ADR 0034: it reaches the resolved LLM host, which is the example
  OpenAI-compatible host only when that is the endpoint in use.*
- A leaked sandbox is never silent, and costs are visible per Attempt and per
  turn. Codex and Claude token counts depend on their session-file formats, and
  a format change shows up as `missing`, not as zero.
- The live run is still synchronous, so long turns hold an HTTP request and a
  slot.
