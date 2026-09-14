"""Core startup -> real scheduler -> real gateway -> fake Feishu boundary.

The chat execution/model are upstream fakes. No notification rule, event,
summary orchestration, persistence, queue or retry is implemented in fixtures.
"""
import json
import os
import signal
import socket
import sqlite3
import subprocess
import threading
import time
import uuid
from contextlib import contextmanager
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import httpx
import psycopg
import pytest
import uvicorn
from psycopg.conninfo import conninfo_to_dict, make_conninfo
from psycopg.rows import dict_row

from channel_gateway.app import app
from conftest import card_text


CORE_DIR = Path(__file__).resolve().parents[3] / 'core'


@pytest.fixture(scope='session')
def notification_core_binary(tmp_path_factory):
    path = tmp_path_factory.mktemp('notification-core-bin') / 'core.test'
    result = subprocess.run(['go', 'test', '-c', '-o', str(path)], cwd=CORE_DIR,
                            capture_output=True, text=True, timeout=120)
    assert result.returncode == 0, result.stderr
    return path


@dataclass
class ExecutionBoundary:
    driver: str
    dsn: str
    answer: str = '前半段说明。\n\n末尾关键结论：成本下降百分之二十。'
    summary: str = '成本下降百分之二十。'
    summary_http_status: int = 200
    chat_http_status: int = 200
    chat_calls: int = 0
    model_requests: list = field(default_factory=list)
    restart_core: object = None

    @contextmanager
    def connection(self):
        if self.driver == 'sqlite':
            with sqlite3.connect(self.dsn, timeout=10) as connection:
                connection.row_factory = sqlite3.Row
                yield connection
        else:
            with psycopg.connect(self.dsn, row_factory=dict_row) as connection:
                yield connection

    def sql(self, query, values=()):
        with self.connection() as connection:
            if self.driver == 'sqlite':
                query = query.replace('%s', '?')
            cursor = connection.execute(query, values)
            return [dict(row) for row in cursor.fetchall()] if cursor.description else []


def reserve_socket():
    sock = socket.socket()
    sock.bind(('127.0.0.1', 0))
    return sock


