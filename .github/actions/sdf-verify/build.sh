#!/usr/bin/env bash
# Builds sdf from the source this action was loaded from, into RUNNER_TEMP.
set -euo pipefail
src="$(cd "$GITHUB_ACTION_PATH/../../.." && pwd)"
if ! grep -qx 'module github.com/paulpham157/sdf' "$src/go.mod"; then
  echo "::error::sdf verify: $src is not the sdf source"
  exit 1
fi
bin="$RUNNER_TEMP/sdf-verify-bin"
mkdir -p "$bin"
(cd "$src" && CGO_ENABLED=0 GOFLAGS=-mod=readonly go build -trimpath -o "$bin/sdf" ./cmd/sdf)
echo "sdf=$bin/sdf" >>"$GITHUB_OUTPUT"
echo "built sdf from $src"
