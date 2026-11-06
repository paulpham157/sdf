#!/usr/bin/env bash
# Offline public-path evidence loop (default fake adapter).
#
# Walks Business Context -> Objective -> Assumption -> Task ->
# POST /tasks/{id}/run, then prints evaluator Evidence, the Attempt
# revision, the task trace, and cost. No E2B or model keys. This fixture
# loop is not production proof (docs/live-full-loop.md).
set -euo pipefail

repo_root="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if ! command -v uv >/dev/null 2>&1; then
  printf 'uv is required. Install it, then run: uv sync --all-groups\n' >&2
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  printf 'curl is required\n' >&2
  exit 1
fi

# Force the offline path. A caller shell or repo .env must not turn this
# into a live run. os.environ wins over .env.
export SDF_RUN_ADAPTER=fake
export SDF_AUTH_DEV_INSECURE=1
export SDF_FIXTURES_ROOT="$repo_root/fixtures"
unset SDF_LIVE_FULL_LOOP SDF_LIVE_E2B SDF_REQUIRE_PLAN_APPROVAL \
  SDF_EVALUATOR_CONTAINMENT SDF_EVALUATOR_CHECKS_FILE SDF_DOTENV || true

demo_dir="$(mktemp -d "${TMPDIR:-/tmp}/sdf-evidence-XXXXXX")"
export SDF_DATABASE_URL="sqlite+pysqlite:///${demo_dir}/sdf.db"
port="${SDF_DEMO_PORT:-8765}"
base="http://127.0.0.1:${port}"
log="$demo_dir/uvicorn.log"
server_pid=""

