from fastapi import FastAPI
from fastapi.testclient import TestClient

from lazymind.chat.api import agent_control_routes
from lazymind.chat.engine.agent_runtime import tool_limit_control
from lazymind.chat.service import chat_service


class _Coordinator:
    def __init__(self, accepted: bool = True) -> None:
        self.accepted = accepted
        self.calls = []
        self.fallback_calls = []

    def submit(self, sid: str, decision_id: str, action: str) -> bool:
        self.calls.append((sid, decision_id, action))
        return self.accepted

    def submit_by_decision(self, conversation_id: str, decision_id: str, action: str) -> bool:
        self.fallback_calls.append((conversation_id, decision_id, action))
        return self.accepted


def _client() -> TestClient:
    app = FastAPI()
    app.include_router(agent_control_routes.router)
    return TestClient(app)


def test_tool_limit_decision_forwards_to_active_agent(monkeypatch) -> None:
    coordinator = _Coordinator()
    monkeypatch.setattr(chat_service, '_active_sessions', {'conversation-1': 'sid-1'})
    monkeypatch.setattr(tool_limit_control, 'tool_limit_decision_coordinator', coordinator)

    response = _client().post('/api/agent/tool-limit-decision', json={
        'conversation_id': 'conversation-1',
        'decision_id': 'decision-1',
        'action': 'continue',
    })

    assert response.status_code == 200
    assert response.json() == {'ok': True}
    assert coordinator.calls == [('sid-1', 'decision-1', 'continue')]
    assert coordinator.fallback_calls == []


def test_tool_limit_decision_rejects_inactive_decision(monkeypatch) -> None:
    coordinator = _Coordinator(accepted=False)
    monkeypatch.setattr(chat_service, '_active_sessions', {'conversation-1': 'sid-1'})
    monkeypatch.setattr(tool_limit_control, 'tool_limit_decision_coordinator', coordinator)

    response = _client().post('/api/agent/tool-limit-decision', json={
        'conversation_id': 'conversation-1',
        'decision_id': 'expired',
        'action': 'summarize',
    })

    assert response.status_code == 200
    assert response.json() == {'ok': False}
    assert coordinator.calls == [('sid-1', 'expired', 'summarize')]
    assert coordinator.fallback_calls == [('conversation-1', 'expired', 'summarize')]


def test_tool_limit_decision_falls_back_without_active_session(monkeypatch) -> None:
    coordinator = _Coordinator()
    monkeypatch.setattr(chat_service, '_active_sessions', {})
    monkeypatch.setattr(tool_limit_control, 'tool_limit_decision_coordinator', coordinator)

    response = _client().post('/api/agent/tool-limit-decision', json={
        'conversation_id': 'conversation-1',
        'decision_id': 'decision-1',
        'action': 'allow_once',
    })

    assert response.status_code == 200
    assert response.json() == {'ok': True}
    assert coordinator.calls == []
    assert coordinator.fallback_calls == [('conversation-1', 'decision-1', 'allow_once')]


def test_tool_limit_decision_validates_action() -> None:
    response = _client().post('/api/agent/tool-limit-decision', json={
        'conversation_id': 'conversation-1',
        'decision_id': 'decision-1',
        'action': 'invalid',
    })

    assert response.status_code == 400


def test_clear_session_env_drops_stored_conversation_vars(monkeypatch) -> None:
    from lazymind.chat.service import chat_service

    previous = dict(chat_service._conversation_env_vars)
    chat_service._conversation_env_vars.clear()
    chat_service._conversation_env_vars['conversation-1'] = {'REDFOX_API_KEY': 'secret'}
    try:
        response = _client().post('/api/chat/session-env:clear', json={
            'conversation_ids': ['conversation-1', 'missing'],
        })
    finally:
        chat_service._conversation_env_vars.clear()
        chat_service._conversation_env_vars.update(previous)

    assert response.status_code == 200
    assert response.json() == {'ok': True, 'cleared': ['conversation-1']}
