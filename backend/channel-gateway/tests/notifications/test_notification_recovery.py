"""Deterministic worker cycles: DB due times change, business time never sleeps."""
import datetime as dt
from concurrent.futures import ThreadPoolExecutor

from channel_gateway.common.errors import RetryableProviderSideEffectError

from conftest import FIXED_PAST
from test_notification_delivery import await_delivery


class OneCycle:
    def __init__(self):
        self.checked = False

    def is_set(self):
        previous, self.checked = self.checked, True
        return previous

    def wait(self, timeout=None):
        return True


def cycle(gateway):
    worker = gateway.components.delivery_worker
    original = worker._stop
    worker._stop = OneCycle()
    try:
        worker._run('controlled-worker')
    finally:
        worker._stop = original


def make_due(gateway):
    with gateway.components.store._connect() as connection:
        connection.execute("UPDATE channel_outbox SET next_attempt_at=%s WHERE purpose='task_notification'",
                           (FIXED_PAST,))


def test_notification_lost_response_reuses_platform_key(gateway):
    gateway.submit(gateway.payload())

    def lose_response(kind, kwargs):
        raise RetryableProviderSideEffectError('fixture response lost after platform accepted')

    gateway.sender.on_send = lose_response
    cycle(gateway)
    assert len(gateway.sender.calls) == 1
    first_key = gateway.sender.calls[0][1]['idempotency_key']
    assert first_key
    gateway.sender.on_send = None
    make_due(gateway)
    cycle(gateway)
    assert gateway.history()['status'] == 'sent'
    assert [kwargs['idempotency_key'] for _, kwargs in gateway.sender.calls] == [first_key, first_key]


def test_notification_unknown_after_dedupe_window_requires_explicit_retry(gateway):
    gateway.submit(gateway.payload())

    def lose_response(kind, kwargs):
        raise RetryableProviderSideEffectError('fixture response lost')

    gateway.sender.on_send = lose_response
    cycle(gateway)
    store = gateway.components.store
    # The contract persists first_request_at in each part's provider state;
    # simulate a restart after the provider's dedupe window has elapsed.
    make_due(gateway)
    claimed = store.claim_next_outbound('clock-fixture', lease_seconds=120)
    assert claimed is not None
    state = dict(claimed.provider_state.get('0', {}))
    assert state.get('first_request_at'), 'ambiguous request has no durable start time'
    state['first_request_at'] = FIXED_PAST.isoformat()
    assert store.save_outbound_part_state(claimed.outbox_id, 'clock-fixture', 0, state)
    with store._connect() as connection:
        connection.execute('UPDATE channel_outbox SET lease_until=%s WHERE id=%s',
                           (FIXED_PAST, claimed.outbox_id))
    store.initialize()
    cycle(gateway)
    record = gateway.history()
    assert record['status'] == 'unknown'
    assert len(gateway.sender.calls) == 1
    response = gateway.request('POST', '/internal/task-notifications/notification-one:retry',
                               {'expected_revision': record['revision']}, internal=True)
    assert response.status_code == 409
    assert response.json()['error']['code'] == 'NOTIFICATION_DUPLICATE_RISK'
    gateway.sender.on_send = None
    response = gateway.request('POST', '/internal/task-notifications/notification-one:retry',
                               {'expected_revision': record['revision'], 'confirm_duplicate_risk': True},
                               internal=True)
    assert response.status_code == 202
    cycle(gateway)
    assert gateway.history()['status'] == 'sent'


def test_notification_rate_limit_honors_retry_after(gateway):
    gateway.submit(gateway.payload())

    def limited(kind, kwargs):
        raise RetryableProviderSideEffectError('fixture rate limit', retry_after_seconds=90)

    gateway.sender.on_send = limited
    before = dt.datetime.now(dt.timezone.utc)
    cycle(gateway)
    with gateway.components.store._connect() as connection:
        row = connection.execute(
            "SELECT next_attempt_at FROM channel_outbox WHERE purpose='task_notification'").fetchone()
    due = row['next_attempt_at']
    if isinstance(due, str):
        due = dt.datetime.fromisoformat(due.replace('Z', '+00:00'))
    if due.tzinfo is None:
        due = due.replace(tzinfo=dt.timezone.utc)
    assert (due - before).total_seconds() >= 89
    cycle(gateway)
    assert len(gateway.sender.calls) == 1


def test_notification_core_unavailable_defers_without_sending(gateway):
    gateway.submit(gateway.payload())
    gateway.core.http_status = 503
    cycle(gateway)
    assert gateway.sender.calls == []
    assert gateway.history()['status'] in ('pending', 'queued', 'retrying')
    gateway.core.http_status = 200
    make_due(gateway)
    cycle(gateway)
    assert gateway.history()['status'] == 'sent'


def test_notification_concurrent_retry_has_one_winner(gateway):
    gateway.submit(gateway.payload())

    def fail(kind, kwargs):
        from channel_gateway.common.errors import GatewayError
        raise GatewayError(403, 'FEISHU_PERMISSION_DENIED', '权限不足')

    gateway.sender.on_send = fail
    cycle(gateway)
    record = gateway.history()
    assert record['status'] == 'failed'
    with ThreadPoolExecutor(max_workers=2) as pool:
        responses = list(pool.map(lambda _: gateway.request(
            'POST', '/internal/task-notifications/notification-one:retry',
            {'expected_revision': record['revision']}, internal=True), range(2)))
    assert sorted(r.status_code for r in responses) == [202, 409]
    gateway.sender.on_send = None
    cycle(gateway)
    assert gateway.history()['status'] == 'sent'
    assert len(gateway.sender.calls) == 2


def test_notification_permanent_bad_artifact_does_not_block_later_file(gateway):
    payload = gateway.payload()
    payload['content']['artifacts'] = [
        {'artifact_id': 'bad', 'kind': 'file', 'name': 'oversize.pdf',
         'size_bytes': 30 * 1024 * 1024 + 1, 'revision': 1},
        {'artifact_id': 'good', 'kind': 'file', 'name': 'good.txt',
         'size_bytes': 18, 'revision': 1, 'source': '/static-files/good.txt'},
    ]
    gateway.submit(payload)
    record = await_delivery(gateway)
    assert record['status'] == 'partial'
    files = [kwargs['filename'] for kind, kwargs in gateway.sender.calls if kind == 'file']
    assert files == ['good.txt']
    assert any(p['status'] == 'failed' and p['artifact_id'] == 'bad' for p in record['parts'])
