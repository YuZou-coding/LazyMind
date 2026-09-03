from __future__ import annotations

import time

import lazyllm

import lazyllm.tools.agent.functionCall as function_call_module
from lazyllm.tools.agent.functionCall import FunctionCall
from lazymind.chat.engine.agent_runtime.tool_limit_control import (
    tool_limit_decision_coordinator,
)


class _FakeFunctionCall:
    def __init__(self) -> None:
        self.replayed = []
        self._tools_manager = self._run_tools

    def _get_visible_tool_names(self):
        return {"run_command"}

    def _run_tools(self, calls, **_kwargs):
        self.replayed.extend(calls)
        return [{"ok": True, "value": {"exit_code": 0}}]


def _approval_call():
    return {
        "function": {
            "name": "run_command",
            "arguments": {"command": "npm install", "cwd": "."},
        }
    }


def test_model_cannot_set_internal_approval_flag():
    calls = [_approval_call()]
    calls[0]["function"]["arguments"]["allow_unsafe"] = True

    sanitized = function_call_module._strip_model_tool_permissions(calls)

    assert sanitized[0]["function"]["arguments"]["allow_unsafe"] is False


def test_allow_once_replays_the_original_command_with_internal_permission(monkeypatch):
    sid = f"workspace-approval-{time.time_ns()}"
    lazyllm.globals._init_sid(sid)
    events = []
    monkeypatch.setattr(
        function_call_module,
        "_write_agent_data",
        lambda tag, **data: events.append((tag, data)),
    )
    monkeypatch.setattr(
        tool_limit_decision_coordinator,
        "_wait_for_action",
        lambda *_args: "allow_once",
    )
    fake = _FakeFunctionCall()

    result = FunctionCall._resolve_tool_approvals(
        fake,
        [_approval_call()],
        [{"ok": False, "needs_approval": True, "value": "approval required"}],
    )

    assert result[0]["ok"] is True
    replayed_arguments = fake.replayed[0]["function"]["arguments"]
    assert replayed_arguments["command"] == "npm install"
    assert replayed_arguments["allow_unsafe"] is True
    assert events[0][0] == "tool_limit_pending"
    assert events[0][1]["command"] == "npm install"
    assert events[0][1]["cwd"] == "."


def test_denied_command_is_not_replayed(monkeypatch):
    sid = f"workspace-denial-{time.time_ns()}"
    lazyllm.globals._init_sid(sid)
    monkeypatch.setattr(
        function_call_module,
        "_write_agent_data",
        lambda *_args, **_kwargs: None,
    )
    monkeypatch.setattr(
        tool_limit_decision_coordinator,
        "_wait_for_action",
        lambda *_args: "deny",
    )
    fake = _FakeFunctionCall()

    result = FunctionCall._resolve_tool_approvals(
        fake,
        [_approval_call()],
        [{"ok": False, "needs_approval": True, "value": "approval required"}],
    )

    assert fake.replayed == []
    assert result == [{
        "ok": False,
        "value": "The user denied or did not approve this operation.",
    }]
