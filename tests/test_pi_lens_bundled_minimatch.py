"""GHSA-g3hv-75wg-m249 in the minimatch copy packaged inside pi-lens.

pi-lens 4.4.6 inlined minimatch 10.2.6. Its ``firstPhasePreProcess`` doubles
glob parts for ``**/..`` whenever ``optimizationLevel >= 2``, and it ignores
``braceExpandMax``. pi-lens 4.4.8 is the first published tarball whose entry
and MCP chunk inline the capped preprocessor (``braceExpandMax`` default
``1e5``, stop when ``globParts.length`` reaches it). Peers of
``@earendil-works`` stay optional, so ``npm ci --legacy-peer-deps`` still
installs no second Pi.

This runs ``npm ci`` and executes both algorithms on the same bounded
pattern. The part cap aborts a preprocessor that does not stop.
"""

import base64
import hashlib
import io
import json
import shutil
import subprocess
import tarfile
import urllib.request
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
EXT = ROOT / "infra/e2b/herdr-agents/pi-extensions"
LOCK = EXT / "package-lock.json"
PROBE = Path(__file__).with_name("pi_lens_minimatch_probe.mjs")
VERIFY = EXT / "verify-pins.mjs"
MINIMATCH_10_2_6 = (
    "https://registry.npmjs.org/minimatch/-/minimatch-10.2.6.tgz",
    "sha512-vpLQEs+VLCr1nU0BXS07maYoFwlDAH0gngQuuttxIwutDFEMHq2blX+8vpgxDdK3J1PwjCJiep77OitTZ4Ll1A==",
)
LIBC_PACKAGES = (
    "node_modules/@ast-grep/cli-linux-arm64-gnu",
    "node_modules/@ast-grep/cli-linux-x64-gnu",
    "node_modules/@ast-grep/napi-linux-arm64-gnu",
    "node_modules/@ast-grep/napi-linux-arm64-musl",
    "node_modules/@ast-grep/napi-linux-x64-gnu",
    "node_modules/@ast-grep/napi-linux-x64-musl",
)


def _sri(blob: bytes) -> str:
    return "sha512-" + base64.b64encode(hashlib.sha512(blob).digest()).decode()


def _download(url: str, integrity: str) -> bytes:
    assert url.startswith("https://registry.npmjs.org/"), url
    with urllib.request.urlopen(url, timeout=60) as response:
        blob = response.read()
    assert _sri(blob) == integrity, url
    return blob


def _extract_npm(blob: bytes, dest: Path) -> None:
    root = dest.resolve()
    with tarfile.open(fileobj=io.BytesIO(blob), mode="r:gz") as tar:
        for member in tar.getmembers():
            name = member.name
            if not name.startswith("package/") or ".." in Path(name).parts:
                raise AssertionError(name)
            if member.issym() or member.islnk():
                raise AssertionError(f"link in tarball: {name}")
            relative = Path(name.removeprefix("package/"))
            if relative.as_posix() in ("", "."):
                continue
            target = (dest / relative).resolve()
            if target != root and root not in target.parents:
                raise AssertionError(name)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
                continue
            source = tar.extractfile(member)
            if source is None:
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(source.read())


def _lock():
    return json.loads(LOCK.read_text())


