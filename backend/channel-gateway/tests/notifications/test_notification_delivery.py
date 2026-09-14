import copy
import threading
import time
from concurrent.futures import ThreadPoolExecutor

from channel_gateway.common.domain.channel import OutboundMessage
from channel_gateway.common.errors import GatewayError

from conftest import FIXED_PAST


def seed_outbound(gateway):
    payload = gateway.payload()
    message = OutboundMessage(
        provider='feishu', account_id=payload['account_id'],
        order_key='notification-order', recipient_id='group-a',
        provider_context={'chat_id': 'group-a'}, text='队列恢复测试',
        intent_kind='task_notification', purpose='task_notification',
        metadata={'task_notification': payload},
    )
    store = gateway.components.store
    with store._connect() as connection:
        store._insert_outbound(connection, None, [message])
    return store


def test_notification_existing_outbox_allows_only_one_concurrent_claim(gateway):
    store = seed_outbound(gateway)
    start = threading.Barrier(2)

    def claim(owner):
        start.wait(timeout=3)
        return store.claim_next_outbound(owner, lease_seconds=120)

    with ThreadPoolExecutor(max_workers=2) as pool:
        claims = list(pool.map(claim, ['worker-a', 'worker-b']))
    assert sum(item is not None for item in claims) == 1


def test_notification_expired_lease_fences_previous_worker(gateway):
    store = seed_outbound(gateway)
    first = store.claim_next_outbound('worker-a', lease_seconds=120)
    assert first is not None
    with store._connect() as connection:
        connection.execute('UPDATE channel_outbox SET lease_until=%s WHERE id=%s',
                           (FIXED_PAST, first.outbox_id))
    second = store.claim_next_outbound('worker-b', lease_seconds=120)
    assert second is not None and second.outbox_id == first.outbox_id
    assert not store.renew_outbound_lease(first.outbox_id, 'worker-a', lease_seconds=120)
    assert not store.complete_outbound(first.outbox_id, 'worker-a')
    assert store.renew_outbound_lease(second.outbox_id, 'worker-b', lease_seconds=120)


def test_notification_existing_queue_stops_after_five_attempts(gateway):
    store = seed_outbound(gateway)
    for attempt in range(1, 6):
        outbound = store.claim_next_outbound('worker', lease_seconds=120)
        assert outbound is not None and outbound.attempt_count == attempt
        store.record_outbound_failure(outbound.outbox_id, 'worker',
                                      error='FIXTURE_RETRYABLE', max_attempts=5)
        # Advance the persisted due time instead of sleeping for backoff.
        with store._connect() as connection:
            connection.execute('UPDATE channel_outbox SET next_attempt_at=%s WHERE id=%s',
                               (FIXED_PAST, outbound.outbox_id))
    assert store.claim_next_outbound('worker', lease_seconds=120) is None
    with store._connect() as connection:
        row = connection.execute('SELECT status,attempt_count FROM channel_outbox').fetchone()
    assert row['status'] == 'dead' and row['attempt_count'] == 5


def test_notification_part_receipt_survives_store_reinitialization(gateway):
    store = seed_outbound(gateway)
    first = store.claim_next_outbound('worker-a', lease_seconds=120)
    assert first is not None
    receipt = {'message_id': 'om_already_sent', 'idempotency_key': 'stable-part-key'}
    assert store.save_outbound_part_state(first.outbox_id, 'worker-a', 0, receipt)
    with store._connect() as connection:
        connection.execute('UPDATE channel_outbox SET lease_until=%s WHERE id=%s',
                           (FIXED_PAST, first.outbox_id))
    store.initialize()
    recovered = store.claim_next_outbound('worker-b', lease_seconds=120)
    assert recovered is not None
    assert recovered.provider_state['0'] == receipt


def test_notification_disconnect_cannot_cascade_delete_sent_history(gateway):
    store = seed_outbound(gateway)
    claimed = store.claim_next_outbound('worker', lease_seconds=120)
    assert store.complete_outbound(claimed.outbox_id, 'worker')
    response = gateway.request('DELETE', '/api/channel-gateway/v1/channel-accounts/'
                               + gateway.accounts[0]['id'])
    assert response.status_code == 204
    with store._connect() as connection:
        row = connection.execute('SELECT COUNT(*) AS n FROM channel_outbox WHERE id=%s',
                                 (claimed.outbox_id,)).fetchone()
    assert row['n'] == 1, 'account deletion cascaded away notification history'


