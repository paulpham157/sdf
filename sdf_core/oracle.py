"""Sealed evaluator oracle (ADR 0039).

Pytest used to run inside the agent-writable workspace and accept a PASS from
stdout. A helper the test imported was not covered by the fixture hash (ADR
0025), so the agent could print a passing summary and ``os._exit(0)``.

The oracle is the fixture's test entrypoints, ``conftest.py``, support modules
and data files next to those tests. They are copied to a directory outside the
workspace, byte-checked against the fixture, and that directory is the check's
cwd. The code under test is copied from the workspace only when it is not part
of the oracle. Pass/fail for pytest is decided in a process that does not execute the
code under test. That code runs in a worker; the oracle evaluates assertions
on the values it returns. A seccomp filter on Linux x86_64 still rejects
ordinary writes to the result pipe, and fails closed when it cannot be
installed. The pytest process and the notary set ``PR_SET_DUMPABLE`` to 0
so the worker cannot ptrace them or open ``/proc/<pid>/fd``. The worker
itself is denied ``ptrace``, ``process_vm_*``, ``pidfd_getfd``,
``io_uring``, and ``chmod``, and that filter fails closed too. Responses from the worker are
capped at 1 MiB. The sealed tree is mode ``0555``/``0444`` for the run;
the worker's temp directory is not.
"""

from __future__ import annotations

import ast
import hashlib
import json
import os
import shutil
import sys
import tempfile
from collections.abc import Sequence
from dataclasses import dataclass
from pathlib import Path, PurePosixPath

# Directories whose files define the check, not the code under test.
SUPPORT_DIRS = frozenset({"tests", "test", "checks", "fixtures"})
SUPPORT_STEMS = frozenset({"helper", "helpers", "conftest"})
# Workspace copies of these must never become the check's configuration.
CONFIG_NAMES = frozenset({
    "conftest.py", "pytest.ini", "tox.ini", "setup.cfg", "pyproject.toml",
    "ruff.toml", "pytest.toml",
})
SKIP_DIRS = frozenset({".git", "__pycache__", ".pytest_cache", ".sdf-oracle", ".venv", "venv", "node_modules"})
THIRD_PARTY = frozenset({"pytest", "_pytest", "sdf_core"})
MAX_FILE_BYTES = 2_000_000
MAX_FILES = 2_000
REPORT_DIR_NAME = ".sdf-oracle"
PLUGIN_MODULE = "sdf_oracle_plugin"
RESULT_NAME = "result.json"

