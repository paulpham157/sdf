"""Confine a fixture child to its workspace, including when it skips the Tool Proxy.

ADR 0004: the sandbox has to stop filesystem and credential escape even if the
agent never submits an ``ActionRequest``. On Linux the child installs a
Landlock ruleset (read the system, write only the workspace and any extra
roots the trusted caller named) and drops credential-shaped environment
variables before ``exec``. ``require_containment=True`` is unchanged: that
flag still refuses to start a child when no OS backend was configured.
"""

from __future__ import annotations

import os
import re
import subprocess
import sys
from collections.abc import Iterator, Mapping, Sequence
from contextlib import contextmanager
from pathlib import Path

_SECRET_ENV = re.compile(r"(?i)(API_KEY|PRIVATE_KEY|SECRET|TOKEN|PASSWORD|CREDENTIAL)")

# Self-contained: the child must not import sdf_core before the ruleset is in
# place. It pops the SDF_LANDLOCK_* variables so the real command never sees them.
_BOOTSTRAP = r"""
import ctypes, os, sys
from ctypes import c_uint64, c_int32, c_long
libc = ctypes.CDLL(None, use_errno=True)
syscall = libc.syscall
syscall.restype = c_long
machine = os.uname().machine
if machine in {"x86_64", "amd64"}:
    SYS_CREATE, SYS_ADD, SYS_RESTRICT, SYS_PRCTL = 444, 445, 446, 157
elif machine in {"aarch64", "arm64"}:
    SYS_CREATE, SYS_ADD, SYS_RESTRICT, SYS_PRCTL = 444, 445, 446, 167
else:
    sys.stderr.write("landlock is not mapped for this machine\n")
    sys.exit(86)
PR_SET_NO_NEW_PRIVS = 38
LANDLOCK_RULE_PATH_BENEATH = 1
VERSION_FLAG = 1
FS_EXECUTE, FS_WRITE_FILE, FS_READ_FILE, FS_READ_DIR = 1 << 0, 1 << 1, 1 << 2, 1 << 3
FS_REMOVE_DIR, FS_REMOVE_FILE = 1 << 4, 1 << 5
FS_MAKE_CHAR, FS_MAKE_DIR, FS_MAKE_REG = 1 << 6, 1 << 7, 1 << 8
FS_MAKE_SOCK, FS_MAKE_FIFO, FS_MAKE_BLOCK, FS_MAKE_SYM = 1 << 9, 1 << 10, 1 << 11, 1 << 12
FS_REFER, FS_TRUNCATE, FS_IOCTL_DEV = 1 << 13, 1 << 14, 1 << 15
READ = FS_EXECUTE | FS_READ_FILE | FS_READ_DIR
WRITE = (FS_WRITE_FILE | FS_REMOVE_DIR | FS_REMOVE_FILE | FS_MAKE_CHAR | FS_MAKE_DIR |
         FS_MAKE_REG | FS_MAKE_SOCK | FS_MAKE_FIFO | FS_MAKE_BLOCK | FS_MAKE_SYM |
         FS_REFER | FS_TRUNCATE | FS_IOCTL_DEV)

class RulesetAttr(ctypes.Structure):
    _fields_ = [("handled_access_fs", c_uint64)]
class PathBeneath(ctypes.Structure):
    _fields_ = [("allowed_access", c_uint64), ("parent_fd", c_int32)]

def apply(roots):
    abi = syscall(SYS_CREATE, None, 0, VERSION_FLAG)
    if abi < 1:
        raise OSError(ctypes.get_errno(), "landlock unavailable")
    handled = READ | WRITE
    if abi < 2:
        handled &= ~FS_REFER
    if abi < 3:
        handled &= ~FS_TRUNCATE
    if abi < 4:
        handled &= ~FS_IOCTL_DEV
    attr = RulesetAttr(handled)
    fd = syscall(SYS_CREATE, ctypes.byref(attr), ctypes.sizeof(attr), 0)
    if fd < 0:
        raise OSError(ctypes.get_errno(), "landlock_create_ruleset")
    def add(path, access):
        pfd = os.open(path, os.O_PATH | os.O_CLOEXEC)
        try:
            beneath = PathBeneath(access & handled, pfd)
            rc = syscall(SYS_ADD, fd, LANDLOCK_RULE_PATH_BENEATH, ctypes.byref(beneath), 0)
            if rc:
                raise OSError(ctypes.get_errno(), "landlock_add_rule " + path)
        finally:
            os.close(pfd)
    try:
        add("/", READ & handled)
        for root in roots:
            add(root, (READ | WRITE) & handled)
        # Python opens /dev/null for write (subprocess, logging). A read-only
        # rule on / does not allow that.
        if os.path.exists("/dev/null"):
            add("/dev/null", (FS_READ_FILE | FS_WRITE_FILE | FS_TRUNCATE) & handled)
        if syscall(SYS_PRCTL, PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0):
            raise OSError(ctypes.get_errno(), "PR_SET_NO_NEW_PRIVS")
        if syscall(SYS_RESTRICT, fd, 0):
            raise OSError(ctypes.get_errno(), "landlock_restrict_self")
    finally:
        os.close(fd)

root = os.environ.pop("SDF_LANDLOCK_ROOT", "")
extra = [part for part in os.environ.pop("SDF_LANDLOCK_WRITE", "").split(os.pathsep) if part]
if not root:
    sys.stderr.write("SDF_LANDLOCK_ROOT is missing\n")
    sys.exit(86)
try:
    apply([root, *extra])
except Exception as exc:
    sys.stderr.write("landlock failed: %s\n" % exc)
    sys.exit(86)
os.execvpe(sys.argv[1], sys.argv[1:], os.environ)
"""


