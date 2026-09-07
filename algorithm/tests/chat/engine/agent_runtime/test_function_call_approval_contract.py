from __future__ import annotations

import time

import lazyllm

from lazymind.chat.engine.agent_runtime import executor as executor_module
from lazymind.chat.engine.agent_runtime.executor import ToolCallGuard
from lazymind.chat.engine.agent_runtime.tool_limit_control import tool_limit_decision_coordinator


def _approval_call(allow_unsafe=False):
    return {
        'function': {
            'name': 'shell_tool',
            'arguments': {'cmd': 'npm install', 'cwd': '.', 'allow_unsafe': allow_unsafe},
        }
    }


class _ApprovalManager:
    def __init__(self):
        self.calls = []

    def __call__(self, calls, **_kwargs):
        self.calls.extend(calls)
        arguments = calls[0]['function']['arguments']
        if arguments.get('allow_unsafe') is True:
            return [{'ok': True, 'value': {'exit_code': 0}}]
        return [{'ok': False, 'needs_approval': True, 'value': 'approval required'}]


def _set_mode(mode='ask_as_needed'):
    previous = lazyllm.globals.get('agentic_config')
    lazyllm.globals['agentic_config'] = {'workspace_permission_mode': mode}
    return previous


def _restore(previous):
    if previous is None:
        lazyllm.globals.pop('agentic_config', None)
    else:
        lazyllm.globals['agentic_config'] = previous


def test_model_cannot_set_internal_approval_flag():
    sanitized = ToolCallGuard._strip_model_permissions(_approval_call(allow_unsafe=True))
    assert sanitized['function']['arguments']['allow_unsafe'] is False


def test_model_permission_sanitizer_does_not_add_internal_argument_to_other_tools():
    call = {'function': {'name': 'url_fetch', 'arguments': {'url': 'https://example.com'}}}

    sanitized = ToolCallGuard._strip_model_permissions(call)

    assert sanitized['function']['arguments'] == {'url': 'https://example.com'}


def test_allow_once_replays_original_command_with_backend_permission(monkeypatch):
    sid = f'workspace-approval-{time.time_ns()}'
    lazyllm.globals._init_sid(sid)
    events = []
    monkeypatch.setattr(executor_module, '_write_agent_data', lambda tag, **data: events.append((tag, data)))
    monkeypatch.setattr(tool_limit_decision_coordinator, '_wait_for_action', lambda *_args: 'allow_once')
    manager = _ApprovalManager()
    guard = ToolCallGuard(manager)
    previous = _set_mode()
    try:
        result = guard(_approval_call())
    finally:
        _restore(previous)

    assert result[0]['ok'] is True
    assert len(manager.calls) == 2
    assert manager.calls[1]['function']['arguments']['cmd'] == 'npm install'
    assert manager.calls[1]['function']['arguments']['allow_unsafe'] is True
    assert events[0][0] == 'tool_limit_pending'


def test_denied_command_is_not_replayed(monkeypatch):
    sid = f'workspace-denial-{time.time_ns()}'
    lazyllm.globals._init_sid(sid)
    monkeypatch.setattr(executor_module, '_write_agent_data', lambda *_args, **_kwargs: None)
    monkeypatch.setattr(tool_limit_decision_coordinator, '_wait_for_action', lambda *_args: 'deny')
    manager = _ApprovalManager()
    guard = ToolCallGuard(manager)
    previous = _set_mode()
    try:
        result = guard(_approval_call())
    finally:
        _restore(previous)

    assert len(manager.calls) == 1
    assert result[0]['ok'] is False
    assert 'denied' in result[0]['value']


def test_allow_all_replays_without_waiting(monkeypatch):
    monkeypatch.setattr(
        tool_limit_decision_coordinator,
        '_wait_for_action',
        lambda *_args: (_ for _ in ()).throw(AssertionError('allow_all must not wait')),
    )
    manager = _ApprovalManager()
    guard = ToolCallGuard(manager)
    previous = _set_mode('allow_all')
    try:
        result = guard(_approval_call())
    finally:
        _restore(previous)

    assert result[0]['ok'] is True
    assert len(manager.calls) == 2