# Runs inside the check, before pytest imports the code under test. The
# process the sandbox spawned is the notary: it forks the pytest child, and
# only after that child has exited does it write the result file. The child
# never sees SDF_ORACLE_REPORT. A seccomp filter lets the result pipe be
# written only from a stub mapped for the plugin, not from libc (os.write).
# The notary holds the result fd across the run. The worker cannot ptrace
# the pytest process, open its /proc fd directory, or chmod the sealed tree.
PYTEST_SEAL = r'''
import ctypes, os, sys

_WRITE_NR = (1, 18, 20, 44, 46, 278, 296, 307, 328)
_STUB = bytes.fromhex("48c7c0010000000f05c3")

def _stub():
    libc = ctypes.CDLL(None, use_errno=True)
    libc.mmap.restype = ctypes.c_void_p
    libc.mmap.argtypes = [ctypes.c_void_p, ctypes.c_size_t, ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_long]
    for _ in range(8):
        mem = libc.mmap(None, 4096, 7, 0x22, -1, 0)
        if mem and (mem >> 32) == ((mem + 4096) >> 32):
            ctypes.memmove(mem, _STUB, len(_STUB))
            return mem
    raise OSError("oracle stub unavailable")

def _seccomp(fd, lo):
    if sys.platform != "linux" or os.uname().machine not in {"x86_64", "amd64"}:
        raise OSError("oracle channel is sealed only on linux x86_64")
    c_u16, c_u8, c_u32, c_us = ctypes.c_uint16, ctypes.c_uint8, ctypes.c_uint32, ctypes.c_ushort
    class _F(ctypes.Structure):
        _fields_ = [("code", c_u16), ("jt", c_u8), ("jf", c_u8), ("k", c_u32)]
    class _P(ctypes.Structure):
        _fields_ = [("len", c_us), ("filter", ctypes.POINTER(_F))]
    LD, JEQ, JGE, RET = 0x20, 0x15, 0x35, 0x06
    ALLOW, KILL, EPERM, ARCH = 0x7FFF0000, 0, 0x00050001, 0xC000003E
    hi = lo + 16
    ins = []
    def emit(code, jt=0, jf=0, k=0):
        ins.append(_F(code, jt, jf, k & 0xFFFFFFFF)); return len(ins) - 1
    def rel(a, b):
        return b - (a + 1)
    emit(LD, k=4)
    arch = emit(JEQ, k=ARCH)
    kill = emit(RET, k=KILL)
    ins[arch].jt = rel(arch, kill + 1)
    emit(LD, k=0)
    jeqs = [emit(JEQ, k=nr) for nr in _WRITE_NR]
    emit(RET, k=ALLOW)
    handler = len(ins)
    for i in jeqs:
        ins[i].jt = rel(i, handler)
    emit(LD, k=16)
    fdj = emit(JEQ, k=fd & 0xFFFFFFFF)
    allow_fd = emit(RET, k=ALLOW)
    ins[fdj].jt, ins[fdj].jf = rel(fdj, allow_fd + 1), rel(fdj, allow_fd)
    emit(LD, k=20)
    hj = emit(JEQ, k=0)
    allow_h = emit(RET, k=ALLOW)
    ins[hj].jt, ins[hj].jf = rel(hj, allow_h + 1), rel(hj, allow_h)
    emit(LD, k=12)
    rh = emit(JEQ, k=(lo >> 32) & 0xFFFFFFFF)
    emit(LD, k=8)
    rl = emit(JGE, k=lo & 0xFFFFFFFF)
    emit(LD, k=8)
    rhi = emit(JGE, k=hi & 0xFFFFFFFF)
    allow = emit(RET, k=ALLOW)
    deny = emit(RET, k=EPERM)
    ins[rh].jt, ins[rh].jf = 0, rel(rh, deny)
    ins[rl].jt, ins[rl].jf = 0, rel(rl, deny)
    ins[rhi].jt, ins[rhi].jf = rel(rhi, deny), 0
    libc = ctypes.CDLL(None, use_errno=True)
    arr = (_F * len(ins))(*ins)
    prog = _P(len(ins), arr)
    if libc.prctl(38, 1, 0, 0, 0) != 0:
        raise OSError("prctl no_new_privs failed")
    libc.syscall.restype = ctypes.c_long
    if libc.syscall(317, 1, 0, ctypes.byref(prog)) != 0:
        raise OSError("seccomp filter failed")
    return ctypes.CFUNCTYPE(ctypes.c_long, ctypes.c_int, ctypes.c_void_p, ctypes.c_size_t)(lo)

_WORKER = r"""
import importlib, json, os, socket, struct, sys
sys.path.append(os.getcwd())

def _deny_cross_process():
    # Fail closed: no ptrace, no process_vm, no pidfd_getfd, no io_uring, no chmod.
    if sys.platform != "linux" or os.uname().machine not in {"x86_64", "amd64"}:
        os._exit(86)
    import ctypes
    libc = ctypes.CDLL(None, use_errno=True)
    c_u16, c_u8, c_u32, c_us = ctypes.c_uint16, ctypes.c_uint8, ctypes.c_uint32, ctypes.c_ushort
    class _F(ctypes.Structure):
        _fields_ = [("code", c_u16), ("jt", c_u8), ("jf", c_u8), ("k", c_u32)]
    class _P(ctypes.Structure):
        _fields_ = [("len", c_us), ("filter", ctypes.POINTER(_F))]
    LD, JEQ, RET = 0x20, 0x15, 0x06
    ALLOW, KILL, EPERM, ARCH = 0x7FFF0000, 0, 0x00050001, 0xC000003E
    # ptrace, chmod, fchmod, fchmodat, process_vm_readv/writev,
    # io_uring_setup/enter/register, pidfd_open, pidfd_getfd, fchmodat2
    denied = (101, 90, 91, 268, 310, 311, 425, 426, 427, 434, 438, 452)
    ins = []
    def emit(code, jt=0, jf=0, k=0):
        ins.append(_F(code, jt, jf, k & 0xFFFFFFFF)); return len(ins) - 1
    emit(LD, k=4)
    arch = emit(JEQ, k=ARCH)
    kill = emit(RET, k=KILL)
    ins[arch].jt = (kill + 1) - (arch + 1)
    emit(LD, k=0)
    jeqs = [emit(JEQ, k=nr) for nr in denied]
    allow = emit(RET, k=ALLOW)
    deny = emit(RET, k=EPERM)
    for i in jeqs:
        ins[i].jt = deny - (i + 1)
        ins[i].jf = 0
    # PDEATHSIG so this process dies with pytest; then no_new_privs, dumpable, seccomp.
    if (
        libc.prctl(1, 9, 0, 0, 0) != 0
        or libc.prctl(38, 1, 0, 0, 0) != 0
        or libc.prctl(4, 0, 0, 0, 0) != 0
    ):
        os._exit(86)
    arr = (_F * len(ins))(*ins)
    prog = _P(len(ins), arr)
    libc.syscall.restype = ctypes.c_long
    if libc.syscall(317, 1, 0, ctypes.byref(prog)) != 0:
        os._exit(86)

_deny_cross_process()
sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM, fileno=int(sys.argv[1]))
refs = {}
seq = 1

def read_exact(n):
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            return None
        buf += chunk
    return buf

def read_msg():
    hdr = read_exact(4)
    if not hdr:
        return None
    n = struct.unpack(">I", hdr)[0]
    if n > 1000000:
        return None
    body = read_exact(n)
    if body is None:
        return None
    return json.loads(body.decode())

def write_msg(obj):
    body = json.dumps(obj).encode()
    sock.sendall(struct.pack(">I", len(body)) + body)

def store(obj):
    global seq
    refs[seq] = obj
    seq += 1
    return seq - 1

def simple(obj):
    if isinstance(obj, bool) or obj is None or isinstance(obj, (int, str)):
        return True
    if isinstance(obj, float):
        return obj == obj and obj not in (float("inf"), float("-inf"))
    return False

def encode(obj):
    if simple(obj):
        return {"kind": "value", "value": obj}
    if isinstance(obj, (list, tuple)) and all(simple(item) for item in obj):
        return {"kind": "value", "value": list(obj)}
    if isinstance(obj, dict) and all(isinstance(k, str) and simple(v) for k, v in obj.items()):
        return {"kind": "value", "value": obj}
    return {"kind": "ref", "ref": store(obj)}

def decode(obj):
    if isinstance(obj, dict) and "$ref" in obj:
        return refs[obj["$ref"]]
    if isinstance(obj, list):
        return [decode(item) for item in obj]
    if isinstance(obj, dict):
        return {key: decode(value) for key, value in obj.items()}
    return obj

while True:
    msg = read_msg()
    if msg is None:
        break
    try:
        op = msg.get("op")
        if op == "import":
            resp = {"id": msg.get("id"), "ok": True, **encode(importlib.import_module(msg["name"]))}
        elif op == "getattr":
            resp = {"id": msg.get("id"), "ok": True, **encode(getattr(refs[msg["ref"]], msg["name"]))}
        elif op == "call":
            fn = refs[msg["ref"]]
            value = fn(*decode(msg.get("args") or []), **decode(msg.get("kwargs") or {}))
            resp = {"id": msg.get("id"), "ok": True, **encode(value)}
        else:
            resp = {"id": msg.get("id"), "ok": False, "exc": "RuntimeError", "msg": "bad op"}
    except BaseException as exc:
        resp = {"id": msg.get("id"), "ok": False, "exc": type(exc).__name__, "msg": str(exc)[:500]}
    try:
        write_msg(resp)
    except Exception:
        break
"""

class _RPC:
    def __init__(self, sock, proc):
        self.sock = sock
        self.proc = proc
        self.n = 0
    def request(self, payload):
        import json, struct
        self.n += 1
        payload = dict(payload, id=self.n)
        body = json.dumps(payload).encode()
        self.sock.sendall(struct.pack(">I", len(body)) + body)
        hdr = self._exact(4)
        if hdr is None:
            raise ImportError("subject worker exited")
        size = struct.unpack(">I", hdr)[0]
        if size > 1000000:
            self.close()
            raise ImportError("subject response exceeds 1000000 bytes")
        data = self._exact(size)
        if data is None:
            raise ImportError("subject worker exited")
        try:
            parsed = json.loads(data.decode())
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError):
            self.close()
            raise ImportError("malformed subject response")
        if not isinstance(parsed, dict):
            self.close()
            raise ImportError("malformed subject response")
        return parsed
    def _exact(self, n):
        buf = b""
        while len(buf) < n:
            chunk = self.sock.recv(n - len(buf))
            if not chunk:
                return None
            buf += chunk
        return buf
    def close(self):
        try:
            self.sock.close()
        except OSError:
            pass
        if self.proc.poll() is None:
            self.proc.kill()
        try:
            self.proc.wait(timeout=5)
        except Exception:
            pass
        tmp = getattr(self, "tmp", None)
        if tmp:
            import shutil
            shutil.rmtree(tmp, ignore_errors=True)

def _unwrap(rpc, resp):
    if not resp or not resp.get("ok"):
        exc = (resp or {}).get("exc") or "RuntimeError"
        msg = (resp or {}).get("msg") or "subject worker failed"
        if exc == "Skipped":
            import pytest
            pytest.skip(msg)
        if exc in {"SystemExit", "KeyboardInterrupt"}:
            raise RuntimeError("subject process exited: " + msg)
        builtin = getattr(__import__("builtins"), exc, None)
        if isinstance(builtin, type) and issubclass(builtin, BaseException):
            raise builtin(msg)
        raise RuntimeError(exc + ": " + msg)
    if resp.get("kind") == "value":
        return resp.get("value")
    return _Handle(rpc, resp["ref"])

class _Handle:
    def __init__(self, rpc, ref):
        object.__setattr__(self, "_rpc", rpc)
        object.__setattr__(self, "_ref", ref)
    def __call__(self, *args, **kwargs):
        return _unwrap(self._rpc, self._rpc.request({
            "op": "call", "ref": self._ref, "args": _encode_args(args), "kwargs": _encode_kwargs(kwargs),
        }))
    def __getattr__(self, name):
        if name.startswith("_"):
            raise AttributeError(name)
        return _unwrap(self._rpc, self._rpc.request({"op": "getattr", "ref": self._ref, "name": name}))

def _encode_args(args):
    return [_encode_one(item) for item in args]

def _encode_kwargs(kwargs):
    return {key: _encode_one(value) for key, value in kwargs.items()}

def _encode_one(value):
    if isinstance(value, _Handle):
        return {"$ref": value._ref}
    if isinstance(value, tuple):
        return [_encode_one(item) for item in value]
    if isinstance(value, list):
        return [_encode_one(item) for item in value]
    if isinstance(value, dict):
        return {str(key): _encode_one(item) for key, item in value.items()}
    return value

def _install_hook(rpc, names):
    import importlib.machinery
    names = set(names)
    class _Loader:
        def __init__(self, fullname):
            self.fullname = fullname
        def create_module(self, spec):
            return None
        def exec_module(self, module):
            resp = rpc.request({"op": "import", "name": self.fullname})
            if not resp or not resp.get("ok"):
                _unwrap(rpc, resp)
            ref = resp["ref"]
            def __getattr__(self, name, _ref=ref):
                if name.startswith("__"):
                    raise AttributeError(name)
                return _unwrap(rpc, rpc.request({"op": "getattr", "ref": _ref, "name": name}))
            module.__class__ = type("ProxyModule", (type(module),), {"__getattr__": __getattr__})
    class _Finder:
        def find_spec(self, fullname, path, target=None):
            top = fullname.split(".", 1)[0]
            if top not in names:
                return None
            return importlib.machinery.ModuleSpec(fullname, _Loader(fullname))
    sys.meta_path.insert(0, _Finder())

def _freeze_sealed_tree(root):
    """Files and directories the worker shares are not writable by it.

    chmod is denied in the worker, so it cannot undo this. A private temp
    directory is given to the worker for legitimate tempfile use. Any chmod
    failure propagates: the caller exits rather than run with a writable tree.
    """
    for dirpath, _dirnames, filenames in os.walk(root, topdown=False):
        for name in filenames:
            path = os.path.join(dirpath, name)
            if not os.path.islink(path):
                os.chmod(path, 0o444)
        os.chmod(dirpath, 0o555)

def _seal_report_path(report):
    """Freeze what the worker must not replace. The notary already holds the fd.

    A result file inside the cwd means this is the sealed tree: freeze all of
    it. A private report (absolute tests) is only its own directory, so the
    workspace the check was pointed at stays writable.
    """
    if not report:
        os._exit(86)
    cwd = os.path.realpath(os.getcwd())
    real = os.path.realpath(report)
    if real == cwd or real.startswith(cwd + os.sep):
        _freeze_sealed_tree(cwd)
        return
    if os.path.islink(report) or not os.path.isfile(report):
        os._exit(86)
    os.chmod(report, 0o444)
    directory = os.path.dirname(report)
    if not directory or os.path.islink(directory) or not os.path.isdir(directory):
        os._exit(86)
    os.chmod(directory, 0o555)

def _start_worker(result_fd):
    import socket, subprocess, tempfile
    parent, child = socket.socketpair()
    child.set_inheritable(True)
    try:
        os.set_inheritable(result_fd, False)
    except OSError:
        pass
    worker_tmp = tempfile.mkdtemp(prefix="sdf-subject-", dir="/tmp")
    env = {key: value for key, value in os.environ.items() if not key.startswith("SDF_ORACLE_")}
    env["TMPDIR"] = env["TMP"] = env["TEMP"] = worker_tmp
    proc = subprocess.Popen(
        [sys.executable, "-I", "-B", "-c", _WORKER, str(child.fileno())],
        pass_fds=(child.fileno(),),
        close_fds=True,
        cwd=os.getcwd(),
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    child.close()
    rpc = _RPC(parent, proc)
    rpc.tmp = worker_tmp
    return rpc

def _plugin(fd, write, rpc):
    import json, types
    mod = types.ModuleType("sdf_oracle_plugin")
    def pytest_sessionfinish(session, exitstatus):
        if rpc is not None:
            rpc.close()
        reporter = session.config.pluginmanager.get_plugin("terminalreporter")
        stats = getattr(reporter, "stats", {}) if reporter is not None else {}
        def count(key):
            return len(stats.get(key) or ())
        passed, failed = count("passed"), count("failed")
        skipped, errors = count("skipped"), count("error")
        total = passed + failed + skipped + errors
        payload = json.dumps({
            "collected": total,
            "testscollected": int(getattr(session, "testscollected", 0) or 0),
            "passed": passed, "failed": failed, "skipped": skipped, "errors": errors,
            "exitstatus": int(exitstatus),
        }).encode()
        buf = ctypes.create_string_buffer(payload)
        write(fd, buf, len(payload))
    mod.pytest_sessionfinish = pytest_sessionfinish
    sys.modules["sdf_oracle_plugin"] = mod

def _undumpable():
    libc = ctypes.CDLL(None, use_errno=True)
    # Same-uid peers cannot ptrace or open /proc/<pid>/fd when this is 0.
    if libc.prctl(4, 0, 0, 0, 0) != 0:
        os._exit(86)

def _notary():
    report = os.environ.pop("SDF_ORACLE_REPORT", "")
    _undumpable()
    # Open the result before the worker exists. Writes later use this fd, so
    # the directory can stay mode 0555 for the whole run (no chmod window).
    report_fd = -1
    if report:
        flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC
        if hasattr(os, "O_NOFOLLOW"):
            flags |= os.O_NOFOLLOW
        try:
            report_fd = os.open(report, flags, 0o600)
            os.set_inheritable(report_fd, False)
        except OSError:
            os._exit(86)
    read_fd, write_fd = os.pipe()
    pid = os.fork()
    if pid == 0:
        os.close(read_fd)
        if report_fd >= 0:
            os.close(report_fd)
        _undumpable()
        subjects = [item for item in os.environ.pop("SDF_ORACLE_SUBJECTS", "").split(",") if item]
        rpc = None
        if subjects:
            try:
                _seal_report_path(report)
            except OSError:
                os._exit(86)
            rpc = _start_worker(write_fd)
        try:
            stub = _stub()
            write = _seccomp(write_fd, stub)
        except OSError:
            if rpc is not None:
                rpc.close()
            os._exit(86)
        _plugin(write_fd, write, rpc)
        if subjects:
            _install_hook(rpc, subjects)
        return
    os.close(write_fd)
    chunks, size = [], 0
    while size <= 100000:
        buf = os.read(read_fd, 65536)
        if not buf:
            break
        chunks.append(buf)
        size += len(buf)
    data = b"".join(chunks)
    _pid, status = os.waitpid(pid, 0)
    if report_fd >= 0:
        try:
            view = memoryview(data)
            while view:
                n = os.write(report_fd, view)
                if n <= 0:
                    break
                view = view[n:]
            os.close(report_fd)
        except OSError:
            pass
    os._exit(os.WEXITSTATUS(status) if os.WIFEXITED(status) else 1)

_notary()
'''


