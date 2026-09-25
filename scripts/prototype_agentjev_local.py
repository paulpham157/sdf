"""PROTOTYPE, throwaway (#23): can AgentJev-0.6B run on this Mac and triage failed Attempts?

Answers one question before #20/#23/#24: does the local model load, how fast is it,
and does it give a sensible `failure_cause` on a few synthetic failed Attempts?

Needs a running server (see the printed hint). Run:
    python scripts/prototype_agentjev_local.py [http://127.0.0.1:8149]
"""

import json
import sys
import time
import urllib.request

URL = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8149"

CAUSES = {
    "agent_solution_wrong": "The agent changed code but the checks show its change is incorrect or incomplete",
    "agent_did_not_attempt": "The diff is empty or unrelated to the instructions",
    "environment_or_runtime": "Sandbox, network, timeout, or process failure unrelated to the code change",
    "credential_or_provider": "Model provider authentication, quota, or rate-limit error",
    "task_or_fixture_defect": "The acceptance check itself is broken or contradicts the instructions",
    "not_stated": "The state does not show enough to tell",
}

# AgentJev's local contract differs from Jev's wire shape: `boolean` not `noul`,
# `options` not `criteria`. A Judge seam has to translate.
QUESTIONS = [
    {"id": "failure_cause", "type": "choice",
     "instructions": "What most likely caused this Attempt's verified failure?", "options": CAUSES},
    {"id": "higher_tier_would_help", "type": "boolean",
     "instructions": "Would a more capable coding model plausibly fix this failure without changing the task or environment?"},
]

# Synthetic cases, label known by construction. No real code, no real credentials.
CASES = [
    ("agent_solution_wrong", {
        "task_instructions": "Make add(a, b) in calc.py return the sum.",
        "evaluator_status": "FAIL",
        "failed_evidence": [{"criterion": "add returns the sum", "command": "pytest -q", "exit_code": 1,
                             "stderr_tail": "E   assert add(2, 3) == 5\nE    +  where 6 = add(2, 3)\n1 failed, 3 passed"}],
        "adapter_stderr_tail": "", "diff_stat": "1 file, +1 -1"}),
    ("agent_did_not_attempt", {
        "task_instructions": "Make add(a, b) in calc.py return the sum.",
        "evaluator_status": "FAIL",
        "failed_evidence": [{"criterion": "add returns the sum", "command": "pytest -q", "exit_code": 1,
                             "stderr_tail": "E   NotImplementedError\n1 failed, 3 passed"}],
        "adapter_stderr_tail": "", "diff_stat": "0 files changed"}),
    ("environment_or_runtime", {
        "task_instructions": "Make add(a, b) in calc.py return the sum.",
        "evaluator_status": "TIMEOUT",
        "failed_evidence": [],
        "adapter_stderr_tail": "e2b: sandbox i8x2 was killed: connection reset by peer\nherdr: pane closed before idle",
        "diff_stat": "unknown"}),
    ("credential_or_provider", {
        "task_instructions": "Make add(a, b) in calc.py return the sum.",
        "evaluator_status": "FAIL",
        "failed_evidence": [],
        "adapter_stderr_tail": "API Error: 401 {\"type\":\"authentication_error\",\"message\":\"invalid x-api-key <REDACTED>\"}",
        "diff_stat": "0 files changed"}),
    ("task_or_fixture_defect", {
        "task_instructions": "Make add(a, b) in calc.py return the sum.",
        "evaluator_status": "FAIL",
        "failed_evidence": [{"criterion": "add returns the sum", "command": "pytest -q", "exit_code": 4,
                             "stderr_tail": "ERROR: file or directory not found: tests/test_calcc.py\nno tests ran"}],
        "adapter_stderr_tail": "", "diff_stat": "1 file, +1 -1"}),
]


def evaluate(state):
    body = json.dumps({"state": state, "questions": QUESTIONS}).encode()
    req = urllib.request.Request(URL + "/api/evaluate", body, {"Content-Type": "application/json"})
    t = time.perf_counter()
    with urllib.request.urlopen(req, timeout=300) as r:
        out = json.load(r)
    return out, (time.perf_counter() - t) * 1000


def main():
    hits = 0
    for label, state in CASES:
        out, ms = evaluate(state)
        answers = {a["id"]: a for r in out.get("results", [out]) for a in r.get("answers", [])} or out
        print(f"\n=== {label}  ({ms:.0f} ms)")
        print(json.dumps(out, indent=1)[:1800])
        fc = answers.get("failure_cause", {})
        best = fc.get("value")
        hits += best == label
        print(f"-> predicted {best} p={fc.get('top_probability', 0):.2f} margin={fc.get('margin', 0):.2f}"
              f"  higher_tier={answers.get('higher_tier_would_help', {}).get('probability')}"
              f"  {'OK' if best == label else 'MISS'}")
    print(f"\n{hits}/{len(CASES)} correct")


if __name__ == "__main__":
    main()