def await_delivery(gateway, notification_id='notification-one', terminal=('sent', 'partial', 'failed', 'skipped')):
    # This is bounded asynchronous I/O synchronization, not a business-time
    # assertion. Lease/backoff/expiry cases manipulate persisted times above.
    gateway.components.delivery_worker.start()
    deadline = time.monotonic() + 3
    wake = threading.Event()
    last = None
    while time.monotonic() < deadline:
        last = gateway.history(notification_id)
        if last['status'] in terminal:
            return last
        wake.wait(0.01)
    raise AssertionError(f'notification did not settle: {last}')


def test_notification_disable_before_send_skips_queue_and_never_replays(gateway):
    gateway.submit(gateway.payload())
    gateway.core.allowed = False
    gateway.core.generation = 2
    record = await_delivery(gateway)
    assert record['status'] == 'skipped'
    assert gateway.sender.calls == []
    gateway.core.allowed = True
    gateway.core.generation = 3
    assert gateway.history()['status'] == 'skipped'
    assert gateway.sender.calls == []


def test_notification_permission_is_checked_before_each_part(gateway):
    payload = gateway.payload()
    payload['content']['body'] = '\n\n'.join(f'分片{i}。' * 500 for i in range(30))
    gateway.submit(payload)

    def disable_after_first(kind, kwargs):
        gateway.core.allowed = False
        gateway.core.generation = 2

    gateway.sender.on_send = disable_after_first
    record = await_delivery(gateway)
    assert record['status'] in ('partial', 'skipped')
    assert len(gateway.sender.calls) == 1
    assert len([c for c in gateway.core.calls if c[0].endswith(':authorize')]) >= 2


def test_notification_failed_target_does_not_block_another_target(gateway):
    gateway.submit(gateway.payload())
    gateway.submit(gateway.payload(recipient='group-b', account=1,
                                   notification_id='notification-two'))

    def deny_first(kind, kwargs):
        if kwargs.get('chat_id') == 'group-a':
            raise GatewayError(403, 'FEISHU_PERMISSION_DENIED', '无发送权限')

    gateway.sender.on_send = deny_first
    second = await_delivery(gateway, 'notification-two')
    assert second['status'] == 'sent'
    assert gateway.history()['status'] == 'failed'


def test_notification_retry_sends_only_failed_parts_and_preserves_content(gateway):
    payload = gateway.payload()
    payload['content']['artifacts'] = [
        {'artifact_id': 'report', 'kind': 'file', 'name': 'report.pdf',
         'size_bytes': 18, 'revision': 1, 'source': '/static-files/report.pdf'},
    ]
    gateway.submit(payload)

    def deny_file(kind, kwargs):
        if kind == 'file':
            raise GatewayError(403, 'FEISHU_PERMISSION_DENIED', '文件发送暂不可用')

    gateway.sender.on_send = deny_file
    record = await_delivery(gateway)
    assert record['status'] == 'partial'
    sent_cards = copy.deepcopy([c for c in gateway.sender.calls if c[0] == 'card'])
    gateway.sender.on_send = None
    response = gateway.request('POST', '/internal/task-notifications/notification-one:retry',
                               {'expected_revision': record['revision']}, internal=True)
    assert response.status_code == 202, response.text
    retried = await_delivery(gateway)
    assert retried['status'] == 'sent'
    assert [c for c in gateway.sender.calls if c[0] == 'card'] == sent_cards
    assert retried['attempts'][-1]['retry_of']


def test_notification_sent_record_cannot_be_retried(gateway):
    gateway.submit(gateway.payload())
    record = await_delivery(gateway)
    assert record['status'] == 'sent'
    before = len(gateway.sender.calls)
    response = gateway.request('POST', '/internal/task-notifications/notification-one:retry',
                               {'expected_revision': record['revision']}, internal=True)
    assert response.status_code == 409, response.text
    assert len(gateway.sender.calls) == before
