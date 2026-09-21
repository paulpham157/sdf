import json

import pytest

from sdf_core.herdr_endpoint_transport import HerdrEndpointTransport
from sdf_core.herdr_runtime import HerdrRuntimeError


def test_endpoint_transport_posts_command_without_starting_local_process():
    calls = []

    def requester(endpoint, body, headers, timeout_ms):
        calls.append((endpoint, json.loads(body), dict(headers), timeout_ms))
        return 200, b'{"stdout":"{\\"status\\":\\"running\\"}\\n"}'

    transport = HerdrEndpointTransport(
        endpoint="https://herdr.example.test/v1/command",
        token="<REDACTED>",
        requester=requester,
    )

    assert transport.run(("herdr", "status", "server", "--json"), 3210) == '{"status":"running"}\n'
    endpoint, payload, headers, timeout_ms = calls[0]
    assert endpoint == "https://herdr.example.test/v1/command"
    assert payload == {"command": ["herdr", "status", "server", "--json"], "timeout_ms": 3210}
    assert headers["Authorization"] == "Bearer <REDACTED>"
    assert headers["Content-Type"] == "application/json"
    assert timeout_ms == 3210


def test_endpoint_transport_preserves_raw_herdr_json_response():
    transport = HerdrEndpointTransport(
        endpoint="https://herdr.example.test/v1/command",
        token="<REDACTED>",
        requester=lambda *_: (200, b'{"result":{"type":"session_snapshot"}}'),
    )
    assert transport.run(("herdr", "api", "snapshot"), 1000) == '{"result":{"type":"session_snapshot"}}'


def test_endpoint_transport_fails_closed_on_http_error():
    transport = HerdrEndpointTransport(
        endpoint="https://herdr.example.test/v1/command",
        token="<REDACTED>",
        requester=lambda *_: (401, b"unauthorized"),
    )
    with pytest.raises(HerdrRuntimeError, match="HTTP 401"):
        transport.run(("herdr", "api", "snapshot"), 1000)


def test_endpoint_transport_requires_https_and_token():
    with pytest.raises(ValueError, match="HTTPS"):
        HerdrEndpointTransport(endpoint="http://herdr.example.test/v1/command", token="x")
    with pytest.raises(ValueError, match="token"):
        HerdrEndpointTransport(endpoint="https://herdr.example.test/v1/command", token=" ")
