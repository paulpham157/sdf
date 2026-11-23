package setup

import (
	"strings"

	"github.com/paulpham157/sdf/internal/genai"
)

// hookScriptTemplate is a POSIX /bin/sh git hook. __SDF_HOOK_URL__,
// __SDF_HOOK_KIND__, __SDF_HOOK_BIN__, and __SDF_HOOK_DB__ are replaced
// before it is written. The kind is post-commit, post-rewrite,
// prepare-commit-msg, or pre-push.
//
// The script sends the commit SHA, the checkout toplevel, the branch name,
// and the agent session id. It does not read the diff or the commit message.
// prepare-commit-msg only appends an SDF-Session trailer, and only when a
// session id is present. A down receiver or a missing curl does not fail
// the commit. Proxy variables are cleared so the POST cannot leave the machine.
//
// pre-push sends nothing. It runs sdf decision pre-push with the hook's
// stdin, which reads the store and only the SDF-Decision trailer of the
// pushed commits. It returns non-zero only when a chained hook fails or
// when that command blocks (block mode); a missing sdf or any failure of
// the check lets the push go on.
const hookScriptTemplate = `#!/bin/sh
# sdf-git-hook:v1
# Installed by sdf init --git-hooks. Re-run that command to replace this file.
# Posts the commit SHA, checkout path, branch, and agent session id to a
# loopback sdf serve. It does not send the diff or the commit message.
# Failure is ignored, and the request cannot use an HTTP proxy.
# A receiver token is read at run time from SDF_OTLP_TOKEN or
# SDF_OTLP_TOKEN_FILE; it is never stored in this file.
# pre-push posts nothing: it runs sdf decision pre-push, which warns about
# pushed commits with no decision and reads only their SDF-Decision trailer.
# It never fails a push because sdf or its store is unavailable.
# Skip it with git push --no-verify or SDF_PRE_PUSH=off.

SDF_HOOK_URL='__SDF_HOOK_URL__'
SDF_HOOK_KIND='__SDF_HOOK_KIND__'
SDF_HOOK_BIN=__SDF_HOOK_BIN__
SDF_HOOK_DB=__SDF_HOOK_DB__

sdf_json_escape() {
  printf '%s' "$1" | awk 'BEGIN{ORS=""} { if (NR>1) printf "\\n"; gsub(/\\/, "\\\\"); gsub(/"/, "\\\""); gsub(/\t/, "\\t"); gsub(/\r/, "\\r"); print }'
}

# One agent only. Pi's exported session id is the session-file stem: pi-otel
# 0.3.0 uses basename of the session file with a trailing .jsonl removed.
# PI_SESSION_ID is the short id and does not match. OpenCode's
# OPENCODE_SESSION_ID matches session.id. Codex's CODEX_THREAD_ID matches
# conversation.id. On a Codex 0.167.0 root thread CODEX_SESSION_ID is that
# same id; a subagent can set CODEX_SESSION_ID to the root id instead, so
# this script does not read CODEX_SESSION_ID. Grok CLI puts GROK_SESSION_ID
# in the environment of its shell tool (grok-build xai-grok-shell
# session/agent_rebuild.rs adds it to session_env, which the bash tool passes
# to the command); it is the session id Grok's hooks receive. Claude Code
# (v2.1.132 and later) sets CLAUDE_CODE_SESSION_ID in its Bash and PowerShell
# tool and hook subprocesses; code.claude.com/docs/en/env-vars says it matches
# the hook session_id, which is the session.id on Claude Code telemetry and
# what sdf's Claude session hook posts. Qwen Code (v0.28.4) sets
# process.env QWEN_CODE_SESSION_ID to Config.getSessionId() (config.ts) and
# passes it to its shell tool (services/shellContextEnv.ts, from QwenLM
# qwen-code #4649); telemetry/loggers.ts puts the same getSessionId() in
# session.id. More than one of Pi, OpenCode, Codex, Grok CLI, Claude Code,
# and Qwen Code set means the commit is not attributed: a Qwen started from
# Claude Code's Bash tool inherits CLAUDE_CODE_SESSION_ID, for example.
sdf_session() {
  pi_id=''
  oc_id=''
  cx_id=''
  gk_id=''
  cc_id=''
  qw_id=''
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
  if [ -n "${GROK_SESSION_ID:-}" ]; then
    gk_id=$GROK_SESSION_ID
    n=$((n + 1))
  fi
  if [ -n "${CLAUDE_CODE_SESSION_ID:-}" ]; then
    cc_id=$CLAUDE_CODE_SESSION_ID
    n=$((n + 1))
  fi
  if [ -n "${QWEN_CODE_SESSION_ID:-}" ]; then
    qw_id=$QWEN_CODE_SESSION_ID
    n=$((n + 1))
  fi
  if [ "$n" -ne 1 ]; then
    return 0
  fi
  if [ -n "$pi_id" ]; then
    printf '%s\n' "pi $pi_id"
  elif [ -n "$oc_id" ]; then
    printf '%s\n' "opencode $oc_id"
  elif [ -n "$gk_id" ]; then
    printf '%s\n' "grok-cli $gk_id"
  elif [ -n "$cc_id" ]; then
    printf '%s\n' "claude-code $cc_id"
  elif [ -n "$qw_id" ]; then
    printf '%s\n' "qwen-code $qw_id"
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
  sdf_auth=$(sdf_auth_header)
  if command -v curl >/dev/null 2>&1; then
    printf '%s\n' "$sdf_auth" | \
      http_proxy= https_proxy= HTTP_PROXY= HTTPS_PROXY= all_proxy= ALL_PROXY= \
      curl -s -o /dev/null --connect-timeout 1 --max-time 1 --max-redirs 0 --noproxy '*' \
      -X POST -H 'Content-Type: application/json' -H @- --data-binary "$payload" \
      "$SDF_HOOK_URL" || true
    return 0
  fi
  if command -v wget >/dev/null 2>&1; then
    if [ -z "$sdf_auth" ]; then
      sdf_wget "$payload"
      return 0
    fi
    sdf_rc=$(mktemp 2>/dev/null) || return 0
    # The wgetrc holds the token: remove it however the hook ends (a kill
    # with SIGKILL is the one case no trap can cover).
    trap 'rm -f "$sdf_rc"' EXIT
    trap 'rm -f "$sdf_rc"; exit 1' HUP INT TERM
    printf 'header = %s\n' "$sdf_auth" > "$sdf_rc"
    sdf_wget "$payload" "$sdf_rc"
    rm -f "$sdf_rc"
    trap - EXIT HUP INT TERM
  fi
  return 0
}

# sdf_wget posts $1. A second argument is a wgetrc that holds the
# Authorization header; without one wget reads its usual configuration.
sdf_wget() {
  if [ -n "${2:-}" ]; then
    WGETRC=$2 http_proxy= https_proxy= HTTP_PROXY= HTTPS_PROXY= all_proxy= ALL_PROXY= \
      wget -q -O /dev/null -T 1 --no-proxy \
      --header='Content-Type: application/json' --post-data="$1" \
      "$SDF_HOOK_URL" || true
    return 0
  fi
  http_proxy= https_proxy= HTTP_PROXY= HTTPS_PROXY= all_proxy= ALL_PROXY= \
    wget -q -O /dev/null -T 1 --no-proxy \
    --header='Content-Type: application/json' --post-data="$1" \
    "$SDF_HOOK_URL" || true
}

# sdf_auth_header prints an Authorization header line when a receiver token
# is available, from the same variables sdf serve reads: SDF_OTLP_TOKEN, else
# the file named by SDF_OTLP_TOKEN_FILE, trimmed at the ends; both set is
# refused with a warning, as sdf serve refuses it. This file never holds the token. The
# header goes to curl on stdin and to wget through a private temporary
# wgetrc, so it is not on a command line.
sdf_auth_header() {
  sdf_tok=$(printf '%s\n' "${SDF_OTLP_TOKEN:-}" | tr -d '\r' | sdf_trim)
  if [ -n "$sdf_tok" ] && [ -n "${SDF_OTLP_TOKEN_FILE:-}" ]; then
    echo 'sdf: SDF_OTLP_TOKEN and SDF_OTLP_TOKEN_FILE are both set (sdf serve refuses that too); not sending a token' >&2
    return 0
  fi
  if [ -n "${SDF_OTLP_TOKEN_FILE:-}" ]; then
    sdf_tok=$(tr -d '\r' 2>/dev/null < "$SDF_OTLP_TOKEN_FILE" | sdf_trim)
  fi
  [ -z "$sdf_tok" ] || printf 'Authorization: Bearer %s' "$sdf_tok"
}

# sdf_trim prints the first non-blank line of stdin with leading and trailing
# blanks removed (read's default IFS), which is strings.TrimSpace for a
# one-line token, as sdf serve reads it.
sdf_trim() {
  while read -r sdf_line && [ -z "$sdf_line" ]; do :; done
  printf '%s' "$sdf_line"
}

# Codex 0.167.0 service.name is the originator, except the app server passes
# service_name_override codex-app-server. codex exec sets the originator to
# codex_exec before OTEL init. CODEX_INTERNAL_ORIGINATOR_OVERRIDE replaces
# that exec name and does not rename the app server. A parent that is not
# Codex uses codex_exec, which is the live exec name.
sdf_codex_mode() {
  printf '%s\n' "$1" | awk '
    function base(p,    n, a) {
      n = split(p, a, "/")
      return a[n]
    }
    BEGIN {
      interps["sh"] = 1
      interps["bash"] = 1
      interps["dash"] = 1
      interps["env"] = 1
    }
    {
      if (NF < 1) exit
      start = 1
      b = base($1)
      if (interps[b] && NF >= 2) {
        start = 2
        b = base($2)
      }
      if (b == "codex" || b == "codex.exe") {
        for (j = start + 1; j <= NF; j++) {
          if ($j ~ /^-/) continue
          if ($j == "exec") { print "exec"; exit }
          print "app"; exit
        }
        print "app"; exit
      }
      if (b == "codex-exec" || b == "codex_exec") { print "exec"; exit }
      if (b == "codex-tui" || b == "codex-app-server" || b == "codex_app_server") { print "app"; exit }
    }
  '
}

sdf_codex_exec_name() {
  override=${CODEX_INTERNAL_ORIGINATOR_OVERRIDE:-}
  case "$override" in
    ""|*[!A-Za-z0-9._-]*) printf '%s' "codex_exec" ;;
    *)
      if [ "${#override}" -le 64 ]; then
        printf '%s' "$override"
      else
        printf '%s' "codex_exec"
      fi
      ;;
  esac
}

sdf_codex_service() {
  pid=$$
  hops=0
  while [ "$hops" -lt 20 ]; do
    hops=$((hops + 1))
    args=$(ps -o args= -p "$pid" 2>/dev/null || true)
    mode=$(sdf_codex_mode "$args" || true)
    case "$mode" in
      exec)
        sdf_codex_exec_name
        return
        ;;
      app)
        printf '%s' "codex-app-server"
        return
        ;;
    esac
    ppid=$(ps -o ppid= -p "$pid" 2>/dev/null || true)
    ppid=$(printf '%s' "$ppid" | tr -cd '0-9')
    if [ -z "$ppid" ] || [ "$ppid" = "0" ] || [ "$ppid" = "1" ] || [ "$ppid" = "$pid" ]; then
      break
    fi
    pid=$ppid
  done
  sdf_codex_exec_name
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
        "{\"key\":\"` + genai.AttrConversationID + `\",\"value\":{\"stringValue\":\"$esc_sid\"}}," \
        "{\"key\":\"` + genai.AttrSystem + `\",\"value\":{\"stringValue\":\"pi\"}},")
      ;;
    opencode)
      service=opencode
      sess=$(printf '%s' "{\"key\":\"session.id\",\"value\":{\"stringValue\":\"$esc_sid\"}},")
      ;;
    grok-cli|claude-code|qwen-code)
      service=$agent
      sess=$(printf '%s' "{\"key\":\"session.id\",\"value\":{\"stringValue\":\"$esc_sid\"}},")
      ;;
    codex)
      service=$(sdf_codex_service)
      case "$service" in
        ""|*[!A-Za-z0-9._-]*) service=codex_exec ;;
      esac
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

# sdf_pre_push runs sdf decision pre-push on the refs git is pushing ($1 is
# the remote). It returns 1 only when that command blocks the push (block
# mode). No sdf, an sdf without the command, or any other failure prints
# one line and lets the push go on.
sdf_pre_push() {
  case "${SDF_PRE_PUSH:-}" in
    off|OFF|Off|0|false|no) return 0 ;;
  esac
  sdf_bin=$SDF_HOOK_BIN
  if [ -z "$sdf_bin" ] || [ ! -x "$sdf_bin" ]; then
    sdf_bin=$(command -v sdf 2>/dev/null || true)
  fi
  if [ -z "$sdf_bin" ]; then
    echo 'sdf: pre-push decision check skipped: sdf is not installed or not on PATH. The push continues.' >&2
    return 0
  fi
  sdf_db=${SDF_PRE_PUSH_DB:-$SDF_HOOK_DB}
  if [ -n "$sdf_db" ]; then
    printf '%s\n' "$captured" | "$sdf_bin" decision pre-push --remote "${1:-}" --db "$sdf_db"
  else
    printf '%s\n' "$captured" | "$sdf_bin" decision pre-push --remote "${1:-}"
  fi
  sdf_status=$?
  case "$sdf_status" in
    0) return 0 ;;
    1) return 1 ;;
  esac
  echo "sdf: pre-push decision check could not run (sdf exited $sdf_status). The push continues." >&2
  return 0
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
  pre-push)
    if [ "$prev_status" -ne 0 ]; then
      exit "$prev_status"
    fi
    sdf_pre_push "$@"
    exit $?
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

func hookScript(cfg hookConfig, kind string) string {
	body := strings.ReplaceAll(hookScriptTemplate, "__SDF_HOOK_URL__", cfg.url)
	body = strings.ReplaceAll(body, "__SDF_HOOK_KIND__", kind)
	body = strings.ReplaceAll(body, "__SDF_HOOK_BIN__", shQuote(cfg.bin))
	return strings.ReplaceAll(body, "__SDF_HOOK_DB__", shQuote(cfg.db))
}
