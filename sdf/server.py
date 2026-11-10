"""Local OTLP/HTTP receiver.

Speaks the JSON and protobuf encodings on ``/v1/traces``, ``/v1/metrics``,
and ``/v1/logs``. gRPC is intentionally not implemented: both Codex and
Claude Code can export OTLP/HTTP.
"""

from __future__ import annotations

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from sdf.otlp import MAX_BODY_BYTES, OtlpError, decode_export
from sdf.store import Store

_SIGNALS = {
    "/v1/traces": "traces",
    "/v1/metrics": "metrics",
    "/v1/logs": "logs",
}


class TelemetryServer(ThreadingHTTPServer):
    daemon_threads = True


def make_server(host: str, port: int, store: Store, *, quiet: bool = False) -> TelemetryServer:
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_GET(self) -> None:  # noqa: N802
            if self.path.split("?", 1)[0] != "/health":
                self._reply(404, b"not found", "text/plain")
                return
            self._reply(200, b'{"status":"ok"}', "application/json")

        def do_POST(self) -> None:  # noqa: N802
            path = self.path.split("?", 1)[0]
            signal = _SIGNALS.get(path)
            if signal is None:
                self._reply(404, b"not found", "text/plain")
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
            except ValueError:
                self._reply(400, b"invalid content length", "text/plain")
                return
            if length < 0 or length > MAX_BODY_BYTES:
                self._reply(413, b"payload too large", "text/plain")
                return
            body = self.rfile.read(length)
            content_type = self.headers.get("Content-Type")
            try:
                records = decode_export(signal, body, content_type)
            except OtlpError:
                self._reply(400, b"could not parse OTLP payload", "text/plain")
                return
            store.insert(records)
            if _is_json(content_type):
                self._reply(200, b"{}", "application/json")
            else:
                self._reply(200, b"", "application/x-protobuf")

        def log_message(self, fmt: str, *args) -> None:
            if not quiet:
                super().log_message(fmt, *args)

        def _reply(self, status: int, payload: bytes, content_type: str) -> None:
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

    return TelemetryServer((host, port), Handler)


def _is_json(content_type: str | None) -> bool:
    media = (content_type or "").split(";", 1)[0].strip().lower()
    return media == "application/json"
