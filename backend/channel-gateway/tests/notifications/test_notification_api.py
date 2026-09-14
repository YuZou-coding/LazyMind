import copy
import json
from concurrent.futures import ThreadPoolExecutor

import pytest


PREFIX = '/api/channel-gateway/v1'


def assert_error(response, status, code):
    assert response.status_code == status, response.text
    error = response.json()['error']
    assert error['code'] == code
    assert error['request_id'] == 'notification-contract-request'
    assert isinstance(error['retryable'], bool)
    for value in ('fixture-not-a-real-secret', 'credentials_ciphertext',
                  'fixture-internal-token', 'Traceback', 'SELECT ', '/var/lib/'):
        assert value not in response.text


def test_notification_capabilities_only_feishu_is_sendable(gateway):
    response = gateway.request('GET', PREFIX + '/notification-capabilities')
    assert response.status_code == 200, response.text
    providers = {row['provider']: row for row in response.json()['items']}
    assert providers['feishu']['task_notifications'] is True
    assert providers['feishu']['proactive_send'] is True
    assert {'card', 'image', 'file'} <= set(providers['feishu']['formats'])
    for name in ('wechat', 'wecom'):
        assert name not in providers or not providers[name]['task_notifications']


def test_notification_acceptance_is_durable_and_not_delivery_success(gateway):
    payload = gateway.payload()
    result = gateway.submit(payload)
    assert result['notification_id'] == payload['notification_id']
    assert result['status'] in ('pending', 'queued')
    assert gateway.sender.calls == []
    # Reinitialize the actual persistent store; no in-memory fake repository.
    gateway.components.store.initialize()
    record = gateway.history()
    assert record['status'] in ('pending', 'queued')
    assert record['recipient_id'] == 'group-a'
    with gateway.components.store._connect() as connection:
        row = connection.execute('SELECT COUNT(*) AS n FROM channel_inbox').fetchone()
    assert row['n'] == 0, 'proactive notification fabricated an inbound message'


def test_notification_duplicate_handoff_has_one_business_record(gateway):
    payload = gateway.payload()
    with ThreadPoolExecutor(max_workers=4) as pool:
        responses = list(pool.map(lambda _: gateway.submit(copy.deepcopy(payload)), range(4)))
    assert {r['notification_id'] for r in responses} == {'notification-one'}
    with gateway.components.store._connect() as connection:
        count = connection.execute(
            "SELECT COUNT(*) AS n FROM channel_outbox WHERE purpose='task_notification'"
        ).fetchone()['n']
    assert count == 1


def test_notification_same_identity_with_changed_content_is_rejected(gateway):
    payload = gateway.payload()
    gateway.submit(payload)
    payload['content']['body'] = '替换已经接受的历史内容'
    response = gateway.request('POST', '/internal/task-notifications', payload, internal=True)
    assert_error(response, 409, 'NOTIFICATION_CONFLICT')


@pytest.mark.parametrize('token', ['', 'wrong'])
def test_notification_internal_routes_require_service_identity(gateway, token):
    response = gateway.client.post('/internal/task-notifications',
                                   json=gateway.payload(), headers={
                                       'X-User-Id': 'owner',
                                       'X-LazyMind-Internal-Token': token,
                                       'X-Request-Id': 'notification-contract-request',
                                   })
    assert_error(response, 401, 'UNAUTHORIZED')
    assert not gateway.sender.calls


def test_notification_missing_configured_internal_token_fails_closed(gateway, monkeypatch):
    monkeypatch.delenv('LAZYMIND_AUTH_SERVICE_INTERNAL_TOKEN')
    # Rebuild after changing startup configuration; never accept two empty tokens.
    from channel_gateway import bootstrap
    from channel_gateway.app import app
    components = bootstrap.build_components(gateway.settings)
    monkeypatch.setattr(app.state, 'components', components)
    response = gateway.request('POST', '/internal/task-notifications', gateway.payload())
    assert response.status_code in (401, 503), response.text


@pytest.mark.parametrize('provider', ['wechat', 'wecom', 'arbitrary-webhook'])
def test_notification_unimplemented_provider_rejected(gateway, provider):
    response = gateway.request('POST', '/internal/task-notifications',
                               gateway.payload(provider=provider), internal=True)
    assert_error(response, 400, 'NOTIFICATION_PROVIDER_UNSUPPORTED')


@pytest.mark.parametrize('patch', [
    {'schema_version': 999}, {'event': 'canceled'}, {'content_mode': 'raw'},
    {'notification_id': ''}, {'recipient_id': ''}, {'rule_version': -1},
])
def test_notification_invalid_envelope_has_safe_error(gateway, patch):
    response = gateway.request('POST', '/internal/task-notifications',
                               gateway.payload(**patch), internal=True)
    assert_error(response, 422, 'INVALID_REQUEST')


def test_notification_cannot_use_another_owners_account(gateway):
    response = gateway.request('POST', '/internal/task-notifications',
                               gateway.payload(user_id='intruder'), internal=True)
    assert_error(response, 404, 'ACCOUNT_NOT_FOUND')


def test_notification_recipient_query_is_owned_and_paginated(gateway):
    account_id = gateway.accounts[0]['id']
    path = PREFIX + '/channel-accounts/' + account_id + '/notification-recipients'
    assert_error(gateway.request('GET', path, owner='intruder'), 404, 'ACCOUNT_NOT_FOUND')
    response = gateway.request('GET', path + '?page_size=1')
    assert response.status_code == 200, response.text
    body = response.json()
    assert len(body['items']) <= 1
    assert 'next_page_token' in body
    assert 'fixture-not-a-real-secret' not in response.text


