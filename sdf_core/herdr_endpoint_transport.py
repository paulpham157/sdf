"""HTTPS transport for a Herdr command endpoint hosted in a sandbox.

The endpoint is a deployment-owned bridge next to Herdr. It accepts a JSON
command request and returns the stdout-shaped Herdr response. SDF remains the
control plane; no Herdr or Codex process is started on the SDF host.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from typing import Callable, Mapping, Sequence

from .herdr_runtime import HerdrRuntimeError


EndpointRequester = Callable[[str, bytes, Mapping[str, str], int], tuple[int, bytes]]


class HerdrEndpointTransport:
    """Send Herdr CLI-shaped commands to a remote HTTPS bridge endpoint."""

    def __init__(
        self,
        *,
        endpoint: str,
        token: str,
        requester: EndpointRequester | None = None,
    ) -> None:
        endpoint = endpoint.strip()
        if not endpoint:
            raise ValueError("Herdr endpoint must be non-empty")
        if not endpoint.startswith("https://"):
            raise ValueError("Herdr endpoint must use HTTPS")
        if not token.strip():
            raise ValueError("Herdr endpoint token must be non-empty")
        self.endpoint = endpoint
        self._token = token
        self._requester = requester or self._request

    def run(self, command: Sequence[str], timeout_ms: int) -> str:
        if not command:
            raise ValueError("Herdr command must not be empty")
        if timeout_ms <= 0:
            raise ValueError("Herdr endpoint timeout must be positive")
        body = json.dumps(
            {"command": [str(part) for part in command], "timeout_ms": timeout_ms},
            separators=(",", ":"),
        ).encode("utf-8")
        headers = {
            "Accept": "application/json, text/plain",
            "Authorization": f"Bearer {self._token}",
            "Content-Type": "application/json",
        }
        try:
            status, raw = self._requester(self.endpoint, body, headers, timeout_ms)
        except Exception as exc:
            raise HerdrRuntimeError(f"Herdr endpoint request failed: {exc}") from exc
        if status < 200 or status >= 300:
            detail = raw.decode("utf-8", errors="replace")[-1000:]
            raise HerdrRuntimeError(f"Herdr endpoint returned HTTP {status}: {detail}")
        return self._decode_response(raw)

    @staticmethod
    def _decode_response(raw: bytes) -> str:
        text = raw.decode("utf-8", errors="replace")
        try:
            payload = json.loads(text)
        except json.JSONDecodeError:
            return text
        if isinstance(payload, dict):
            for key in ("stdout", "output", "body"):
                value = payload.get(key)
                if isinstance(value, str):
                    return value
        return text

    @staticmethod
    def _request(endpoint: str, body: bytes, headers: Mapping[str, str], timeout_ms: int) -> tuple[int, bytes]:
        request = urllib.request.Request(endpoint, data=body, headers=dict(headers), method="POST")
        try:
            with urllib.request.urlopen(request, timeout=timeout_ms / 1000) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as exc:
            return exc.code, exc.read()
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            raise HerdrRuntimeError(f"Herdr endpoint connection failed: {exc}") from exc
