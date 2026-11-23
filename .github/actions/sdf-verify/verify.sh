#!/usr/bin/env bash
# Resolves the task id, runs sdf verify, writes the job summary and outputs.
# It exits 0 once a status is set; the action's last step turns anything but
# PASS into a failure. Inputs come only through environment variables, never
# through ${{ }} in the script, so a label or branch name cannot inject shell.
set -uo pipefail

out="$RUNNER_TEMP/sdf-verify-out"
id_re='^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'

finish() { # status, message
  echo "status=$1" >>"$GITHUB_OUTPUT"
  if [ -n "${2:-}" ]; then
    echo "::error title=sdf verify::$2"
    printf '## sdf verify: %s\n\n%s\n' "$1" "$2" >>"$GITHUB_STEP_SUMMARY"
  fi
  exit 0
}

task="${SDF_TASK:-}"
source="${SDF_TASK_SOURCE:-label}"
case "$source" in label | trailer | label-or-trailer) ;; *) finish ERROR "task-source must be label, trailer, or label-or-trailer" ;; esac

if [ -z "$task" ] && [ "$source" != trailer ]; then
  # Each label is one JSON string; a name with a newline stays one value
  # and then fails the id pattern below.
  sel='[.[]? | strings | select(startswith("sdf-task:"))]'
  n="$(printf '%s' "${SDF_LABELS:-[]}" | jq "$sel | length" 2>/dev/null)" || finish ERROR "labels input is not a JSON array"
  if [ "${n:-0}" -gt 1 ]; then
    finish ERROR "more than one sdf-task: label"
  fi
  if [ "${n:-0}" -eq 1 ]; then
    task="$(printf '%s' "$SDF_LABELS" | jq -r "$sel | .[0][9:]")"
  fi
fi

case "$SDF_SHA" in
  *[!0-9a-f]*) finish ERROR "sha is not a full commit id" ;;
esac
if [ "${#SDF_SHA}" -ne 40 ] && [ "${#SDF_SHA}" -ne 64 ]; then
  finish ERROR "sha is not a full commit id"
fi

if [ -z "$task" ] && [ "$source" != label ]; then
  task="$(git -C "$SDF_REPO" log -1 --format='%(trailers:key=SDF-Task,valueonly,separator=%x0a)' "$SDF_SHA" 2>/dev/null | sed '/^$/d')"
  if [ "$(printf '%s' "$task" | grep -c .)" -gt 1 ]; then
    finish ERROR "more than one SDF-Task trailer"
  fi
fi

[ -n "$task" ] || finish ERROR "no task id: set the task input, or add one label sdf-task:<id>"
[[ "$task" =~ $id_re ]] || finish ERROR "task id is not a valid sdf task id"
echo "task=$task" >>"$GITHUB_OUTPUT"

if [ -z "${SDF_BASE_REF:-}" ] || ! git check-ref-format --branch "$SDF_BASE_REF" >/dev/null 2>&1; then
  finish ERROR "base-ref is not a branch name"
fi
task_ref="refs/remotes/origin/$SDF_BASE_REF"
if ! git -C "$SDF_REPO" rev-parse --verify --quiet "$task_ref^{commit}" >/dev/null; then
  finish ERROR "origin/$SDF_BASE_REF is not in the checkout"
fi

mkdir -p "$out" "$RUNNER_TEMP/sdf-verify-work"
echo "out-dir=$out" >>"$GITHUB_OUTPUT"

# stderr goes straight to the log: the verdict hash printed there is the
# copy the pull request's checks could not rewrite afterwards.
"$SDF_BIN" verify "$SDF_SHA" --task "$task" --task-ref "$task_ref" --repo "$SDF_REPO" \
  --no-data-dir --out "$out/verdict.json" --artifacts "$out/artifacts" \
  --work-dir "$RUNNER_TEMP/sdf-verify-work" >/dev/null
code=$?

status="$(jq -r '.status // empty' "$out/verdict.json" 2>/dev/null)"
hash="$(jq -r '.verdict.hash // empty' "$out/verdict.json" 2>/dev/null)"
want=""
case "$code" in 0) want=PASS ;; 1) want=FAIL ;; *) want=ERROR ;; esac
# The exit code decides. A verdict file that disagrees with it was not
# written by this run of sdf verify.
note=""
if [ "$status" != "$want" ]; then
  note="sdf verify exited $code but verdict.json says ${status:-nothing}"
  status=ERROR
  hash=""
fi
echo "verdict-hash=$hash" >>"$GITHUB_OUTPUT"

{
  printf '## sdf verify: %s\n\n' "$status"
  printf '| | |\n|---|---|\n'
  # shellcheck disable=SC2016 # the backticks are markdown, not expansions
  printf '| task | `%s` |\n| commit | `%s` |\n| task ref | `origin/%s` |\n| verdict | `%s` |\n\n' \
    "$task" "$SDF_SHA" "$SDF_BASE_REF" "${hash:-none}"
  if [ -n "$note" ]; then printf '%s\n\n' "$note"; fi
  jq -r '
    def cell: tostring | gsub("[|`\r\n]"; " ");
    if (.verdict.record.reasons | length) > 0 then
      "| code | detail |", "|---|---|",
      (.verdict.record.reasons[] | "| \(.code | cell) | \(.detail | cell) |")
    else "No reasons: every check passed." end
  ' "$out/verdict.json" 2>/dev/null || true
} >>"$GITHUB_STEP_SUMMARY"

finish "$status"
