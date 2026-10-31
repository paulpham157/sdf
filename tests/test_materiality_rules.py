"""Rule-based materiality of agent decision requests (#60, ADR 0033)."""

from __future__ import annotations

import json
import re
import uuid

import pytest
from sqlalchemy.orm import Session

from sdf_core.agent_messaging import Member, MessageHub
from sdf_core.db import AgentRunMemberRow, AgentRunRow, Base, DecisionRequestRow, PlanGateError, make_engine
from sdf_core.decision_gate import is_material, material_payload, pending_material
from sdf_core.materiality import DEFAULT_RULES, MaterialityRule, matched_rules
from sdf_core.model import utcnow
from sdf_core.plans import PlanSpec, decide_plan, propose_plan, resolve_request
from tests.agent_comm_support import make_fixture, plan_dict

# One or more texts per rule category; each must match exactly that rule.
CATEGORY_EXAMPLES = {
    "data.delete": ["May I delete the old user records?", "Drop the users table?", "rm -rf build/ first?",
                    "Should I wipe the test database?", "Run DELETE FROM sessions?", "Remove these failing tests?"],
    "external.send": ["Send an email to all customers?", "Publish the package to PyPI?", "npm publish now?",
                      "Post the release notes to the Slack channel?"],
    "money.spend": ["Spend $40 on GPU time?", "Purchase a license for the linter?", "Upgrade the plan to pro?",
                    "Provision two GPU instances?", "Pay for the API quota?"],
    "credentials.secret": ["Print the API key so I can debug?", "Commit the .env file?", "Rotate the GitHub token?",
                           "Store the password in the config?"],
    "git.destructive": ["git push --force origin feature?", "May I force-push?", "Push straight to main?",
                        "git reset --hard to drop the commits?", "Rewrite history to remove the blob?",
                        "Delete the remote branch release-1?"],
    "schema.shared": ["Run the alembic upgrade?", "ALTER TABLE users ADD COLUMN?", "Migrate the database schema now?",
                      "Apply it on the production database?"],
    "network.egress": ["Allow outbound traffic to pypi.org?", "Open port 8080?", "Add the host to the network allowlist?",
                       "Change the egress policy?", "Change the firewall rule?"],
    "merge": ["Merge the PR when green?", "git merge feature into main?", "Should I merge #12?"],
}

BENIGN = [
    "Should the helper be called add or plus?", "May I also refactor calc.py?", "Ship it?",
    "Should I also rewrite calc.py from scratch?", "The token budget is low; continue?",
    "Use AllowlistPolicy or a custom policy class?", "Migrate this function to the new API style?",
    "Pay attention to the edge case for zero?", "Should deleted accounts keep their invoices?",
    "Which test name reads better?", "Keep the docstring or shorten it?",
]


def test_every_category_has_a_rule_and_ids_are_unique():
    assert set(CATEGORY_EXAMPLES) == {r.id for r in DEFAULT_RULES}
    assert len({r.id for r in DEFAULT_RULES}) == len(DEFAULT_RULES)
    assert all(r.description and r.category for r in DEFAULT_RULES)


@pytest.mark.parametrize("rule_id,text", [(rid, t) for rid, texts in CATEGORY_EXAMPLES.items() for t in texts])
def test_each_rule_category_matches(rule_id, text):
    assert rule_id in matched_rules([text]), text


@pytest.mark.parametrize("text", BENIGN)
def test_ordinary_questions_match_no_rule(text):
    assert matched_rules([text]) == []


def test_rules_see_options_consequence_and_action_and_are_case_insensitive():
    assert matched_rules(["Which branch?", "rebase", "FORCE-PUSH"]) == ["git.destructive"]
    assert matched_rules(["Which option?", None, "", "x"], ) == []
    assert matched_rules(["ok?"] + ["SEND AN EMAIL TO CUSTOMERS"]) == ["external.send"]


def test_invisible_characters_and_full_width_letters_do_not_evade_the_rules():
    assert matched_rules(["May I force\u200b-push?"]) == ["git.destructive"]
    assert matched_rules(["\uff46\uff4f\uff52\uff43\uff45-push to main?"]) == ["git.destructive"]
    assert matched_rules(["drop\u2060 the users ta\u200dble"]) == ["data.delete"]


def test_rules_are_extendable_and_ids_must_be_unique():
    extra = MaterialityRule("custom.deploy", "deploys", "deploys to production",
                            (re.compile(r"\bdeploy\b", re.IGNORECASE),))
    assert matched_rules(["Deploy now?"]) == []
    assert matched_rules(["Deploy now?"], (*DEFAULT_RULES, extra)) == ["custom.deploy"]
    with pytest.raises(ValueError, match="unique"):
        matched_rules(["x"], (extra, extra))


