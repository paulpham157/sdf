"""Structured logs and the turn error taxonomy (ADR 0023).

* :func:`configure_logging` installs one stdlib ``logging`` handler on the
  ``sdf`` logger: JSON lines (``SDF_LOG_FORMAT=json``, the default) or plain
  text (``text``), at ``SDF_LOG_LEVEL`` (default ``INFO``), on stderr.  Nothing
  listens on a port and nothing leaves the host: an operator ships stderr.
* :func:`log_context` binds correlation ids (run, task, attempt, agent, turn,
  sandbox, principal, request) in :mod:`contextvars`, so every record logged
  inside the block - on this thread or a task copied from it - carries them.
* Every record is redacted twice: values of known secret variables present in
  the process environment are replaced, and token-shaped strings
  (``Bearer ...``, ``sk-...``, ``e2b_...``, ``key=value`` with a secret-ish
  key) are masked.  Secret *names* may be logged; values never.
* When tracing is on (ADR 0024) records also carry ``trace_id``/``span_id``
  of the active span.
* :func:`classify_error` maps a turn/Attempt failure to one stable class
  (:data:`ERROR_CLASSES`) recorded in the usage ledger and on the Attempt.
"""

from __future__ import annotations

import contextvars
import json
import logging
import os
import re
import sys
from collections.abc import Iterator, Mapping
from contextlib import contextmanager
from datetime import datetime, timezone
from typing import Any
from urllib.parse import unquote

LOGGER_NAME = "sdf"
CONTEXT_FIELDS = ("request_id", "principal", "run_id", "task_id", "attempt_id", "agent", "turn_id", "sandbox_id")
_CONTEXT: contextvars.ContextVar[Mapping[str, str]] = contextvars.ContextVar("sdf_log_context", default={})

# --- redaction ---------------------------------------------------------------

REDACTED = "[REDACTED]"
# Environment variables whose *values* must never appear in a log line.
_SECRET_NAME = re.compile(
    r"(API_KEY|_TOKEN|_SECRET|PASSWORD|PEPPER|AUTH_JSON|PRIVATE_KEY|CREDENTIAL|OTLP(?:_TRACES)?_HEADERS)", re.I)
# Comma-separated ``name=value`` lists: each value is a secret on its own too.
_HEADER_LIST_NAME = re.compile(r"OTLP(?:_TRACES)?_HEADERS$", re.I)
_SECRET_NAME_EXCLUDE = re.compile(r"(_ENV|_MODE_[A-Z]+|CREDENTIAL_MODE.*)$", re.I)
_PATTERNS = (
    re.compile(r"(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{8,}"),
    re.compile(r"\b(sk|e2b|dtn|ghp|gho|xox[abp])[-_][A-Za-z0-9_-]{8,}"),
    re.compile(r"(?i)\b([A-Z0-9_]*(?:api[_-]?key|token|secret|password)[A-Z0-9_]*)(\s*[=:]\s*)([^\s,;\"']{4,})"),
)
_MIN_SECRET_LEN = 8


def _secret_values(environ: Mapping[str, str] | None = None) -> tuple[str, ...]:
    env = os.environ if environ is None else environ
    values = {
        value for name, value in env.items()
        if value and len(value) >= _MIN_SECRET_LEN and _SECRET_NAME.search(name) and not _SECRET_NAME_EXCLUDE.search(name)
    }
    for name, value in env.items():
        if value and _HEADER_LIST_NAME.search(name):
            for entry in value.split(","):
                part = entry.partition("=")[2].strip()
                decoded = unquote(part)
                # "Bearer%20<token>" / "Basic <b64>": the bare credential is a secret too.
                for candidate in (part, decoded, *decoded.split()[1:]):
                    if len(candidate) >= _MIN_SECRET_LEN:
                        values.add(candidate)
    return tuple(sorted(values, key=len, reverse=True))


