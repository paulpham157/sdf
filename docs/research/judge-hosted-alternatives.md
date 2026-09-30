# Alternatives to hosted Jev for SDF's advisory Judge

Researched 2026-09-30 from SDF's current Judge seam, the #19/#22/#24/#35 issues,
and first-party model, repository, and provider documentation. No live judge API
was called and no credential was used. Hosted terms and catalogs can change;
recheck them before any later live test.

**Verdict: defer hosted Jev; keep the seam experimental and fail-open.** The
local AgentJev-0.6B result is a negative result for that checkpoint, not proof
that hosted Jev fails. But it also gives no reason to spend operator time on two
gateway adapters before establishing that SDF benefits from a probabilistic
triage label at all. The most defensible next comparison, if the Human wants
one, is a bounded offline run of a stronger open typed-decision model such as
Kev-4B, alongside a code-first reducer. Do not wire any judge into
`ExecutionService`, the Evaluator, or escalation.

## What SDF is actually asking

The existing seam asks two narrow questions about an already-verified failed
Attempt: choose one of six `failure_cause` labels (including `not_stated`), and
estimate whether a higher coding-model tier would plausibly help. The current
state builder limits fields and log tails, redacts known secret patterns and
provided secret values, and redacts again after truncation. The protocol
validates labels/probabilities; `FakeJudge` supports deterministic tests. The
only model client today is the opt-in local AgentJev process, outside the
package's inference lifecycle; transport/model errors return unavailable. These
are useful portability and containment properties, but syntactic validation
does not make a probability calibrated or a label true.

