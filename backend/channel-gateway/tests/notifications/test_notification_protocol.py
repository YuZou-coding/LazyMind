import importlib.util
from pathlib import Path

import pytest

from channel_gateway.common.application.workers import _LeaseHeartbeat, LeaseLostError
from channel_gateway.common.errors import RetryableProviderSideEffectError
from channel_gateway.feishu import sdk
from channel_gateway.feishu.domain import FeishuAppCredentials


@pytest.mark.parametrize('kind', ['card', 'image', 'file'])
def test_notification_sdk_timeout_and_stable_provider_uuid(monkeypatch, kind):
    calls, deadlines = [], []

    class TimeoutFuture:
        def result(self, *, timeout):
            deadlines.append(timeout)
            raise TimeoutError('fixture response timeout')

    class Transport:
        def __init__(self, **kwargs):
            pass

        def send(self, chat_id, message, options):
            calls.append(options.uuid)

        def schedule(self, operation):
            return TimeoutFuture()

    monkeypatch.setattr(sdk, '_WorkspaceFeishuChannel', Transport)
    client = sdk.LarkChannelClient(FeishuAppCredentials('fixture', 'fixture', 'owner', 'tenant', 'name'))
    kwargs = {'chat_id': 'group-a', 'idempotency_key': 'stable-fixture-key'}
    if kind == 'card':
        kwargs['card'] = {'elements': []}
    else:
        kwargs['content'] = b'fixture bytes'
        kwargs['caption' if kind == 'image' else 'filename'] = 'fixture'
    for _ in range(2):
        with pytest.raises(RetryableProviderSideEffectError):
            getattr(client, 'send_' + kind)(**kwargs)
    assert deadlines == [60, 60]
    assert len(calls) == 2 and calls[0] and calls[0] == calls[1]


def test_notification_heartbeat_renews_after_thirty_seconds_and_fences_loss():
    waits = []

    class ClockEvent:
        def wait(self, seconds):
            waits.append(seconds)
            return False

    heartbeat = _LeaseHeartbeat(lambda: False, name='fixture-heartbeat')
    heartbeat._stop = ClockEvent()
    heartbeat._run()
    assert waits == [30]
    with pytest.raises(LeaseLostError):
        heartbeat.ensure_owned()


def test_notification_core_routes_are_in_central_permission_extraction():
    backend = Path(__file__).resolve().parents[3]
    spec = importlib.util.spec_from_file_location(
        'permission_extractor', backend / 'scripts' / 'extract_api_permissions.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    entries = []
    for file in (backend / 'core').glob('*.go'):
        if not file.name.endswith('_test.go'):
            entries.extend(module.extract_from_go_file(file))
    routes = {(entry['method'], entry['path']): entry for entry in entries}
    for method, path in [
        ('GET', '/user/notification-settings'), ('PUT', '/user/notification-settings'),
        ('GET', '/user/notification-settings/disable-impact'),
        ('GET', '/schedules/{schedule_id}/notification-rule'),
        ('PUT', '/schedules/{schedule_id}/notification-rule'),
        ('POST', '/schedules/{schedule_id}/notification-rule:reset'),
        ('GET', '/task-center/tasks/{task_id}/notifications'),
        ('POST', '/task-center/notifications/{notification_id}:retry'),
    ]:
        entry = routes.get((method, '/api/core' + path))
        assert entry is not None, f'missing central permission registration: {method} {path}'
        assert entry['permissions']