def test_notification_disconnect_retains_account_identity_and_history(gateway):
    account = gateway.accounts[0]
    # Existing disconnect API is part of compatibility, not a new stub.
    response = gateway.request('DELETE', PREFIX + '/channel-accounts/' + account['id'])
    assert response.status_code == 204, response.text
    saved = gateway.components.store.get_account('owner', account['id'])
    assert saved is not None, 'disconnect deleted the stable account'
    assert saved['status'] == 'disconnected'
    assert saved['external_id_hash'] == account['external_id_hash']
    assert gateway.components.store.get_account('owner', gateway.accounts[1]['id']) is not None


def test_notification_accounts_keep_distinct_same_name_identities(gateway):
    response = gateway.request('GET', PREFIX + '/channel-accounts?provider=feishu')
    assert response.status_code == 200, response.text
    items = response.json()['items']
    assert len(items) == 2
    assert len({a['id'] for a in items}) == 2
    assert len({a['label'] for a in items}) == 1
    assert 'credentials_ciphertext' not in response.text


def test_notification_account_references_include_impact(gateway):
    gateway.core.references = [{'schedule_id': 'schedule-one', 'name': '日报'}]
    response = gateway.request('GET', PREFIX + '/channel-accounts/'
                               + gateway.accounts[0]['id'] + '/notification-references')
    assert response.status_code == 200, response.text
    assert response.json()['items'][0]['schedule_id'] == 'schedule-one'


def test_notification_disconnected_account_cannot_be_used(gateway):
    account_id = gateway.accounts[0]['id']
    response = gateway.request('DELETE', PREFIX + '/channel-accounts/' + account_id)
    assert response.status_code == 204, response.text
    response = gateway.request('POST', '/internal/task-notifications',
                               gateway.payload(), internal=True)
    assert_error(response, 409, 'ACCOUNT_UNAVAILABLE')


def test_notification_openapi_has_public_and_internal_contracts(gateway):
    response = gateway.client.get(PREFIX + '/openapi.json')
    assert response.status_code == 200
    paths = response.json()['paths']
    for path, method in [
        (PREFIX + '/notification-capabilities', 'get'),
        (PREFIX + '/channel-accounts/{account_id}/notification-recipients', 'get'),
        (PREFIX + '/channel-accounts/{account_id}/notification-references', 'get'),
        (PREFIX + '/channel-accounts/{account_id}:reconnect', 'post'),
        ('/internal/task-notifications', 'post'),
        ('/internal/task-notifications/{notification_id}', 'get'),
        ('/internal/task-notifications/{notification_id}:retry', 'post'),
    ]:
        assert method in paths.get(path, {}), f'missing {method} {path}'
    # Check serialization is stable JSON, without depending on key order.
    assert isinstance(json.loads(json.dumps(paths)), dict)


@pytest.mark.parametrize('patch', [
    {'recipient_id': 'x' * 4096}, {'task_id': ['wrong-type']},
    {'user_id': ''}, {'account_id': {}}, {'content': None}])
def test_notification_envelope_type_and_size_validation(gateway, patch):
    response = gateway.request('POST', '/internal/task-notifications',
                               gateway.payload(**patch), internal=True)
    assert_error(response, 422, 'INVALID_REQUEST')


def test_notification_internal_target_validation_uses_authenticated_owner(gateway):
    payload = {'user_id': 'owner', 'provider': 'feishu', 'targets': [
        {'account_id': gateway.accounts[0]['id'], 'recipient_id': 'group-a'}]}
    response = gateway.request('POST', '/internal/notification-targets:validate', payload, internal=True)
    assert response.status_code == 200, response.text
    assert response.json()['valid'] is True
    payload['user_id'] = 'intruder'
    response = gateway.request('POST', '/internal/notification-targets:validate', payload, internal=True)
    assert_error(response, 404, 'ACCOUNT_NOT_FOUND')


def test_notification_different_transport_id_cannot_duplicate_business_event(gateway):
    gateway.submit(gateway.payload())
    duplicate = gateway.payload(notification_id='different-transport-id')
    response = gateway.request('POST', '/internal/task-notifications', duplicate, internal=True)
    assert response.status_code in (202, 409), response.text
    with gateway.components.store._connect() as connection:
        count = connection.execute(
            "SELECT COUNT(*) AS n FROM channel_outbox WHERE purpose='task_notification'").fetchone()['n']
    assert count == 1


def test_notification_accounts_expose_avatar_state_and_reference_metadata(gateway):
    response = gateway.request('GET', PREFIX + '/channel-accounts?provider=feishu')
    assert response.status_code == 200
    for account in response.json()['items']:
        assert {'avatar_url', 'connected_at', 'status', 'notification_reference_count'} <= account.keys()


def test_notification_disconnect_impact_is_owned(gateway):
    gateway.core.references = [{'schedule_id': 'schedule-one', 'name': '日报'}]
    path = PREFIX + '/channel-accounts/' + gateway.accounts[0]['id'] + '/disconnect-impact'
    response = gateway.request('GET', path)
    assert response.status_code == 200, response.text
    assert response.json()['items'][0]['schedule_id'] == 'schedule-one'
    assert_error(gateway.request('GET', path, owner='intruder'), 404, 'ACCOUNT_NOT_FOUND')