cleanup() {
  if [[ -n "$server_pid" ]] && kill -0 "$server_pid" 2>/dev/null; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT

if curl -sf -m 1 "$base/healthz" >/dev/null 2>&1; then
  printf 'port %s is already in use; set SDF_DEMO_PORT\n' "$port" >&2
  exit 1
fi

uv run uvicorn sdf_core.api:app --host 127.0.0.1 --port "$port" >"$log" 2>&1 &
server_pid=$!

ready=0
for _ in $(seq 1 100); do
  if curl -sf -m 1 "$base/healthz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! kill -0 "$server_pid" 2>/dev/null; then
    break
  fi
  sleep 0.1
done
if [[ "$ready" -ne 1 ]]; then
  printf 'API did not become ready. Log:\n' >&2
  cat "$log" >&2 || true
  exit 1
fi

post() {
  local name="$1" path="$2" body="$3"
  curl -sS -m 60 -D "$demo_dir/$name.hdr" -o "$demo_dir/$name.json" \
    -X POST "$base$path" -H 'content-type: application/json' -d "$body"
  printf '%s %s\n' "$(head -n 1 "$demo_dir/$name.hdr" | tr -d '\r')" "$path"
}

printf 'fixture: %s\n' "$SDF_FIXTURES_ROOT/offline-evidence"
printf 'database: %s\n' "$SDF_DATABASE_URL"
printf 'SDF_RUN_ADAPTER=%s SDF_LIVE_FULL_LOOP unset\n' "$SDF_RUN_ADAPTER"
if [[ -n "${E2B_API_KEY:-}${OPENAI_API_KEY:-}${ANTHROPIC_API_KEY:-}${OPENCODE_API_KEY:-}${PI_API_KEY:-}${QWEN_API_KEY:-}${GOOSE_API_KEY:-}" ]]; then
  printf 'provider keys are set in this shell and were not used\n'
else
  printf 'no E2B or model API keys in this shell\n'
fi

post bc /business-contexts \
  '{"id":"BC-OFFLINE-001","title":"Offline evidence demo","owner":"local-dev","source":"demo"}'
post obj /objectives \
  '{"id":"OBJ-OFFLINE-001","title":"Show independent fake-adapter evidence","business_context_id":"BC-OFFLINE-001","owner":"local-dev","source":"demo"}'
post asm /assumptions \
  '{"id":"ASSUMPTION-OFFLINE-001","title":"fake adapter writes print(updated)","owner":"local-dev","source":"demo"}'
post task /tasks \
  '{"id":"TASK-OFFLINE-001","title":"Apply the deterministic fake edit","objective_id":"OBJ-OFFLINE-001","idempotency_key":"offline-evidence-001","acceptance_criteria":["app.py prints updated"]}'
post run /tasks/TASK-OFFLINE-001/run \
  '{"dispatch_key":"offline-evidence-001","fixture_dir":"offline-evidence","instructions":"replace app.py so it prints updated","criterion_checks":{"app.py prints updated":["app-updated"]},"validation_target_kind":"assumption","validation_target_id":"ASSUMPTION-OFFLINE-001"}'

uv run python - "$demo_dir" "$base" <<'PY'
import json
import os
import sys
import urllib.request
from pathlib import Path

from sdf_core.db import UsageLedgerRow, make_session_factory

demo = Path(sys.argv[1])
base = sys.argv[2]

def load(name):
    return json.loads((demo / name).read_text(encoding="utf-8"))

def get(path):
    with urllib.request.urlopen(base + path, timeout=30) as response:
        return json.load(response)

run = load("run.json")
problems = []
if run.get("task_status") != "succeeded" or run.get("provider") != "fake":
    problems.append(f"run response: {run}")
if run.get("evidence_mode") != "synthetic":
    problems.append(f"evidence_mode: {run.get('evidence_mode')}")
attempt_id = run.get("attempt_id")
if not attempt_id:
    problems.append("missing attempt_id")
    print("\n".join(problems), file=sys.stderr)
    sys.exit(1)

trace = get(f"/tasks/TASK-OFFLINE-001/trace")
(demo / "trace.json").write_text(json.dumps(trace, indent=2) + "\n", encoding="utf-8")
evidence_ids = [node["id"] for node in trace["nodes"] if node.get("kind") == "evidence"]
if len(evidence_ids) != 1:
    problems.append(f"expected one evidence node, got {evidence_ids}")
evidence = get(f"/evidence/{evidence_ids[0]}") if evidence_ids else {}
(demo / "evidence.json").write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
revision = get(f"/attempts/{attempt_id}/revision")
(demo / "revision.json").write_text(json.dumps(revision, indent=2) + "\n", encoding="utf-8")

if evidence.get("status") != "PASS":
    problems.append(f"evidence status {evidence.get('status')}")
if evidence.get("attempt_id") != attempt_id:
    problems.append("evidence is not bound to this attempt")
if evidence.get("verifier_identity") != "sdf-evaluator" or evidence.get("verifier_role") != "evaluator":
    problems.append(f"verifier {evidence.get('verifier_identity')!r} {evidence.get('verifier_role')!r}")
if evidence.get("evidence_class") != "verification":
    problems.append(f"evidence_class {evidence.get('evidence_class')}")
if not evidence.get("revision_id") or not evidence.get("tree_sha256"):
    problems.append("evidence has no revision binding")
edges = trace.get("edges") or []
if not any(edge.get("source_id") == evidence.get("id") and edge.get("relation") == "validates"
           and edge.get("target_id") == "ASSUMPTION-OFFLINE-001" for edge in edges):
    problems.append("trace has no Evidence validates Assumption edge")
rev = revision.get("revision") or {}
if revision.get("verification", {}).get("status") != "verified":
    problems.append(f"revision verification: {revision.get('verification')}")
if rev.get("result_tree_sha256") != evidence.get("tree_sha256"):
    problems.append("evidence tree_sha256 does not match the attempt revision")

db = make_session_factory(os.environ["SDF_DATABASE_URL"])()
try:
    ledger_rows = db.query(UsageLedgerRow).filter_by(attempt_id=attempt_id).count()
finally:
    db.close()

print("--- run ---")
for key in ("task_id", "task_status", "attempt_id", "attempt_status", "provider",
            "evidence_mode", "cost_usd", "latency_ms", "input_tokens", "output_tokens"):
    print(f"{key}={run.get(key)}")
print("--- evidence (evaluator, not the adapter stdout) ---")
for key in ("id", "attempt_id", "status", "criterion", "evidence_class", "verifier_identity",
            "verifier_role", "producer_identity", "evidence_mode", "revision_id", "tree_sha256",
            "exit_code", "oracle_visibility"):
    print(f"{key}={evidence.get(key)}")
print("--- revision ---")
for key in ("id", "repository", "tree_format", "base_tree_sha256", "result_tree_sha256",
            "base_commit", "base_commit_source", "evidence_mode"):
    print(f"{key}={rev.get(key)}")
print(f"verification={revision.get('verification')}")
print("--- trace ---")
print("nodes:", ", ".join(f"{node['kind']}:{node['id']}" for node in trace["nodes"]))
print("edges:")
for edge in edges:
    print(f"  {edge['source_kind']}:{edge['source_id']} {edge['relation']} {edge['target_kind']}:{edge['target_id']}")
print(f"runtime_events={len(trace.get('runtime_events') or [])}")
print("--- cost ---")
print(f"attempt.cost_usd={run.get('cost_usd')} usage_ledger_rows={ledger_rows}")
print("cost_usd is the unmetered fake adapter. It is not an invoice and not an SDF_PRICE_* or SDF_E2B_USD_PER_SECOND estimate.")
if problems:
    print("--- failed ---", file=sys.stderr)
    print("\n".join(problems), file=sys.stderr)
    sys.exit(1)
print(f"ATTEMPT_ID={attempt_id}")
PY

attempt_id="$(uv run python -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["attempt_id"])' "$demo_dir/run.json")"
printf '%s\n' "--- python -m sdf_core.cli runs show ${attempt_id} ---"
uv run python -m sdf_core.cli runs show "$attempt_id"

printf 'workspace: %s/.sdf-workspaces/%s\n' "$repo_root" "$attempt_id"
printf 'artifacts: %s/.sdf-artifacts/\n' "$repo_root"
printf 'offline evidence demo passed (synthetic fixture loop, not production proof)\n'
