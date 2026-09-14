"""Real gateway/DB fixtures; only platform and Core HTTP boundaries are fake."""
import copy
import datetime as dt
import hashlib
import json
import os
import threading
import uuid
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import psycopg
import pytest
import httpx
from fastapi.testclient import TestClient
from psycopg.conninfo import make_conninfo

from channel_gateway import bootstrap
from channel_gateway.app import app
from channel_gateway.common.domain.channel import ClaimedOutbound
from channel_gateway.common.infrastructure.security import JsonCipher
from channel_gateway.feishu.domain import FeishuRuntimeError


class ControlledRegistrar:
    """Only the remote authorization process is controlled by the test."""

    def __init__(self):
        self.started = threading.Event()
        self.release = threading.Event()
        self.outcome = FeishuRuntimeError('fixture authorization denied')

    def register(self, *, on_qr_code, on_status_change, cancel_event):
        on_qr_code('https://fixture.invalid/qr', 300)
        self.started.set()
        while not self.release.wait(0.01):
            if cancel_event.is_set():
                raise FeishuRuntimeError('fixture authorization canceled')
        if isinstance(self.outcome, Exception):
            raise self.outcome
        return self.outcome


class FakeFeishuSender:
    """Platform seam: records real rendered requests; never renders or queues."""

    def __init__(self):
        self.calls = []
        self.on_send = None
        self.sent = threading.Event()

    def _send(self, kind, **kwargs):
        self.calls.append((kind, copy.deepcopy(kwargs)))
        if self.on_send:
            self.on_send(kind, kwargs)
        self.sent.set()
        return f'om_fixture_{len(self.calls)}'

    def send_card(self, **kwargs):
        return self._send('card', **kwargs)

    def send_markdown(self, **kwargs):
        return self._send('text', **kwargs)

    def send_image(self, **kwargs):
        return self._send('image', **kwargs)

    def send_file(self, **kwargs):
        return self._send('file', **kwargs)

    def list_chats(self, **kwargs):
        return {'items': [
            {'chat_id': 'group-a', 'name': '产品组', 'chat_mode': 'group'},
            {'chat_id': 'group-b', 'name': '研发组', 'chat_mode': 'group'},
        ], 'has_more': False}

    def get_chat(self, chat_id, **kwargs):
        if chat_id not in {'group-a', 'group-b', 'owner-chat'}:
            raise ValueError('fixture unknown chat')
        return {'chat_id': chat_id, 'name': '测试会话',
                'chat_mode': 'p2p' if chat_id == 'owner-chat' else 'group',
                'bot_in_chat': True, 'can_send': True}

    def close(self):
        pass


class FakeFeishuFactory:
    def __init__(self, sender):
        self.sender = sender

    def create_sender(self, credentials):
        return self.sender

    def create_receiver(self, *args, **kwargs):
        raise AssertionError('notification tests must not start inbound receivers')


@dataclass
class CoreBoundary:
    allowed: bool = True
    generation: int = 1
    http_status: int = 200
    calls: list = field(default_factory=list)
    references: list = field(default_factory=list)
    forward_url: str = ''


@pytest.fixture
def core_boundary():
    state = CoreBoundary()

    class Handler(BaseHTTPRequestHandler):
        def respond(self):
            size = int(self.headers.get('Content-Length', 0))
            body = json.loads(self.rfile.read(size)) if size else {}
            state.calls.append((self.path, body, dict(self.headers)))
            if self.path.startswith('/static-files/'):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'fixture file bytes')
                return
            if self.path == '/static-files:sign':
                self.send_response(200 if self.headers.get('X-User-Id') == 'owner' else 403)
                self.send_header('Content-Type', 'application/json')
                self.end_headers()
                self.wfile.write(json.dumps({'urls': {p: p for p in body['paths']}}).encode())
                return
            if state.forward_url:
                response = httpx.request(self.command, state.forward_url + self.path,
                                         json=body if size else None,
                                         headers={k: v for k, v in self.headers.items()
                                                  if k.lower() not in {'host', 'content-length'}}, timeout=5)
                self.send_response(response.status_code)
                self.send_header('Content-Type', 'application/json')
                self.end_headers()
                self.wfile.write(response.content)
                return
            status = state.http_status
            if self.headers.get('X-LazyMind-Internal-Token') != 'fixture-internal-token':
                status = 401
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps({
                'allowed': state.allowed, 'generation': state.generation,
                'items': state.references, 'owner_user_id': 'owner',
            }).encode())

        do_GET = respond
        do_POST = respond

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    yield state, f'http://127.0.0.1:{server.server_port}'
    server.shutdown()
    server.server_close()
    thread.join(timeout=2)


@pytest.fixture(params=['sqlite', 'postgres'])
def gateway_dsn(request, tmp_path):
    if request.param == 'sqlite':
        yield f'sqlite:///{tmp_path / "gateway.db"}'
        return
    base = os.getenv('NOTIFICATION_TEST_POSTGRES_DSN', '')
    if not base:
        pytest.skip('PostgreSQL not verified: set NOTIFICATION_TEST_POSTGRES_DSN')
    schema = 'notification_test_' + uuid.uuid4().hex
    with psycopg.connect(base, autocommit=True) as connection:
        connection.execute(psycopg.sql.SQL('CREATE SCHEMA {}').format(
            psycopg.sql.Identifier(schema)))
    try:
        yield make_conninfo(base, options=f'-c search_path={schema}')
    finally:
        with psycopg.connect(base, autocommit=True) as connection:
            connection.execute(psycopg.sql.SQL('DROP SCHEMA {} CASCADE').format(
                psycopg.sql.Identifier(schema)))