def _thaw_tree(root: Path) -> None:
    """Undo the worker-time freeze so the evaluator can delete the tree.

    The notary writes the result through an fd it opened before the freeze,
    so the tree stays mode 0555/0444 until the check has been read. Cleanup
    then sets owner-only modes. Restoring the pre-freeze modes would put
    world-readable 0644 files and 0755 directories back, and the owner write
    bit on 0700 directories is what ``rmtree`` needs.
    """

    if not root.exists():
        return
    for dirpath, _dirnames, filenames in os.walk(root, topdown=False):
        for name in filenames:
            path = os.path.join(dirpath, name)
            if os.path.islink(path):
                continue
            try:
                os.chmod(path, 0o600)
            except OSError:
                pass
        try:
            os.chmod(dirpath, 0o700)
        except OSError:
            pass


class OracleTamper(Exception):
    """An oracle file is missing, replaced, or not safe to run."""

    def __init__(self, reason: str):
        super().__init__(reason)
        self.reason = reason


@dataclass(frozen=True)
class SealedOracle:
    """A hash-checked copy of the oracle plus the workspace's subject files."""

    root: Path
    oracle_paths: tuple[str, ...]
    oracle_digest: str
    fixture: Path

    def changed(self) -> bool:
        """True when a sealed oracle file no longer matches the fixture."""

        for relative in self.oracle_paths:
            if _read_plain(self.root, relative) != _read_plain(self.fixture, relative):
                return True
        return False

    def cleanup(self) -> None:
        _thaw_tree(self.root)
        shutil.rmtree(self.root, ignore_errors=True)


