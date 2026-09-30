"""Local AgentJev-0.6B Judge backend: loopback HTTP process, fail-open, token fit.

Transport is stdlib ``urllib`` only (injectable for tests). Wire translation stays
in ``judge_agentjev.AgentJevJudge``. Model weights and torch live outside this
package; see ``docs/research/agentjev-local.md``.
"""

from __future__ import annotations

import copy
import json
import urllib.error
import urllib.request
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from typing import Any

from sdf_core.judge_agentjev import TOKEN_LIMIT, AgentJevError, AgentJevJudge

DEFAULT_AGENTJEV_URL = "http://127.0.0.1:8149"
EVALUATE_PATH = "/api/evaluate"

# Qwen-style English/code is often ~3–4 characters per token. Use 3 so the
# estimate stays at or above a real tokenizer count for ASCII-heavy triage state.
_CHARS_PER_TOKEN = 3

# Leave headroom inside the 2,048-token path for the triage questions/options
# when callers pass ``questions`` into ``fit_state_for_agentjev``.
_DEFAULT_QUESTION_RESERVE = 700


PostFn = Callable[[dict[str, Any]], Mapping[str, Any]]
UrlOpen = Callable[..., Any]


@dataclass(frozen=True)
class LocalJudgeResult:
    """Outcome of one local ask; transport and service failures fail open."""

    answers: dict[str, dict[str, Any]] | None
    unavailable_reason: str | None = None
    model: str | None = None
    usage: dict[str, Any] = field(default_factory=dict)

    @property
    def available(self) -> bool:
        return self.answers is not None


def estimate_tokens(text: str) -> int:
    """Conservative token estimate without loading a tokenizer or torch."""
    if not text:
        return 0
    return (len(text) + _CHARS_PER_TOKEN - 1) // _CHARS_PER_TOKEN


def _compact(value: Any) -> str:
    return json.dumps(value, separators=(",", ":"), ensure_ascii=False)


def _request_tokens(state: Mapping[str, Any], questions: Mapping[str, Mapping[str, Any]] | None) -> int:
    if questions is None:
        return estimate_tokens(_compact(state))
    from sdf_core.judge_agentjev import to_agentjev_request

    return estimate_tokens(_compact(to_agentjev_request(state, questions)))


def _shrink_string(text: str, target_len: int) -> str:
    if target_len <= 0:
        return ""
    if len(text) <= target_len:
        return text
    return text[-target_len:]


