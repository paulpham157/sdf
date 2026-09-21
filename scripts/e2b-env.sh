#!/usr/bin/env bash
set -euo pipefail

repo_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -f "$repo_root/.env" ]]; then
  set -a
  # .env is local, ignored, and owned by the operator running this wrapper.
  # shellcheck disable=SC1091
  source "$repo_root/.env"
  set +a
fi

if [[ "$#" -eq 0 ]]; then
  printf 'usage: %s COMMAND [ARGS...]\n' "$0" >&2
  exit 2
fi

exec "$@"