# Top-level modules the pytest process must not import. They run in the subject
# worker. pytest itself and the stdlib are never proxied, even if a workspace
# file shadows them (that shadow stays on disk and is not loaded here).
_NOT_SUBJECT = frozenset({"pytest", "_pytest", "py", "pluggy", "conftest", "sdf_oracle_plugin"})


def subject_module_names(root: Path, oracle_paths: set[str]) -> str:
    """Comma-separated top-level module names under ``root`` that are not oracle files."""

    names: set[str] = set()
    if not root.is_dir():
        return ""
    stdlib = getattr(sys, "stdlib_module_names", frozenset())
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [name for name in dirnames if name not in SKIP_DIRS and not name.startswith(".")]
        for name in filenames:
            if not name.endswith(".py"):
                continue
            relative = Path(dirpath, name).relative_to(root).as_posix()
            if relative in oracle_paths or relative.startswith(REPORT_DIR_NAME + "/"):
                continue
            top = relative.split("/", 1)[0]
            module = top[:-3] if top.endswith(".py") else top
            if not module.isidentifier() or module in _NOT_SUBJECT or module in stdlib:
                continue
            names.add(module)
    return ",".join(sorted(names))


@dataclass(frozen=True)
class OracleReport:
    """Directory the notary writes the result frame into, after the child exits."""

    directory: Path
    result_path: Path

    def cleanup(self) -> None:
        if self.directory.exists():
            _thaw_tree(self.directory)
            shutil.rmtree(self.directory, ignore_errors=True)


