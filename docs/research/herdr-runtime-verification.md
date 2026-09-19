# Herdr runtime verification

Status: verified with integration blockers

Date: 2026-09-19

This note verifies the public Herdr identity and documented control surface for
SDF ticket `05-herdr-verification`. It uses the current upstream repository and
first-party documentation. No Herdr agent session was started, no provider call
was made, and no credentials were used.

## Authoritative identity

The integration target is the public repository
[`herdrdev/herdr`](https://github.com/herdrdev/herdr), linked from the official
site [`herdr.dev`](https://herdr.dev/). The repository describes Herdr as a
terminal workspace manager/runtime for AI coding agents. Its `Cargo.toml`
declares:

- package: `herdr`
- repository: `https://github.com/herdrdev/herdr`
- homepage: `https://herdr.dev`
- current source package version: `0.9.1`
- license: `Apache-2.0`

Evidence: [Cargo.toml](https://github.com/herdrdev/herdr/blob/master/Cargo.toml),
[README](https://github.com/herdrdev/herdr/blob/master/README.md), and
[LICENSE](https://github.com/herdrdev/herdr/blob/master/LICENSE).

The latest stable release visible at verification time is
[`v0.9.1`](https://github.com/herdrdev/herdr/releases/tag/v0.9.1), released from
commit `065ef9d`. The SDF adapter should pin a release/tag, initially `v0.9.1`,
and should not depend on moving `master` behavior.

There are public forks, including
[`motionharvest/herdr`](https://github.com/motionharvest/herdr) and
[`SuperCodeAgents/herdr-terminal`](https://github.com/SuperCodeAgents/herdr-terminal).
They are not the integration target. Older examples may mention
`ogulcancelik/herdr`; use the current repository and release above unless a
separate fork is deliberately selected and re-verified.

## Verified documented contract

| Capability | Primary evidence | SDF interpretation | Status |
|---|---|---|---|
| Start/attach a server session | [`herdr`](https://herdr.dev/docs/cli-reference/#launch-and-status), [`session attach`](https://herdr.dev/docs/cli-reference/#sessions) | A Herdr named session is a server/socket namespace. Store it separately from the SDF Attempt and from an agent conversation ID. | Documented |
| Create a bounded workspace/pane | [`workspace create`](https://herdr.dev/docs/cli-reference/#workspaces), [`pane split`](https://herdr.dev/docs/cli-reference/#panes) | Create the disposable fixture workspace first, then launch the agent in an identified pane. Herdr workspace ownership does not itself enforce SDF policy. | Documented, containment separate |
| Start a supported agent | [`agent start`](https://herdr.dev/docs/cli-reference/#agents) | `agent start <name> --kind <kind> --pane <id>` starts an interactive agent in an existing shell pane and returns only after the expected agent owns the terminal and is ready. Supported kinds include `codex`, `pi`, `claude`, and `hermes`. | Documented |
| Send input and wait | [`agent prompt`](https://herdr.dev/docs/cli-reference/#agents) | `agent prompt` submits text; `--wait --until idle|done|blocked` waits for a Herdr-observed settled state. An acknowledgement without `--wait` proves only that bytes were written. | Documented |
| Read output | [`agent read`](https://herdr.dev/docs/cli-reference/#agents), [`pane read`](https://herdr.dev/docs/socket-api/#reading-panes) | Capture terminal output as an untrusted process Artifact. Output is not a policy decision or evaluator result. | Documented |
| Observe a live stream | [`terminal session observe`](https://herdr.dev/docs/cli-reference/#direct-terminal-attach) | Read-only stream emits newline-delimited `terminal.frame` records with base64 ANSI bytes and normally a `terminal.closed` record. A stalled observer can end without the close record. | Documented with stream-loss edge case |
| Read process information | [`pane.process_info`](https://herdr.dev/docs/socket-api/#raw-methods) | Useful for correlation and diagnostics; do not treat a PID as proof that a process was terminated until SDF verifies it. | Documented |
| Observe semantic agent state | [`agent wait`](https://herdr.dev/docs/cli-reference/#agents), [`agent status reporting`](https://herdr.dev/docs/socket-api/#agent-state-reporting) | States are Herdr observations: `idle`, `working`, `blocked`, `done`, `unknown`. `done` means ready/seen state, not that SDF acceptance criteria passed. | Documented |
| Subscribe to lifecycle events | [`events.subscribe`](https://herdr.dev/docs/socket-api/#event-subscriptions) | Events include `pane.created`, `pane.updated`, `pane.closed`, `pane.exited`, `pane.agent_detected`, `pane.agent_status_changed`, and output-related events. Subscriptions begin at acceptance and do not replay earlier events. | Documented |
| Rebuild state after socket reconnect | [`session.snapshot`](https://herdr.dev/docs/socket-api/#raw-methods), [`snapshot bootstrap`](https://herdr.dev/docs/socket-api/#raw-methods) | On reconnect, subscribe first, call `session.snapshot`, apply buffered events, then continue streaming. This is a client cache protocol, not SDF state recovery. | Documented |
| Detach and reattach while server remains alive | [`session state`](https://herdr.dev/docs/session-state/) | Detach keeps the original pane processes running. This is the strongest documented persistence path. | Documented |
| Recover after Herdr server restart | [`session state`](https://herdr.dev/docs/session-state/) | Herdr restores layout; original processes are gone. Agent conversation resume requires an official integration-reported native session reference. | Documented, conditional |
| Remote access | [`CLI reference`](https://herdr.dev/docs/cli-reference/#launch-and-status) | SSH/remote support exists, but it is outside the first local SDF slice and introduces another trust boundary. | Documented, out of scope |
| Version/API negotiation | [`protocol stability`](https://herdr.dev/docs/socket-api/#protocol-stability) | JSON clients must tolerate unknown fields and unsupported methods. Pin a release and probe capabilities before use. | Documented |

## Explicit gaps and blockers

### Cancellation and termination are not verified

The current public CLI/API lists input, prompt, wait, attach, pane close and
server stop operations. It does not document a per-agent `cancel` or
`terminate` method. `agent send-keys ... ctrl+c` is validated key injection,
not a proof that the agent or its descendants exited. `pane close` may be a
useful cleanup action, but the public contract does not establish the process
tree, exit-code, descendant cleanup, or force-kill semantics that SDF needs for
an enforced timeout.

Therefore `AgentRuntime.cancel()` and `AgentRuntime.terminate()` cannot be
claimed as implemented against Herdr yet. The adapter must either:

1. run a disposable local behavior test that proves `ctrl+c`/pane close plus
   `pane.exited` and process inspection are sufficient for the chosen agent; or
2. add an outer process supervisor/containment boundary that owns timeout and
   termination, with Herdr used for session and terminal control.

Until one of these is proven, a Herdr Attempt must remain restricted to a
disposable fixture and must not be treated as safely cancellable.

### Herdr is not the SDF sandbox or Tool Proxy

The documented product is a terminal workspace/session manager. Its API can
create panes, launch commands, set working directories/environment variables,
send input, and read output. The agent-state reporting API reports semantic
state to Herdr; it is not an authorization hook. The public documentation
does not establish filesystem, process, network, or secret isolation.

SDF must keep the Tool Proxy and enforced sandbox as separate boundaries:

```text
SDF policy -> Tool Proxy -> allowed action
                         \
                          -> enforced fixture sandbox

HerdrRuntime -> Herdr pane/session -> agent terminal
```

Terminal text, screen detection, and Herdr `done`/`blocked` state are untrusted
runtime observations. They cannot create Evidence or authorize a tool action by
themselves.

## Recommended SDF adapter shape

Use the internal interface from ADR-0004 and map Herdr identifiers explicitly:

```text
SDF Attempt
  ├─ herdr_server_session     # named Herdr socket namespace, if used
  ├─ herdr_workspace_id
  ├─ herdr_tab_id
  ├─ herdr_pane_id
  ├─ herdr_terminal_id
  └─ agent_session             # optional native agent reference from integration
```

The first implementation should use the documented CLI wrappers for ordinary
operations (`workspace create`, `agent start`, `agent prompt --wait`, `agent
read`, `pane process-info`) and use the raw socket API only for event
subscriptions or a long-lived observer. The CLI is the documented portable
surface; raw clients must handle Unix sockets versus Windows named pipes.

Suggested mapping:

```text
start      -> workspace/pane setup + agent start
send       -> agent prompt
stream     -> terminal session observe or events.subscribe
status     -> agent get/list + pane process-info
resume     -> reconnect, subscribe, session.snapshot, then re-read state
cancel     -> unverified until local process/exit behavior is proven
terminate  -> unverified until containment supervisor or proven cleanup exists
```

For the first real agent, use one pinned kind and retain `FakeRuntime` for
deterministic tests. The local machine currently has `herdr 0.7.3` installed,
while the verified upstream release is `v0.9.1`; this version gap must be
resolved before integration tests are interpreted as release evidence.

## Verification boundary

Completed in this research task:

- identified the current upstream, release, repository metadata, and license;
- verified the documented start, prompt, output, status, event, snapshot and
  reconnect surfaces from first-party sources;
- identified the absence of a documented per-agent cancel/terminate contract;
- separated Herdr session management from SDF policy, sandbox, evaluation and
  Evidence ownership.

Not performed:

- no Herdr server or real agent session was started;
- no `herdr integration install` was run;
- no provider credentials or paid model call was used;
- no cancellation, descendant cleanup, reconnect, native session restore, or
  sandbox behavior was locally proven.

Ticket 05 should remain blocked from resolution until a later disposable local
smoke test verifies the selected pinned binary and one agent kind. Ticket 06 may
implement the adapter seam and fake/runtime contract, but it must keep
cancel/terminate and real-provider claims explicitly unverified until that
smoke test completes.

