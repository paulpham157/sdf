"""Local OTLP/HTTP receiver.

Speaks the JSON and protobuf encodings on ``/v1/traces``, ``/v1/metrics``,
and ``/v1/logs``. A body may arrive with ``Content-Length`` or
``Transfer-Encoding: chunked`` (no ``Content-Length``). gRPC is intentionally
not implemented.
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
_MAX_CHUNK_LINE = 4096
_MAX_TRAILERS = 65_536


class _BodyStatus(Exception):
    def __init__(self, status: int, message: bytes):
        super().__init__(message.decode("ascii", "replace"))
        self.status = status
        self.message = message


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
                body = self._read_body()
            except _BodyStatus as exc:
                self._reply(exc.status, exc.message, "text/plain")
                return
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

        def _read_body(self) -> bytes:
            # Chunked overrides Content-Length (RFC 9112). OpenCode traces and
            # pi-otel posts omit Content-Length and send chunked JSON.
            encodings = [
                part.strip().lower()
                for part in (self.headers.get("Transfer-Encoding") or "").split(",")
                if part.strip()
            ]
            if encodings:
                if encodings != ["chunked"]:
                    self.close_connection = True
                    raise _BodyStatus(400, b"unsupported transfer encoding")
                return self._read_chunked()
            try:
                length = int(self.headers.get("Content-Length", "0"))
            except ValueError as exc:
                raise _BodyStatus(400, b"invalid content length") from exc
            if length < 0 or length > MAX_BODY_BYTES:
                raise _BodyStatus(413, b"payload too large")
            if length == 0:
                return b""
            body = self.rfile.read(length)
            if len(body) != length:
                self.close_connection = True
                raise _BodyStatus(400, b"incomplete body")
            return body

        def _read_chunked(self) -> bytes:
            body = bytearray()
            while True:
                line = self.rfile.readline(_MAX_CHUNK_LINE)
                if not line.endswith(b"\n"):
                    self.close_connection = True
                    raise _BodyStatus(400, b"invalid chunked body")
                size_token = line.split(b";", 1)[0].strip()
                try:
                    size = int(size_token, 16)
                except ValueError as exc:
                    self.close_connection = True
                    raise _BodyStatus(400, b"invalid chunk size") from exc
                if size < 0:
                    self.close_connection = True
                    raise _BodyStatus(400, b"invalid chunk size")
                if size == 0:
                    self._discard_trailers()
                    return bytes(body)
                if size > MAX_BODY_BYTES or len(body) + size > MAX_BODY_BYTES:
                    self.close_connection = True
                    raise _BodyStatus(413, b"payload too large")
                chunk = self.rfile.read(size)
                if len(chunk) != size:
                    self.close_connection = True
                    raise _BodyStatus(400, b"incomplete chunked body")
                body.extend(chunk)
                delimiter = self.rfile.read(2)
                if delimiter != b"\r\n":
                    self.close_connection = True
                    raise _BodyStatus(400, b"invalid chunked body")

        def _discard_trailers(self) -> None:
            seen = 0
            while True:
                line = self.rfile.readline(_MAX_CHUNK_LINE)
                seen += len(line)
                if seen > _MAX_TRAILERS or not line.endswith(b"\n"):
                    self.close_connection = True
                    raise _BodyStatus(400, b"invalid chunked body")
                if line in (b"\r\n", b"\n"):
                    return

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