def materialize(fixture: Path, workspace: Path, targets: Sequence[str]) -> SealedOracle:
    """Build the sealed tree. Raises :class:`OracleTamper` instead of running."""

    fixture = Path(fixture)
    workspace = Path(workspace)
    oracle_paths, planted = discover_oracle(fixture, workspace, targets)
    if planted:
        name = planted[0]
        raise OracleTamper(f"oracle import {name} is not in the server fixture (the agent added it)")
    for relative in oracle_paths:
        expected = _read_plain(fixture, relative)
        if expected is None:
            raise OracleTamper(f"oracle file {relative} is not a regular file in the server fixture")
        actual = _read_plain(workspace, relative)
        if actual is None:
            raise OracleTamper(
                f"oracle file {relative} is missing from the workspace (the agent removed or replaced it)"
            )
        if actual != expected:
            raise OracleTamper(
                f"oracle file {relative} differs from the server fixture (the agent changed or replaced it)"
            )
    root = Path(tempfile.mkdtemp(prefix="sdf-oracle-"))
    try:
        workspace_root = workspace.resolve()
        if root.resolve() == workspace_root or root.resolve().is_relative_to(workspace_root):
            raise OracleTamper("sealed oracle would lie inside the agent workspace")
        digest_lines: list[str] = []
        for relative in oracle_paths:
            data = _read_plain(fixture, relative)
            assert data is not None
            _place(root, relative, data, readonly=True)
            digest_lines.append(f"{relative}\0{hashlib.sha256(data).hexdigest()}")
        copied = 0
        for dirpath, dirnames, filenames in os.walk(workspace, followlinks=False):
            dirnames[:] = [name for name in dirnames if name not in SKIP_DIRS]
            for name in filenames:
                path = Path(dirpath) / name
                relative = path.relative_to(workspace).as_posix()
                if relative in oracle_paths or _skip_workspace_file(relative, name):
                    continue
                if path.is_symlink():
                    raise OracleTamper(f"subject file {relative} is a symlink")
                if not path.is_file():
                    continue
                size = path.stat().st_size
                if size > MAX_FILE_BYTES:
                    raise OracleTamper(f"subject file {relative} is too large to seal")
                copied += 1
                if copied + len(oracle_paths) > MAX_FILES:
                    raise OracleTamper("workspace is too large to seal")
                _place(root, relative, path.read_bytes(), readonly=False)
        digest = hashlib.sha256("\n".join(digest_lines).encode()).hexdigest()
        return SealedOracle(root=root, oracle_paths=oracle_paths, oracle_digest=digest, fixture=fixture)
    except Exception:
        shutil.rmtree(root, ignore_errors=True)
        raise


