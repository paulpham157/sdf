package setup

import "strings"

// hookScriptTemplate is a POSIX /bin/sh git hook. __SDF_HOOK_URL__ and
// __SDF_HOOK_KIND__ are replaced before it is written. The kind is
// post-commit, post-rewrite, or prepare-commit-msg.
//
// The script sends the commit SHA, the checkout toplevel, the branch name,
// and the agent session id. It does not read the diff or the commit message.
// prepare-commit-msg only appends an SDF-Session trailer, and only when a
// session id is present. A down receiver or a missing curl does not fail
// the commit. Proxy variables are cleared so the POST cannot leave the machine.
const hookScriptTemplate = `#!/bin/sh
# sdf-git-hook:v1
# Installed by sdf init --git-hooks. Re-run that command to replace this file.
# Posts the commit SHA, checkout path, branch, and agent session id to a
# loopback sdf serve. It does not send the diff or the commit message.
# Failure is ignored, and the request cannot use an HTTP proxy.

SDF_HOOK_URL='__SDF_HOOK_URL__'
SDF_HOOK_KIND='__SDF_HOOK_KIND__'

sdf_json_escape() {
  printf '%s' "$1" | awk 'BEGIN{ORS=""} { if (NR>1) printf "\\n"; gsub(/\\/, "\\\\"); gsub(/"/, "\\\""); gsub(/\t/, "\\t"); gsub(/\r/, "\\r"); print }'
}

# One agent only. Pi's exported session id is the session-file stem: pi-otel
# 0.3.0 uses basename of the session file with a trailing .jsonl removed.
# PI_SESSION_ID is the short id and does not match. OpenCode's
# OPENCODE_SESSION_ID matches session.id. Codex's CODEX_THREAD_ID matches
# conversation.id; CODEX_SESSION_ID is a different value and is ignored.
# More than one of these set means the commit is not attributed.
sdf_session() {
  pi_id=''
  oc_id=''
  cx_id=''
  n=0
  if [ -n "${PI_SESSION_FILE:-}" ]; then
    pi_base=$(basename -- "$PI_SESSION_FILE")
    case "$pi_base" in
      *.jsonl) pi_id=${pi_base%.jsonl} ;;
      *) pi_id=$pi_base ;;
    esac
    case "$pi_id" in
      ''|.|..) pi_id='' ;;
    esac
    if [ -n "$pi_id" ]; then
      n=$((n + 1))
    fi
  fi
  if [ -n "${OPENCODE_SESSION_ID:-}" ]; then
    oc_id=$OPENCODE_SESSION_ID
    n=$((n + 1))
  fi
  if [ -n "${CODEX_THREAD_ID:-}" ]; then
    cx_id=$CODEX_THREAD_ID
    n=$((n + 1))
  fi
  if [ "$n" -ne 1 ]; then
    return 0
  fi
  if [ -n "$pi_id" ]; then
    printf '%s\n' "pi $pi_id"
  elif [ -n "$oc_id" ]; then
    printf '%s\n' "opencode $oc_id"
  else
    printf '%s\n' "codex $cx_id"
  fi
}

sdf_ok_id() {
  id=$1
  if [ -z "$id" ] || [ "${#id}" -gt 256 ]; then
    return 1
  fi
  case "$id" in
    *[!A-Za-z0-9._:-]*) return 1 ;;
  esac
  return 0
}

sdf_ok_sha() {
  sha=$1
  if [ "${#sha}" -ne 40 ]; then
    return 1
  fi
  case "$sha" in
    *[!0-9a-fA-F]*) return 1 ;;
  esac
  return 0
}

sdf_post() {
  payload=$1
  case "$SDF_HOOK_URL" in
    http://127.0.0.1:*/v1/logs|http://[::1]:*/v1/logs) ;;
    *) return 0 ;;
  esac
  if command -v curl >/dev/null 2>&1; then
    http_proxy= https_proxy= HTTP_PROXY= HTTPS_PROXY= all_proxy= ALL_PROXY= \
      curl -s -o /dev/null --connect-timeout 1 --max-time 1 --max-redirs 0 --noproxy '*' \
      -X POST -H 'Content-Type: application/json' --data-binary "$payload" \
      "$SDF_HOOK_URL" || true
    return 0
  fi
  if command -v wget >/dev/null 2>&1; then
    http_proxy= https_proxy= HTTP_PROXY= HTTPS_PROXY= all_proxy= ALL_PROXY= \
      wget -q -O /dev/null -T 1 --no-proxy \
      --header='Content-Type: application/json' --post-data="$payload" \
      "$SDF_HOOK_URL" || true
  fi
  return 0
}

sdf_send_one() {
  sha=$1
  rewritten=$2
  line=$(sdf_session || true)
  if [ -z "$line" ]; then
    return 0
  fi
  agent=${line%% *}
  sid=${line#* }
  sdf_ok_id "$sid" || return 0
  sdf_ok_sha "$sha" || return 0
  sha=$(printf '%s' "$sha" | tr 'ABCDEF' 'abcdef')
  repo=$(git rev-parse --show-toplevel 2>/dev/null || true)
  if [ -z "$repo" ]; then
    return 0
  fi
  case "$repo" in
    *"
"*) return 0 ;;
  esac
  branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || true)
  if [ -z "$branch" ]; then
    branch=HEAD
  fi
  now=$(date +%s 2>/dev/null || printf '%s' 0)
  case "$now" in
    *[!0-9]*) now=0 ;;
  esac
  esc_sid=$(sdf_json_escape "$sid")
  esc_repo=$(sdf_json_escape "$repo")
  esc_branch=$(sdf_json_escape "$branch")
  esc_sha=$(sdf_json_escape "$sha")
  service=''
  sess=''
  case "$agent" in
    pi)
      service=pi
      sess=$(printf '%s' \
        "{\"key\":\"session.id\",\"value\":{\"stringValue\":\"$esc_sid\"}}," \
        "{\"key\":\"pi.session.id\",\"value\":{\"stringValue\":\"$esc_sid\"}}," \
        "{\"key\":\"gen_ai.conversation.id\",\"value\":{\"stringValue\":\"$esc_sid\"}}," \
        "{\"key\":\"gen_ai.system\",\"value\":{\"stringValue\":\"pi\"}},")
      ;;
    opencode)
      service=opencode
      sess=$(printf '%s' "{\"key\":\"session.id\",\"value\":{\"stringValue\":\"$esc_sid\"}},")
      ;;
    codex)
      service=codex
      sess=$(printf '%s' "{\"key\":\"conversation.id\",\"value\":{\"stringValue\":\"$esc_sid\"}},")
      ;;
    *)
      return 0
      ;;
  esac
  rewrite_attr=''
  if sdf_ok_sha "$rewritten"; then
    rewritten=$(printf '%s' "$rewritten" | tr 'ABCDEF' 'abcdef')
    esc_old=$(sdf_json_escape "$rewritten")
    rewrite_attr=$(printf '%s' "{\"key\":\"sdf.git.rewritten_from\",\"value\":{\"stringValue\":\"$esc_old\"}},")
  fi
  nano="${now}000000000"
  payload=$(printf '%s' \
    '{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"'"$service"'"}},{"key":"sdf.git.hook","value":{"stringValue":"1"}}]},"scopeLogs":[{"logRecords":[{"timeUnixNano":"'"$nano"'","body":{"stringValue":"sdf.git.commit"},"attributes":[{"key":"event.name","value":{"stringValue":"sdf.git.commit"}},'"$sess"'{"key":"sdf.git.agent","value":{"stringValue":"'"$agent"'"}},{"key":"vcs.ref.head.revision","value":{"stringValue":"'"$esc_sha"'"}},{"key":"sdf.git.repo","value":{"stringValue":"'"$esc_repo"'"}},{"key":"sdf.git.branch","value":{"stringValue":"'"$esc_branch"'"}},'"$rewrite_attr"'{"key":"sdf.git.hook","value":{"stringValue":"1"}}]}]}]}]}')
  sdf_post "$payload"
}

prev="${0}.sdf-previous"
prev_status=0
if [ -t 0 ]; then
  captured=''
else
  captured=$(cat || true)
fi
if [ -f "$prev" ]; then
  printf '%s\n' "$captured" | "$prev" "$@"
  prev_status=$?
fi

case "$SDF_HOOK_KIND" in
  prepare-commit-msg)
    msg=${1:-}
    line=$(sdf_session || true)
    if [ -n "$msg" ] && [ -f "$msg" ] && [ -n "$line" ]; then
      sid=${line#* }
      if sdf_ok_id "$sid"; then
        if ! git interpret-trailers --if-exists addIfDifferent --if-missing add --trailer "SDF-Session: $sid" --in-place "$msg" 2>/dev/null; then
          if ! grep -F -x -q "SDF-Session: $sid" "$msg" 2>/dev/null; then
            printf '\nSDF-Session: %s\n' "$sid" >> "$msg" || true
          fi
        fi
      fi
    fi
    ;;
  post-rewrite)
    printf '%s\n' "$captured" | while read -r old new extra; do
      if [ -n "$new" ]; then
        sdf_send_one "$new" "$old" || true
      fi
    done
    ;;
  *)
    sha=$(git rev-parse HEAD 2>/dev/null || true)
    sdf_send_one "$sha" "" || true
    ;;
esac

exit "$prev_status"
`

func hookScript(url, kind string) string {
	body := strings.ReplaceAll(hookScriptTemplate, "__SDF_HOOK_URL__", url)
	return strings.ReplaceAll(body, "__SDF_HOOK_KIND__", kind)
}
