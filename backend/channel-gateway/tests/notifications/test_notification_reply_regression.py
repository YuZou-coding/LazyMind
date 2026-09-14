from channel_gateway.common.domain.channel import ClaimedOutbound
from channel_gateway.common.infrastructure.security import JsonCipher

from conftest import card_text


def reply(account_id, provider):
    return ClaimedOutbound(
        outbox_id='reply-fixture', created_sequence=1, provider=provider,
        account_id=account_id, order_key='existing-conversation', recipient_id='group-a',
        provider_context={'chat_id': 'group-a', 'context_token': 'fixture-context'},
        text='原有会话回复仍然可用', intent_kind='reply', purpose='reply', metadata={},
        rendered_parts=[], next_part_index=0, provider_state={}, attempt_count=1)


def test_notification_change_preserves_feishu_conversation_reply(gateway):
    provider = gateway.components.delivery_worker._providers.delivery('feishu')
    outbound = reply(gateway.accounts[0]['id'], 'feishu')
    parts = provider.render(outbound)
    assert '原有会话回复仍然可用' in card_text(parts)
    for index, part in enumerate(parts):
        provider.send_part(outbound, part, part_index=index,
                           idempotency_key=f'fixture-reply-{index}', saved_state={})
    assert gateway.sender.calls


def test_notification_change_preserves_wechat_reply_context(gateway, monkeypatch):
    cipher = JsonCipher(gateway.settings.credential_key_path)
    account = gateway.components.store.connect_referenced_account(
        owner_user_id='owner', provider='wechat', external_id_hash='fixture-wechat',
        label='微信', status='connected', credentials_ciphertext=cipher.encrypt('owner', {
            'token': 'fixture-token', 'account_id': 'fixture-wechat',
            'authorized_user_id': 'fixture-user', 'base_url': 'https://fixture.invalid',
        }))
    provider = gateway.components.delivery_worker._providers.delivery('wechat')
    calls = []
    monkeypatch.setattr(provider._client, 'send_text', lambda **kwargs: calls.append(kwargs))
    outbound = reply(account['id'], 'wechat')
    for index, part in enumerate(provider.render(outbound)):
        provider.send_part(outbound, part, part_index=index,
                           idempotency_key=f'fixture-wechat-{index}', saved_state={})
    assert calls
    assert all(call['context_token'] == 'fixture-context' for call in calls)
    assert '原有会话回复仍然可用' in ''.join(call['text'] for call in calls)
