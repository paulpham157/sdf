from __future__ import annotations

import re
from collections.abc import Iterable
from typing import Any

from sdf_core.evaluator import EvaluationResult

REDACTED = "<REDACTED>"
MIN_SECRET_VALUE_CHARS = 6
TASK_INSTRUCTIONS_CHARS = 1200
FIELD_CHARS = 160

_NAME = r"(?:[A-Za-z0-9_.-]*[_.-]|api|auth|access|private|client)?(?:key|token|secret|password|passwd|pwd)"
_PATTERNS: tuple[tuple[re.Pattern[str], str], ...] = (
    (re.compile(r"(?i)(\bauthorization\s*[:=]\s*)[^\r\n'\"]+"), rf"\1{REDACTED}"),
    (re.compile(r"(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}"), REDACTED),
    (re.compile(rf"(?i)([\"']{_NAME}[\"']\s*:\s*[\"'])[^\"'\r\n]*([\"'])"), rf"\1{REDACTED}\2"),
    (re.compile(rf"(?i)\b({_NAME}\s*[=:](?!=)\s*)(?:'[^'\r\n]*'|\"[^\"\r\n]*\"|[^\s,;'\"]+)"), rf"\1{REDACTED}"),
    (re.compile(r"\bsk-(?:ant-)?[A-Za-z0-9_-]{16,}"), REDACTED),
    (re.compile(r"\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})"), REDACTED),
    (re.compile(r"\b(?:AKIA|ASIA)[0-9A-Z]{16}\b"), REDACTED),
    (re.compile(r"\be2b_[A-Za-z0-9]{16,}"), REDACTED),
)


def redact(text: str, *, secret_values: Iterable[str] = ()) -> str:
    values = sorted({v for v in secret_values if v and len(v) >= MIN_SECRET_VALUE_CHARS}, key=len, reverse=True)
    for value in values:
        text = text.replace(value, REDACTED)
    for pattern, replacement in _PATTERNS:
        text = pattern.sub(replacement, text)
    return text


def build_triage_state(
    *,
    task_instructions: str,
    evaluation: EvaluationResult,
    adapter_stderr: str = "",
    diff_stat: str = "unknown",
    secret_values: Iterable[str] = (),
    tail_chars: int = 1500,
    max_failed: int = 3,
) -> dict[str, Any]:
    """Build the small, redacted Attempt view a Judge may see.

    Every string is redacted whole, then cut, then redacted again, so a
    secret split by a cut cannot leave a fragment. Failed-evidence tails share
    one ``tail_chars`` budget to keep the state inside a 2,048-token judge.
    """

    secrets = tuple(secret_values)

    def clean(text: str, limit: int, *, tail: bool = True) -> str:
        text = redact(text or "", secret_values=secrets)
        text = (text[-limit:] if limit > 0 else "") if tail else text[:limit]
        return redact(text, secret_values=secrets)

    failed = [item for item in evaluation.evidence if item.status == "FAIL"][: max(max_failed, 0)]
    per_failure = tail_chars // len(failed) if failed else 0
    return {
        "task_instructions": clean(task_instructions, TASK_INSTRUCTIONS_CHARS, tail=False),
        "evaluator_status": clean(evaluation.status, FIELD_CHARS, tail=False),
        "failed_evidence": [
            {
                "criterion": None if item.criterion is None else clean(item.criterion, FIELD_CHARS, tail=False),
                "command": clean(item.command, FIELD_CHARS, tail=False),
                "exit_code": item.exit_code,
                "stderr_tail": clean(item.stderr or item.stdout, per_failure),
            }
            for item in failed
        ],
        "adapter_stderr_tail": clean(adapter_stderr, tail_chars),
        "diff_stat": clean(diff_stat, FIELD_CHARS, tail=False),
    }