def discover_oracle(
    fixture: Path, workspace: Path, targets: Sequence[str],
) -> tuple[tuple[str, ...], tuple[str, ...]]:
    """Oracle paths (sorted) and support imports that exist only in the workspace."""

    oracle: set[str] = set()
    for target in targets:
        if target and not os.path.isabs(target):
            oracle.add(PurePosixPath(target).as_posix())
    if fixture.is_dir():
        for dirpath, dirnames, filenames in os.walk(fixture, followlinks=False):
            dirnames[:] = [name for name in dirnames if name not in SKIP_DIRS]
            for name in filenames:
                if name != "conftest.py":
                    continue
                path = Path(dirpath) / name
                if path.is_symlink() or not path.is_file():
                    raise OracleTamper(f"oracle file {path.relative_to(fixture).as_posix()} is not a regular file")
                oracle.add(path.relative_to(fixture).as_posix())
    planted: list[str] = []
    queue = [path for path in sorted(oracle) if path.endswith(".py")]
    seen: set[str] = set()
    while queue:
        relative = queue.pop()
        if relative in seen:
            continue
        seen.add(relative)
        raw = _read_plain(fixture, relative)
        if raw is None:
            continue
        try:
            tree = ast.parse(raw.decode("utf-8"))
        except (UnicodeDecodeError, SyntaxError):
            continue
        importer = relative
        names = list(_import_names(tree)) + [(name, False) for name in _plugin_names(tree)]
        for name, is_relative in names:
            if _is_stdlib_or_third_party(name):
                continue
            resolved = _resolve_module(fixture, importer, name)
            if resolved is not None and _is_support(resolved):
                if resolved not in oracle:
                    oracle.add(resolved)
                    if resolved.endswith(".py"):
                        queue.append(resolved)
                continue
            if resolved is None and (_is_support_name(name) or is_relative):
                if _resolve_module(workspace, importer, name) is not None:
                    planted.append(name)
    _add_data_files(fixture, oracle)
    return tuple(sorted(oracle)), tuple(dict.fromkeys(planted))


def declared_test_count(root: Path, node_ids: Sequence[str]) -> int:
    """How many test functions the oracle files declare (a lower bound).

    Parametrize may make pytest report more cases than this count. It must
    not report fewer.
    """

    total = 0
    for node_id in node_ids:
        relative, _, node = node_id.partition("::")
        if os.path.isabs(relative):
            path = Path(relative)
        else:
            path = root.joinpath(*PurePosixPath(relative).parts)
        try:
            tree = ast.parse(path.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError, SyntaxError):
            continue
        names = _test_names(tree)
        if node:
            total += 1 if node in names or node.split("[", 1)[0] in names else 0
        else:
            total += sum(1 for name in names if "::" not in name)
    return total


