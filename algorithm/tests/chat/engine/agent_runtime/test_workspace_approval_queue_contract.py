from __future__ import annotations

import json
import threading

from lazymind.chat.engine.agent_runtime import tool_limit_control
from lazymind.chat.engine.agent_runtime.tool_limit_control import ToolLimitDecisionCoordinator


class MemoryQueue:
    def __init__(self):
        self.items = []

    def enqueue(self, value):
        self.items.append(value)


def test_workspace_approvals_accept_only_allow_once_or_deny_and_are_idempotent(monkeypatch):
    queue = MemoryQueue()
    monkeypatch.setattr(tool_limit_control, "FileSystemQueue", lambda **_kwargs: queue)
    coordinator = ToolLimitDecisionCoordinator()
    coordinator._register("sid-1", "decision-1")

    assert coordinator.submit("sid-1", "decision-1", "allow_once") is True
    assert coordinator.submit("sid-1", "decision-1", "allow_once") is True
    assert [json.loads(item)["action"] for item in queue.items] == ["allow_once"]


def test_two_workspace_approvals_for_one_task_are_fifo(monkeypatch):
    queue = MemoryQueue()
    monkeypatch.setattr(tool_limit_control, "FileSystemQueue", lambda **_kwargs: queue)
    coordinator = ToolLimitDecisionCoordinator()
    coordinator._register("sid-1", "decision-1")
    coordinator._register("sid-1", "decision-2")

    assert coordinator.submit("sid-1", "decision-1", "deny") is True
    assert coordinator.submit("sid-1", "decision-2", "allow_once") is True
    assert [json.loads(item)["decision_id"] for item in queue.items] == ["decision-1", "decision-2"]


def test_queued_workspace_approval_does_not_activate_until_head_completes(monkeypatch):
    queue = MemoryQueue()
    monkeypatch.setattr(tool_limit_control, "FileSystemQueue", lambda **_kwargs: queue)
    coordinator = ToolLimitDecisionCoordinator()
    coordinator._register("sid-1", "decision-1")
    coordinator._register("sid-1", "decision-2")
    activated = threading.Event()

    waiter = threading.Thread(
        target=lambda: (coordinator._wait_until_active("sid-1", "decision-2"), activated.set()),
    )
    waiter.start()
    assert activated.wait(0.05) is False
    assert coordinator.submit("sid-1", "decision-1", "deny") is True
    assert activated.wait(1) is True
    waiter.join(timeout=1)


def test_completed_approval_idempotency_expires_after_ten_minutes(monkeypatch):
    queue = MemoryQueue()
    now = [100.0]
    monkeypatch.setattr(tool_limit_control, "FileSystemQueue", lambda **_kwargs: queue)
    coordinator = ToolLimitDecisionCoordinator(now=lambda: now[0])
    coordinator._register("sid-1", "decision-1")
    assert coordinator.submit("sid-1", "decision-1", "allow_once") is True
    assert coordinator.submit("sid-1", "decision-1", "allow_once") is True
    now[0] += 601
    assert coordinator.submit("sid-1", "decision-1", "allow_once") is False
