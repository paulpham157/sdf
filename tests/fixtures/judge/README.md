# Judge dataset: synthetic failed Attempts

This directory holds 30–50 synthetic failed Attempts. Each one carries a hand
label for its likely failure cause (issue #21). The dataset feeds the offline
triage experiment in #24 and the advisory `Judge` seam proposed in
[ADR-0010](../../../docs/adr/0010-advisory-typed-judgments.md). See
[docs/research/jev.md](../../../docs/research/jev.md) §5 for the plan.

Every case is built from small made-up Python modules. Where possible the
builders run the real `DeterministicEvaluator` or `AgentFixtureLoop` with a
scripted `FakeRuntime`, so commands, exit codes and log tails are real output.
There is no customer code, and no real credential, hostname or private path.
Secret-shaped text is written as `<REDACTED>`.

## Labels are by construction

Nobody inspects a failure after the fact to decide its label. Each builder
induces one specific failure on purpose, and the label records that cause.
`label.induced_by` says exactly how. If a builder's run stops failing the way
it was induced (for example, the check starts passing), the builder raises an
error instead of writing a mislabeled case.

## Case schema

One JSON file per case, `cases/<failure_cause>-NNN.json`, written with
`indent=2`, sorted keys and a trailing newline:

```json
{
  "id": "task_or_fixture_defect-001",
  "label": {
    "failure_cause": "task_or_fixture_defect",
    "higher_tier_would_help": false,
    "induced_by": "One sentence on how the failure was induced."
  },
  "state": {
    "task_instructions": "...",
    "evaluator_status": "FAIL | TIMEOUT | ERROR",
    "failed_evidence": [
      {"criterion": "...", "command": "...", "exit_code": 4, "stderr_tail": "..."}
    ],
    "adapter_stderr_tail": "...",
    "diff_stat": "1 file, +1 -1 | 0 files changed | unknown"
  }
}
```

`state` uses the same keys as the AgentJev prototype (#23). It must stay
within 3,000 characters of JSON, because AgentJev-0.6B has a 2,048-token
context. Log tails keep at most the last 15 lines. Temp paths are normalized
to `/workspace`, and timings are stripped, so every rerun writes the same
bytes.

## Causes

`higher_tier_would_help` is `true` only for the two agent-caused labels.

| `failure_cause` | Meaning | How it was induced |
|---|---|---|
| `agent_solution_wrong` | The agent changed code, but the checks show the change is wrong or incomplete. | A scripted agent edit with a deliberate bug (off by one, wrong branch, partial fix, a rename that breaks the import). |
| `agent_did_not_attempt` | The diff is empty or has nothing to do with the instructions. | No edit, or edits only to unrelated files, docstrings or whitespace. |
| `environment_or_runtime` | A sandbox, network, timeout or process failure unrelated to the code change. | Real evaluator timeouts, a missing runner, a killed process, a full disk, or the real transport errors from E2B and Herdr. |
| `credential_or_provider` | A model provider authentication, quota or rate-limit error. | Revoked or expired keys, exhausted credit or quota, rate limits and a misconfigured base URL. Keys are shown as `<REDACTED>`. |
| `task_or_fixture_defect` | The acceptance check itself is broken or contradicts the instructions. | A correct agent solution judged by a broken check: a missing test file, an assertion that contradicts the instructions, a syntax or import error in the test, an unmentioned function name, a typo in the command, or a check that fails on any correct solution. |
| `not_stated` | The state does not show enough to tell. | Deliberately not labeled. It is a valid judge answer, but a case built on purpose always has a known cause. |

## Regenerate

```sh
uv run python -m scripts.judge_dataset.agent_cases
uv run python -m scripts.judge_dataset.infra_cases
uv run python -m scripts.judge_dataset.fixture_cases
uv run pytest -q tests/test_judge_dataset.py
```

The test checks the schema, the counts per cause, a secret and privacy scan,
and that every builder's `build_cases()` matches the files on disk.
