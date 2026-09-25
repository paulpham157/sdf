# TypeSafe Jev: what it is and whether SDF should use it

Question: what is TypeSafe's Jev, and can or should SDF Core use it?

Researched on 2026-09-25/26 from TypeSafe's own pages (launch post, HTTP API
reference, jaggedness page, privacy policy), the Cloudflare model page, and the
READMEs of six community repositories. No Jev or paid API was called and no key
was used, so nothing here is measured by SDF. A claim marked *secondary* comes
from a third party, not TypeSafe.

**Verdict: experiment, offline and advisory only.** Jev fits SDF's
"cheap typed question about messy text" shape and costs almost nothing per
call. But SDF's central invariant is that an **Evaluator**, deterministic or
policy-controlled, turns an **Attempt**'s outputs into **Evidence**, and an
agent cannot self-certify its result (ADR-0002, ADR-0004). A hosted,
probabilistic, weeks-old model must not sit on that path. The first step is an
offline experiment on failed-Attempt triage behind a `Judge` seam with a fake
backend. Nothing ships into `ExecutionService` until that experiment shows
agreement with hand labels. Details and ranking follow.

## 1. What Jev is

- **A "System One" decision model, not a text model.** TypeSafe calls it "a
  frontier-intelligence function call: unstructured state in, typed
  probabilistic decisions out". It gives up string generation and returns only
  typed values whose possible outputs are "defined in advance"
  ([launch post][blog]). The name System One comes from Kahneman's
  fast/slow split.
- **Request.** `POST https://api.typesafe.ai/v1/systemone` with
  `Authorization: Bearer <API_KEY>`. The body has `state` (a string or a JSON
  object/array), `model` (`"jev-latest"`), and `questions`, a map from
  caller-chosen keys to typed questions ([HTTP API][api]).
- **Three question types** ([Cloudflare model page][cf], [HTTP API][api]):
  - `noul`: a yes/no proposition with optional `criteria` for `true` and
    `false`. It returns `noul`, the probability of true.
  - `choice`: up to 255 labelled options. It returns `choice`, `probabilities`
    and `confidence`.
  - `score`: 2–10 ordered levels. It returns a fractional `score`,
    `probabilities`, `confidence` and a `legend`.
- **Response.** `model` gives the resolved version (e.g. `"jev-1.13.0"`),
  followed by `answers` keyed like the questions and
  `usage.{input_tokens,output_tokens}` ([cf]).
- **Latency.** TypeSafe claims 70–500 ms end to end, measured from laptops on
  the US West Coast. Its headline "193.6x faster, 444.6x cheaper" figures are
  ones it expects "are on the higher end of real world gains" ([blog]).
- **Price.** Input costs $0.042 per million tokens and output is "FREE (too
  cheap to meter)". TypeSafe also writes "We can't prove it isn't subsidized"
  ([blog]). At that rate a 4k-token SDF state costs about $0.00017.
- **Limits.** The Cloudflare page lists a 32,000-token context window ([cf]).
  The HTTP API page gives no token limit, and neither page publishes numeric
  rate limits. 429 and 529 responses call for exponential backoff, which the
  official SDKs do for you ([api]).
- **Calibration.** Training uses "Reinforcement Learning for Calibrated
  Decisions (RLCD)", aimed at "epistemically honest probabilities on System One
  tasks" ([blog]). Accuracy is reported relative to a reference (the average of
  two frontier models) on TypeSafe's own workflows. TypeSafe notes "some bias
  could exist" because its own team built those workflows.
- **Known weaknesses.** These come from TypeSafe's own [jaggedness page][jag]
  for `jev-1.13`, reviewed 2026-09-17:
  - Literal reading: it "answers the question you wrote, not the one you
    meant".
  - It "is not a calculator" and "does not count reliably".
  - It "reads dates as text, not as ordered quantities".
  - Irrelevant state is a distractor and it "suffers from context rot".
  - It "does not treat [adversarial content] as hostile by default".
  - Structural invariants "simply aren't guaranteed". In TypeSafe's example, a
    Noul and its negation summed to 1.19.
  - A threshold tuned on a Noul must not be reused for a Choice.
