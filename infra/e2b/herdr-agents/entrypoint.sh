#!/usr/bin/env bash
set -euo pipefail

herdr server &
herdr_pid=$!
node /usr/local/lib/herdr-endpoint.mjs &
bridge_pid=$!

cleanup() {
  kill "$bridge_pid" "$herdr_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

wait -n "$bridge_pid" "$herdr_pid"
