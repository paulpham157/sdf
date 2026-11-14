# Receiver

`./sdf serve` listens for OTLP/HTTP on `http://127.0.0.1:4318` and OTLP/gRPC on
`127.0.0.1:4317`, and writes `sdf-telemetry.sqlite` in the current directory.
4318 is the OTLP/HTTP default. 4317 is the OTLP/gRPC default, which is where
pi-otel exports unless you change it. There is no authentication on either
port; keep the bind address on localhost. Override with `--host`, `--port`
(HTTP), `--grpc-port`, and `--db`.

The same process serves a read-only UI at `http://127.0.0.1:4320/` unless
you pass `--no-ui`. `--ui-addr` moves the UI. `--host` does not: the page
stays on localhost when the receivers are bound wider. `--prices`, `--repo`,
`--github`, and `--budget` match `sdf summary` and apply to the page. `--budget`
names one local ceiling file; without it the page has no budget.

`GET /health` returns `{"status":"ok"}`. HTTP exports are `POST /v1/traces`,
`POST /v1/metrics`, and `POST /v1/logs`, `Content-Type: application/json` or
`application/x-protobuf`. The body may use `Content-Length` or
`Transfer-Encoding: chunked`. OpenCode trace batches and pi-otel HTTP posts
are chunked and omit `Content-Length`. When both headers are present, chunked
encoding wins. `Content-Encoding: gzip` is inflated after the body is
framed, and the decompressed size is capped at 8,000,000 bytes. Any other
content encoding gets HTTP 415. gRPC clients call the OTLP `TraceService`,
`MetricsService`, and `LogsService` `Export` RPCs, and may gzip the message.

Rows are one span, one metric point, or one log record. A span also stores
`status_code` (`UNSET`, `OK`, or `ERROR`) and `status_message`. Databases
written before those columns existed still open; the columns are added on
open, and a missing code is `UNSET`. Inspect them with SQLite:

```bash
sqlite3 sdf-telemetry.sqlite \
  "select signal, session_id, name from records order by id desc limit 20"
```

## The UI

The process prints `UI listening on http://127.0.0.1:4320/` (the default).
Open that URL on the same machine.

The page has three views. Overview is the total across sessions and the cost
per accepted outcome, including that figure per agent. Sessions lists every
stored session, newest first, with the agent, start time, duration, models,
input and output tokens, computed cost, the cost the agent reported, the
linked commit or pull request, and whether the link was exact, hook, trailer,
heuristic, or none. Filter the list by agent, or click a column to sort it. A session
opens onto the totals, the token buckets, a per-model note, tool-call counts,
the two costs side by side, and the outcome. A field the summary could not
establish says `unknown` and gives the reason (no published price, no working
directory or commit, merge status not requested).

Those numbers are the `sdf summary` report. The decision records and the budget are drawn on every view, the overview, the session list, and the session detail, and both are drawn even when the store has no sessions. The page does not price tokens
again. `GET /api/summary` on the UI port is the same document as
`sdf summary --json` for that database, including `--prices` and `--repo`.
`GET /api/snapshot` is that report newest-first, plus the per-agent totals.
`GET /api/decisions` reads the decision records. It is empty when none have
been recorded, and recording one does not change the summary. All three are
read-only. There is no route that writes or deletes.

A known `0` is a published price of zero, such as a `:free` model whose
token buckets are all priced. It is not a stand-in for a missing price. If
any session in a total has an unknown computed cost, the total is unknown
too. The summary does not split one session's tokens across several models,
so the page does not either: several models stay unknown, with the session
total still shown as a session total.

`--github` asks whether a linked commit's pull request merged, on each
refresh, the same way `sdf summary --github` does. Without it, accepted
stays unknown because merge status was not requested. The cost per accepted
outcome uses that answer. It is unknown, not zero, when nothing merged or
when a session has no known outcome.

The UI is on by default so a local demo is `sdf serve` and a browser.
`--no-ui` leaves the OTLP ports only. It is a second listener, embedded in
the sdf binary (HTML, CSS, and script). The page does not load fonts,
scripts, or anything else from the network.