def test_rule_matching_is_linear_on_hostile_input():
    import time

    hostile = ("delete " + "!" * 5000 + " ") * 4 + "rm -" + "r" * 5000 + " push" + " a" * 4000
    started = time.monotonic()
    matched_rules([hostile])
    assert time.monotonic() - started < 2


# --- ingest: upgrade only, recorded on the immutable message ---------------------------------


@pytest.fixture()
def hub_env(tmp_path):
    engine = make_engine("sqlite+pysqlite:///:memory:")
    Base.metadata.create_all(engine)
    db = Session(engine, expire_on_commit=False)
    plan = propose_plan(db, PlanSpec(**plan_dict(make_fixture(tmp_path), "PLAN-MR")))
    db.commit()
    decide_plan(db, plan.id, approve=True, decided_by="hung")
    db.add(AgentRunRow(id="RUN-MR", plan_id=plan.id, status="running", config={}, started_at=utcnow()))
    db.flush()
    db.add(AgentRunMemberRow(run_id="RUN-MR", agent_id="worker-1", role="worker", agent_kind="pi",
                             subtask_key="add", status="ready", turns=0))
    db.commit()
    hub = MessageHub(db, "RUN-MR", {"worker-1": Member("worker-1", "worker", "add", "sbx")})
    try:
        yield db, hub
    finally:
        db.close()
        engine.dispose()


def _ingest(hub, **fields):
    raw = json.dumps({"id": uuid.uuid4().hex, "kind": "decision_request", **fields}).encode()
    return hub.ingest("worker-1", raw, sandbox_id="sbx")


def test_a_rule_upgrades_an_undeclared_request(hub_env):
    db, hub = hub_env
    res = _ingest(hub, body="May I force-push the fix?", options=["yes", "no"])
    meta = res.message.meta
    assert (meta["material"], meta["materiality_source"], meta["matched_rules"]) == (True, "rule", ["git.destructive"])
    assert meta["authority"] == "human" and "git.destructive" in meta["consequence"]
    request = db.get(DecisionRequestRow, res.decision_request_id)
    assert is_material(db, request) and [g.scope for g in pending_material(db, run_id="RUN-MR")] == ["add"]
    payload = material_payload(db, request)
    assert (payload["material"], payload["materiality_source"], payload["matched_rules"], payload["blocks"]) == (
        True, "rule", ["git.destructive"], "add")
    # A policy cannot answer it, exactly like a declared one.
    with pytest.raises(PlanGateError, match="only by a human"):
        resolve_request(db, request.id, outcome="answered", resolved_by="rules", resolver_kind="policy", resolution="no")


def test_the_action_field_is_classified_and_kept(hub_env):
    _, hub = hub_env
    res = _ingest(hub, body="Is this fine?", options=["a", "b"], action="gh pr merge 12 --squash")
    assert (res.message.meta["matched_rules"], res.message.meta["action"]) == (["merge"], "gh pr merge 12 --squash")


def test_text_past_the_truncation_limits_is_still_classified(hub_env):
    _, hub = hub_env
    res = _ingest(hub, body="Which name? " + "x " * 4500 + "then drop the users table", options=["a", "b"])
    assert res.message.truncated and res.message.meta["matched_rules"] == ["data.delete"]
    res = _ingest(hub, body="Which?", options=[*"abcdefghij", "force-push"])
    assert res.message.meta["matched_rules"] == ["git.destructive"]


def test_no_match_stays_non_material(hub_env):
    db, hub = hub_env
    res = _ingest(hub, body="Should the helper be called add or plus?", options=["add", "plus"])
    assert (res.message.meta["material"], res.message.meta["matched_rules"]) == (False, [])
    request = db.get(DecisionRequestRow, res.decision_request_id)
    assert not is_material(db, request)
    assert material_payload(db, request) == {"material": False, "materiality_source": None, "matched_rules": []}


def test_an_agent_declaration_is_never_downgraded(hub_env):
    db, hub = hub_env
    res = _ingest(hub, body="Should the helper be called add or plus?", options=["add", "plus"], material=True,
                  consequence="Public API name, hard to change later.")
    meta = res.message.meta
    assert (meta["material"], meta["materiality_source"], meta["matched_rules"]) == (True, "agent", [])
    assert meta["consequence"] == "Public API name, hard to change later."
    # Declared and rule-matched: the source stays "agent", the matches are still recorded.
    res = _ingest(hub, body="Push to main or open a PR?", options=["push", "pr"], material=True,
                  consequence="Pushing skips review.")
    assert (res.message.meta["materiality_source"], res.message.meta["matched_rules"]) == ("agent", ["git.destructive"])


def test_non_string_consequence_or_action_is_refused(hub_env):
    from sdf_core.agent_messaging import EnvelopeError

    db, hub = hub_env
    for fields in ({"consequence": 3}, {"action": ["x"]}):
        with pytest.raises(EnvelopeError):
            _ingest(hub, body="Which?", options=["a", "b"], **fields)
        db.rollback()
