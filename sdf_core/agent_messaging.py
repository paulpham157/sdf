"""Host-side messaging hub for lead/worker agents (ADR 0014).

Agents never talk to each other or to SDF directly.  The relay collects the
JSON envelopes an agent's ``sdf-msg`` shim drops in its sandbox outbox and
hands the raw bytes to :class:`MessageHub` together with the *host-known*
identity of the sandbox they came from.  The hub:

* authenticates the sender by that binding (an envelope ``from`` is ignored),
* validates kind and recipient against the run's membership and role rules,
* sanitizes the body (Unicode NFC, no control / bidi-override characters) and
  caps its size and the sender's message count,
* persists every message append-only, and
* turns ``report`` into an agent *claim* (never Evidence) and
  ``decision_request`` into a pending decision request (never auto-approved).
"""

from __future__ import annotations

import hashlib
import json
import re
import unicodedata
from dataclasses import dataclass, field
from typing import Any
from uuid import uuid4

from sqlalchemy import func, select
from sqlalchemy.orm import Session

from .db import AgentClaimRow, AgentMessageRow
from .model import utcnow
from .plans import open_agent_request

MAX_BODY_CHARS = 8000
MAX_ENVELOPE_BYTES = 32 * 1024
MAX_MESSAGES_PER_AGENT = 200
MAX_OPTIONS = 10
MAX_OPTION_CHARS = 200
MAX_CONSEQUENCE_CHARS = 1000
ENVELOPE_NAME = re.compile(r"[0-9a-f]{32}\.json")
AGENT_KINDS = frozenset({"message", "question", "report", "decision_request"})
HOST = "sdf"
MANAGER = "manager"
_ID = re.compile(r"[a-z][a-z0-9-]{0,63}")
_CONTROL = re.compile(r"[\x00-\x08\x0b-\x1f\x7f-\x9f\u200b-\u200f\u202a-\u202e\u2066-\u2069\ufeff]")


class EnvelopeError(ValueError):
    """An outbox envelope was rejected; nothing was persisted."""


def sanitize_body(text: Any, limit: int = MAX_BODY_CHARS) -> tuple[str, bool]:
    if not isinstance(text, str):
        raise EnvelopeError("body must be text")
    clean = _CONTROL.sub("", unicodedata.normalize("NFC", text.replace("\r\n", "\n"))).strip()
    if not clean:
        raise EnvelopeError("body is empty after sanitizing")
    if len(clean) > limit:
        return clean[:limit] + "\n[truncated by SDF]", True
    return clean, False


@dataclass(frozen=True)
class Member:
    agent_id: str
    role: str  # lead | worker
    subtask_key: str | None = None
    sandbox_id: str | None = None


@dataclass
class IngestResult:
    message: AgentMessageRow
    recipients: list[str] = field(default_factory=list)
    claim: AgentClaimRow | None = None
    decision_request_id: str | None = None


def delivery_payload(message: AgentMessageRow) -> bytes:
    """The inbox file an agent reads.  Only host-assigned identity fields."""

    return json.dumps({
        "id": message.id, "seq": message.seq, "from": message.from_agent, "from_role": message.from_role,
        "kind": message.kind, "body": message.body, "truncated": message.truncated,
        "meta": message.meta or {}, "created_at": message.created_at.isoformat(),
    }).encode()