def test_packaged_pi_lens_minimatch_is_capped_and_one_pi(tmp_path):
    node = shutil.which("node")
    npm = shutil.which("npm")
    if node is None or npm is None:
        pytest.fail("node and npm are required to install and execute the pinned pi-lens tarball")

    from sdf_core.pi_runtime import PI_EXTENSIONS

    lens_pin = PI_EXTENSIONS["pi-lens"]
    assert (lens_pin.version, lens_pin.entry) == ("4.4.8", "dist/index.js")
    lock = _lock()
    locked = lock["packages"]["node_modules/pi-lens"]
    assert (locked["version"], locked["integrity"]) == (lens_pin.version, lens_pin.integrity)
    assert locked["dependencies"]["minimatch"] == "^11.0.1"
    assert lock["packages"]["node_modules/minimatch"]["version"] == "11.0.5"
    assert not any(
        key != "node_modules/minimatch" and key.endswith("/minimatch") for key in lock["packages"]
    )
    for key in LIBC_PACKAGES:
        assert lock["packages"][key]["libc"] in (["glibc"], ["musl"])
    for key in lock["packages"]:
        assert "@earendil-works/" not in key and "@mariozechner/" not in key

    install = tmp_path / "ext"
    install.mkdir()
    for name in ("package.json", "package-lock.json"):
        (install / name).write_text((EXT / name).read_text())
    subprocess.run(
        [npm, "ci", "--ignore-scripts", "--legacy-peer-deps", "--no-fund", "--no-audit"],
        cwd=install, check=True, capture_output=True, text=True, timeout=120,
    )
    subprocess.run(
        [node, str(VERIFY), str(install)],
        check=True, capture_output=True, text=True, timeout=30,
    )
    installed = []
    for path in (install / "node_modules").rglob("package.json"):
        if path.parent.name == "node_modules":
            continue
        installed.append(path.parent.relative_to(install / "node_modules").as_posix())
    assert "minimatch" in installed
    assert "pi-lens" in installed
    assert not any("earendil" in name or "mariozechner" in name for name in installed)
    assert not (
        "@ast-grep/napi-linux-x64-gnu" in installed and "@ast-grep/napi-linux-x64-musl" in installed
    )
    lens_root = install / "node_modules" / "pi-lens"
    assert json.loads((lens_root / "package.json").read_text())["version"] == "4.4.8"

    modules = tmp_path / "node_modules"
    modules.mkdir()
    for name, (url, integrity) in {
        "minimatch": MINIMATCH_10_2_6,
        "brace-expansion": (
            lock["packages"]["node_modules/brace-expansion"]["resolved"],
            lock["packages"]["node_modules/brace-expansion"]["integrity"],
        ),
        "balanced-match": (
            lock["packages"]["node_modules/balanced-match"]["resolved"],
            lock["packages"]["node_modules/balanced-match"]["integrity"],
        ),
    }.items():
        dest = modules / name
        dest.mkdir()
        _extract_npm(_download(url, integrity), dest)

    report = json.loads(subprocess.run(
        [node, str(PROBE), "--lens", str(lens_root), "--minimatch", str(modules / "minimatch")],
        check=True, capture_output=True, text=True, timeout=30,
    ).stdout)

    assert report["copies"]
    files = {copy["file"] for copy in report["copies"]}
    assert "dist/index.js" in files
    assert any(name.startswith("dist/chunk-") for name in files)
    for copy in report["copies"]:
        assert copy["hasCap"] is True
        assert copy["cappedLength"] == 4
        assert copy["defaultLength"] == 8
        assert copy["outerCapHit"] is False
        assert copy["error"] is None
        assert copy["cappedMs"] < 1000
        assert copy["defaultMs"] < 1000

    ignored = report["vulnerable"]["level2IgnoringCap"]
    assert ignored["entered"] == 1
    assert ignored["maxParts"] == 8
    assert ignored["outerCapHit"] is False
    assert ignored["error"] is None
    assert ignored["ms"] < 1000
    assert report["vulnerable"]["level2"]["maxParts"] == 8

    assert report["firstPhaseGated"] is True
    assert report["firstPhaseCalls"] == 1
    assert set(report["optimizationLevelMentions"]) <= {"default", "gate1", "gate2"}
    assert "gate2" in report["optimizationLevelMentions"]
    assert report["entryImportsChunk"] is False
    assert report["entryMentionsMcpBin"] is False
    kinds = {call["kind"] for call in report["entryCalls"]}
    assert {"minimatch", "Minimatch"} <= kinds
    for call in report["entryCalls"]:
        assert call["spread"] is False
        assert set(call["keys"]) <= {"dot", "nocase"}
        assert call["keys"]
    for result in report["vulnerable"]["callSiteOptions"]:
        assert set(result["options"]) <= {"dot", "nocase"}
        assert result["entered"] == 0
        assert result["maxParts"] == 0
        assert result["setLen"] == 1
        assert result["outerCapHit"] is False
        assert result["error"] is None
        assert result["ms"] < 1000

    analyze = report["analyzeScript"]
    assert analyze["importsPackage"] is True
    assert len(analyze["calls"]) == 2
    for call in analyze["calls"]:
        assert call["spread"] is False
        assert set(call["keys"]) <= {"dot", "nocase"}