def prepare_sealed_report(sealed: SealedOracle) -> OracleReport:
    """Directory inside the sealed tree where the notary drops the result frame."""

    directory = sealed.root / REPORT_DIR_NAME
    directory.mkdir(mode=0o755)
    return OracleReport(directory=directory, result_path=directory / RESULT_NAME)


def prepare_private_report() -> OracleReport:
    """A report directory outside any workspace, for a check that is not sealed.

    Absolute server-owned test paths cannot move into the sealed tree without
    changing the argv. The report still lives somewhere the workspace is not.
    """

    directory = Path(tempfile.mkdtemp(prefix="sdf-oracle-report-"))
    return OracleReport(directory=directory, result_path=directory / RESULT_NAME)


def retarget_pytest(argv: Sequence[str], *, sealed: bool) -> list[str]:
    """Load the in-memory oracle plugin. A sealed run also drops workspace discovery.

    There is no ``--junitxml`` path. The child is not told where the result
    file will be written; the notary writes it after the child has exited.
    """

    if not sealed:
        return [*argv, "-p", PLUGIN_MODULE]
    out: list[str] = []
    index = 0
    while index < len(argv):
        part = argv[index]
        if part == "--noconftest":
            index += 1
            continue
        if part == "--rootdir" and index + 1 < len(argv):
            index += 2
            continue
        out.append(part)
        index += 1
    extra = ["--rootdir", ".", "--confcutdir", ".", "-p", PLUGIN_MODULE]
    for index, part in enumerate(out):
        if part == "pytest" or os.path.basename(part) == "pytest":
            return [*out[: index + 1], *extra, *out[index + 1 :]]
    return [*extra, *out]


def pytest_oracle_verdict(
    *,
    exit_code: int | None,
    frame: dict[str, int] | None,
    declared: int,
    oracle_changed: bool,
) -> tuple[str, str]:
    """``(status, reason)`` from the notary's result frame. Never from stdout.

    A missing frame after exit 0 is an unexpected early exit (``os._exit``
    before the plugin's stub ran). Zero collected tests and tests that were
    only skipped are failures. A frame the counts cannot support is
    inconclusive — not a pass.
    """

    if oracle_changed:
        return "INCONCLUSIVE", "oracle files changed while the check ran"
    if frame is None:
        if exit_code == 0:
            return "FAIL", "pytest exited 0 without an evaluator result (unexpected early exit)"
        return "FAIL", "pytest ended without an evaluator result"
    if frame["failed"] or frame["errors"]:
        return "FAIL", "pytest reported failing tests"
    if frame["testscollected"] == 0 or frame["collected"] == 0:
        return "FAIL", "pytest collected zero tests"
    if frame["collected"] != frame["testscollected"]:
        return "INCONCLUSIVE", "evaluator result counts do not agree"
    if declared and frame["collected"] < declared:
        return "FAIL", f"pytest reported {frame['collected']} tests but the oracle declares {declared}"
    if frame["passed"] == 0:
        return "FAIL", (
            f"pytest exited with no test passed (skipped={frame['skipped']}); skipped tests are not a run"
        )
    if exit_code != 0 or frame["exitstatus"] != 0:
        return "INCONCLUSIVE", (
            f"pytest exit code {exit_code} disagrees with a passing result (frame exit {frame['exitstatus']})"
        )
    return "PASS", ""


def load_result_frame(path: Path) -> dict[str, int] | None:
    """The notary's result file, or ``None`` when it is missing or not a frame."""

    try:
        if not path.is_file() or path.is_symlink() or path.stat().st_size > 100_000:
            return None
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, ValueError):
        return None
    if not isinstance(payload, dict):
        return None
    parsed: dict[str, int] = {}
    for key in ("collected", "testscollected", "passed", "failed", "skipped", "errors", "exitstatus"):
        value = payload.get(key)
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            return None
        parsed[key] = value
    if parsed["passed"] + parsed["failed"] + parsed["skipped"] + parsed["errors"] != parsed["collected"]:
        return None
    return parsed


def _import_names(tree: ast.AST) -> list[tuple[str, bool]]:
    found: list[tuple[str, bool]] = []
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                found.append((alias.name.split(".", 1)[0], False))
        elif isinstance(node, ast.ImportFrom):
            if node.level and node.level > 0:
                if node.module:
                    found.append((node.module.split(".", 1)[0], True))
                else:
                    for alias in node.names:
                        found.append((alias.name.split(".", 1)[0], True))
            elif node.module:
                found.append((node.module.split(".", 1)[0], False))
    return found


def _plugin_names(tree: ast.AST) -> list[str]:
    names: list[str] = []
    for node in ast.walk(tree):
        if not isinstance(node, ast.Assign):
            continue
        if not any(isinstance(target, ast.Name) and target.id == "pytest_plugins" for target in node.targets):
            continue
        names.extend(_constant_strings(node.value))
    return names