@pytest.fixture
def notification_stack(gateway, notification_core_binary, tmp_path):
    driver = 'sqlite' if gateway.settings.database_dsn.startswith('sqlite:') else 'postgres'
    database_name = 'notification_e2e_' + uuid.uuid4().hex
    base = os.getenv('NOTIFICATION_TEST_POSTGRES_DSN', '')
    if driver == 'sqlite':
        dsn = str(tmp_path / 'core.db')
    else:
        with psycopg.connect(base, autocommit=True) as connection:
            connection.execute(psycopg.sql.SQL('CREATE DATABASE {}').format(
                psycopg.sql.Identifier(database_name)))
        config = conninfo_to_dict(base)
        config['dbname'] = database_name
        dsn = make_conninfo(**config)
    execution = ExecutionBoundary(driver, dsn)

    class UpstreamHandler(BaseHTTPRequestHandler):
        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', 0))) or b'{}')
            status, data = 200, {}
            if self.path == '/api/chat/sensitive-check':
                data = {'passed': True}
            elif self.path == '/conversations:chat':
                execution.chat_calls += 1
                status = execution.chat_http_status
                if status == 200:
                    execution.sql('INSERT INTO chat_histories '
                                  '(id,seq,conversation_id,result,create_time,update_time) '
                                  'VALUES (%s,1,%s,%s,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)',
                                  ('h_' + uuid.uuid4().hex, body['conversation_id'], execution.answer))
            elif self.path == '/api/chat/llm-task:run':
                execution.model_requests.append(body)
                status = execution.summary_http_status
                data = {'status': 'succeeded', 'text': execution.summary,
                        'output': {'summary': execution.summary}, 'task_id': 'model-fixture'}
            else:
                status = 404
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps(data).encode())

        def log_message(self, *args):
            pass

    upstream = ThreadingHTTPServer(('127.0.0.1', 0), UpstreamHandler)
    upstream_thread = threading.Thread(target=upstream.serve_forever, daemon=True)
    upstream_thread.start()
    upstream_url = f'http://127.0.0.1:{upstream.server_port}'
    gateway_socket = reserve_socket()
    gateway_url = f'http://127.0.0.1:{gateway_socket.getsockname()[1]}'

    async def gateway_transport(scope, receive, send):
        is_handoff = (scope['type'] == 'http' and scope['method'] == 'POST'
                      and scope['path'] == '/internal/task-notifications')
        if is_handoff:
            gateway.handoff_attempted.set()
            if not gateway.accepting_notifications:
                await send({'type': 'http.response.start', 'status': 503, 'headers': []})
                await send({'type': 'http.response.body', 'body': b'fixture service unavailable'})
                return
            if gateway.drop_next_handoff_response:
                gateway.drop_next_handoff_response = False

                async def discard_response(message):
                    pass

                # The real handler/transaction runs, then the HTTP response
                # is lost at the transport boundary. No fake queue logic.
                await app(scope, receive, discard_response)
                await send({'type': 'http.response.start', 'status': 502, 'headers': []})
                await send({'type': 'http.response.body', 'body': b'fixture response lost'})
                return
        await app(scope, receive, send)

    gateway_server = uvicorn.Server(uvicorn.Config(gateway_transport, lifespan='off', log_level='critical'))
    gateway_thread = threading.Thread(target=lambda: gateway_server.run(sockets=[gateway_socket]), daemon=True)
    gateway_thread.start()
    core_socket = reserve_socket()
    core_port = core_socket.getsockname()[1]
    core_socket.close()
    core_url = f'http://127.0.0.1:{core_port}'
    # Forward Core authorization checks from the gateway's existing local
    # boundary server to the actual Core, without reproducing authorization.
    gateway.core.forward_url = core_url
    environment = dict(os.environ)
    environment.update({
        'NOTIFICATION_FIXTURE_PROCESS': '1',
        'ACL_DB_DRIVER': driver, 'ACL_DB_DSN': dsn,
        'LAZYMIND_CORE_HOST': '127.0.0.1', 'LAZYMIND_CORE_PORT': str(core_port),
        'LAZYMIND_CORE_SELF_URL': upstream_url,
        'LAZYMIND_CHAT_SERVICE_URL': upstream_url,
        'LAZYMIND_AUTH_SERVICE_URL': upstream_url,
        'LAZYMIND_CHANNEL_GATEWAY_URL': gateway_url,
        'LAZYMIND_AUTH_SERVICE_INTERNAL_TOKEN': 'fixture-internal-token',
        'LAZYMIND_STATE_BACKEND': 'sqlite',
        'LAZYMIND_STATE_SQLITE_DIR': str(tmp_path / 'state'),
        'LAZYMIND_STATE_SQLITE_PATH': str(tmp_path / 'state.db'),
        'LAZYMIND_OPENAPI_ARTIFACT_EXPORT_ENABLED': 'false',
        'LAZYMIND_HISTORY_INJECTION_ENABLED': 'false',
        'LAZYMIND_BACKGROUND_JOBS_ENABLED': 'true',
        'LAZYMIND_SHUTDOWN_TIMEOUT': '3s',
        'LAZYMIND_UPLOAD_ROOT': str(tmp_path / 'uploads'),
        'LAZYMIND_SUBAGENT_WORKSPACE': str(tmp_path / 'subagent'),
    })
    # Do not inherit a user's optional external DB/config into an isolated test.
    for key in ('LAZYMIND_READONLY_DB_DRIVER', 'LAZYMIND_READONLY_DB_DSN',
                'LAZYMIND_LAZYLLM_DB_DRIVER', 'LAZYMIND_LAZYLLM_DB_DSN', 'LAZYMIND_READONLY_VALIDATE'):
        environment.pop(key, None)
    log_path = tmp_path / 'core.log'
    process = None
    client = httpx.Client(base_url=core_url, timeout=10,
                          headers={'X-User-Id': 'owner', 'X-Request-Id': 'notification-e2e'})

    def start_core():
        nonlocal process
        with log_path.open('a') as output:
            process = subprocess.Popen([str(notification_core_binary),
                                        '-test.run=^TestRunNotificationFixtureProcess$', '-test.timeout=120s'],
                                       cwd=CORE_DIR, env=environment, stdout=output, stderr=subprocess.STDOUT)
        wake = threading.Event()
        for _ in range(400):
            if process.poll() is not None:
                pytest.fail('isolated Core startup failed:\n' + log_path.read_text()[-3000:])
            try:
                if client.get('/openapi.json').status_code == 200:
                    break
            except httpx.TransportError:
                pass
            wake.wait(0.05)
        else:
            pytest.fail('isolated Core startup timed out:\n' + log_path.read_text()[-3000:])

    def restart_core():
        process.send_signal(signal.SIGTERM)
        process.wait(timeout=8)
        start_core()

    execution.restart_core = restart_core
    try:
        start_core()
        # A selected, verified owner model; values are explicitly non-secret fixtures.
        execution.sql('INSERT INTO user_model_provider_groups '
                      '(id,user_model_provider_id,name,base_url,api_key,is_verified,create_user_id,'
                      'create_user_name,created_at,updated_at) '
                      'VALUES (%s,%s,%s,%s,%s,TRUE,%s,%s,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)',
                      ('fixture-group', 'fixture-provider', 'fixture', upstream_url,
                       'fixture-non-secret', 'owner', 'owner'))
        execution.sql('INSERT INTO user_model_provider_group_models '
                      '(id,user_model_provider_id,user_model_provider_group_id,provider_name,name,'
                      'model_type,max_input_tokens,create_user_id,create_user_name,created_at,updated_at) '
                      'VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)',
                      ('fixture-model', 'fixture-provider', 'fixture-group', 'openai',
                       'notification-fixture-model', 'llm', '8192', 'owner', 'owner'))
        execution.sql('INSERT INTO user_selected_models '
                      '(user_id,model_type,user_model_provider_group_model_id,created_at,updated_at) '
                      'VALUES (%s,%s,%s,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)',
                      ('owner', 'llm', 'fixture-model'))
        gateway.components.delivery_worker.start()
        yield client, execution, gateway
    finally:
        gateway.components.delivery_worker.stop()
        if process is not None and process.poll() is None:
            process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
        client.close()
        gateway.core.forward_url = ''
        gateway_server.should_exit = True
        gateway_thread.join(timeout=3)
        gateway_socket.close()
        upstream.shutdown()
        upstream.server_close()
        upstream_thread.join(timeout=2)
        if driver == 'postgres':
            with psycopg.connect(base, autocommit=True) as connection:
                connection.execute(psycopg.sql.SQL('DROP DATABASE {} WITH (FORCE)').format(
                    psycopg.sql.Identifier(database_name)))


