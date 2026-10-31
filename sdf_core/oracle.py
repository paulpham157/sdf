"""Sealed evaluator oracle (ADR 0034).

Pytest used to run inside the agent-writable workspace and accept a PASS from
stdout. A helper the test imported was not covered by the fixture hash (ADR
0025), so the agent could print a passing summary and ``os._exit(0)``.

The oracle is the fixture's test entrypoints, ``conftest.py``, support modules
and data files next to those tests. They are copied to a directory outside the
workspace, byte-checked against the fixture, and that directory is the check's
cwd. The code under test is copied from the workspace only when it is not part
of the oracle. Pass/fail for pytest comes from a JUnit report and a completion
marker the evaluator's own plugin writes, checked against the exit code and
the number of tests the oracle declares.
"""

from __future__ import annotations

import ast
import hashlib
import hmac
import json
import os
import shutil
import sys
import tempfile
import xml.etree.ElementTree as ET
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
MAX_JUNIT_BYTES = 2_000_000
REPORT_DIR_NAME = ".sdf-oracle"
PLUGIN_MODULE = "sdf_oracle_plugin"

# Loaded by the evaluator (-p), never from the workspace. It records a
# completion marker only from pytest_sessionfinish, which os._exit skips.
PLUGIN_SOURCE = '''\
"""Evaluator-owned pytest plugin (ADR 0034). Do not import from a check."""


def pytest_sessionfinish(session, exitstatus):
    import json
    import os
    from pathlib import Path

    marker = os.environ.get("SDF_ORACLE_MARKER")
    if not marker:
        return
    reporter = session.config.pluginmanager.get_plugin("terminalreporter")
    stats = getattr(reporter, "stats", {}) if reporter is not None else {}

    def count(key):
        return len(stats.get(key) or ())

    Path(marker).write_text(json.dumps({
        "token": os.environ.get("SDF_ORACLE_TOKEN", ""),
        "collected": int(getattr(session, "testscollected", 0) or 0),
        "passed": count("passed"),
        "failed": count("failed"),
        "skipped": count("skipped"),
        "errors": count("error"),
        "exitstatus": int(exitstatus),
    }), encoding="utf-8")
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
    """Where the evaluator, not the agent, reads the check's result."""

    directory: Path
    junit_arg: str
    marker_arg: str
    plugin_dir_arg: str
    token: str

    @property
    def junit_path(self) -> Path:
        return self.directory / "junit.xml"

    @property
    def marker_path(self) -> Path:
        return self.directory / "marker.json"

    def cleanup(self) -> None:
        if self.directory.exists() and self.directory.name == REPORT_DIR_NAME:
            shutil.rmtree(self.directory, ignore_errors=True)
        elif self.directory.exists():
            shutil.rmtree(self.directory, ignore_errors=True)


@dataclass(frozen=True)
class JunitReport:
    tests: int
    passed: int
    failed: int
    errors: int
    skipped: int


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
    """Report files inside the sealed tree, addressed relatively for the child."""

    import secrets

    directory = sealed.root / REPORT_DIR_NAME
    directory.mkdir(mode=0o755)
    (directory / f"{PLUGIN_MODULE}.py").write_text(PLUGIN_SOURCE, encoding="utf-8")
    os.chmod(directory / f"{PLUGIN_MODULE}.py", 0o644)
    return OracleReport(
        directory=directory,
        junit_arg=f"{REPORT_DIR_NAME}/junit.xml",
        marker_arg=f"{REPORT_DIR_NAME}/marker.json",
        plugin_dir_arg=REPORT_DIR_NAME,
        token=secrets.token_hex(16),
    )


def prepare_private_report() -> OracleReport:
    """A report directory outside any workspace, for a check that is not sealed.

    Absolute server-owned test paths cannot move into the sealed tree without
    changing the argv. The report still lives somewhere the workspace is not.
    """

    import secrets

    directory = Path(tempfile.mkdtemp(prefix="sdf-oracle-report-"))
    (directory / f"{PLUGIN_MODULE}.py").write_text(PLUGIN_SOURCE, encoding="utf-8")
    return OracleReport(
        directory=directory,
        junit_arg=str(directory / "junit.xml"),
        marker_arg=str(directory / "marker.json"),
        plugin_dir_arg=str(directory),
        token=secrets.token_hex(16),
    )


def retarget_pytest(argv: Sequence[str], *, junit: str, sealed: bool) -> list[str]:
    """Point pytest at the evaluator's report. A sealed run also drops workspace discovery."""

    if not sealed:
        return [*argv, "--junitxml", junit, "-p", PLUGIN_MODULE]
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
    extra = ["--rootdir", ".", "--confcutdir", ".", "--junitxml", junit, "-p", PLUGIN_MODULE]
    for index, part in enumerate(out):
        if part == "pytest" or os.path.basename(part) == "pytest":
            return [*out[: index + 1], *extra, *out[index + 1 :]]
    return [*extra, *out]


def pytest_oracle_verdict(
    *,
    exit_code: int | None,
    report: OracleReport,
    declared: int,
    oracle_changed: bool,
) -> tuple[str, str]:
    """``(status, reason)`` from the evaluator's own report. Never from stdout.

    A missing marker or report after exit 0 is an unexpected early exit
    (``os._exit``, a forged summary). Zero collected tests and tests that were
    only skipped are failures. Disagreement between the marker, the JUnit
    report and the exit code is inconclusive — not a pass.
    """

    if oracle_changed:
        return "INCONCLUSIVE", "oracle files changed while the check ran"
    marker = _read_marker(report)
    junit = _read_junit(report)
    if marker is None:
        if exit_code == 0:
            return "FAIL", "pytest exited 0 without an evaluator completion marker (unexpected early exit)"
        return "FAIL", "pytest ended without an evaluator completion marker"
    if junit is None:
        if exit_code == 0:
            return "FAIL", "pytest exited 0 without a junit report (unexpected early exit)"
        return "FAIL", "pytest ended without a junit report"
    accounted = marker["passed"] + marker["failed"] + marker["skipped"] + marker["errors"]
    if marker["collected"] == 0 or junit.tests == 0:
        return "FAIL", "pytest collected zero tests"
    if declared and junit.tests < declared:
        return "FAIL", f"junit reported {junit.tests} tests but the oracle declares {declared}"
    if accounted != junit.tests or marker["collected"] < junit.tests:
        return "INCONCLUSIVE", "junit report does not match the evaluator completion marker"
    if junit.failed or junit.errors or marker["failed"] or marker["errors"]:
        return "FAIL", "pytest reported failing tests"
    if junit.passed == 0:
        return "FAIL", f"pytest exited with no test passed (skipped={junit.skipped}); skipped tests are not a run"
    if exit_code != 0 or marker["exitstatus"] != 0:
        return "INCONCLUSIVE", (
            f"pytest exit code {exit_code} disagrees with a passing junit report "
            f"(marker exit {marker['exitstatus']})"
        )
    if junit.passed != marker["passed"]:
        return "INCONCLUSIVE", "junit passed-count does not match the evaluator completion marker"
    return "PASS", ""


def _read_marker(report: OracleReport) -> dict[str, int | str] | None:
    path = report.marker_path
    try:
        if not path.is_file() or path.is_symlink() or path.stat().st_size > 100_000:
            return None
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, ValueError):
        return None
    if not isinstance(payload, dict):
        return None
    token = payload.get("token")
    if not isinstance(token, str) or len(token) != len(report.token) or not hmac.compare_digest(token, report.token):
        return None
    parsed: dict[str, int | str] = {"token": token}
    for key in ("collected", "passed", "failed", "skipped", "errors", "exitstatus"):
        value = payload.get(key)
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            return None
        parsed[key] = value
    return parsed


def _read_junit(report: OracleReport) -> JunitReport | None:
    path = report.junit_path
    try:
        if not path.is_file() or path.is_symlink():
            return None
        data = path.read_bytes()
    except OSError:
        return None
    if not data or len(data) > MAX_JUNIT_BYTES:
        return None
    folded = data.upper()
    if b"<!DOCTYPE" in folded or b"<!ENTITY" in folded:
        return None
    try:
        root = ET.fromstring(data)
    except ET.ParseError:
        return None
    suites = list(root) if root.tag == "testsuites" else [root] if root.tag == "testsuite" else []
    if not suites:
        return None
    tests = passed = failed = errors = skipped = 0
    for suite in suites:
        if suite.tag != "testsuite":
            continue
        for case in suite.findall("testcase"):
            tests += 1
            children = {child.tag for child in list(case)}
            if "failure" in children:
                failed += 1
            elif "error" in children:
                errors += 1
            elif "skipped" in children:
                skipped += 1
            else:
                passed += 1
    if tests != passed + failed + errors + skipped:
        return None
    return JunitReport(tests=tests, passed=passed, failed=failed, errors=errors, skipped=skipped)


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
