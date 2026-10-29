"""Write a fixture's server-registered check manifest (ADR 0025)."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from sdf_core.checks import FIXTURE_MANIFEST


def write_checks(fixture: Path, checks: dict[str, Any]) -> Path:
    path = Path(fixture) / FIXTURE_MANIFEST
    path.write_text(json.dumps({"checks": checks}), encoding="utf-8")
    return path