def data(response):
    assert response.status_code == 200, response.text
    value = response.json()
    return value.get('data', value)


def configured_run(stack, mode='summary'):
    client, execution, gateway = stack
    schedule = data(client.post('/schedules', json={
        'name': '通知端到端日报', 'cron_expr': '0 9 * * *',
        'timezone': 'UTC', 'prompt_template': '生成测试日报',
    }))
    schedule_id = schedule['id']
    view = data(client.get(f'/schedules/{schedule_id}/notification-rule'))
    view['rule'] = {
        'events': {event: {'enabled': True, 'content_mode': mode}
                   for event in ('succeeded', 'failed', 'paused')},
        'channels': [{'provider': 'feishu', 'enabled': True,
                      'targets': [{'account_id': account['id'], 'recipient_id': recipient}
                                  for account, recipient in zip(gateway.accounts, ['group-a', 'group-b'])]}],
    }
    data(client.put(f'/schedules/{schedule_id}/notification-rule', json=view))
    run = data(client.post(f'/schedules/{schedule_id}:run-now', json={}))
    return run['task_id']


def await_history(client, task_id, wanted):
    wake = threading.Event()
    deadline = time.monotonic() + 10
    result = None
    while time.monotonic() < deadline:
        result = data(client.get(f'/task-center/tasks/{task_id}/notifications'))
        if result['items'] and all(item['status'] in wanted for item in result['items']):
            return result
        wake.wait(0.02)
    raise AssertionError(f'notification pipeline did not finish: {result}')


