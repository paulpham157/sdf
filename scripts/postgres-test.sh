#!/usr/bin/env bash
set -euo pipefail

compose_file="${SDF_COMPOSE_FILE:-docker-compose.test.yml}"
database_url="${SDF_POSTGRES_TEST_URL:-postgresql+psycopg://postgres:postgres@127.0.0.1:55432/sdf_core}"

cleanup() {
  docker compose -f "$compose_file" down --remove-orphans >/dev/null
}
trap cleanup EXIT

docker compose -f "$compose_file" up -d --wait postgres
SDF_DATABASE_URL="$database_url" .venv/bin/alembic upgrade head
SDF_DATABASE_URL="$database_url" SDF_POSTGRES_TEST_URL="$database_url" .venv/bin/pytest -q tests/test_postgres_integration.py