@dataclass
class GatewayHarness:
    components: object
    client: TestClient
    sender: FakeFeishuSender
    core: CoreBoundary
    accounts: list
    settings: object
    registrar: ControlledRegistrar
    accepting_notifications: bool = True
    drop_next_handoff_response: bool = False
    handoff_attempted: threading.Event = field(default_factory=threading.Event)

    def request(self, method, path, body=None, owner='owner', internal=False):
        headers = {'X-Request-Id': 'notification-contract-request'}
        if owner:
            headers['X-User-Id'] = owner
        if internal:
            headers['X-LazyMind-Internal-Token'] = 'fixture-internal-token'
        return self.client.request(method, path, json=body, headers=headers)

    def payload(self, recipient='group-a', account=0, **changes):
        value = {
            'schema_version': 1, 'notification_id': 'notification-one',
            'user_id': 'owner', 'task_id': 'task-one',
            'event': 'succeeded', 'event_instance_id': 'event-one',
            'rule_version': 1, 'settings_generation': 1,
            'provider': 'feishu', 'account_id': self.accounts[account]['id'],
            'recipient_id': recipient, 'content_mode': 'full',
            'content': {
                'title': '行业日报', 'executed_at': '2026-09-10T01:00:00Z',
                'body': '第一项结论\n\n最后一项结论',
                'summary': '已核实的摘要', 'summary_status': 'ready',
                'artifacts': [],
            },
        }
        value.update(changes)
        return value

    def submit(self, payload):
        response = self.request('POST', '/internal/task-notifications',
                                payload, internal=True)
        assert response.status_code == 202, response.text
        return response.json()

    def history(self, notification_id='notification-one'):
        response = self.request('GET', '/internal/task-notifications/'
                                + notification_id, internal=True)
        assert response.status_code == 200, response.text
        return response.json()

    def render(self, payload):
        outbound = ClaimedOutbound(
            outbox_id='outbound-one', created_sequence=1, provider='feishu',
            account_id=payload['account_id'], order_key='notification-order',
            recipient_id=payload['recipient_id'],
            provider_context={'chat_id': payload['recipient_id']},
            text=payload['content']['body'], intent_kind='task_notification',
            purpose='task_notification', metadata={'task_notification': payload},
            rendered_parts=[], next_part_index=0, provider_state={},
            attempt_count=1,
        )
        provider = self.components.delivery_worker._providers.delivery('feishu')
        return provider.render(outbound)


@pytest.fixture
def gateway(gateway_dsn, core_boundary, tmp_path, monkeypatch):
    core, core_url = core_boundary
    sender = FakeFeishuSender()
    registrar = ControlledRegistrar()
    monkeypatch.setattr(bootstrap, 'LarkAppRegistrar', lambda: registrar)
    monkeypatch.setenv('LAZYMIND_AUTH_SERVICE_INTERNAL_TOKEN',
                       'fixture-internal-token')
    monkeypatch.setattr(bootstrap, 'LarkChannelFactory',
                        lambda: FakeFeishuFactory(sender))
    settings = bootstrap.Settings(
        database_dsn=gateway_dsn, credential_key_path=str(tmp_path / 'key'),
        core_base_url=core_url,
    )
    components = bootstrap.build_components(settings)
    components.store.initialize()
    cipher = JsonCipher(settings.credential_key_path)
    accounts = []
    for index in range(2):
        app_id, person = f'fixture-app-{index}', f'fixture-person-{index}'
        identity = hashlib.sha256(f'{app_id}:{person}'.encode()).hexdigest()
        accounts.append(components.store.connect_referenced_account(
            owner_user_id='owner', provider='feishu', external_id_hash=identity,
            label='同名飞书账号', status='connected',
            credentials_ciphertext=cipher.encrypt('owner', {
                'app_id': app_id, 'app_secret': 'fixture-not-a-real-secret',
                'provider_account_id': person, 'provider_tenant_key': 'tenant',
            }),
        ))
    monkeypatch.setattr(app.state, 'components', components, raising=False)
    # No lifespan: only the outgoing worker is started explicitly by tests.
    # Real incoming receivers/registration are outside this fixture's scope.
    client = TestClient(app, raise_server_exceptions=False)
    yield GatewayHarness(components, client, sender, core, accounts, settings, registrar)
    registrar.release.set()
    components.connections._providers.connection('feishu').stop()
    components.delivery_worker.stop()
    client.close()


def card_text(value):
    if isinstance(value, list):
        return '\n'.join(card_text(item) for item in value)
    if isinstance(value, dict):
        own = value.get('content', value.get('text', ''))
        own = own if isinstance(own, str) else ''
        children = [card_text(v) for k, v in value.items()
                    if isinstance(v, (dict, list)) and k not in {'content', 'text'}]
        return '\n'.join([own] + children)
    return ''


FIXED_PAST = dt.datetime(2000, 1, 1, tzinfo=dt.timezone.utc)