def fit_state_for_agentjev(
    state: Mapping[str, Any],
    *,
    questions: Mapping[str, Mapping[str, Any]] | None = None,
    token_limit: int = TOKEN_LIMIT,
    question_reserve: int = _DEFAULT_QUESTION_RESERVE,
) -> dict[str, Any]:
    """Return a copy of ``state`` with log tails cut to fit AgentJev's token limit.

    AgentJev rejects (never truncates) input over its context. When ``questions``
    is given, the whole evaluate request is estimated; otherwise a reserve is
    held for typical triage questions.
    """

    fitted: dict[str, Any] = copy.deepcopy(dict(state))
    budget = token_limit if questions is not None else max(token_limit - max(question_reserve, 0), 1)

    def over() -> bool:
        return _request_tokens(fitted, questions) > budget

    if not over():
        return fitted

    # Prefer cutting the long tails that dominate token use.
    for _ in range(64):
        if not over():
            return fitted
        candidates: list[tuple[int, str, Any]] = []
        adapter = fitted.get("adapter_stderr_tail")
        if isinstance(adapter, str) and adapter:
            candidates.append((len(adapter), "adapter", None))
        instructions = fitted.get("task_instructions")
        if isinstance(instructions, str) and len(instructions) > 80:
            candidates.append((len(instructions), "instructions", None))
        failed = fitted.get("failed_evidence")
        if isinstance(failed, list):
            for index, item in enumerate(failed):
                if isinstance(item, dict):
                    tail = item.get("stderr_tail")
                    if isinstance(tail, str) and tail:
                        candidates.append((len(tail), "stderr", index))
        if not candidates:
            break
        candidates.sort(reverse=True)
        _, kind, index = candidates[0]
        if kind == "adapter":
            fitted["adapter_stderr_tail"] = _shrink_string(str(fitted["adapter_stderr_tail"]), len(str(fitted["adapter_stderr_tail"])) // 2)
        elif kind == "instructions":
            text = str(fitted["task_instructions"])
            fitted["task_instructions"] = text[: max(len(text) // 2, 40)]
        else:
            item = fitted["failed_evidence"][index]
            tail = str(item["stderr_tail"])
            item["stderr_tail"] = _shrink_string(tail, max(len(tail) // 2, 1))

    # Final hard cut if still over (pathological fields).
    while over():
        adapter = fitted.get("adapter_stderr_tail")
        if isinstance(adapter, str) and adapter:
            fitted["adapter_stderr_tail"] = _shrink_string(adapter, max(len(adapter) - 200, 0))
            continue
        failed = fitted.get("failed_evidence")
        if isinstance(failed, list) and failed:
            shortened = False
            for item in failed:
                if isinstance(item, dict) and isinstance(item.get("stderr_tail"), str) and item["stderr_tail"]:
                    item["stderr_tail"] = _shrink_string(item["stderr_tail"], max(len(item["stderr_tail"]) - 200, 0))
                    shortened = True
                    break
            if shortened:
                continue
        instructions = fitted.get("task_instructions")
        if isinstance(instructions, str) and instructions:
            fitted["task_instructions"] = instructions[: max(len(instructions) - 200, 0)]
            continue
        break

    return fitted


def make_agentjev_post(
    base_url: str = DEFAULT_AGENTJEV_URL,
    *,
    timeout_s: float = 60.0,
    urlopen: UrlOpen | None = None,
) -> PostFn:
    """Build a ``post(request) -> response`` that talks to a local AgentJev process."""

    root = base_url.rstrip("/")
    evaluate_url = f"{root}{EVALUATE_PATH}"
    open_url = urlopen or urllib.request.urlopen

    def post(request: dict[str, Any]) -> Mapping[str, Any]:
        body = json.dumps(request, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
        req = urllib.request.Request(
            evaluate_url,
            data=body,
            headers={"Content-Type": "application/json", "Accept": "application/json"},
            method="POST",
        )
        with open_url(req, timeout=timeout_s) as response:
            raw = response.read()
        payload = json.loads(raw.decode("utf-8"))
        if not isinstance(payload, dict):
            raise ValueError("AgentJev response must be an object")
        return payload

    return post


class LocalAgentJevJudge:
    """Ask a local AgentJev process through an injected ``post``; fail open on errors.

    Does not start or own the model process. Unit tests inject a fake process;
    production uses ``make_agentjev_post`` against a loopback server started from
    weights kept outside the repo.
    """

    def __init__(self, post: PostFn) -> None:
        self._judge = AgentJevJudge(post)
        self.last_result: LocalJudgeResult | None = None

    def ask(self, state: Mapping[str, Any], questions: Mapping[str, Mapping[str, Any]]) -> LocalJudgeResult:
        self.last_result = None
        try:
            fitted = fit_state_for_agentjev(state, questions=questions)
            answers = self._judge.ask(fitted, questions)
            reply = self._judge.last_reply
            assert reply is not None
            result = LocalJudgeResult(answers, model=reply.model, usage=dict(reply.usage))
        except (AgentJevError, ValueError, TypeError, OSError, TimeoutError, urllib.error.URLError) as exc:
            result = LocalJudgeResult(None, unavailable_reason=str(exc))
        except Exception as exc:  # noqa: BLE001 — fail open for any transport/runtime surprise
            result = LocalJudgeResult(None, unavailable_reason=f"local AgentJev unavailable: {exc}")
        self.last_result = result
        return result