- **Privacy.** TypeSafe says "We will not train or fine tune any artificial
  intelligence or machine learning models on your prompts or other Input".
  Personal data is kept "for as long as reasonably necessary". "The Services
  are hosted in the United States". Service providers and Google Analytics
  receive some data ([privacy policy][priv], last updated 2025-11-19). The
  policy does not mention zero data retention. Cloudflare's hosted
  `typesafe/jev` is tagged "Zero data retention" ([cf]).
- **Maturity.** It launched "in early access" with a 2026-09-15 byline
  ([blog]), so it is about ten days old. The version is `jev-1.13.0` behind
  the moving alias `jev-latest`. There are official Python and JS SDKs. There
  is no public changelog, status page or SLA (none found by the author; the
  jaggedness page is the closest thing to release notes).
  Jev is also offered through Cloudflare Workers AI and, per community
  READMEs, OpenRouter, Vercel AI Gateway and LiteLLM.
- **Independent checks (*secondary*).** A community test finds Jev
  well calibrated on three public benchmarks. On an unseen synthetic rule
  task, though, its calibration error was 4.4× the noise floor (0.107 vs
  0.024), overconfident on choice/score and underconfident on yes/no
  ([jev-ood-calibration][ood]). A 60-case tool-call risk benchmark reports
  91.7% accuracy with calibration holding, but one wrong answer came at 0.97
  confidence ([dev.to][devto]). Neither covers SDF-like state such as test
  logs and diffs.

## 2. Ecosystem patterns (community, all unofficial)

All six repositories exist. Each is under two weeks old, and every one except
the awesome list says it is not affiliated with TypeSafe.

| Repo | What it is | Pattern worth noting |
| --- | --- | --- |
| [nandansrikrishna/jev-agent-tool][t1] (MIT, 0★, beta 0.1.0b1) | Python CLI/API/MCP over the official SDK | Batch JSONL evaluation. A resume fingerprint covers record, questions and model name. It warns that `jev-latest` can move without changing the fingerprint. Errors omit upstream bodies and private input. |
| [walidboulanouar/jev-agent-kit][t2] (MIT, 2★) | Node CLI + MCP: `check`, `judge`, `route`, `triage`, `guard`, `rank`, `compact` | **Abstain**: `route` "abstains when nothing fits". `guard` maps to allow/**ask**/deny. Exit code 3 separates API error from "no". It recommends pinning `jev-1.13.0`. |
| [openlayer-ai/jevals][t3] (MIT, 86★) | Evals and guardrails as Jev questions | An eval is `state()` / `questions()` / `reduce()`, and **plain code does what code is good at** before any question is asked. Backends are pluggable: TypeSafe, Vercel, local Kev/Laya, an emulated chat LLM, and `backend="mock"` in tests. |
| [malevrigns/agent-jev][t4] (Apache-2.0, 303★) | AgentJev-0.6B, open weights on a Qwen3-0.6B backbone, speaking the same three primitives | Runs locally with a 2,048-token context. It reports 79.25% top-1 on the Typed Decisions test split (self-reported, measured as agreement with a teacher model, not ground truth). Choice `margin` "is not an independent probability that the action will succeed". |
| [vinilana/jev-gateway][t5] (MIT, 223★) | Local LLM gateway that asks Jev which tool the coding agent should call | **Fail open**: "If Jev is down, slow, or your key is wrong, every request simply goes straight to the LLM". It steers only when Jev is confident and has a `--routing off` baseline mode for comparison. |
| [hellogumbo/awesome-jev][t6] (CC0, 191★) | Directory of about 1,094 entries | Links the official SDKs, docs, the [workflow evals][evals] and the official `system-one-adapter-python` (a TypeSafe client backed by ordinary LLMs, useful for comparison). |

