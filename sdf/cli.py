from __future__ import annotations

import argparse
import json
import sys

from sdf.server import make_server
from sdf.store import Store
from sdf.summary import SummaryReport, format_summary, summarize


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="sdf",
        description="Collect OpenTelemetry from Codex CLI and Claude Code.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    serve = sub.add_parser("serve", help="listen for OTLP/HTTP and store it in SQLite")
    serve.add_argument("--host", default="127.0.0.1")
    serve.add_argument("--port", type=int, default=4318)
    serve.add_argument("--db", default="sdf-telemetry.sqlite")

    summary = sub.add_parser("summary", help="print one summary per collected session")
    summary.add_argument("--db", default="sdf-telemetry.sqlite")
    summary.add_argument("--session", help="only this session.id or conversation.id")
    summary.add_argument("--json", action="store_true", help="print JSON instead of text")
    summary.add_argument(
        "--all-spans",
        action="store_true",
        help="include internal spans in the tree (default hides them)",
    )

    args = parser.parse_args(argv)
    if args.command == "serve":
        return _serve(args.host, args.port, args.db)
    return _summary(args.db, args.session, args.json, args.all_spans)


def _serve(host: str, port: int, db: str) -> int:
    if host not in {"127.0.0.1", "localhost", "::1"}:
        print(
            "warning: this receiver has no authentication; binding beyond localhost "
            "exposes telemetry that can include prompts when the agent is opted in",
            file=sys.stderr,
        )
    store = Store(db)
    server = make_server(host, port, store)
    bound_host, bound_port = server.server_address[:2]
    print(f"OTLP/HTTP listening on http://{bound_host}:{bound_port} db={db}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nstopped", file=sys.stderr)
    finally:
        server.server_close()
        store.close()
    return 0


def _summary(db: str, session: str | None, as_json: bool, all_spans: bool) -> int:
    store = Store(db)
    try:
        records = store.load()
    finally:
        store.close()
    report = summarize(records, all_spans=all_spans)
    if session is not None:
        report = SummaryReport(
            sessions=[item for item in report.sessions if item.session_id == session]
        )
    if as_json:
        json.dump(report.as_dict(), sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        sys.stdout.write(format_summary(report))
    return 0
