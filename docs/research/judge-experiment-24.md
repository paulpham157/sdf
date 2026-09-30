# Offline judge experiment (#24) — local Evidence

**Evidence mode:** synthetic (local Evidence per [ADR-0006](../adr/0006-impact-measurement.md)).  
**Not** production impact. **No** wiring into `ExecutionService` or Escalation.

Part of [#19](https://github.com/paulpham157/sdf/issues/19). Dataset from [#21](https://github.com/paulpham157/sdf/issues/21). Local backend from [#23](https://github.com/paulpham157/sdf/issues/23).

## How to reproduce

```bash
# Fake oracle smoke (CI-safe, no weights)
SDF_JUDGE_BACKEND=fake uv run python -m scripts.judge_experiment \
  --output artifacts/judge_experiment/fake-smoke.json --runs 3

# Local AgentJev (needs process on SDF_AGENTJEV_URL; see agentjev-local.md)
SDF_JUDGE_BACKEND=local uv run python -m scripts.judge_experiment \
  --output artifacts/judge_experiment/local-3x.json --runs 3
```

Tests: `uv run pytest -q tests/test_judge_experiment.py`.

## Backends run

| Backend | Runs × cases | AgentJev |
| --- | --- | --- |
| `fake` (oracle labels) | 3 × 41 | N/A — smoke / schema |
| `local` | 3 × 41 | **AgentJev-0.6B**, checkpoint SHA-256 `76d409b3ff2dd5ebb69887ab3929e8f793ede03f4b71a1b851c6dd4c868c53c2`, process clone commit `a965ca8ff06ccabc0c796dca5447b55cc2069cee` |

Raw prediction dumps (JSON) live under `artifacts/judge_experiment/` (gitignored). Summaries: `artifacts/judge_experiment/*.md`.

## Key metric (withhold policy)

Question from [jev.md §5](jev.md): *how many escalations would a confident non-agent label have withheld, and how many of those were wrong?*

Non-agent causes: everything except `agent_solution_wrong` and `agent_did_not_attempt`.  
Default confidence threshold: **0.8**. Wrong = hand label `higher_tier_would_help=true`.

| Backend | Accuracy | ECE | Withheld @0.8 | Wrong @0.8 |
| --- | ---: | ---: | ---: | ---: |
| fake | 1.000 | 0.050 | 75 | 0 |
| local | **0.293** | 0.096 | **0** | **0** |

Local never reached confidence ≥ 0.8 (max observed ≈ 0.575). At a softer 0.5 threshold it would withhold 12 predictions (4 unique cases × 3 runs) with **0** wrong withholds — but that is a thin slice, not a policy-ready band.

## What AgentJev handles / misses

Per-cause correct / support (3 runs pooled, 123 available answers):

| Cause | Correct | Support | Note |
| --- | ---: | ---: | --- |
| `task_or_fixture_defect` | 18 | 24 | Best class; often over-predicted |
| `environment_or_runtime` | 9 | 24 | Partial signal |
| `credential_or_provider` | 6 | 27 | Weak; often → `agent_solution_wrong` |
| `agent_solution_wrong` | 3 | 24 | Collapses into `task_or_fixture_defect` |
| `agent_did_not_attempt` | 0 | 24 | Missed entirely |
| `not_stated` | — | 0 | No labelled cases in #21 set |

Runs were **deterministic** on this machine (identical accuracy each of 3 passes). Bias: prefers `task_or_fixture_defect` and `agent_solution_wrong` over empty-diff / credential stories.

Binary `higher_tier_would_help` (noul ≥ 0.5 vs label): **75 / 123 ≈ 61%** — better than cause accuracy, still not decision-grade.

## Does the 2,048-token cut lose signal?

**No on this dataset.** `fit_state_for_agentjev` trimmed **0 / 41** cases. The #21 fixtures were already built to stay under ~3k JSON characters. Token-budget loss is not the explanation for the accuracy gap.

## Recommendation on #19 / ADR-0010

**Local AgentJev-0.6B alone is not good enough** to accept ADR-0010 or to withhold escalations in production.

- Cause accuracy (~29%) is near chance for five labelled causes.
- At the intended high-confidence gate (0.8) the model **never** withholds — so it cannot deliver the budget-saving policy the research memo describes.
- Softening the gate finds a few safe non-agent withholds, but that is not the same as calibrated high-confidence triage.

**Hosted comparison (#22 Cloudflare / typesafe Jev) is needed before deciding ADR-0010.** Keep the `Judge` seam, `FakeJudge`, and local backend; do not wire Escalation. If hosted Jev is also weak on this set, revisit the question shape or drop typed triage for escalation withhold.

## Acceptance checklist

- [x] Results for `local` and `fake`, with AgentJev version/commit
- [x] Notes on which causes are handled/missed; 2,048-token cut
- [x] Recommendation on #19: local not enough; need #22 before ADR-0010
- [x] No production wiring
