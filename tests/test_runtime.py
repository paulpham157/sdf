from sdf_core.runtime import FakeRuntime, RuntimeStatus


def test_fake_runtime_correlates_attempt_and_deduplicates_start():
    runtime = FakeRuntime()
    first = runtime.start(attempt_id="ATTEMPT-001", agent="codex")
    again = runtime.start(attempt_id="ATTEMPT-001", agent="codex")
    assert first == again
    assert first.attempt_id == "ATTEMPT-001"
    assert runtime.send(first.session_id, "inspect").output == ("inspect",)
    assert runtime.stream(first.session_id) == ("inspect",)


def test_fake_runtime_lifecycle_is_explicit():
    runtime = FakeRuntime()
    session = runtime.start(attempt_id="ATTEMPT-002", agent="codex")
    assert runtime.cancel(session.session_id).status is RuntimeStatus.CANCELLED
    assert runtime.reconnect(session.session_id).status is RuntimeStatus.CANCELLED
    assert runtime.terminate(session.session_id).status is RuntimeStatus.TERMINATED