def test_permission_change_does_not_auto_approve_pending_operation(monkeypatch):
    sid = f'pending-mode-change-{time.time_ns()}'
    lazyllm.globals._init_sid(sid)
    monkeypatch.setattr(executor_module, '_write_agent_data', lambda *_args, **_kwargs: None)
    waits = []
    monkeypatch.setattr(
        tool_limit_decision_coordinator,
        '_wait_for_action',
        lambda *_args: waits.append(True) or 'deny',
    )

    class ModeChangingManager(_ApprovalManager):
        def __call__(self, calls, **kwargs):
            result = super().__call__(calls, **kwargs)
            lazyllm.globals['agentic_config']['workspace_permission_mode'] = 'allow_all'
            return result

    manager = ModeChangingManager()
    guard = ToolCallGuard(manager)
    previous = _set_mode('ask_as_needed')
    try:
        result = guard(_approval_call())
    finally:
        _restore(previous)

    assert waits == [True]
    assert len(manager.calls) == 1
    assert result[0]['ok'] is False
    assert 'denied' in result[0]['value']


def test_subagent_decision_is_routed_by_opaque_decision_id(monkeypatch):
    writes = []

    class Queue:
        def __init__(self, klass=None):
            self.klass = klass

        def enqueue(self, value):
            writes.append((lazyllm.globals._sid, self.klass, value))

    monkeypatch.setattr(
        'lazymind.chat.engine.agent_runtime.tool_limit_control.FileSystemQueue', Queue,
    )
    coordinator = executor_module.tool_limit_decision_coordinator
    sid = f'subagent-task-{time.time_ns()}'
    decision_id = f'decision-{time.time_ns()}'
    coordinator._register(sid, decision_id, 'conversation-1')
    try:
        assert coordinator.submit_by_decision(
            'conversation-other', decision_id, 'allow_once',
        ) is False
        assert coordinator.submit_by_decision(
            'conversation-1', decision_id, 'allow_once',
        ) is True
    finally:
        coordinator._unregister(sid, decision_id)

    assert writes and writes[0][0] == sid
    assert writes[0][1] == 'agent_control'


def test_always_ask_approves_connected_app_before_first_call(monkeypatch):
    sid = f'connected-app-{time.time_ns()}'
    lazyllm.globals._init_sid(sid)
    monkeypatch.setattr(executor_module, '_write_agent_data', lambda *_args, **_kwargs: None)
    monkeypatch.setattr(tool_limit_decision_coordinator, '_wait_for_action', lambda *_args: 'allow_once')

    class ConnectedManager:
        def __init__(self):
            self.calls = []

        def __call__(self, calls, **_kwargs):
            self.calls.extend(calls)
            assert 'connected_action' in (
                lazyllm.globals.get('agentic_config') or {}
            ).get('approved_connected_app_tools', [])
            return [{'ok': True, 'value': 'done'}]

    manager = ConnectedManager()
    guard = ToolCallGuard(manager)
    previous = lazyllm.globals.get('agentic_config')
    lazyllm.globals['agentic_config'] = {
        'workspace_permission_mode': 'always_ask',
        'connected_app_tool_names': ['connected_action'],
    }
    try:
        result = guard({'function': {'name': 'connected_action', 'arguments': {'value': 1}}})
    finally:
        _restore(previous)

    assert result[0]['ok'] is True
    assert len(manager.calls) == 1


def test_always_ask_approves_network_tool_before_first_call(monkeypatch):
    sid = f'network-tool-{time.time_ns()}'
    lazyllm.globals._init_sid(sid)
    events = []
    monkeypatch.setattr(executor_module, '_write_agent_data', lambda tag, **data: events.append((tag, data)))
    monkeypatch.setattr(tool_limit_decision_coordinator, '_wait_for_action', lambda *_args: 'allow_once')

    class NetworkManager:
        def __init__(self):
            self.calls = []

        def __call__(self, calls, **_kwargs):
            self.calls.extend(calls)
            return [{'ok': True, 'value': 'fetched'}]

    manager = NetworkManager()
    guard = ToolCallGuard(manager)
    previous = lazyllm.globals.get('agentic_config')
    lazyllm.globals['agentic_config'] = {
        'workspace_permission_mode': 'always_ask',
        'network_tool_names': ['url_fetch'],
    }
    try:
        result = guard({'function': {'name': 'url_fetch', 'arguments': {'url': 'https://example.com'}}})
    finally:
        _restore(previous)

    assert result[0]['ok'] is True
    assert len(manager.calls) == 1
    assert events[0][1]['tool_name'] == 'url_fetch'
