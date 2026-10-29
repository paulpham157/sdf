"""Operator CLI: ``python -m sdf_core.cli runs show <id> [--json]`` (ADR 0023).

Reads the state store named by ``SDF_DATABASE_URL`` directly (the same
read model as ``GET /runs/{id}/timeline``); it never writes.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from collections.abc import Sequence

from .db import make_session_factory
from .timeline import build_timeline, format_timeline


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="python -m sdf_core.cli")
    groups = parser.add_subparsers(dest="group", required=True)
    runs = groups.add_parser("runs", help="inspect runs and attempts").add_subparsers(dest="command", required=True)
    show = runs.add_parser("show", help="merged timeline for an agent run id or an attempt id")
    show.add_argument("id")
    show.add_argument("--json", action="store_true", help="print the timeline as JSON")
    show.add_argument("--database-url", default=None, help="defaults to SDF_DATABASE_URL")
    args = parser.parse_args(argv)

    url = args.database_url or os.environ.get("SDF_DATABASE_URL")
    if not url:
        print("set SDF_DATABASE_URL (or --database-url) to the SDF Core state store", file=sys.stderr)
        return 2
    db = make_session_factory(url)()
    try:
        timeline = build_timeline(db, args.id)
    finally:
        db.close()
    if timeline is None:
        print(f"no agent run or attempt with id {args.id!r}", file=sys.stderr)
        return 1
    print(json.dumps(timeline, indent=2, default=str) if args.json else format_timeline(timeline))
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
