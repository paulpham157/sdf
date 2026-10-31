"""Rule-based materiality of agent decision requests (#60, ADR 0033).

A decision request is **material** when the agent declares it
(``material: true``) *or* when one of these deterministic rules matches its
question, options, consequence or declared action.  Rules can only upgrade a
request to material, never downgrade an agent's declaration.  They are plain,
case-insensitive regular expressions (no model, no network), so the same
text always classifies the same way and the matched rule ids are recorded on
the immutable agent message for audit.

The rules are deliberately conservative in the safety sense: they name the
operations ADR 0033 treats as consequential and accept some false positives
(a human then answers a question that did not need one).  To extend them,
append a :class:`MaterialityRule` to :data:`DEFAULT_RULES` (or pass your own
tuple to :class:`~sdf_core.agent_messaging.MessageHub`); ids must be unique.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Iterable, Sequence


@dataclass(frozen=True)
class MaterialityRule:
    id: str          # stable, recorded in agent_messages.meta["matched_rules"]
    category: str
    description: str
    patterns: tuple[re.Pattern[str], ...]

    def matches(self, text: str) -> bool:
        return any(p.search(text) for p in self.patterns)


def _rule(rule_id: str, category: str, description: str, *patterns: str) -> MaterialityRule:
    return MaterialityRule(rule_id, category, description,
                           tuple(re.compile(p, re.IGNORECASE) for p in patterns))


_DATA = r"(?:data(?:base|set)?s?|db|tables?|rows?|records?|columns?|collections?|buckets?|backups?|volumes?|" \
        r"files?|director(?:y|ies)|folders?|tests?|branch(?:es)?|repo(?:sitor(?:y|ies))?s?|users?|accounts?|logs?|history)"

DEFAULT_RULES: tuple[MaterialityRule, ...] = (
    _rule("data.delete", "deleting data", "deletes, drops, truncates or wipes data, files, tests or history",
          rf"\b(?:delete|deleting|deletion|remove|removing|drop|dropping|truncat\w*|wip(?:e|ing)|purg\w*|eras\w*|destroy\w*)\b"
          rf"(?:\W+\w+){{0,4}}?\W+{_DATA}\b",
          r"\brm\s+-(?=[a-z]*r)(?=[a-z]*f)[a-z]+",
          r"\b(?:drop|truncate)\s+(?:table|database|schema)\b",
          r"\bdelete\s+from\b"),
    _rule("external.send", "external sends / publishing",
          "sends email or messages, posts or publishes outside the sandbox",
          r"\b(?:send|sending|email|e-mail|post|posting|tweet|publish\w*|release|announce\w*|notify\w*)\b"
          r"(?:\W+\w+){0,4}?\W+(?:e-?mails?|messages?|slack|customers?|users?|public(?:ly)?|pypi|npm|registry|"
          r"channel|newsletter|webhook|announcement|externally|production)\b",
          r"\b(?:npm|twine|cargo|gem)\s+publish\b|\btwine\s+upload\b|\bpublish(?:ing)?\s+(?:the\s+)?(?:package|release)\b"),
    _rule("money.spend", "spending money / paid resources",
          "spends money, buys, upgrades a plan or provisions paid resources",
          r"\b(?:spend\w*\s+(?:money|budget|credits?|\$)|pay(?:ing)?\s+(?:for|the|a|an)\b|payments?|purchas\w*|"
          r"buy(?:ing)?\s+(?:a|an|the|more|credits?|licen[cs]es?|seats?)|charg(?:e|ing)\s+(?:the\s+)?(?:card|customer)s?|"
          r"paid\s+(?:plans?|tiers?|services?|apis?|resources?|instances?)|subscri(?:be|ption)|upgrade\s+(?:the\s+)?plan)\b",
          r"[$€£]\s?\d|\b\d+(?:\.\d+)?\s?(?:usd|eur|dollars?)\b",
          r"\b(?:provision\w*|launch\w*|spin(?:ning)?\s+up|scal(?:e|ing)\s+up)\b(?:\W+\w+){0,4}?\W+"
          r"(?:gpus?|instances?|clusters?|servers?|vms?|nodes?)\b"),
    _rule("credentials.secret", "credentials / secrets",
          "reads, prints, rotates, shares or commits a credential, token, key or secret",
          r"\b(?:credentials?|secrets?|passwords?|passphrases?|api[\s_-]?keys?|access[\s_-]?keys?|private[\s_-]?keys?|"
          r"(?:access|auth|bearer|api|github|gh|oauth|refresh|session|deploy)[\s_-]?tokens?|ssh[\s_-]?keys?|keychain|vault)\b",
          r"(?:^|[\s/'\"`])\.env\b"),
    _rule("git.destructive", "irreversible git operations",
          "force-push, push to a protected branch, history rewrite or hard reset",
          r"\bforce[\s-]?push\w*|\bpush\b[^\n]{0,40}(?:--force\b|-f\b|--force-with-lease\b)",
          r"\bpush\w*\b(?:\W+\w+){0,4}?\W+(?:main|master|trunk|production|release)\b",
          r"\breset\s+--hard\b|\bfilter-(?:branch|repo)\b|\brewrit\w*\s+(?:the\s+|git\s+)?history\b",
          r"\brebas\w*\b(?:\W+\w+){0,4}?\W+(?:main|master|trunk|published|pushed|remote|shared)\b",
          r"\bdelete\w*\s+(?:the\s+)?(?:remote\s+)?(?:branch|tag)s?\b|\bpush\b[^\n]{0,40}--delete\b"),
    _rule("schema.shared", "schema / migration on a shared database",
          "runs or changes a schema migration, or alters a shared or production database",
          r"\b(?:alembic|alter\s+table|ddl|schema\s+(?:change|migration)s?|(?:db|database|sql)\s+migrations?)\b",
          r"\bmigrat\w*\b(?:\W+\w+){0,3}?\W+(?:db|database|schema|tables?|postgres\w*|sqlite|data)\b",
          r"\b(?:shared|production|prod|live|staging)\s+(?:db|database|schema|postgres\w*|tables?)\b"),
    _rule("network.egress", "network egress changes",
          "opens, allows or changes outbound network access, a firewall or a network allowlist",
          r"\b(?:egress|outbound\s+(?:traffic|access|connections?|network|requests?)|firewall|security\s+groups?|"
          r"open\s+(?:a\s+|the\s+)?ports?)\b",
          r"\b(?:network|domain|host|ip|url|egress)\s+(?:allow-?list|whitelist)s?\b",
          r"\b(?:enable|allow|grant)\w*\s+(?:the\s+)?(?:network|internet)\s+access\b"),
    _rule("merge", "merging", "merges a pull request or a branch",
          r"\bmerg\w*\b(?:\W+\w+){0,4}?\W+(?:pr|pull\s+requests?|branch(?:es)?|main|master|trunk|#\d+)\b",
          r"\b(?:gh\s+pr\s+merge|git\s+merge)\b"),
)


def _check_unique(rules: Sequence[MaterialityRule]) -> None:
    ids = [r.id for r in rules]
    if len(ids) != len(set(ids)):
        raise ValueError("materiality rule ids must be unique")


_check_unique(DEFAULT_RULES)


def matched_rules(texts: Iterable[str | None], rules: Sequence[MaterialityRule] = DEFAULT_RULES) -> list[str]:
    """Ids of every rule matching any one of ``texts``, in rule order.

    Each text (question, each option, consequence, action) is matched on its
    own, so words from two different fields never combine into a match.
    """

    _check_unique(rules)
    items = [t for t in texts if t]
    return [rule.id for rule in rules if any(rule.matches(t) for t in items)]
