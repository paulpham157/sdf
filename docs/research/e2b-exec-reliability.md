# E2B `sandbox exec` hangs and agent credential injection

Date: 2026-09-25. Scope: `sdf_core/e2b_herdr_transport.py` (persistent Herdr path).

## Finding 1 — the hang is a local stdin that never reaches EOF

E2B CLI 2.16.1 (`/opt/homebrew/Cellar/e2b/2.16.1/.../@e2b/cli/dist/index.js`,
`execCommand` / `runCommand` / `sendStdin`):

- `isPipedStdin()` treats fd 0 as piped when it is a FIFO, file or socket.
- If piped (and envd supports `closeStdin`), the command is started with
  `stdin: true` and `sendStdin()` iterates `process.stdin` until EOF, then calls
  `commands.closeStdin(pid)`, and only then `handle.wait()`.
- The command runs with `timeoutMs: NO_COMMAND_TIMEOUT` (0).

So a caller whose stdin is an open pipe that is never closed blocks forever,
even after the remote command has exited. `subprocess.Popen` without `stdin=`
inherits the parent's fd 0, which in agent harnesses and servers is often
exactly such a pipe.

Controlled live check (sandbox `igkf4amncuw55idi14ftu`, template
`sdf-herdr-codex`, since retired, killed afterwards):

| Case | Result |
| --- | --- |
| `echo ok`, stdin = open pipe never closed | hang (> 110 s) |
| `echo ok`, stdin = closed pipe | exit 0 in 2.0 s |
| `sleep 70; echo done`, stdin = DEVNULL | exit 0 in 71.1 s |
| `sleep 30 & echo started`, stdin = DEVNULL | exit 0 in 31.0 s (waits for the child holding stdout) |

There is no internal ~45 s CLI timeout; an earlier repro that suggested one
had wrapped every call in `alarm 45`. Heredocs, `-e KEY=VALUE` and Herdr
commands were not factors.

Secondary: a background child that keeps stdout/stderr open delays exit until
it ends (`herdr server &` style starts must redirect to `/dev/null`).

## Finding 2 — known upstream stream issues (not the cause here)

Open in e2b-dev/E2B, relevant if we move to pause/resume or reconnect:

- #1352, #1587: reattached stdout stalls after resume (regression in python-sdk 2.29.1).
- #1877: envd runs the process on a context decoupled from the streaming RPC;
  cancelling the stream does not kill the process. A client-side timeout must
  therefore also kill the remote PID.
- #1881: code-interpreter `run_code(timeout=N)` enforced at ~2N.
- PR #1887 (resumable command streams), #1878, #1886 in progress.

## Finding 3 — credentials belong at sandbox creation, not in the pane

- `e2b sandbox create` has no env flag; `-e` on `exec` is per command. The SDK
  `Sandbox.create(envs=...)` sets sandbox-wide envs.
- The herdr-e2b plugin (JS SDK) injects a selected connection as envs at
  create time (`src/connections.js`): Claude `CLAUDE_CODE_OAUTH_TOKEN` from
  `claude setup-token`; Codex a plugin-defined `CODEX_AUTH_JSON` that a seed
  command writes to `~/.codex/auth.json` (`src/fleet-seed.js`). Seed commands
  reference only variable names so secrets never appear in pane scrollback.
- Claude Code also needs first-run state (`~/.claude.json` onboarding/trust,
  `skipDangerousModePermissionPrompt`). Verified live: with that state Herdr
  0.9.1 `agent start --kind claude` reaches `idle`/`interactive_ready`; the only
  failure was a 401 from a host-local proxy key, i.e. a credential problem.

## Recommendations

Status as of 2026-09-25 (agent-credentials, #5–#15):

1. **Done.** Transport runner: `stdin=subprocess.DEVNULL` on every CLI call
   (fixes the hang). The persistent path no longer shells out to the CLI at
   all (item 3); the remaining CLI callers in `sdf_core/herdr_e2b.py` and the
   live tests pass `DEVNULL`.
2. **Done.** Keep the host-side timeout, and on timeout also kill the remote
   process or sandbox (#1877). `E2BHerdrTransport._exec` runs each command with
   an SDK `timeout` plus a host watchdog, and a watchdog expiry kills the whole
   sandbox.
3. **Done.** Prefer the Python SDK (`Sandbox.create(envs=)`, `commands.run(timeout=,
   request_timeout=)`) for the persistent path; `sdf_core/e2b_herdr_transport.py`
   uses it for create, exec, file sync and kill.
4. **Done** (ADR 0007). Credential injection: resolve an `e2b-box` connection
   (or operator env) on the host, pass it as create-time envs, seed agent
   config by variable name, never type secrets into a Herdr pane.
   `sdf_core/credential_injection.create_credentialed_transport` resolves one
   Credential Mode per agent kind (`subscription` through
   `sdf_core/plugin_bridge.py`, `api-key` from `SDF_*` host variables) before
   the sandbox exists. Two details found live:
   - Herdr's server starts from the image entrypoint before the seed steps
     run, so panes do not inherit create-time envs directly. The seed writes
     the agent variables to `~/.config/sdf/agent-env.sh` (mode 600, names
     only in the command) and `~/.bashrc` sources it, so every Herdr pane gets
     them.
   - Codex reads its base URL from the config key `openai_base_url`, not from
     `OPENAI_BASE_URL` ([codex-base-url.md](codex-base-url.md)), and needs
     `-c projects.<dir>.trust_level="trusted"` for the Attempt workspace so no
     trust dialog blocks the pane.
5. **Done** (ADR 0008). Template: `sdf-herdr-agents` pins
   `@anthropic-ai/claude-code` next to Codex; `HerdrRuntime` starts Claude with
   `--dangerously-skip-permissions` only inside the disposable sandbox. It
   replaces `sdf-herdr-codex`, which is retired (ADR 0009).

## Headless path: plugin-owned credential selection

The headless adapter (`sdf_core/herdr_e2b.py`, `e2b-box run -t <template>
--task ... --kill --json`) does not use `create_credentialed_transport`. SDF
passes no credential and no Credential Mode on this path: the herdr-e2b plugin
picks the connection itself (`selectConnection` in the plugin's
`src/connections.js`: an explicit `--connection`, else
`templates.<name>.connection` in the plugin config, else the only connection
for that agent's harness, and an error when several match) and injects it at
box creation with its own seed commands. SDF only sees the one terminal JSON
result, so it records no Credential Mode for a headless Attempt and emits no
per-step Runtime Session events. Use the persistent Herdr path when the
Attempt needs recorded credential metadata or lifecycle events.

## Live evidence (2026-09-25, #15)

Both live Attempts ran on `sdf-herdr-agents` through `HerdrRuntime` on the
persistent transport, each in its own sandbox, killed afterwards
(`e2b sandbox list` empty):

- Claude, `api-key`: `tests/test_claude_api_key_live.py` — 2 passed.
- Codex, `subscription` (`codex-personal`):
  `tests/test_subscription_codex_live.py`, model `gpt-6-luna` — passed 5 of 5
  consecutive runs after #16 (turn completion on `idle` and Codex prompt
  resubmission in `HerdrRuntime`). Before #16 it passed 2 of 10, with every
  failure being Codex staying `idle` at prompt submission. The recorded
  events are in `.scratch/sdf-runtime/issues/08-events.md`.
