# Offline judge experiment (issue #24)

- Recorded at: `2026-09-30T04:35:54.123548+00:00`
- Backend: `local`
- Runs: 3
- Cases: 41
- Evidence: local Evidence per ADR-0006; not production impact
- Confidence threshold (withhold): 0.8

## Key metrics

- `failure_cause` accuracy: **0.2927**
- `failure_cause` calibration ECE: **0.0956**
- Escalations a confident non-agent label would withhold: **0**
- Of those, wrong (hand label says higher tier would help): **0**
- Available / unavailable predictions: 123 / 0

## Per-cause accuracy

| cause | correct | support |
| --- | ---: | ---: |
| `agent_solution_wrong` | 3 | 24 |
| `agent_did_not_attempt` | 0 | 24 |
| `environment_or_runtime` | 9 | 24 |
| `credential_or_provider` | 6 | 27 |
| `task_or_fixture_defect` | 18 | 24 |
| `not_stated` | 0 | 0 |

## Token fit (2,048)

- Cases needing trim before ask: 0
- Trimmed ids: (none)

## AgentJev

- Model: `AgentJev-0.6B`
- Commit: `a965ca8ff06ccabc0c796dca5447b55cc2069cee`
- URL: `http://127.0.0.1:8149`
