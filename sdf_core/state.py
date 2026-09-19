from __future__ import annotations

from .model import AttemptState, TaskState


TASK_TRANSITIONS: dict[TaskState, set[TaskState]] = {
    TaskState.CREATED: {TaskState.READY},
    TaskState.READY: {TaskState.RUNNING},
    TaskState.RUNNING: {TaskState.EVALUATING},
    TaskState.EVALUATING: {TaskState.SUCCEEDED, TaskState.FAILED, TaskState.INCONCLUSIVE},
    TaskState.SUCCEEDED: set(),
    TaskState.FAILED: {TaskState.READY},
    TaskState.INCONCLUSIVE: {TaskState.READY},
}

ATTEMPT_TRANSITIONS: dict[AttemptState, set[AttemptState]] = {
    AttemptState.CREATED: {AttemptState.DISPATCHED},
    AttemptState.DISPATCHED: {AttemptState.RUNNING},
    AttemptState.RUNNING: {AttemptState.COMPLETED, AttemptState.FAILED, AttemptState.CANCELLED},
    AttemptState.COMPLETED: set(),
    AttemptState.FAILED: set(),
    AttemptState.CANCELLED: set(),
}


def transition_task(current: TaskState, target: TaskState) -> TaskState:
    if target not in TASK_TRANSITIONS[current]:
        raise ValueError(f"invalid task transition: {current} -> {target}")
    return target


def transition_attempt(current: AttemptState, target: AttemptState) -> AttemptState:
    if target not in ATTEMPT_TRANSITIONS[current]:
        raise ValueError(f"invalid attempt transition: {current} -> {target}")
    return target
