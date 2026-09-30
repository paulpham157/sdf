# Offline judge experiment (issue #24)

- Recorded at: `2026-09-30T04:33:44.723305+00:00`
- Backend: `fake`
- Runs: 3
- Cases: 41
- Evidence: local Evidence per ADR-0006; not production impact
- Confidence threshold (withhold): 0.8

## Key metrics

- `failure_cause` accuracy: **1.0000**
- `failure_cause` calibration ECE: **0.0500**
- Escalations a confident non-agent label would withhold: **75**
- Of those, wrong (hand label says higher tier would help): **0**
- Available / unavailable predictions: 123 / 0

## Per-cause accuracy

| cause | correct | support |
| --- | ---: | ---: |
| `agent_solution_wrong` | 24 | 24 |
| `agent_did_not_attempt` | 24 | 24 |
| `environment_or_runtime` | 24 | 24 |
| `credential_or_provider` | 27 | 27 |
| `task_or_fixture_defect` | 24 | 24 |
| `not_stated` | 0 | 0 |

## Token fit (2,048)

- Cases needing trim before ask: 0
- Trimmed ids: (none)

## AgentJev

- N/A (fake oracle backend)
