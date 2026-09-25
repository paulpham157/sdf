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
`sdf-herdr-codex`, killed afterwards):

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

1. Transport runner: `stdin=subprocess.DEVNULL` on every CLI call (fixes the hang).
2. Keep the host-side timeout, and on timeout also kill the remote process or
   sandbox (#1877).
3. Prefer the Python SDK (`Sandbox.create(envs=)`, `commands.run(timeout=,
   request_timeout=)`) for the persistent path; it removes the CLI stdin
   heuristic and gives create-time envs.
4. Credential injection: resolve an `e2b-box` connection (or operator env) on
   the host, pass it as create-time envs, seed agent config by variable name,
   never type secrets into a Herdr pane.
5. Template: add pinned `@anthropic-ai/claude-code`; start Claude with
   `--dangerously-skip-permissions` only inside the disposable sandbox.
