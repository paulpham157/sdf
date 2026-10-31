"""Write a fixture's server-registered check manifest (ADR 0025)."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

# The full-loop add-sum check. A single add(2, 3) == 5 is satisfiable by
# hard-coding that pair; this covers negatives, zero and larger values, and
# rejects a non-int whose __eq__ is always true. return a - b (the full-loop
# "should fail" fixture) still fails.
ADD_SUM_CHECK = (
    "from calc import add\n"
    "for a in (-10, -1, 0, 1, 2, 7):\n"
    "    for b in (-10, -1, 0, 1, 3, 9):\n"
    "        got = add(a, b)\n"
    "        if type(got) is not int or got != a + b:\n"
    "            raise SystemExit(f'add({a}, {b}) -> {got!r}')\n"
)

from sdf_core.checks import FIXTURE_MANIFEST


def write_checks(fixture: Path, checks: dict[str, Any]) -> Path:
    path = Path(fixture) / FIXTURE_MANIFEST
    path.write_text(json.dumps({"checks": checks}), encoding="utf-8")
    return path