def test_notification_e2e_fixture_starts_real_core(notification_stack):
    client, execution, _ = notification_stack
    assert client.get('/openapi.json').status_code == 200
    assert execution.sql('SELECT COUNT(*) AS n FROM user_selected_models')[0]['n'] == 1


def test_notification_e2e_summary_is_generated_once_for_two_targets(notification_stack):
    client, execution, gateway = notification_stack
    task_id = configured_run(notification_stack)
    await_history(client, task_id, {'sent'})
    assert execution.chat_calls == 1
    assert len(execution.model_requests) == 1
    request = execution.model_requests[0]
    assert request['mode'] == 'llm'
    assert not request.get('tools') and not request.get('skills')
    assert 'notification-fixture-model' in json.dumps(request['llm_config'])
    assert execution.answer in json.dumps(request['input'], ensure_ascii=False).replace('\\n', '\n')
    cards = [kwargs['card'] for kind, kwargs in gateway.sender.calls if kind == 'card']
    assert len(cards) == 2
    assert all(execution.summary in card_text(card) for card in cards)


@pytest.mark.parametrize('model_status', [502, 408])
def test_notification_e2e_summary_failure_and_retry_never_reruns_task(notification_stack, model_status):
    client, execution, gateway = notification_stack
    execution.summary_http_status = model_status
    task_id = configured_run(notification_stack)
    history = await_history(client, task_id, {'partial'})
    assert execution.chat_calls == 1
    assert '摘要暂不可用' in card_text([kwargs.get('card', {}) for _, kwargs in gateway.sender.calls])
    execution.summary_http_status = 200
    for item in history['items']:
        response = client.post('/task-center/notifications/' + item['id'] + ':retry',
                               json={'expected_revision': item['revision']})
        assert response.status_code == 202, response.text
    await_history(client, task_id, {'sent'})
    assert execution.chat_calls == 1
    assert execution.sql('SELECT status FROM task_center_tasks WHERE id=%s', (task_id,))[0]['status'] == 'succeeded'


def test_notification_e2e_full_content_does_not_call_summary_model(notification_stack):
    client, execution, gateway = notification_stack
    task_id = configured_run(notification_stack, mode='full')
    await_history(client, task_id, {'sent'})
    assert execution.model_requests == []
    text = card_text([kwargs.get('card', {}) for _, kwargs in gateway.sender.calls])
    assert '末尾关键结论：成本下降百分之二十。' in text
    assert execution.chat_calls == 1


def test_notification_e2e_long_summary_covers_tail_with_bounded_model_inputs(notification_stack):
    client, execution, _ = notification_stack
    sections = [f'第{i:04d}节的独立事实。' for i in range(5000)]
    execution.answer = '\n\n'.join(sections)
    task_id = configured_run(notification_stack)
    await_history(client, task_id, {'sent'})
    assert len(execution.model_requests) > 1
    inputs = [json.dumps(r['input'], ensure_ascii=False) for r in execution.model_requests]
    # Fixture model context is 8192 tokens. This generous character guard
    # catches forwarding the complete oversized document as one request.
    assert all(len(value) < 8192 * 4 for value in inputs)
    combined = '\n'.join(inputs)
    assert all(section in combined for section in sections)
    assert all(r['mode'] == 'llm' and not r.get('tools') and not r.get('skills')
               for r in execution.model_requests)
    assert execution.chat_calls == 1


