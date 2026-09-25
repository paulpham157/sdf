# Typed model judgments are advisory, never Evaluator authority

Status: proposed

SDF may ask a fast typed-judgment model (a System One model such as TypeSafe
Jev, or an open-weight model speaking the same state-plus-typed-questions
shape) to label an Attempt's already-verified outcome. The first use is the
likely cause of a verified failure. It goes through a `Judge` seam with a
deterministic fake for tests, and no production path requires a live backend.

Judgment output is advisory:

- It is recorded with the model version and confidence as a separate Evidence
  kind.
- It can never produce `PASS`, an Accepted Outcome, or an escalation on its
  own.
- An Escalation Policy may use a confident label only to withhold or redirect
  an escalation (for example, not moving to a higher Model Tier when the cause
  is environmental). Low confidence or "not stated" changes nothing.
- The integration fails open: when the judge is unavailable, slow or
  misconfigured, SDF behaves as it does today.

Any state sent to a hosted judge is filtered and redacted in code first,
because Attempt output can contain an Agent Credential or proprietary code.
The model version is pinned. Thresholds are calibrated on SDF's own labelled
Attempts before any policy reads them.

This ADR becomes accepted only after the offline experiment in
`docs/research/jev.md` §5 shows agreement with hand labels on SDF failures. It
does not amend ADR-0002 or ADR-0005: the Evaluator still determines outcomes
and escalation still starts from verified Evaluator statuses.
