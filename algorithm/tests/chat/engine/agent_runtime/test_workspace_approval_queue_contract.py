from __future__ import annotations

import json

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