class MessageHub:
    def __init__(self, db: Session, run_id: str, members: dict[str, Member], *,
                 max_body_chars: int = MAX_BODY_CHARS, max_messages_per_agent: int = MAX_MESSAGES_PER_AGENT) -> None:
        for agent_id in members:
            if not _ID.fullmatch(agent_id) or agent_id in (HOST, MANAGER, "lead", "worker"):
                raise ValueError(f"invalid member id {agent_id!r}")
        self.db = db
        self.run_id = run_id
        self.members = dict(members)
        self.max_body_chars = max_body_chars
        self.max_messages_per_agent = max_messages_per_agent
        self._counts: dict[str, int] = {}
        self.attempt_for: dict[str, str] = {}  # agent_id -> current Attempt id (workers)

    # -- persistence ---------------------------------------------------------
    def _next_seq(self) -> int:
        current = self.db.scalar(select(func.max(AgentMessageRow.seq)).where(AgentMessageRow.run_id == self.run_id))
        return (current or 0) + 1

    def _persist(self, **fields: Any) -> AgentMessageRow:
        body = fields["body"]
        row = AgentMessageRow(
            id=f"MSG-{uuid4().hex[:16]}", run_id=self.run_id, seq=self._next_seq(),
            body_sha256=hashlib.sha256(body.encode()).hexdigest(), created_at=utcnow(), **fields,
        )
        self.db.add(row)
        self.db.flush()
        return row

    # -- inbound from a sandbox ---------------------------------------------
    def ingest(self, sender: str, raw: bytes, *, sandbox_id: str | None = None) -> IngestResult:
        member = self.members.get(sender)
        if member is None:
            raise EnvelopeError(f"unknown sender binding {sender!r}")
        if sandbox_id is not None and member.sandbox_id is not None and sandbox_id != member.sandbox_id:
            raise EnvelopeError("envelope came from a sandbox not bound to this member")
        if len(raw) > MAX_ENVELOPE_BYTES:
            raise EnvelopeError(f"envelope is {len(raw)} bytes; the limit is {MAX_ENVELOPE_BYTES}")
        count = self._counts.get(sender)
        if count is None:
            count = self.db.scalar(select(func.count()).select_from(AgentMessageRow).where(
                AgentMessageRow.run_id == self.run_id, AgentMessageRow.from_agent == sender)) or 0
        if count >= self.max_messages_per_agent:
            raise EnvelopeError(f"{sender} reached the per-run message cap ({self.max_messages_per_agent})")
        try:
            envelope = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, ValueError) as exc:
            raise EnvelopeError("envelope is not UTF-8 JSON") from exc
        if not isinstance(envelope, dict):
            raise EnvelopeError("envelope must be a JSON object")
        envelope_id = envelope.get("id")
        if not isinstance(envelope_id, str) or not re.fullmatch(r"[0-9a-f]{32}", envelope_id):
            raise EnvelopeError("envelope id must be 32 hex chars")
        existing = self.db.scalar(select(AgentMessageRow).where(
            AgentMessageRow.run_id == self.run_id, AgentMessageRow.from_agent == sender,
            AgentMessageRow.envelope_id == envelope_id))
        if existing is not None:  # redelivery after a failed remove: idempotent
            return IngestResult(existing, [])
        kind = envelope.get("kind")
        if kind not in AGENT_KINDS:
            raise EnvelopeError(f"agents may not send kind {kind!r}")
        body, truncated = sanitize_body(envelope.get("body"), self.max_body_chars)
        meta: dict[str, Any] = {}
        claimed_from = envelope.get("from")
        if claimed_from not in (None, sender):
            # Spoofing attempt or confusion: recorded, never trusted.
            meta["claimed_from_ignored"] = str(claimed_from)[:64]
        to_agent, to_role, recipients = self._route(member, kind, envelope.get("to"))
        attempt_id = self.attempt_for.get(sender)
        status = None
        options: list[str] = []
        if kind == "report":
            status = envelope.get("status") or "done"
            if status not in ("done", "failed", "progress"):
                raise EnvelopeError("report status must be done, failed or progress")
            meta["status"] = status
        if kind == "decision_request":
            raw_options = envelope.get("options") or []
            if not isinstance(raw_options, list):
                raise EnvelopeError("options must be a list")
            options = [sanitize_body(str(o), MAX_OPTION_CHARS)[0] for o in raw_options[:MAX_OPTIONS] if str(o).strip()]
            meta["options"] = options
            # ADR 0033 (#60): a material request blocks its scope until a human
            # resolves it.  Materiality is declared, never inferred from text.
            material = envelope.get("material", False)
            if not isinstance(material, bool):
                raise EnvelopeError("material must be true or false")
            if material:
                consequence = envelope.get("consequence")
                if not isinstance(consequence, str) or not consequence.strip():
                    raise EnvelopeError("a material decision request needs a consequence")
                if len(set(options)) < 2:
                    raise EnvelopeError("a material decision request needs at least two distinct options")
                meta["material"] = True
                meta["consequence"] = sanitize_body(consequence, MAX_CONSEQUENCE_CHARS)[0]
                meta["authority"] = "human"
        message = self._persist(
            envelope_id=envelope_id, from_agent=sender, from_role=member.role, to_agent=to_agent, to_role=to_role,
            kind=kind, body=body, truncated=truncated, attempt_id=attempt_id, source_sandbox_id=sandbox_id,
            meta=meta or None,
        )
        self._counts[sender] = count + 1
        result = IngestResult(message, recipients)
        if kind == "report":
            claim = AgentClaimRow(
                id=f"CLAIM-{uuid4().hex[:12]}", run_id=self.run_id, message_id=message.id, agent_id=sender,
                attempt_id=attempt_id, subtask_key=member.subtask_key, claimed_status=status, body=body,
                created_at=utcnow(),
            )
            self.db.add(claim)
            self.db.flush()
            result.claim = claim
        if kind == "decision_request":
            request = open_agent_request(self.db, run_id=self.run_id, requested_by=sender, question=body,
                                         options=options, message_id=message.id)
            result.decision_request_id = request.id
        return result

    def _route(self, member: Member, kind: str, to: Any) -> tuple[str | None, str | None, list[str]]:
        if kind in ("report", "decision_request"):
            return None, MANAGER, []
        if not isinstance(to, str) or not to.strip():
            raise EnvelopeError("message needs a recipient")
        to = to.strip().lower()
        if to in (MANAGER, HOST):
            return None, MANAGER, []
        if to == "lead":
            leads = [m.agent_id for m in self.members.values() if m.role == "lead" and m.agent_id != member.agent_id]
            if not leads:
                raise EnvelopeError("no lead to receive this message")
            return (leads[0] if len(leads) == 1 else None), "lead", leads
        if to in ("worker", "workers"):
            if member.role != "lead":
                raise EnvelopeError("only the lead may broadcast to all workers")
            return None, "worker", [m.agent_id for m in self.members.values() if m.role == "worker"]
        target = self.members.get(to)
        if target is None:
            raise EnvelopeError(f"unknown recipient {to!r}")
        if target.agent_id == member.agent_id:
            raise EnvelopeError("an agent cannot message itself")
        return target.agent_id, target.role, [target.agent_id]

    # -- outbound from the host ----------------------------------------------
    def post_from_host(self, to_agent: str, body: str, *, kind: str = "system",
                       meta: dict[str, Any] | None = None) -> AgentMessageRow:
        if to_agent not in self.members:
            raise ValueError(f"unknown member {to_agent}")
        if kind not in ("system", "decision"):
            raise ValueError("host messages are system or decision")
        clean, truncated = sanitize_body(body, 4 * self.max_body_chars)
        return self._persist(
            envelope_id=uuid4().hex, from_agent=HOST, from_role=HOST, to_agent=to_agent,
            to_role=self.members[to_agent].role, kind=kind, body=clean, truncated=truncated,
            attempt_id=self.attempt_for.get(to_agent), source_sandbox_id=None, meta=meta,
        )