def _constant_strings(node: ast.AST) -> list[str]:
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return [node.value.split(".", 1)[0]]
    if isinstance(node, (ast.List, ast.Tuple)):
        found: list[str] = []
        for element in node.elts:
            found.extend(_constant_strings(element))
        return found
    return []


def _test_names(tree: ast.AST) -> set[str]:
    names: set[str] = set()
    for node in getattr(tree, "body", ()):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name.startswith("test_"):
            names.add(node.name)
        elif isinstance(node, ast.ClassDef):
            for item in node.body:
                if isinstance(item, (ast.FunctionDef, ast.AsyncFunctionDef)) and item.name.startswith("test_"):
                    names.add(item.name)
                    names.add(f"{node.name}::{item.name}")
    return names


def _is_stdlib_or_third_party(name: str) -> bool:
    if name in THIRD_PARTY:
        return True
    stdlib = getattr(sys, "stdlib_module_names", frozenset())
    return name in stdlib


def _is_support_name(name: str) -> bool:
    return name in SUPPORT_STEMS or name.startswith("test_") or name.startswith("conftest")


def _is_support(relative: str) -> bool:
    path = PurePosixPath(relative)
    if path.name == "conftest.py" or path.stem in SUPPORT_STEMS or path.stem.startswith("test_"):
        return True
    return any(part in SUPPORT_DIRS for part in path.parts[:-1])


def _skip_workspace_file(relative: str, name: str) -> bool:
    """Drop check machinery from the subject copy. Keep a workspace ``helper.py``.

    A helper the oracle imports is already an oracle path, so the caller does
    not copy the workspace bytes over the fixture. A helper the oracle does
    not import is code under test: a ``python -c`` check can import it, and
    main's path finder still has to be able to load it. Config, ``conftest``,
    test modules and files under test directories are the check itself.
    """

    if name in CONFIG_NAMES:
        return True
    path = PurePosixPath(relative)
    if path.name == "conftest.py" or path.stem.startswith("test_"):
        return True
    return any(part in SUPPORT_DIRS for part in path.parts[:-1])


def _resolve_module(root: Path, importer: str, name: str) -> str | None:
    importer_dir = PurePosixPath(importer).parent.as_posix()
    candidates = []
    if importer_dir not in ("", "."):
        candidates.append(f"{importer_dir}/{name}.py")
        candidates.append(f"{importer_dir}/{name}/__init__.py")
    candidates.append(f"{name}.py")
    candidates.append(f"{name}/__init__.py")
    seen: set[str] = set()
    for candidate in candidates:
        candidate = PurePosixPath(candidate).as_posix()
        if candidate in seen:
            continue
        seen.add(candidate)
        if _read_plain(root, candidate) is not None:
            return candidate
    return None


def _add_data_files(fixture: Path, oracle: set[str]) -> None:
    """Non-Python files under a sealed test directory are fixtures, not agent input."""

    roots: set[str] = set()
    for path in oracle:
        parent = PurePosixPath(path).parent.as_posix()
        if parent in ("", "."):
            continue
        parts = PurePosixPath(parent).parts
        if any(part in SUPPORT_DIRS for part in parts):
            # Seal from the outermost support directory so nested data is included.
            top = next(index for index, part in enumerate(parts) if part in SUPPORT_DIRS)
            roots.add(PurePosixPath(*parts[: top + 1]).as_posix())
    for directory in sorted(roots):
        folder = fixture.joinpath(*PurePosixPath(directory).parts)
        if not folder.is_dir() or folder.is_symlink():
            continue
        for dirpath, dirnames, filenames in os.walk(folder, followlinks=False):
            dirnames[:] = [name for name in dirnames if name not in SKIP_DIRS]
            for name in filenames:
                child = Path(dirpath) / name
                if child.is_symlink() or not child.is_file() or child.suffix == ".py" or name in CONFIG_NAMES:
                    continue
                oracle.add(child.relative_to(fixture).as_posix())


def _place(root: Path, relative: str, data: bytes, *, readonly: bool) -> None:
    if len(data) > MAX_FILE_BYTES:
        raise OracleTamper(f"oracle file {relative} is too large to seal")
    target = root.joinpath(*PurePosixPath(relative).parts)
    if target.exists() or target.is_symlink():
        raise OracleTamper(f"oracle path {relative} collided while sealing")
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(data)
    target.chmod(0o444 if readonly else 0o644)


def _read_plain(root: Path, relative: str) -> bytes | None:
    """Bytes of ``root/relative`` when every component is a real file inside ``root``."""

    current = Path(root)
    for part in PurePosixPath(relative).parts:
        if part in ("", ".", ".."):
            return None
        current = current / part
        if current.is_symlink():
            return None
    try:
        if not current.resolve().is_relative_to(Path(root).resolve()) or not current.is_file():
            return None
        if current.stat().st_size > MAX_FILE_BYTES:
            return None
        return current.read_bytes()
    except OSError:
        return None