Four points shape the SDF recommendation. The wire format already has
open-weight implementations (Kev, Laya, AgentJev), so the API shape carries
less lock-in than the vendor does. Every serious integration keeps a fake or
mock backend. Integrations fail open or abstain rather than block. And code
filters the state before the model sees it, as TypeSafe's own jaggedness page
advises.

## 3. Where SDF makes decisions today

Every decision SDF makes today is deterministic:

- **Escalation Policy.** `sdf_core/escalation.py:48` escalates only from the
  statuses `FAIL`/`INCONCLUSIVE`/`TIMEOUT`/`POLICY_FAILURE`/`TOOL_FAILURE`. It
  rejects anything unverified and is bounded by attempts, Model Tier and hard
  cost (ADR-0005, 07a).
- **Evaluator.** `sdf_core/evaluator.py:36` produces `PASS` only when every
  acceptance criterion maps to a deterministic check and every check passes.
  Otherwise it produces `FAIL` or `INCONCLUSIVE`.
- **Adapter success gate.** `sdf_core/execution.py:144` checks runtime
  completion and exit code 0. A failure here never reaches the Evaluator.
- **Turn completion.** In `sdf_core/herdr_runtime.py`, `send` treats a normal
  return of `agent prompt --wait --until idle` as the end of the turn, then
  `_settle_turn` requires `idle` to hold for a settle window (#16,
  [herdr-idle-not-done](herdr-idle-not-done.md)).
- **Policy Decision.** `AllowlistPolicy.decide` (`sdf_core/policy.py:196`) is
  an exact allowlist over tool/action, actor, resource and context. The Tool
  Proxy (`sdf_core/tools.py:284`) enforces it.
- **Routing.** `ModelRouter.route_measured` (`sdf_core/routing.py:138`) routes
  only on measured availability, success, latency, cost and load, and stale
  metadata is never a fallback.

The tests already fake every external boundary. Examples are `FakeRuntime`
(`sdf_core/runtime.py:121`), `FakeNativeAdapter` (`sdf_core/adapter.py:25`),
`FakeHerdr` and `FakeSandbox`. Configuration uses `SDF_*` environment
variables read in `sdf_core/api.py`. A Jev seam would follow the same two
conventions.

## 4. Candidate integration points, ranked by value and risk

Each candidate lists the question set Jev would receive. State is filtered in
code first: only the fields named, with each log tail capped at a few kB.

### 1. Failed-Attempt triage as advisory input to escalation (value: medium-high, risk: low)

Today every escalatable status looks the same to the Escalation Policy.
Suppose an Attempt fails because the E2B sandbox dropped, a credential expired,
or the fixture itself is broken. Escalating to a more expensive Model Tier then
spends budget without any chance of an Accepted Outcome. Jev could label *why*
a verified failure happened. It would never decide *whether* the Attempt
failed.

```json
{
  "state": {"task_instructions": "...", "evaluator_status": "FAIL",
            "failed_evidence": [{"criterion": "...", "command": "pytest -q",
                                 "exit_code": 1, "stderr_tail": "..."}],
            "adapter_stderr_tail": "...", "diff_stat": "3 files, +40 -2"},
  "model": "jev-1.13.0",
  "questions": {
    "failure_cause": {"type": "choice",
      "instructions": "What most likely caused this Attempt's verified failure?",
      "criteria": {
        "agent_solution_wrong": "The agent changed code but the checks show its change is incorrect or incomplete",
        "agent_did_not_attempt": "The diff is empty or unrelated to the instructions",
        "environment_or_runtime": "Sandbox, network, timeout, or process failure unrelated to the code change",
        "credential_or_provider": "Model provider authentication, quota, or rate-limit error",
        "task_or_fixture_defect": "The acceptance check itself is broken or contradicts the instructions",
        "not_stated": "The state does not show enough to tell"}},
    "higher_tier_would_help": {"type": "noul",
      "instructions": "Would a more capable coding model plausibly fix this failure without changing the task or environment?"}
  }
}
```

