"""Per-turn process containment for agent turns in a sandbox (ADR-0019).

Every agent turn runs in its own process session and, where the sandbox
allows it, its own cgroup v2 leaf; descendants also inherit a
``SDF_TURN_ID=<id>`` environment tag.  A turn's members are the union of

* the processes in its cgroup (``/sys/fs/cgroup/sdf-turns/<id>``) - nothing a
  non-root process does (``setsid``, double fork, re-exec, clearing its
  environment, reparenting to PID 1) leaves it;
* the processes in its session (``setsid`` leader recorded at start);
* the processes whose initial environment carries the tag.

The cgroup needs passwordless ``sudo`` and a writable cgroup2 mount (the E2B
``sdf-herdr-agents`` sandboxes have both); without them the session and the
tag still apply.  A process that becomes root and moves itself out of the
cgroup *and* clears its environment *and* calls ``setsid`` is not found; the
provider sandbox teardown stays the final boundary for cancel/terminate.

Killing is TERM, a grace period, then KILL (plus ``cgroup.kill``) until no
member is left.  The headless runtimes (qwen, goose, pi, opencode) wrap every
turn with :func:`guard_argv`: when the turn's own process exits - normally,
by timeout, or because something SIGKILLed the agent - the guard sweeps the
turn's leftovers before it returns.  :func:`kill_argv` is the cancel path.
The Herdr runtime (codex, claude) keeps one long-lived agent process per
session, so its pane shell is enrolled with :func:`enter_argv` and after each
turn :func:`sweep_argv` kills only members that are no longer descendants of
the pane shell (detached or orphaned leftovers) or that left the pane's
session (``setsid`` / detached tool commands).  A turn that outlives
Herdr's wait stays RUNNING and is not swept (the agent may still be working);
the caller terminates the session, which kills the whole cgroup, as do
cancel and terminate.
"""

from __future__ import annotations

import re
from typing import Sequence

TURN_DIR = "/tmp/sdf-turns"
CGROUP_DIR = "/sys/fs/cgroup/sdf-turns"
TAG_ENV = "SDF_TURN_ID"
GRACE_S = 3
_ID = re.compile(r"[A-Za-z0-9_-]{1,120}")

# POSIX sh helpers shared by every script below.
_LIB = (
    'R=' + TURN_DIR + '; CG=' + CGROUP_DIR + '\n'
    # Candidate member pids of turn $1, minus descendants of pid $2 (if set).
    'sdf_members() {\n'
    '  _d=$R/$1; _cg=$(cat "$_d/cgroup" 2>/dev/null); _sid=$(cat "$_d/sid" 2>/dev/null)\n'
    '  _m=$( { [ -n "$_cg" ] && cat "$_cg/cgroup.procs" 2>/dev/null\n'
    '    [ -n "$_sid" ] && ps -e -o pid=,sid= 2>/dev/null | awk -v s="$_sid" \'$2==s {print $1}\'\n'
    '    grep -lsxz "' + TAG_ENV + '=$1" /proc/[0-9]*/environ 2>/dev/null | sed -n \'s|^/proc/\\([0-9]*\\)/environ$|\\1|p\'\n'
    '  } | sort -un | grep -vx -e 1 -e "$$" )\n'
    '  [ -n "$_m" ] || return 0\n'
    '  if [ -z "$2" ]; then printf "%s\\n" "$_m"; return 0; fi\n'
    # A kept descendant must also still be in the root's session: a tool
    # command the agent started with setsid (or a detached background job) is
    # a leftover once the turn is over.
    '  ps -e -o pid=,ppid=,sid= | awk -v r="$2" -v m="$_m" \'{par[$1]=$2; sid[$1]=$3} END {rs=sid[r]; n=split(m,a,"\\n");'
    ' for(i=1;i<=n;i++){p=a[i]; if(p=="") continue; q=p; k=0;'
    ' while(q!="" && q!="0" && k<512){ if(q==r){ if(sid[p]==rs) p=""; break}; q=par[q]; k++ } if(p!="") print p}}\'\n'
    '}\n'
    # TERM, wait up to $2 s, then KILL (cgroup.kill when nothing is kept) until empty.
    'sdf_kill() {\n'
    '  _p=$(sdf_members "$1" "$3"); [ -n "$_p" ] || return 0\n'
    '  echo "$_p" | wc -l > "$R/$1/swept" 2>/dev/null\n'
    '  kill -TERM $_p 2>/dev/null; sudo -n kill -TERM $_p 2>/dev/null\n'
    '  _i=0; while [ "$_i" -lt "$(( ${2:-3} * 5 ))" ]; do sleep 0.2; _p=$(sdf_members "$1" "$3"); [ -n "$_p" ] || return 0; _i=$((_i+1)); done\n'
    '  _i=0; while [ -n "$_p" ] && [ "$_i" -lt 10 ]; do\n'
    '    _cg=$(cat "$R/$1/cgroup" 2>/dev/null)\n'
    '    if [ -z "$3" ] && [ -n "$_cg" ]; then echo 1 | sudo -n tee "$_cg/cgroup.kill" >/dev/null 2>&1; fi\n'
    '    kill -KILL $_p 2>/dev/null; sudo -n kill -KILL $_p 2>/dev/null\n'
    '    sleep 0.2; _p=$(sdf_members "$1" "$3"); _i=$((_i+1))\n'
    '  done\n'
    '  [ -z "$_p" ]\n'
    '}\n'
    'sdf_finish() { _cg=$(cat "$R/$1/cgroup" 2>/dev/null); [ -n "$_cg" ] && sudo -n rmdir "$_cg" 2>/dev/null; rm -rf "$R/$1"; return 0; }\n'
    # Create turn $1's record and, when possible, its cgroup.
    'sdf_open() {\n'
    '  (umask 077; mkdir -p "$R/$1") || return 1\n'
    '  if [ -f /sys/fs/cgroup/cgroup.controllers ] && sudo -n mkdir -p "$CG/$1" 2>/dev/null; then echo "$CG/$1" > "$R/$1/cgroup"; fi\n'
    '}\n'
)

