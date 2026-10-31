"""Sealed evaluator oracle (ADR 0034).

Pytest used to run inside the agent-writable workspace and accept a PASS from
stdout. A helper the test imported was not covered by the fixture hash (ADR
0025), so the agent could print a passing summary and ``os._exit(0)``.

The oracle is the fixture's test entrypoints, ``conftest.py``, support modules
and data files next to those tests. They are copied to a directory outside the
workspace, byte-checked against the fixture, and that directory is the check's
cwd. The code under test is copied from the workspace only when it is not part
of the oracle. Pass/fail for pytest comes from a result frame the plugin
writes through a pipe the subject cannot see. A seccomp filter on Linux
x86_64 rejects ordinary writes to that pipe.
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

def _plugin(fd, write):
    import json, types
    mod = types.ModuleType("sdf_oracle_plugin")
    def pytest_sessionfinish(session, exitstatus):
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

def _notary():
    report = os.environ.pop("SDF_ORACLE_REPORT", "")
    read_fd, write_fd = os.pipe()
    pid = os.fork()
    if pid == 0:
        os.close(read_fd)
        try:
            stub = _stub()
            write = _seccomp(write_fd, stub)
        except OSError:
            os._exit(86)
        _plugin(write_fd, write)
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
    if report:
        try:
            out = os.open(report, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | getattr(os, "O_NOFOLLOW", 0), 0o600)
            os.write(out, data)
            os.close(out)
        except OSError:
            pass
    _pid, status = os.waitpid(pid, 0)
    os._exit(os.WEXITSTATUS(status) if os.WIFEXITED(status) else 1)

_notary()
'''


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
        shutil.rmtree(self.root, ignore_errors=True)


@dataclass(frozen=True)
class OracleReport:
    """Directory the notary writes the result frame into, after the child exits."""

    directory: Path
    result_path: Path

    def cleanup(self) -> None:
        if self.directory.exists() and self.directory.name == REPORT_DIR_NAME:
            shutil.rmtree(self.directory, ignore_errors=True)
        elif self.directory.exists():
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
    if name in CONFIG_NAMES:
        return True
    return _is_support(relative)


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