The result would be used only as a recorded, advisory annotation. The rules:

- The escalation reason stays the Evaluator status.
- A future policy may *withhold* escalation on a confident
  `environment_or_runtime` or `credential_or_provider` label (retry at the
  same tier, or flag for a human). It must never escalate on Jev's word.
- `not_stated` or low confidence changes nothing, so the policy abstains.

### 2. Rubric criteria that no deterministic check covers (value: high, risk: high)

The Evaluator reports `INCONCLUSIVE` for criteria with no mapped check. A
`score` or `noul` per criterion over the diff could fill that gap, for example
"Does the change keep the public API backward compatible?". This is also the
closest fit to how [jevals][t3] works. But it changes what **Evidence** means.
It would need its own ADR and a distinct Evidence kind (e.g. `judgment`) with
`confidence` taken from Jev. On its own it could never produce `PASS` or an
**Accepted Outcome**. Jev's own caveats (literal reading, adversarial content,
context rot on large diffs) weigh most here, so this should come after
candidate 1 has proven itself.

### 3. Validation-edge suggestions in the Decision Graph (value: medium, risk: low-medium)

Given one piece of Evidence and one **Assumption**, Jev would answer a
`choice` of `supports` / `contradicts` / `unrelated`, and a human would
confirm before a **Validation edge** is written. This would help trace
completeness and Evidence coverage (ADR-0006) as the graph grows. There is too
little graph data today to justify it.

### 4. Tool Proxy second opinion (value: low today, risk: medium)

This is jevkit's `guard` idea: allow/ask/deny on a requested action. SDF
already has a deterministic allowlist plus enforced containment (ADR-0004), so
Jev could only ever *tighten*, turning an allowed action into one that needs a
human. It cannot tighten usefully while every allowlisted action is already
bounded. Jev also "does not treat [adversarial content] as hostile by
default", which is the wrong property for a guard.

### 5. Turn-completion judgement (value: low after #16, risk: medium)

A `noul` over the visible Herdr pane could answer "Is the agent waiting for
new user input, having finished its turn?". Since #16 the deterministic
settle window handles this. The pane is terminal output, which per ADR-0004
"is not a trusted tool request or evidence of policy enforcement". It can also show secrets or proprietary code, and the call
would add a network hop inside the runtime loop. **Skip.**

### 6. Model Tier or agent routing (value: low, risk: medium)

A `choice` over Model Tiers from the Task text would replace measured routing
and "lowest suitable tier" with a judgment. That contradicts ADR-0005 and
issue 09 ("stale static metadata is not a fallback"). **Skip.**

## 5. Recommended first experiment

The experiment is offline failed-Attempt triage against a fake and a
recorded baseline. No production path calls Jev.

