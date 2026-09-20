from sdf_core.db import Base, RuntimeEventRow, make_engine
import pytest

from sdf_core.runtime import (
    FakeRuntime,
    RuntimeController,
    RuntimeEvent,
    RuntimeEventConflictError,
    RuntimeEventKind,
    RuntimeStatus,
    SqlAlchemyRuntimeEventSink,
)
from sqlalchemy.orm import sessionmaker


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


def test_runtime_controller_normalizes_attempt_bound_lifecycle_events():
    controller = RuntimeController(FakeRuntime())
    session = controller.start(attempt_id="ATTEMPT-003", agent="codex")
    controller.send(session.session_id, "inspect")
    assert controller.stream(session.session_id) == ("inspect",)
    controller.cancel(session.session_id)
    controller.reconnect(session.session_id)

    assert [event.kind for event in controller.events] == [
        RuntimeEventKind.STARTED,
        RuntimeEventKind.INPUT_SENT,
        RuntimeEventKind.OUTPUT_OBSERVED,
        RuntimeEventKind.CANCELLED,
        RuntimeEventKind.RECONNECTED,
    ]
    assert all(event.attempt_id == "ATTEMPT-003" for event in controller.events)
    assert controller.events[1].payload == {"input": "inspect"}
    assert controller.events[2].payload == {"output": ("inspect",)}


def test_runtime_events_persist_with_attempt_order_and_payload():
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        controller = RuntimeController(FakeRuntime(), event_sink=SqlAlchemyRuntimeEventSink(db))
        session = controller.start(attempt_id="ATTEMPT-004", agent="codex")
        controller.send(session.session_id, "inspect")
        controller.cancel(session.session_id)
        db.commit()

        rows = db.query(RuntimeEventRow).filter_by(attempt_id="ATTEMPT-004").order_by(RuntimeEventRow.sequence).all()
        assert [row.sequence for row in rows] == [1, 2, 3]
        assert [row.kind for row in rows] == ["runtime_started", "runtime_input_sent", "runtime_cancelled"]
        assert rows[1].payload == {"input": "inspect"}


def test_runtime_event_replay_is_idempotent_by_source_attempt_session_sequence():
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        controller = RuntimeController(FakeRuntime(), event_sink=SqlAlchemyRuntimeEventSink(db))
        session = controller.start(attempt_id="ATTEMPT-005", agent="codex")
        event = controller.events[0]
        sink = SqlAlchemyRuntimeEventSink(db)
        sink.append(event)
        db.commit()

        rows = db.query(RuntimeEventRow).filter_by(attempt_id="ATTEMPT-005").all()
        assert len(rows) == 1
        assert rows[0].source == "runtime"


def test_runtime_event_identity_allows_independent_trusted_sources():
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        sink = SqlAlchemyRuntimeEventSink(db)
        event = RuntimeEvent(
            RuntimeEventKind.STARTED,
            "SESSION-006",
            "ATTEMPT-006",
            1,
            RuntimeStatus.RUNNING,
            {},
            source="runtime",
        )
        tool_event = RuntimeEvent(
            RuntimeEventKind.STARTED,
            "SESSION-006",
            "ATTEMPT-006",
            1,
            RuntimeStatus.RUNNING,
            {},
            source="tool-proxy",
        )
        sink.replay((event, tool_event, event))
        db.commit()

        rows = db.query(RuntimeEventRow).filter_by(attempt_id="ATTEMPT-006").all()
        assert {(row.source, row.sequence) for row in rows} == {("runtime", 1), ("tool-proxy", 1)}


def test_runtime_event_replay_rejects_identity_conflicts():
    engine = make_engine()
    Base.metadata.create_all(engine)
    with sessionmaker(engine, expire_on_commit=False)() as db:
        sink = SqlAlchemyRuntimeEventSink(db)
        event = RuntimeEvent(
            RuntimeEventKind.INPUT_SENT,
            "SESSION-007",
            "ATTEMPT-007",
            1,
            RuntimeStatus.RUNNING,
            {"input": "first"},
        )
        sink.append(event)
        with pytest.raises(RuntimeEventConflictError):
            sink.append(
                RuntimeEvent(
                    RuntimeEventKind.INPUT_SENT,
                    "SESSION-007",
                    "ATTEMPT-007",
                    1,
                    RuntimeStatus.RUNNING,
                    {"input": "different"},
                )
            )


def test_runtime_controller_replay_does_not_dispatch_again():
    runtime = FakeRuntime()
    original = RuntimeController(runtime)
    session = original.start(attempt_id="ATTEMPT-008", agent="codex")
    original.send(session.session_id, "inspect")

    recovered = RuntimeController(runtime)
    recovered.replay(original.events)
    assert recovered.events == original.events
    assert runtime.stream(session.session_id) == ("inspect",)
    # Starting the same Attempt after replay returns the existing session and
    # does not emit a second runtime-start observation.
    assert recovered.start(attempt_id="ATTEMPT-008", agent="codex") == runtime.status(session.session_id)


def test_runtime_replay_rejects_cross_attempt_session_rebinding():
    runtime = FakeRuntime()
    controller = RuntimeController(runtime)
    controller.replay((RuntimeEvent(
        RuntimeEventKind.STARTED, "SESSION-REBIND", "ATTEMPT-009", 1,
        RuntimeStatus.RUNNING, {},
    ),))
    with pytest.raises(ValueError, match="different attempt"):
        controller.replay((RuntimeEvent(
            RuntimeEventKind.OUTPUT_OBSERVED, "SESSION-REBIND", "ATTEMPT-010", 2,
            RuntimeStatus.RUNNING, {"output": ()},
        ),))
    with pytest.raises(ValueError, match="different runtime session"):
        controller.replay((RuntimeEvent(
            RuntimeEventKind.OUTPUT_OBSERVED, "SESSION-OTHER", "ATTEMPT-009", 2,
            RuntimeStatus.RUNNING, {"output": ()},
        ),))
