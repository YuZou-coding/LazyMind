import datetime as dt
import threading
import time

import pytest

from channel_gateway.common.errors import GatewayError
from channel_gateway.feishu.domain import FeishuAppCredentials, FeishuAppRegistration
from channel_gateway.feishu.registration import _ADDONS

from test_notification_api import PREFIX, assert_error


def connection_service(gateway):
    return gateway.components.connections._providers.connection('feishu')


def session_row(gateway, *, expires_at=None):
    return gateway.components.store.reserve_session(
        session_id='session-fixture', owner_user_id='owner', provider='feishu',
        idempotency_key=None,
        expires_at=expires_at or dt.datetime.now(dt.timezone.utc) + dt.timedelta(minutes=5),
    )[0]


def wait_session(gateway, session_id, wanted):
    deadline = time.monotonic() + 3
    wake = threading.Event()
    while time.monotonic() < deadline:
        row = connection_service(gateway).get_session('owner', session_id)
        if row['status'] in wanted:
            return row
        wake.wait(0.01)
    raise AssertionError(f'connection did not reach {wanted}: {row}')


def test_notification_connection_expiry_refresh_and_cancel(gateway):
    row = session_row(gateway, expires_at=dt.datetime(2000, 1, 1, tzinfo=dt.timezone.utc))
    service = connection_service(gateway)
    service._reconcile_sessions()
    expired = service.get_session('owner', row['id'])
    assert expired['status'] == 'expired' and expired['allowed_actions'] == ['refresh']
    refreshed = service.refresh_session('owner', row['id'])
    assert refreshed['revision'] > expired['revision']
    assert gateway.registrar.started.wait(2)
    active = service.get_session('owner', row['id'])
    assert active['qr']['version'] > row['qr_version']
    service.cancel_session('owner', row['id'])
    assert service.get_session('owner', row['id'])['status'] == 'canceled'


@pytest.mark.parametrize('error', [
    OSError('fixture network failure'), PermissionError('fixture authorization refused')])
def test_notification_connection_failure_is_safe_and_keeps_existing_accounts(gateway, error):
    gateway.registrar.outcome = error
    service = connection_service(gateway)
    session = service.create_session(owner_user_id='owner', idempotency_key=None)
    gateway.registrar.release.set()
    failed = wait_session(gateway, session['id'], {'failed'})
    assert failed['error']['code']
    assert 'fixture' not in failed['error']['message']
    assert all(gateway.components.store.get_account('owner', a['id']) for a in gateway.accounts)


def test_notification_reconnect_same_identity_preserves_id(gateway):
    account = gateway.accounts[0]
    service = gateway.components.accounts._providers.accounts('feishu')
    service.disconnect_account('owner', account['id'])
    lease = gateway.components.store.acquire_runtime_lease('reconnect-fixture')
    assert lease is not None
    try:
        reconnected = service.connect_registered_account(
            owner_user_id='owner', runtime_fence=lease.fence, notify_runtime=False,
            credentials=FeishuAppCredentials('fixture-app-0', 'fixture-reconnected-secret',
                                             'fixture-person-0', 'tenant', '新的显示名'))
    finally:
        lease.close()
    assert reconnected['id'] == account['id']
    assert len(service.list_accounts('owner')['items']) == 2


def test_notification_reconnect_cannot_replace_identity(gateway):
    account = gateway.accounts[0]
    gateway.request('DELETE', PREFIX + '/channel-accounts/' + account['id'])
    gateway.registrar.outcome = FeishuAppRegistration(
        'different-app', 'fixture-secret', 'different-owner', '同名飞书账号', 'tenant')
    response = gateway.request('POST', PREFIX + '/channel-accounts/' + account['id'] + ':reconnect', {})
    assert response.status_code in (200, 202), response.text
    gateway.registrar.release.set()
    failed = wait_session(gateway, response.json()['id'], {'failed'})
    assert failed['error']['code'] == 'ACCOUNT_IDENTITY_MISMATCH'
    saved = gateway.components.store.get_account('owner', account['id'])
    assert saved['external_id_hash'] == account['external_id_hash']
    assert saved['status'] == 'disconnected'


def test_notification_orphan_cleanup_still_removes_unregistered_account(gateway):
    service = gateway.components.accounts._providers.accounts('feishu')
    account = gateway.components.store.connect_referenced_account(
        owner_user_id='owner', provider='feishu', external_id_hash='fixture-orphan',
        label='fixture orphan', status='provisioning', credentials_ciphertext='')
    assert service.discard_provisioned_account(owner_user_id='owner', account_id=account['id'])
    assert gateway.components.store.get_account('owner', account['id']) is None
    assert gateway.components.store.get_account('owner', gateway.accounts[0]['id']) is not None


@pytest.mark.parametrize('code', ['FEISHU_SCOPE_REQUIRED', 'FEISHU_CHAT_UNAVAILABLE', 'FEISHU_PERMISSION_DENIED'])
def test_notification_invalid_or_unavailable_recipient_is_not_saved(gateway, monkeypatch, code):
    def unavailable(**kwargs):
        raise GatewayError(422, code, '接收对象需要重新授权或已不可用')
    monkeypatch.setattr(gateway.sender, 'get_chat', unavailable)
    response = gateway.request('POST', '/internal/task-notifications', gateway.payload(), internal=True)
    assert_error(response, 422, code)
    assert gateway.sender.calls == []


def test_notification_unverified_person_cannot_receive(gateway):
    response = gateway.request('POST', '/internal/task-notifications',
                               gateway.payload(recipient='unverified-person'), internal=True)
    assert_error(response, 422, 'NOTIFICATION_RECIPIENT_UNVERIFIED')


def test_notification_registration_requests_group_read_scope():
    scopes = set(_ADDONS['scopes']['tenant'])
    assert {'im:chat:read', 'im:chat:readonly', 'im:chat'} & scopes


def test_notification_public_routes_declare_central_permissions(gateway):
    from channel_gateway.app import app
    paths = {PREFIX + '/notification-capabilities',
             PREFIX + '/channel-accounts/{account_id}/notification-recipients',
             PREFIX + '/channel-accounts/{account_id}/notification-references',
             PREFIX + '/channel-accounts/{account_id}/disconnect-impact',
             PREFIX + '/channel-accounts/{account_id}:reconnect'}
    routes = {route.path: route for route in app.routes if route.path in paths}
    assert set(routes) == paths
    for route in routes.values():
        assert getattr(route.endpoint, '__required_permissions__', set())