The evidence bar is now concrete. On the 41 synthetic #21 cases, AgentJev-0.6B
was run three times: cause accuracy 0.293, ECE 0.096, and zero non-agent
withholds at the intended confidence threshold of 0.8 (maximum confidence
about 0.575). Its 61% binary higher-tier answer accuracy is still not
decision-grade. The 2,048-token fit trimmed no cases. This model often
confused provider/agent-caused failures and never detected
`agent_did_not_attempt`. See [the #24 report](judge-experiment-24.md) for
per-cause results and reproduction. The fake backend's perfect score is an
oracle/schema smoke test, not a model baseline.

The invariant remains the one in [ADR-0010](../adr/0010-advisory-typed-judgments.md):
a Judge may add an advisory annotation, but it cannot produce `PASS`, an
Accepted Outcome, or an escalation. Unavailable, malformed, or low-confidence
output must leave today's deterministic behavior unchanged. Only the small,
redacted triage view may leave the process. No inference server belongs in
`sdf_core`.

## Ranked alternatives

Ranking is by expected value for SDF's failed-Attempt triage, not general model
quality. “Not yet evidenced” means there is no SDF-specific accuracy or
calibration result.

| Rank | Approach | Fit and evidence | Boundary fit: advisory / fail-open / process placement | Cost, privacy, and ops |
| --- | --- | --- | --- | --- |
| **1** | **Code-first reduction; judge only unresolved cases (jevals pattern)** | Best immediate fit. Extract direct signals deterministically first: evaluator status, process/adapter outcome, exit code, known provider status, whether a diff exists, and whether a fixture/check itself failed. A small reducer can return a cause only for unambiguous cases and `not_stated` otherwise; reserve a model for genuinely ambiguous cases. `jevals` documents the `state()` / `questions()` / `reduce()` pattern and pluggable backends, including local Kev/Laya and mocks. | Excellent by construction: deterministic advisory annotation, no inference process, and errors simply leave the label unknown. | No model cost or provider ToS for reduced cases; smallest secret-exposure surface. Rules need tests and must not infer that an Attempt passed or authorize escalation. **Not yet benchmarked on the 41 cases.** |
| **2** | **Kev, first as an offline local comparator** | Strongest near-term model candidate found. Kev serves the System One request shape and has 0.8B, 4B, 9B, and 27B checkpoints. Its first-party benchmark reports a meaningful size/quality tradeoff; Kev-0.8B's locked out-of-domain test is 0.697 accuracy / 0.416 Brier, while larger versions score higher on that suite. Those are unrelated benchmark tasks, not SDF failures; its authors explicitly advise measuring on one's own data. Laya is a different small typed-decision family based on ModernBERT; its model card documents a 1,024-token context, so SDF's task text/log evidence may need careful fit. | Strong if a wrapper returns unavailable on any model/transport error and the separately managed process stays outside `sdf_core`; sanitize the request first. No embedded server or automatic startup. | No provider transfer or per-call charge, but GPU/Apple-Silicon memory, downloads, model/runtime pinning, and process management are real ops costs. Kev-4B's serving requirements are substantially larger than AgentJev-0.6B. **Best next model experiment only if the Human wants a comparison.** |
| **3** | **Task-specific rubric/classifier after better labels exist** | A fixed cause taxonomy is a classification problem, not necessarily a generative-judge problem. A small classifier trained or calibrated on representative, hand-labeled failed Attempts could exploit structured signals and output `not_stated`. With only 41 synthetic cases, however, a learned classifier would overfit and the synthetic distribution may not match real failures. | Strong if an external/local process, advisory-only caller, and unavailable-on-error wrapper are maintained. | No hosted data transfer if local; low inference cost, but label collection, drift monitoring, threshold calibration, and class imbalance become SDF's burden. Do not train/evaluate on the same cases; reserve a genuinely held-out set. This is a later option, not a recommendation to build now. |
| **4** | **General hosted LLM with strict JSON Schema** | OpenAI Structured Outputs (and equivalent provider features) can constrain output to an enum/JSON schema. That solves response shape, not cause accuracy, confidence calibration, prompt-injection resistance, or abstention quality. A rubric prompt can request `not_stated`, but a model-authored confidence is not a calibrated probability. Better as a benchmark arm than a production Judge. | Conditional: easy to keep advisory and fail-open, but only with explicit schema validation, timeouts, and an unavailable fallback; no inference server in SDF. Redact before the request. | Direct-provider routing is simpler than adding a gateway, but it is still a new credential, dependency, paid call, and processor of failure text. As a scale example, 600k GPT-4.1-mini input tokens at the currently listed $0.40/M rate are about $0.24 before output; Jev's Cloudflare list rate puts the same input at about $0.03. These are tiny experiment costs, not the main decision. OpenAI says API data is not used for training by default, while abuse-monitoring logs may retain content up to 30 days unless eligible controls apply; verify endpoint terms. |
| **5** | **Hosted Jev through Cloudflare Workers AI** | The cleanest existing hosted option if a hosted model comparison is explicitly authorized: Jev natively accepts typed Noul/Choice/Score questions. Cloudflare currently lists `typesafe/jev` at $0.042/M input tokens, $0 output, 32k context, and “Zero data retention: Yes.” This tests hosted Jev, but it does not address whether SDF needs the model or whether it is better than code reduction/Kev on #21. Catalog name is not a pinned checkpoint; record returned model/version. | Conditional, same as any remote Judge: only after in-code redaction, strict response validation, timeout, and fail-open behavior; advisory annotation only. | Requires Cloudflare account/token and another outbound path. Its ZDR label is a relative advantage, not a reason to send unredacted logs; TypeSafe terms still apply. It is **not** a harmless confirmation of #24: AgentJev is a distinct checkpoint, and hosted quality remains unknown until an explicitly approved live run. |
| **6** | **Hosted Jev through Vercel AI Gateway or OpenRouter** | Same underlying TypeSafe Jev family, not an independent model-quality alternative. Vercel documents `typesafe-ai/jev`; OpenRouter lists Jev versions and a moving latest alias. A gateway may simplify access/routing, but it does not strengthen the evidence from Cloudflare. | Conditional, with the same advisory/redaction/validation/fail-open requirements as Cloudflare; gateway use does not remove the external processor. | Adds gateway credentials, billing/terms, possible routing/version variation, and another processor. Vercel's example can request `zeroDataRetention`, but its current Jev listing does not itself establish ZDR for every route; verify actual provider policy. OpenRouter says inputs are forwarded to the selected/automatically selected model provider and practices differ. Unless a Human has an existing account or a specific operational reason, this is worse than one Cloudflare comparison and should not receive a sibling implementation ticket. |

### Notes on non-Jev typed judges

* **Schema-constrained chat models** produce a machine-parseable label; they do
  not automatically produce a trustworthy posterior distribution. If used in an
  experiment, evaluate top-label accuracy, class-wise errors, Brier/ECE where
  probabilities are available, and selective risk at each threshold. Treat
  free-form self-reported confidence as a separate, untrusted field.
* **Rubric classifiers** can return a cause and abstain, but their value depends
  on labeled SDF-like examples. The existing cases are synthetic and small;
  they support a controlled comparison, not a claim of production
  generalization.
* **Reward models** usually score/rank a candidate against a criterion. A scalar
  preference score is not the same as a calibrated six-way failure cause or
  “higher tier would help” probability. Converting the scalar to a threshold
  adds calibration and distribution-shift risk without an obvious advantage for
  this taxonomy. No reward model surfaced with a better SDF-specific fit than
  the classifier/typed-decision candidates above.

### Screened open typed-decision models (not next experiments)

A 2026 open-weight wave now speaks the same state-plus-typed-questions shape
([Ferr0 “Open Jev” collection](https://huggingface.co/collections/Ferr0/open-jev-typed-decision-models)).
They were screened as candidate-screening evidence only; none have SDF
#21/#24 numbers:

* **AgentJev-0.6B** — already measured on SDF fixtures; negative for the
  withhold policy. Keep as regression baseline, not the next bet.
* **Laya** ([base](https://huggingface.co/convaiinnovations/laya) /
  [domain FT](https://huggingface.co/convaiinnovations/laya-typed-decisions)) —
  pip-installable, Apache-2.0, Jev-compatible serve path; English base context
  is short (1,024; multilingual up to 8,192). Published “beats Jev” numbers
  often come from domain-finetuned checkpoints on public typed-decision
  splits—treat as non-transferable until rerun on SDF labels.
* **Kev family** — preferred open comparator above because it keeps the
  `/v1/systemone` wire and publishes a size ladder with held-out cards.
* **Decider-2B, openjev/openjev, Lumma-fev, Bosun, Tev1, XOR** — active Hub
  models in the same class. Useful if Kev ops prove awkward, but they do not
  outrank code reduction or justify a hosted Jev adapter first.

Human skepticism of Cloudflare/Vercel hosted Jev is treated as an ops and
opportunity-cost judgment, not as measured quality: those routes remain
unknown on the SDF set until an explicitly authorized live comparison.

## Recommended next step

1. Keep the current Judge, fake, redaction, and local AgentJev experiment code
   as isolated tooling. Do not add a production backend or connect it to
   Escalation.
2. If a follow-up experiment is desired, compare (a) deterministic reduction,
   (b) Kev-4B or Kev-9B as a separately managed local process, and only then
   (c) one hosted Jev route if access is already available. Use the same frozen
   cases and question wording; record the exact checkpoint/provider and raw
   version metadata. The broader open-model benchmarks are candidate-screening
   evidence only.
3. Report macro and per-class accuracy, confusion matrix, Brier/ECE for
   probabilities, coverage and wrong-withhold count at predeclared thresholds,
   and unavailable/malformed rates. Use uncertainty intervals because 41 cases
   give weak per-class evidence. Do not choose a threshold on the final test
   cases. Do not use live customer code or real credentials; sanitize before
   any external request.
4. Revisit ADR-0010 only after an independent holdout shows a useful reduction
   in unnecessary escalations at a pre-agreed, very low wrong-withhold rate.
   Until then, the operational default is unchanged deterministic policy.

This is a research recommendation, not a request to run the experiments or
acquire credentials now.

## Issue recommendations (Human decides)

* **#22 — defer, and amend the acceptance gate before any implementation.** The
  ticket's requested Cloudflare backend plus one live call would establish
  access/wire feasibility, not that hosted Jev helps SDF. Keep it deferred
  unless the Human explicitly wants one hosted comparison after reviewing this
  survey. If revived, make it an opt-in synthetic evaluation of the existing
  triage dataset, with no production wiring, and require a quality comparison
  against the reducer/local candidate—not merely “credentials work.”
* **#35 — recommend `wontfix` as a separate backend ticket.** It is another
  gateway for the same Jev model, so it adds an adapter and processor without
  answering an independent model-quality question. If a Human later chooses
  Vercel for account or deployment reasons, amend #22 or open a replacement
  scoped to that route; no need to build both gateways now.
* **#24 is already closed** with the reported experiment; this note does not
  change it, #19, or ADR-0010's proposed status. No issue was edited or closed
  during this research.

## Primary sources

* SDF local findings and contract: [#24 experiment](judge-experiment-24.md),
  [local AgentJev setup](agentjev-local.md), [Judge seam](../../sdf_core/judge.py),
  [state filtering/redaction](../../sdf_core/judge_state.py),
  [ADR-0010](../adr/0010-advisory-typed-judgments.md).
* Current issue records: [#19](https://github.com/paulpham157/sdf/issues/19),
  [#22](https://github.com/paulpham157/sdf/issues/22),
  [#24](https://github.com/paulpham157/sdf/issues/24),
  [#35](https://github.com/paulpham157/sdf/issues/35).
* Open typed models and reducer pattern: [Kev repository and benchmark README](https://github.com/jaredpalmer/kev),
  [Kev-0.8B model card and held-out results](https://github.com/jaredpalmer/kev/blob/main/docs/model-cards/kev-0.8b.md),
  [Laya model card](https://huggingface.co/convaiinnovations/laya-typed-decisions),
  [jevals repository](https://github.com/openlayer-ai/jevals).
* Hosted Jev: [Cloudflare model page](https://developers.cloudflare.com/ai/models/typesafe/jev/),
  [Vercel Jev model page](https://vercel.com/ai-gateway/models/jev),
  [Vercel Jev launch/integration example](https://vercel.com/changelog/typesafe-ai-jev-now-available-on-ai-gateway),
  [OpenRouter TypeSafe catalog](https://openrouter.ai/typesafe/jev-1.13),
  [TypeSafe API](https://docs.typesafe.ai/api),
  [TypeSafe privacy policy](https://typesafe.ai/legal/privacy-policy).
* Structured outputs and data handling: [OpenAI Structured Outputs](https://developers.openai.com/api/docs/guides/structured-outputs),
  [OpenAI API data controls](https://platform.openai.com/docs/models/default-usage-policies-by-endpoint),
  [OpenRouter privacy policy](https://openrouter.ai/privacy/),
  [OpenRouter guardrails](https://openrouter.ai/docs/guides/features/guardrails/overview).