def test_notification_e2e_delivery_retry_reuses_summary_after_core_restart(notification_stack):
    from channel_gateway.common.errors import GatewayError
    client, execution, gateway = notification_stack

    def fail_one(kind, kwargs):
        if kwargs.get('chat_id') == 'group-b':
            raise GatewayError(403, 'FEISHU_PERMISSION_DENIED', '测试接收对象不可用')

    gateway.sender.on_send = fail_one
    task_id = configured_run(notification_stack)
    history = await_history(client, task_id, {'partial', 'sent', 'failed'})
    assert len(execution.model_requests) == 1
    before = len([1 for _, kwargs in gateway.sender.calls if kwargs.get('chat_id') == 'group-a'])
    execution.restart_core()
    execution.summary_http_status = 502
    gateway.sender.on_send = None
    for item in history['items']:
        if item['status'] == 'sent':
            continue
        response = client.post('/task-center/notifications/' + item['id'] + ':retry',
                               json={'expected_revision': item['revision']})
        assert response.status_code == 202, response.text
    await_history(client, task_id, {'sent'})
    assert len(execution.model_requests) == 1
    assert execution.chat_calls == 1
    assert len([1 for _, kwargs in gateway.sender.calls if kwargs.get('chat_id') == 'group-a']) == before


def test_notification_e2e_failed_execution_preserves_failure(notification_stack):
    client, execution, _ = notification_stack
    execution.chat_http_status = 500
    task_id = configured_run(notification_stack)
    history = await_history(client, task_id, {'sent'})
    assert all(item['event'] == 'failed' for item in history['items'])
    assert execution.sql('SELECT status FROM task_center_tasks WHERE id=%s', (task_id,))[0]['status'] == 'failed'
    assert execution.chat_calls == 1


def test_notification_e2e_foreign_history_and_retry_are_hidden(notification_stack):
    client, _, _ = notification_stack
    task_id = configured_run(notification_stack)
    history = await_history(client, task_id, {'sent'})
    response = client.get(f'/task-center/tasks/{task_id}/notifications', headers={'X-User-Id': 'intruder'})
    assert response.status_code == 404
    for item in history['items']:
        response = client.post('/task-center/notifications/' + item['id'] + ':retry',
                               json={'expected_revision': item['revision']}, headers={'X-User-Id': 'intruder'})
        assert response.status_code == 404


def test_notification_e2e_pending_handoff_survives_core_restart(notification_stack):
    client, execution, gateway = notification_stack
    gateway.accepting_notifications = False
    task_id = configured_run(notification_stack, mode='full')
    assert gateway.handoff_attempted.wait(3), 'Core never attempted durable handoff'
    task = execution.sql('SELECT status FROM task_center_tasks WHERE id=%s', (task_id,))[0]
    assert task['status'] == 'succeeded', 'gateway outage changed task outcome'
    assert not gateway.sender.calls
    execution.restart_core()
    gateway.accepting_notifications = True
    await_history(client, task_id, {'sent'})
    assert execution.chat_calls == 1
    assert len([1 for kind, _ in gateway.sender.calls if kind == 'card']) == 2


def test_notification_e2e_lost_handoff_response_does_not_duplicate_delivery(notification_stack):
    client, execution, gateway = notification_stack
    gateway.drop_next_handoff_response = True
    task_id = configured_run(notification_stack, mode='full')
    await_history(client, task_id, {'sent'})
    assert execution.chat_calls == 1
    assert len([1 for kind, _ in gateway.sender.calls if kind == 'card']) == 2
    with gateway.components.store._connect() as connection:
        count = connection.execute(
            "SELECT COUNT(*) AS n FROM channel_outbox WHERE purpose='task_notification'").fetchone()['n']
    assert count == 2
