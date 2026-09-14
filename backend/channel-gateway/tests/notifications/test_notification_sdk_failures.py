"""Review batch: exercise explicit SDK rejections through the real worker.

Existing permission tests raise GatewayError at the fake sender. This batch
keeps the real SDK result-to-error translation, which is a different boundary.
No production error-classification change is included with these tests.
"""
import json
from types import SimpleNamespace

import pytest

from channel_gateway.feishu import sdk
from channel_gateway.feishu.domain import FeishuAppCredentials
from conftest import card_text
from test_notification_recovery import cycle


@pytest.fixture
def rejecting_sdk(monkeypatch):
    class RejectedFuture:
        def result(self, *, timeout):
            assert timeout == 60
            return SimpleNamespace(
                success=False, message_id='',
                error=SimpleNamespace(retryable=False, code='invalid_argument',
                                      message='fixture provider rejected the message'))

    class Transport:
        def __init__(self, **kwargs):
            pass

        def send(self, chat_id, message, options):
            return None

        def schedule(self, operation):
            return RejectedFuture()

    monkeypatch.setattr(sdk, '_WorkspaceFeishuChannel', Transport)
    return sdk.LarkChannelClient(FeishuAppCredentials(
        'fixture-app', 'fixture-secret', 'fixture-owner', 'fixture-tenant', 'fixture'))


def test_notification_explicit_sdk_rejection_does_not_block_later_artifact(gateway, rejecting_sdk):
    payload = gateway.payload()
    payload['content']['artifacts'] = [
        {'artifact_id': 'bad', 'kind': 'file', 'name': 'bad.txt', 'size_bytes': 18,
         'revision': 1, 'source': '/static-files/bad.txt'},
        {'artifact_id': 'good', 'kind': 'file', 'name': 'good.txt', 'size_bytes': 18,
         'revision': 1, 'source': '/static-files/good.txt'},
    ]
    gateway.submit(payload)

    def reject_bad_file(kind, kwargs):
        if kind == 'file' and kwargs['filename'] == 'bad.txt':
            rejecting_sdk.send_file(**kwargs)

    gateway.sender.on_send = reject_bad_file
    cycle(gateway)
    record = gateway.history()
    assert record['status'] == 'partial', 'a definite SDK rejection must not back off the remaining files'
    files = {part['artifact_id']: part for part in record['parts'] if part['artifact_id']}
    assert files['bad']['status'] == 'failed'
    assert files['good']['status'] == 'sent'
    assert [kwargs['filename'] for kind, kwargs in gateway.sender.calls if kind == 'file'] == ['bad.txt', 'good.txt']
    notices = '\n'.join(card_text(kwargs['card']) for kind, kwargs in gateway.sender.calls if kind == 'card')
    assert 'bad.txt' in notices and 'LazyMind' in notices


def test_notification_explicit_sdk_rejection_has_no_ambiguous_send_window(gateway, rejecting_sdk):
    gateway.submit(gateway.payload())

    def reject_card(kind, kwargs):
        if kind == 'card':
            rejecting_sdk.send_card(**kwargs)

    gateway.sender.on_send = reject_card
    cycle(gateway)
    record = gateway.history()
    assert record['status'] == 'failed', 'a definite rejection is terminal until manual retry'
    with gateway.components.store._connect() as connection:
        row = connection.execute(
            "SELECT provider_state FROM channel_outbox WHERE purpose='task_notification'").fetchone()
    states = json.loads(row['provider_state']) if isinstance(row['provider_state'], str) else row['provider_state']
    assert not states['0'].get('first_request_at'), 'explicit rejection was confused with an accepted/unknown send'
    assert record['parts'][0]['error_code']
    assert 'fixture provider rejected' not in json.dumps(record)
