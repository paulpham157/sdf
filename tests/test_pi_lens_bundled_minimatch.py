"""GHSA-g3hv-75wg-m249 in the minimatch copy inlined by pi-lens.

pi-lens 4.4.6's ``dist/index.js`` (the only file ``PiRuntime`` loads) inlines
minimatch 10.2.6. That algorithm's ``firstPhasePreProcess`` doubles glob
parts on ``**/..`` and is what the advisory describes. It runs only when
``optimizationLevel >= 2``. The default is 1, and the entry never sets the
option. The external log script imports the installed ``minimatch`` package
(the lockfile override, 11.0.5), which is not this bundle.

The reference algorithm is the published minimatch 10.2.6 tarball, pinned by
its registry integrity. A part cap keeps the level-2 repro bounded.
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
LOCK = ROOT / "infra/e2b/herdr-agents/pi-extensions/package-lock.json"
PROBE = Path(__file__).with_name("pi_lens_minimatch_probe.mjs")
# Published algorithm inlined by pi-lens 4.4.6. Not a runtime dependency.
MINIMATCH_10_2_6 = (
    "https://registry.npmjs.org/minimatch/-/minimatch-10.2.6.tgz",
    "sha512-vpLQEs+VLCr1nU0BXS07maYoFwlDAH0gngQuuttxIwutDFEMHq2blX+8vpgxDdK3J1PwjCJiep77OitTZ4Ll1A==",
)
PATTERN_PARTS = 8
PART_CAP = 16


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


def _lock_package(name: str) -> dict:
    lock = json.loads(LOCK.read_text())
    return lock["packages"][f"node_modules/{name}"]


def test_inlined_minimatch_ghsa_is_present_but_not_reachable(tmp_path):
    node = shutil.which("node")
    if node is None:
        pytest.fail("node is required to execute the pinned minimatch algorithm")

    from sdf_core.pi_runtime import PI_EXTENSIONS

    lens_pin = PI_EXTENSIONS["pi-lens"]
    assert (lens_pin.version, lens_pin.entry) == ("4.4.6", "dist/index.js")
    locked = _lock_package("pi-lens")
    assert (locked["version"], locked["integrity"]) == (lens_pin.version, lens_pin.integrity)
    minimatch = _lock_package("minimatch")
    assert minimatch["version"] == "11.0.5"
    assert not any(
        key != "node_modules/minimatch" and key.endswith("/minimatch")
        for key in json.loads(LOCK.read_text())["packages"]
    )

    lens_root = tmp_path / "pi-lens"
    lens_root.mkdir()
    _extract_npm(_download(locked["resolved"], locked["integrity"]), lens_root)
    assert json.loads((lens_root / "package.json").read_text())["version"] == "4.4.6"

    modules = tmp_path / "node_modules"
    modules.mkdir()
    for name, (url, integrity) in {
        "minimatch": MINIMATCH_10_2_6,
        "brace-expansion": (_lock_package("brace-expansion")["resolved"], _lock_package("brace-expansion")["integrity"]),
        "balanced-match": (_lock_package("balanced-match")["resolved"], _lock_package("balanced-match")["integrity"]),
    }.items():
        dest = modules / name
        dest.mkdir()
        _extract_npm(_download(url, integrity), dest)

    report = json.loads(subprocess.run(
        [node, str(PROBE), "--lens", str(lens_root), "--minimatch", str(modules / "minimatch")],
        check=True, capture_output=True, text=True, timeout=30,
    ).stdout)

    assert report["firstPhaseEqual"] is True
    assert report["bundledHasCap"] is False
    assert report["firstPhaseCalls"] == 1
    assert report["firstPhaseGated"] is True
    assert report["optimizationLevelMentions"] == ["default", "gate2", "gate1", "default", "gate2"]
    assert report["entryImportsChunk"] is False
    assert report["entryMentionsMcpBin"] is False
    assert len(report["entryCalls"]) == 8
    for call in report["entryCalls"]:
        assert call["spread"] is False
        assert set(call["keys"]) <= {"dot", "nocase"}
        assert call["keys"]

    level2 = report["behavior"]["level2"]
    assert level2 == {
        "entered": 1, "maxParts": PATTERN_PARTS, "setLen": PATTERN_PARTS,
        "capped": False, "error": None, "ms": level2["ms"],
    }
    assert level2["ms"] < 1000
    assert PATTERN_PARTS < PART_CAP == report["partCap"]
    assert report["behavior"]["callSiteOptions"]
    for result in report["behavior"]["callSiteOptions"]:
        assert set(result["options"]) <= {"dot", "nocase"}
        assert result["entered"] == 0
        assert result["maxParts"] == 0
        assert result["setLen"] == 1
        assert result["capped"] is False
        assert result["error"] is None
        assert result["ms"] < 1000

    analyze = report["analyzeScript"]
    assert analyze["importsPackage"] is True
    assert len(analyze["calls"]) == 2
    for call in analyze["calls"]:
        assert call["spread"] is False
        assert set(call["keys"]) <= {"dot", "nocase"}