`sdf serve --host 0.0.0.0` exposes the OTLP receivers on every interface.
Those payloads can include prompts and filesystem paths when the agent is
opted in, and the receiver has no authentication. The UI still binds to
`127.0.0.1` in that case. `--ui-addr 0.0.0.0:4320` exposes the same
telemetry in a browser and prints a warning. Do not do that on a network
you do not trust.

An empty database shows an empty state that points at `sdf init`.

## One-step setup

`sdf init` prints the exporter settings for one agent. It does not change a
file unless you pass `--write`. An existing file is copied to a sibling
ending in `.sdf-bak` before it is changed. A file that is not valid JSON is
left untouched. An existing Codex `[otel]` table is left untouched.

```bash
sdf init claude-code
sdf init claude-code --write
sdf init codex --write
sdf init opencode --write
sdf init pi --write
sdf init pi --write --global
sdf init pi --install
sdf init --git-template --set-template-dir
sdf init --check
```

`--check` GETs `http://127.0.0.1:4318/health` and opens a TCP connection to
`127.0.0.1:4317`. The TCP connect does not speak gRPC. Either listener
counts as reachable. `--host`, `--port`, and `--grpc-port` match `sdf serve`.

`--write` also installs a session hook for that agent. The hook posts to
`POST /v1/sessions` when a session opens and, where the agent has such an
event, when it closes. The collector stores each as a log named
`sdf.session.open` or `sdf.session.close` and uses those as the session's
window instead of inferring one from the records. A session with no close
stays open. An agent with no hook keeps the inferred window. The hook exits
without effect when the collector is not running, so it never blocks the
agent. Codex posts only for the root thread, because a descendant's hook
reports the shared session id while its spans carry the thread id. OpenCode
has no close event, so only the open is posted. Pi reads the session id from
the session manager rather than from the file name pi-otel exports.