def redact(text: str, *, environ: Mapping[str, str] | None = None, extra: tuple[str, ...] = ()) -> str:
    """Mask secret values and token-shaped strings in ``text``."""

    out = text
    for value in (*extra, *_secret_values(environ)):
        if value and len(value) >= _MIN_SECRET_LEN and value in out:
            out = out.replace(value, REDACTED)
    out = _PATTERNS[0].sub(lambda m: f"{m.group(1)} {REDACTED}", out)
    out = _PATTERNS[1].sub(REDACTED, out)
    out = _PATTERNS[2].sub(lambda m: f"{m.group(1)}{m.group(2)}{REDACTED}", out)
    return out


def redact_value(value: Any, *, environ: Mapping[str, str] | None = None) -> Any:
    if isinstance(value, str):
        return redact(value, environ=environ)
    if isinstance(value, Mapping):
        return {str(k): (REDACTED if _SECRET_NAME.search(str(k)) and not _SECRET_NAME_EXCLUDE.search(str(k))
                         and isinstance(v, str) and v else redact_value(v, environ=environ))
                for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [redact_value(v, environ=environ) for v in value]
    return value


# --- context -----------------------------------------------------------------


def current_context() -> dict[str, str]:
    return dict(_CONTEXT.get())


@contextmanager
def log_context(**fields: Any) -> Iterator[None]:
    """Bind correlation ids for every record logged inside the block."""

    unknown = set(fields) - set(CONTEXT_FIELDS)
    if unknown:
        raise ValueError(f"unknown log context field(s): {sorted(unknown)}")
    merged = {**_CONTEXT.get(), **{k: str(v) for k, v in fields.items() if v is not None}}
    token = _CONTEXT.set(merged)
    try:
        yield
    finally:
        _CONTEXT.reset(token)


def bind(**fields: Any) -> None:
    """Add ids to the current context (until the enclosing block exits)."""

    unknown = set(fields) - set(CONTEXT_FIELDS)
    if unknown:
        raise ValueError(f"unknown log context field(s): {sorted(unknown)}")
    _CONTEXT.set({**_CONTEXT.get(), **{k: str(v) for k, v in fields.items() if v is not None}})


# --- formatting --------------------------------------------------------------

_RESERVED = set(vars(logging.LogRecord("", 0, "", 0, "", (), None))) | {"message", "asctime"}


class ContextFilter(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        for key, value in _CONTEXT.get().items():
            if not hasattr(record, key):
                setattr(record, key, value)
        # ADR 0024: correlate with the active OpenTelemetry span (no-op when tracing is off).
        tracing = sys.modules.get("sdf_core.tracing")
        ids = tracing.current_ids() if tracing is not None and tracing.enabled() else None
        if ids is not None and not hasattr(record, "trace_id"):
            record.trace_id, record.span_id = ids
        return True


class JsonFormatter(logging.Formatter):
    """One JSON object per line; every string is redacted."""

    def format(self, record: logging.LogRecord) -> str:
        payload: dict[str, Any] = {
            "ts": datetime.fromtimestamp(record.created, timezone.utc).isoformat(timespec="milliseconds"),
            "level": record.levelname.lower(),
            "logger": record.name,
            "event": record.getMessage(),
        }
        for key in CONTEXT_FIELDS:
            value = getattr(record, key, None)
            if value is not None:
                payload[key] = value
        for key, value in vars(record).items():
            if key not in _RESERVED and key not in payload and not key.startswith("_"):
                payload[key] = value
        if record.exc_info:
            payload["exc_type"] = record.exc_info[0].__name__ if record.exc_info[0] else None
            payload["exc"] = self.formatException(record.exc_info)[-4000:]
        return redact(json.dumps(redact_value(payload), default=str, sort_keys=False))


class TextFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        ids = " ".join(f"{k}={getattr(record, k)}" for k in CONTEXT_FIELDS if getattr(record, k, None) is not None)
        extra = " ".join(
            f"{k}={v}" for k, v in vars(record).items()
            if k not in _RESERVED and k not in CONTEXT_FIELDS and not k.startswith("_")
        )
        line = f"{self.formatTime(record)} {record.levelname} {record.name} {record.getMessage()} {ids} {extra}".rstrip()
        if record.exc_info:
            line += "\n" + self.formatException(record.exc_info)
        return redact(line)


_HANDLER_FLAG = "_sdf_handler"


def configure_logging(environ: Mapping[str, str] | None = None, stream: Any = None) -> logging.Logger:
    """Install (or replace) the ``sdf`` logger's handler from the environment."""

    env = os.environ if environ is None else environ
    level_name = (env.get("SDF_LOG_LEVEL") or "INFO").strip().upper()
    level = logging.getLevelName(level_name)
    if not isinstance(level, int):
        raise ValueError("SDF_LOG_LEVEL must be DEBUG, INFO, WARNING, ERROR or CRITICAL")
    fmt = (env.get("SDF_LOG_FORMAT") or "json").strip().lower()
    if fmt not in ("json", "text"):
        raise ValueError("SDF_LOG_FORMAT must be json or text")
    logger = logging.getLogger(LOGGER_NAME)
    for handler in list(logger.handlers):
        if getattr(handler, _HANDLER_FLAG, False):
            logger.removeHandler(handler)
    handler = logging.StreamHandler(stream or sys.stderr)
    setattr(handler, _HANDLER_FLAG, True)
    handler.addFilter(ContextFilter())
    handler.setFormatter(JsonFormatter() if fmt == "json" else TextFormatter())
    logger.addHandler(handler)
    logger.setLevel(level)
    logger.propagate = False
    return logger


def get_logger(name: str) -> logging.Logger:
    """A child of the ``sdf`` logger (``sdf.<name>``)."""

    return logging.getLogger(f"{LOGGER_NAME}.{name}")


# --- turn error taxonomy -----------------------------------------------------

ERROR_CLASSES = (
    "timeout", "killed", "settled_grace", "provider_4xx", "provider_429", "provider_5xx",
    "sandbox_error", "cleanup_failed", "config_error", "agent_error",
)
_TIMEOUT = re.compile(r"(?i)(exceeded \d+s|timed out|timeout|did not become ready|outlived)")
_KILLED = re.compile(r"(?i)(\bkilled\b|sigkill|exit 137|terminated|cancelled)")
_P429 = re.compile(r"(?i)(\b429\b|rate[ -]?limit|too many requests|quota)")
_P5XX = re.compile(r"(?i)(\b5\d\d\b|internal server error|bad gateway|service unavailable|gateway timeout|overloaded)")
_P4XX = re.compile(r"(?i)(\b4(0[0-9]|1[0-9]|2[0-8])\b|unauthorized|forbidden|bad request|unsupported|invalid api key|not found)")
_SANDBOX = re.compile(r"(?i)(e2b|daytona|sandbox provisioning|sandbox has no id|envd|staged attempt workspace|"
                      r"credential seed|herdr (command|server)|exit marker)")


def classify_error(error: BaseException | str | None, *, status: str | None = None) -> str | None:
    """Map a failure to one :data:`ERROR_CLASSES` value (``None``: no error).

    ``status`` is the runtime's final status when the turn did not raise (a
    Herdr turn that outlived its wait stays ``running``).
    """

    if error is None:
        if status in (None, "completed", "succeeded", "success"):
            return None
        if status in ("running", "created"):
            return "timeout"
        if status in ("terminated", "cancelled"):
            return "killed"
        return "agent_error"
    from .credentials import CredentialConfigError

    if isinstance(error, CredentialConfigError):
        return "config_error"
    text = error if isinstance(error, str) else f"{type(error).__name__}: {error}"
    if isinstance(error, TimeoutError) or _TIMEOUT.search(text):
        return "timeout"
    if _P429.search(text):
        return "provider_429"
    if _P5XX.search(text):
        return "provider_5xx"
    if _P4XX.search(text):
        return "provider_4xx"
    if _KILLED.search(text):
        return "killed"
    if _SANDBOX.search(text):
        return "sandbox_error"
    return "agent_error"