1. **Seam.** Add a small `Judge` protocol with a single method
   `ask(state, questions) -> answers`, where questions and answers use the
   System One wire shape. Add a `FakeJudge` that returns scripted answers,
   like `FakeRuntime`. The typed-question shape costs little lock-in because
   Kev, Laya and AgentJev speak it too, and a plain LLM can emulate it
   (TypeSafe's own `system-one-adapter-python`).
2. **Dataset.** Build 30–50 failed Attempts from the repo's own deterministic
   fixtures and test runs: public, synthetic, no customer code. Label
   `failure_cause` by hand, including deliberately induced environment and
   credential failures with any secret-shaped text replaced by `<REDACTED>`.
3. **Run.** A script under `scripts/` (not `sdf_core/`) sends each case
   through the judge. It is opt-in behind
   `SDF_JUDGE_BACKEND=typesafe|local|fake`, defaults to `fake`, and is marked
   live like the existing `*_live` tests so CI never calls out. It pins
   `jev-1.13.0`. A local AgentJev/Laya run on the same set shows whether the
   hosted model is even needed.
4. **Measure.** Report accuracy and calibration for `failure_cause` against
   the labels. The number that matters is: "how many escalations would a
   confident non-agent label have withheld, and how many of those were
   wrong?" Record it as local Evidence per ADR-0006, not as production impact.
5. **Decide.** Wire an advisory annotation into escalation only if
   high-confidence labels are right on almost every withheld case. That
   step needs its own ticket and the ADR draft below.

Expected cost of the whole experiment: 50 cases × 3 runs × about 4k tokens is
about 600k input tokens, or about $0.03 at list price.

## 6. Risks

- **Invariant erosion.** This is the main risk. A probability that looks
  authoritative could quietly become a certification path. Mitigations: Jev
  output is advisory Evidence with its own kind; it may withhold escalation
  but never grant `PASS` or escalation; and every answer records the model
  version.
- **Secrets and proprietary code.** Attempt stdout, stderr and diffs come from
  a sandbox that holds an **Agent Credential** as environment variables
  (ADR-0007). An agent can print it. Any real state must be filtered and
  redacted in code before leaving the host. Customer code sent to a US-hosted
  third party with unspecified retention ([priv]) needs operator consent. ZDR
  exists only via specific gateways (e.g. [Cloudflare][cf]). A local
  open-weight backend avoids the question.
- **Public repo.** The Jev key must be a dedicated `SDF_*` variable, never
  committed or printed, following the ADR-0007 conventions. Experiment
  datasets must stay synthetic.
- **Calibration drift and version drift.** `jev-latest` moves upstream, so pin
  the version. Thresholds are per question type and per domain ([jag]).
  Community tests show calibration can break on unseen task types ([ood])
  and use small samples ([devto]), and none cover test logs or diffs. Recalibrate on SDF data before
  trusting any threshold.
- **Adversarial state.** Agent output is attacker-influenced by definition
  (prompt injection through fixture content). Jev does not treat it as hostile
  ([jag]).
- **Vendor and maturity.** The product is about ten days old, in early access
  with a waitlist, with no public SLA, status page or changelog. Pricing may
  be subsidized ([blog]). Mitigations: fail open, keep a fake plus a local
  backend, and never block a Task on Jev availability.
- **Cost.** This is negligible at list price and not a real risk.

## 7. Verdict

**Experiment.** Do not adopt now, and do not skip. The experiment in §5 is
small, test-side and reversible, and it answers the one question that
matters: can a typed judgment reliably tell "a better model would help" from
"nothing a model does will help" on SDF's own failures? If it can, a
fail-open, advisory annotation on escalation saves budget without touching
the Evaluator's authority. If it cannot, the seam and fake cost almost
nothing to delete. A proposed ADR for the boundary is at
[`docs/adr/0010-advisory-typed-judgments.md`](../adr/0010-advisory-typed-judgments.md).

[blog]: https://typesafe.ai/blog/introducing-system-one-models-and-jev
[api]: https://docs.typesafe.ai/api
[jag]: https://docs.typesafe.ai/model-jaggedness/jev-1.13
[priv]: https://typesafe.ai/legal/privacy-policy
[cf]: https://developers.cloudflare.com/ai/models/typesafe/jev/
[evals]: https://evals.typesafe.ai
[ood]: https://github.com/scienthoon/jev-ood-calibration
[devto]: https://dev.to/webofmike/i-benchmarked-jev-on-agent-tool-call-risk-calibration-held-49i3
[t1]: https://github.com/nandansrikrishna/jev-agent-tool
[t2]: https://github.com/walidboulanouar/jev-agent-kit
[t3]: https://github.com/openlayer-ai/jevals
[t4]: https://github.com/malevrigns/agent-jev
[t5]: https://github.com/vinilana/jev-gateway
[t6]: https://github.com/hellogumbo/awesome-jev