def scrub_credentials(env: Mapping[str, str], *, allowed: Sequence[str] = ()) -> dict[str, str]:
    """Drop credential-shaped variables and any caller-supplied landlock paths.

    ``allowed`` names variables the Attempt's credential scope explicitly
    grants. Everything else that looks like a secret stays out of the child.
    """

    keep = set(allowed)
    cleaned: dict[str, str] = {}
    for key, value in env.items():
        if key.startswith("SDF_LANDLOCK_"):
            continue
        if key not in keep and _SECRET_ENV.search(key):
            continue
        cleaned[str(key)] = str(value)
    return cleaned


def landlock_env(env: Mapping[str, str], root: Path, writable_roots: Sequence[Path] = ()) -> dict[str, str]:
    """Environment for a child that will install Landlock before exec."""

    cleaned = scrub_credentials(env)
    cleaned["SDF_LANDLOCK_ROOT"] = str(Path(root).resolve())
    extras = []
    root_resolved = Path(root).resolve()
    for path in writable_roots:
        resolved = Path(path).resolve()
        if resolved != root_resolved and resolved.is_dir():
            extras.append(str(resolved))
    if extras:
        cleaned["SDF_LANDLOCK_WRITE"] = os.pathsep.join(extras)
    return cleaned


def landlock_command(command: Sequence[str]) -> list[str]:
    """argv that applies the ruleset, then execs ``command``."""

    return [sys.executable, "-c", _BOOTSTRAP, *map(str, command)]


def _already_confined(args: object) -> bool:
    return (
        isinstance(args, (list, tuple))
        and len(args) >= 3
        and args[1] == "-c"
        and args[2] == _BOOTSTRAP
    )


@contextmanager
def confine_workspace_subprocesses(workspace: Path) -> Iterator[None]:
    """While an agent turn runs, a child whose cwd is inside ``workspace`` cannot
    write outside it or read credential env vars that were not allowlisted.

    Host control-plane commands (no workspace cwd) are left alone: the Herdr
    CLI and provider SDK are not the agent's shell. The agent's own
    ``subprocess`` is. This is the live-adapter seam (``RuntimeAgentAdapter``).
    """

    if sys.platform != "linux":
        yield
        return
    root = Path(workspace).resolve()
    original = subprocess.Popen

    def popen(args, *pos, **kwargs):  # type: ignore[no-untyped-def]
        if kwargs.get("shell") or _already_confined(args) or not isinstance(args, (list, tuple)):
            return original(args, *pos, **kwargs)
        cwd = kwargs.get("cwd")
        if cwd is None:
            return original(args, *pos, **kwargs)
        try:
            resolved = Path(cwd).resolve()
            inside = resolved == root or resolved.is_relative_to(root)
        except (OSError, ValueError):
            inside = False
        if not inside:
            return original(args, *pos, **kwargs)
        base = dict(kwargs.get("env") or os.environ)
        kwargs["env"] = landlock_env(base, root)
        return original(landlock_command(args), *pos, **kwargs)

    subprocess.Popen = popen  # type: ignore[method-assign]
    try:
        yield
    finally:
        subprocess.Popen = original  # type: ignore[method-assign]
