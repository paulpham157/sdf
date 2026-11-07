#!/usr/bin/env bash
# Offline public-path evidence loop (default fake adapter).
#
# Historical entry point for scripts/demo.py. Walks Business Context ->
# Objective -> Assumption -> Task -> POST /tasks/{id}/run, then prints a
# plain-language story and writes a local HTML page. No E2B or model keys.
# This fixture loop is not production proof (docs/live-full-loop.md).
set -euo pipefail

repo_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if ! command -v uv >/dev/null 2>&1; then
  printf 'uv is required. Install it, then run: uv sync --all-groups\n' >&2
  exit 1
fi
exec uv run scripts/demo.py "$@"
