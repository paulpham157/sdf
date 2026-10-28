# Per-turn process containment

Status: accepted (2026-10-29). Amends the cancel sections of
[ADR 0011](0011-opencode-agent-kind-and-per-agent-provider-config.md),
[ADR 0012](0012-pi-agent-kind.md), [ADR 0013](0013-openai-compatible-headless-agent-kinds.md)
and [ADR 0018](0018-opencode-2-and-pi-1-upgrade.md).

## Context

Cancel used `pkill -f '<pattern>'` on the agent's command line, and nothing
ran after a normal turn. Anything an agent started outside that pattern -
a `setsid`/`nohup` process, a double-forked child reparented to PID 1, a
bash-tool background job, an MCP server, a pi-fabric `worker.js`/`--mode rpc`
child - survived the turn and could keep writing to the workspace after SDF
recorded the attempt as finished or cancelled. Pi also retitles itself `pi`,
so the Pi pattern matched only the `timeout` wrapper.

E2B `sdf-herdr-agents` sandboxes (probed): kernel 6.1, cgroup v2 at
`/sys/fs/cgroup` with `cgroup.kill`, commands run as `user` with passwordless
`sudo`, PID 1 is systemd, procps `ps` and util-linux `setsid` present.

## Decision

`sdf_core/turn_guard.py` contains every agent turn. A turn's **members** are the union of

1. processes in its cgroup v2 leaf `/sys/fs/cgroup/sdf-turns/<turn-id>`
   (created with `sudo -n`; a non-root process cannot leave it by `setsid`,
   double fork, re-exec, clearing its environment or reparenting);
2. processes in its session (the `setsid` leader is recorded);
3. processes whose initial environment contains `SDF_TURN_ID=<turn-id>`.

Records live in `/tmp/sdf-turns/<turn-id>/` (mode 0700: `cgroup`, `sid`,
`root`, `swept`). **Kill** = `SIGTERM`, a 3 s grace period (polled), then
`SIGKILL` plus `cgroup.kill` until no member is left (at most 10 rounds).

- **Headless kinds (qwen, goose, pi, opencode).** Every turn is
  `guard_argv(<session>-t<n>, argv)`. The guard is outside the turn's cgroup and
  session and does not carry the tag. It starts the turn as a session leader in the cgroup,
  waits for it, then sweeps all members before it returns the turn's exit status.
  This applies when the turn exits normally, when `timeout --kill-after` kills it, and when
  something SIGKILLs the agent itself. Cancel runs
  `kill_argv("<session>-t")` (all of the session's turns) before the provider
  environment is closed. The transport budget gets an extra 4 x grace.
- **Herdr kinds (codex, claude).** The agent outlives a turn. Before
  `agent start` the pane shell (`herdr pane process-info` → `shell_pid`) and
  its descendants are moved into cgroup `herdr-<attempt>-<pane>` (`enter_argv`).
  When a turn ends, `sweep_argv` kills only members that are no longer
  descendants of the pane shell, i.e. detached or orphaned leftovers. The live agent
  and its own children keep running. Cancel and terminate kill every member
  before the environment is closed. Enrolment and sweeping are best-effort and
  never fail a turn.
- **Fallback.** Without `sudo` or a writable cgroup2 mount, the session and the tag still
  apply. A process that both clears its environment (`env -i`) and calls
  `setsid` escapes containment. The same applies to a process that becomes root and leaves the cgroup.
  The provider sandbox teardown on cancel/terminate remains the final
  boundary.

## Consequences

- Each turn costs a few extra `ps`/`grep` invocations. Sweeps run inside the
  sandbox and never on the SDF host.
- `pkill` patterns are gone, so a renamed process (Pi) no longer escapes cancel.
- Verified live (E2B, 2026-10-29). Each case ran a spawn script that starts
  `setsid sleep 300`, a double-forked workspace writer, and an `env -i setsid sleep 301`:
  a real Pi 1.4.0 turn running it via its bash tool, with Pi then SIGKILLed;
  a guard with the agent SIGKILLed; cancel; `timeout`; and a Herdr pane sweep that
  kept the shell. In each case, 0 members remained, the workspace file stopped growing, and the cgroup was removed.
  In fallback mode only the `env -i setsid` process survived, as documented.
