"""Manage SDF Core API tokens (ADR 0015).

    python -m sdf_core.auth_cli create --principal hung --role approver --role operator
    python -m sdf_core.auth_cli list
    python -m sdf_core.auth_cli revoke <token-id>

``create`` prints the plaintext token once, to stdout, for the operator to
store; it is never stored or logged.  Uses ``SDF_DATABASE_URL`` and
``SDF_AUTH_PEPPER`` from the environment or the repo-root ``.env``.
"""

from __future__ import annotations

import argparse
import getpass
import json
import sys

from . import auth
from .db import make_session_factory


def _actor() -> str:
    try:
        user = getpass.getuser()
    except Exception:  # noqa: BLE001
        user = "unknown"
    return f"cli:{user}"[:120]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="python -m sdf_core.auth_cli", description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)
    create = sub.add_parser("create", help="create a token and print it once")
    create.add_argument("--principal", required=True, help="the human or service the token identifies")
    create.add_argument("--role", action="append", required=True, choices=auth.ROLES,
                        help="repeatable: viewer, operator, approver, admin")
    create.add_argument("--kind", default="human", choices=auth.TOKEN_KINDS)
    create.add_argument("--expires-days", type=int, default=auth.DEFAULT_EXPIRY_DAYS)
    create.add_argument("--description", default=None)
    sub.add_parser("list", help="list token metadata (never secrets)")
    revoke = sub.add_parser("revoke", help="revoke a token by id")
    revoke.add_argument("token_id")
    args = parser.parse_args(argv)

    url = auth.setting("SDF_DATABASE_URL")
    if not url or ":memory:" in url:
        print("error: set SDF_DATABASE_URL to the server's database", file=sys.stderr)
        return 2
    session = make_session_factory(url)()
    try:
        if args.command == "create":
            try:
                row, plaintext = auth.create_token(
                    session, principal=args.principal, roles=args.role, created_by=_actor(), kind=args.kind,
                    expires_in_days=args.expires_days, description=args.description)
            except (ValueError, auth.AuthConfigError) as exc:
                print(f"error: {exc}", file=sys.stderr)
                return 2
            session.commit()
            print(f"created token {row.id} for {row.principal} roles={','.join(row.roles)} "
                  f"expires={row.expires_at.isoformat()}", file=sys.stderr)
            print("store it now; it is shown only once:", file=sys.stderr)
            print(plaintext)
        elif args.command == "list":
            print(json.dumps(auth.list_tokens(session), indent=2))
        else:
            try:
                row = auth.revoke_token(session, args.token_id, revoked_by=_actor())
            except LookupError:
                print("error: token not found", file=sys.stderr)
                return 1
            session.commit()
            print(f"revoked {row.id} ({row.principal}) at {row.revoked_at.isoformat()}", file=sys.stderr)
        return 0
    finally:
        session.close()


if __name__ == "__main__":
    raise SystemExit(main())