The settings are the ones in [docs/agents.md](agents.md). Claude Code's variables are
from the [monitoring doc](https://code.claude.com/docs/en/monitoring-usage)
and are written to `~/.claude/settings.json`, not to a repository settings
file. Codex's `[otel]` table is from the
[config reference](https://developers.openai.com/codex/config-reference) and
is written to `$CODEX_HOME/config.toml`. OpenCode v2 only needs
`OTEL_EXPORTER_OTLP_ENDPOINT`; `--write` saves a snippet you source, because
OpenCode does not load that file itself. Pi needs `pi install npm:pi-otel`
(the extension checked here is 0.3.0). `--install` is what runs that command.
`--write` updates `.pi/settings.json` in the current directory. `--write
--global` updates `~/.pi/agent/settings.json` instead and leaves the project
file alone. pi-otel 0.3.0 reads the project file first, then the global file.
Environment overrides both. Either write copies an existing file to a
`.sdf-bak` sibling and leaves a file that is not valid JSON untouched. The
protocol in that file is `http/json`. pi-otel's own default is gRPC on port
4317, which this receiver also accepts. `sdf init --check pi` reports whether
the process environment, the project file, and the global file point at this
receiver.

## Git hooks

Pi, OpenCode, and Codex do not export a commit SHA. `sdf init --git-hooks`
installs `post-commit`, `post-rewrite`, and `prepare-commit-msg` into the
hooks directory Git actually runs (`git rev-parse --git-path hooks`). On
Git 2.43 that directory is shared by linked worktrees, so one install covers
them. The event records the toplevel of the worktree that committed.

Run `sdf serve` first. In the checkout the agent will commit in:

```bash
sdf init --git-hooks
```

`--host`, `--port` match `sdf serve`. The hook posts only to
`http://127.0.0.1:<port>/v1/logs` (or `[::1]`). Any other host is rejected.
`http_proxy` is ignored. Re-running with the same port leaves an existing
install in place. `sdf init --uninstall-git-hooks` removes the sdf scripts
and puts back a hook they had chained.

An existing hook is not overwritten. An executable script is renamed to
`<name>.sdf-previous` and run first. A symlink, or a file that is not
executable, is left untouched and the command fails. `core.hooksPath` is
never changed. If it already points at husky, lefthook, or pre-commit, sdf
writes chain scripts under the common git dir's `sdf-hooks/` and prints the
lines to add. It does not edit those tools' files.

```bash
# husky: add to .husky/post-commit and .husky/post-rewrite
# Put these lines before any exit 0, or at the top of the file.
sh /path/to/.git/sdf-hooks/post-commit
# and to .husky/prepare-commit-msg
sh /path/to/.git/sdf-hooks/prepare-commit-msg "$@"
```

Put each line before any `exit 0`, or at the top of the file. Lines after
`exit 0` never run, and the commit still succeeds. Lefthook gets the same
commands in `lefthook.yml`. pre-commit does not run `post-commit` unless you
add a hook that calls the script. A shell script that lefthook or pre-commit
calls has the same rule: the sdf line has to run before any `exit 0`. If
`.husky`, `lefthook.yml`, or `.pre-commit-config.yaml` is present but
`core.hooksPath` is unset, Git is not using them yet; the install says so.

`sdf init --git-hooks` changes one repository. To give every future clone and
`git init` the same hooks, install them into Git's template directory:

```bash
sdf init --git-template --set-template-dir
```

`--set-template-dir` creates `~/.config/sdf/git-template`, copies Git's
default template into it when the directory is empty (so new repositories
still get the stock sample hooks and `info/exclude`), installs the sdf hooks
there, and runs `git config --global init.templateDir` to that path. The
command prints the previous value, which is unset the first time.
`sdf init --git-template` alone does not change git config.

If `init.templateDir` is already set, `sdf init --git-template` installs into
that directory and does not change git config, including when
`--set-template-dir` is also passed. `GIT_TEMPLATE_DIR`, when set, is the
directory Git actually uses and overrides `init.templateDir`. The command
installs there and does not write git config. An existing hook is chained or
refused the same way as in a repository. A template you already have is not
replaced.

```bash
sdf init --uninstall-git-template
sdf init --uninstall-git-template --unset-template-dir
```

Uninstall removes the sdf hooks from the template directory in use.
`--unset-template-dir` unsets `init.templateDir` only when that value is
`~/.config/sdf/git-template`, and it prints the previous value. A template
you configured yourself is left in git config. The directory is left on disk.

Existing repositories are unchanged. Run `sdf init --git-hooks` in each one,
or re-run `git init` there. `git init` copies template hooks that are missing
and does not replace a hook that already exists. A new clone, and `git init`
of a new directory, pick the template up. `GIT_TEMPLATE_DIR` still overrides
the config value.

The hook is POSIX `sh`. It uses `curl` when that is on `PATH`, otherwise
`wget`. A missing client, a refused connection, or a slow receiver does not
fail the commit (`--connect-timeout 1 --max-time 1`). The POST body is an
OTLP log named `sdf.git.commit` with the commit SHA, the checkout toplevel,
the branch name, and the agent session id. It does not include the diff or
the commit message. `sdf summary` reads the subject from the local git
object after the link, the same way it does for a SHA the agent exported.

`prepare-commit-msg` appends an `SDF-Session: <id>` trailer only when a
session id is present. A commit made outside an agent gets neither a trailer
nor an event. If two agents' ids are set at once, the commit is not
attributed.

The id has to be the one the agent's telemetry already uses:

| Agent | Variable | Stored session id |
| --- | --- | --- |
| Pi | `PI_SESSION_FILE` | File name with a trailing `.jsonl` removed. pi-otel 0.3.0 does `basename(file, ".jsonl")`. `PI_SESSION_ID` is only the short trailing id and is not used. An ephemeral session has no file, exports no session id, and is not linked. |
| OpenCode | `OPENCODE_SESSION_ID` | `session.id` on runner spans. Taken from the OpenCode shell-tool change; not run live here. |
| Codex | `CODEX_THREAD_ID` | `conversation.id`, the emitting thread. On Codex 0.167.0, `CODEX_SESSION_ID` equaled `CODEX_THREAD_ID` for a live `codex exec` (`SessionId::from(thread_id)` in `codex-rs/core/src/session/session.rs` on tag `rust-v0.167.0` sets them equal on a root thread). A subagent can set `CODEX_SESSION_ID` to the root id instead, so the hook reads `CODEX_THREAD_ID` and does not read `CODEX_SESSION_ID`. |

Pi was checked against pi-otel 0.3.0's source (`sessionIdRef = basename(file, ".jsonl")`) and the session file name `<timestamp>_<id>.jsonl`. The hook behavior below does not start an agent.

Try it without a model call. From the checkout, with `sdf serve` writing
its database in this directory:

```bash
sdf init --git-hooks
export OPENCODE_SESSION_ID=ses_demo
git commit --allow-empty -m "try sdf git hook"
unset OPENCODE_SESSION_ID
sdf summary
```

Summary shows session `ses_demo`, tool `opencode`, match `hook`. That commit
is the whole session until OpenCode exports a trace with the same
`session.id`; the trace joins it. A later commit in this shell with the
variable unset is not linked to it. For Pi, set `PI_SESSION_FILE` to the
session JSONL path (the stem is what pi-otel exports) rather than only
`PI_SESSION_ID`. For Codex, set `CODEX_THREAD_ID` to the thread id from
`conversation.id`. On Codex 0.167.0, `CODEX_SESSION_ID` equaled
`CODEX_THREAD_ID` for a live `codex exec`.

The hook's `service.name` matches the Codex process that committed, so one
session does not show two names. A parent command of `codex exec` (or
`codex-exec`) is `codex_exec`. On a live `codex exec` on 2026-11-11, the
hook event and Codex telemetry both used `service.name` `codex_exec`. If
`CODEX_INTERNAL_ORIGINATOR_OVERRIDE` is a short token of letters, digits,
`.`, `_`, and `-`, that token is the exec service name instead, which is
what Codex 0.167.0 uses for `codex exec`. A parent `codex`, `codex-tui`, or
`codex-app-server` is `codex-app-server`. If the parent chain is not Codex,
the hook uses `codex_exec`. The interactive name is from the 0.167.0 source
and was not re-checked against a live TUI.

Codex's default `workspace-write` sandbox mounts `.git` read-only.
`FileSystemSandboxPolicy::workspace_write` in Codex 0.167.0 adds `.git`
unless an explicit write rule already covers that path. The agent cannot
`git add` or `git commit`, the hooks never run, and no outcome is recorded.
That default was verified on 2026-11-11 (Asia/Bangkok) with Codex 0.167.0,
model `mistralai/mistral-nemo` via OpenRouter, and sdf built from `3f0a35a`.

The preferred option is the narrower one: keep `workspace-write` and list
the checkout `.git` in `sandbox_workspace_write.writable_roots`. That was
live-verified on the same Codex. The banner listed the `.git` path as
writable, the commit succeeded, and sdf linked it with match `hook`. The
`SDF-Session` trailer matched `CODEX_THREAD_ID` and `conversation.id`.
`CODEX_SESSION_ID` equaled `CODEX_THREAD_ID`. A human commit with no agent
session variables set stayed unlinked.

```bash
codex exec -C <repo> -s workspace-write \
  -c 'sandbox_workspace_write.writable_roots=["<repo>/.git"]'
```

The same setting in `config.toml`:

```toml
[sandbox_workspace_write]
writable_roots = ["<repo>/.git"]
```

A linked worktree also needs the common git dir on `writable_roots`
(`git rev-parse --git-common-dir`). That path was not live-tested.

`--sandbox danger-full-access` also committed and linked on that run. It
is broader than a `.git` writable root.

`post-rewrite` runs on `git commit --amend` and `git rebase`. The replacement
SHA is stored, and the previous SHA from that rewrite is not linked once the
new object is in the checkout. A GitHub squash merge does not run a local
hook. The `SDF-Session` trailer survives only when the squash commit message
keeps it. GitHub's default squash message is the pull request title, not the
commit trailers, so this does not happen unless the message is edited to
include the trailer (or the merge method keeps the commit messages). GitHub
then adds a blank line and `Co-authored-by`. Summary still matches the
`SDF-Session` line. When the hook SHA is gone and the line is still in a
local commit, summary links that commit with match `trailer`. A trailer for
one session is not given to another session by the time-window heuristic.