# Positional args: turn id, grace seconds, then the turn argv.  The leader
# joins the cgroup and records its session id before it execs the turn, so
# every descendant starts inside both.  Stdout/stderr pass through; the exit
# status is the turn's.  The guard itself is in neither and has no tag.
_GUARD = _LIB + (
    'id=$1; g=$2; shift 2\n'
    'sdf_open "$id" || { echo "sdf: cannot create turn record" >&2; exit 125; }\n'
    'cg=$(cat "$R/$id/cgroup" 2>/dev/null)\n'
    'trap \'sdf_kill "$id" "$g"; sdf_finish "$id"; exit 143\' TERM HUP INT\n'
    + TAG_ENV + '=$id setsid -w sh -c \'echo $$ > "$1/sid"; if [ -n "$2" ]; then echo $$ | sudo -n tee "$2/cgroup.procs" >/dev/null 2>&1; fi; '
    'shift 2; exec "$@"\' sdf-turn "$R/$id" "$cg" "$@" &\n'
    'wait $!; rc=$?\n'
    'sdf_kill "$id" "$g"; sdf_finish "$id"\n'
    'exit $rc\n'
)

# Positional args: grace, then turn-id prefixes.  Kills every turn whose id
# starts with a prefix (cancel / terminate of a session).
_KILL = _LIB + (
    'g=$1; shift\n'
    'for pre; do for d in "$R/$pre"*; do [ -d "$d" ] || continue; t=${d##*/}; sdf_kill "$t" "$g"; sdf_finish "$t"; done; done\n'
    'echo SDF_TURN_KILLED\n'
)

# Positional args: turn id, root pid.  Enrol an existing process tree (a
# Herdr pane shell and its descendants): cgroup when possible, and the root's
# session id.  Prints ``cgroup`` or ``session``.
_ENTER = _LIB + (
    'id=$1; root=$2\n'
    'sdf_open "$id" || exit 0\n'
    'ps -o sid= -p "$root" 2>/dev/null | tr -d " " > "$R/$id/sid"\n'
    'echo "$root" > "$R/$id/root"\n'
    'cg=$(cat "$R/$id/cgroup" 2>/dev/null)\n'
    'if [ -n "$cg" ]; then\n'
    '  for p in $root $(ps -e -o pid=,ppid= | awk -v r="$root" \'{c[$2]=c[$2]" "$1} END {q=r; out="";'
    ' while(q!=""){split(q,a," "); q=""; for(i in a){n=split(c[a[i]],b," "); for(j=1;j<=n;j++){out=out" "b[j]; q=q" "b[j]}}} print out}\'); do\n'
    '    echo "$p" | sudo -n tee "$cg/cgroup.procs" >/dev/null 2>&1\n'
    '  done\n'
    '  grep -qx "$root" "$cg/cgroup.procs" && { echo cgroup; exit 0; }\n'
    'fi\n'
    'echo session\n'
)

# Positional args: grace, turn id, root pid.  Kill the turn's members that
# are not descendants of ``root`` (the live agent keeps running).
_SWEEP = _LIB + (
    'g=$1; id=$2; root=$3\n'
    '[ -d "$R/$id" ] || { echo SDF_TURN_SWEPT; exit 0; }\n'
    'sdf_kill "$id" "$g" "$root"; echo SDF_TURN_SWEPT\n'
)


def _check_id(value: str) -> str:
    if not _ID.fullmatch(value):
        raise ValueError("turn id must be 1-120 letters, digits, underscores or hyphens")
    return value


def guard_argv(turn_id: str, argv: Sequence[str], *, grace_s: int = GRACE_S) -> list[str]:
    """Run ``argv`` as one contained turn; sweep its leftovers on exit."""

    if not argv:
        raise ValueError("turn argv must not be empty")
    return ["sh", "-c", _GUARD, "sdf-turn-guard", _check_id(turn_id), str(int(grace_s)), *argv]


def kill_argv(*prefixes: str, grace_s: int = GRACE_S) -> list[str]:
    """Kill every recorded turn whose id starts with one of ``prefixes``."""

    if not prefixes:
        raise ValueError("at least one turn-id prefix is required")
    return ["sh", "-c", _KILL, "sdf-turn-kill", str(int(grace_s)), *(_check_id(p) for p in prefixes)]


def enter_argv(turn_id: str, root_pid: int) -> list[str]:
    """Enrol a running process tree (Herdr pane shell) as turn ``turn_id``."""

    if not isinstance(root_pid, int) or isinstance(root_pid, bool) or root_pid <= 1:
        raise ValueError("root pid must be an integer > 1")
    return ["sh", "-c", _ENTER, "sdf-turn-enter", _check_id(turn_id), str(root_pid)]


def sweep_argv(turn_id: str, root_pid: int, *, grace_s: int = GRACE_S) -> list[str]:
    """Kill members of ``turn_id`` that are not descendants of ``root_pid``."""

    if not isinstance(root_pid, int) or isinstance(root_pid, bool) or root_pid <= 1:
        raise ValueError("root pid must be an integer > 1")
    return ["sh", "-c", _SWEEP, "sdf-turn-sweep", str(int(grace_s)), _check_id(turn_id), str(root_pid)]


__all__ = ["CGROUP_DIR", "GRACE_S", "TAG_ENV", "TURN_DIR", "enter_argv", "guard_argv", "kill_argv", "sweep_argv"]
