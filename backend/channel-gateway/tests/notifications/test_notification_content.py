import json

import pytest

from conftest import card_text


@pytest.mark.parametrize('event', ['succeeded', 'failed', 'paused'])
def test_notification_card_has_title_time_and_actual_event(gateway, event):
    payload = gateway.payload(event=event)
    payload['content']['reason'] = '需要确认下一步' if event == 'paused' else '任务安全原因'
    parts = gateway.render(payload)
    cards = [p for p in parts if p.get('kind') == 'card']
    assert cards, 'notification is not rendered as a card'
    text = card_text(cards)
    assert '行业日报' in text
    assert '2026-09-10' in text
    assert {'succeeded': '成功', 'failed': '失败', 'paused': '暂停'}[event] in text
    if event != 'succeeded':
        assert payload['content']['reason'] in text


def test_notification_full_body_is_split_without_losing_any_paragraph(gateway):
    payload = gateway.payload()
    paragraphs = [f'第{i:04d}段：中文正文🙂与末尾信息。' for i in range(2000)]
    payload['content']['body'] = '\n\n'.join(paragraphs)
    cards = [p for p in gateway.render(payload) if p.get('kind') == 'card']
    assert len(cards) > 1
    text = card_text(cards)
    last = -1
    for paragraph in paragraphs:
        position = text.find(paragraph)
        assert position > last, f'missing, duplicated, or reordered {paragraph}'
        assert text.count(paragraph) == 1
        last = position
    for part in cards:
        wire = json.dumps(part['card'], ensure_ascii=False).encode('utf-8')
        assert len(wire) <= 28 * 1024
        assert wire.decode('utf-8')


def test_notification_summary_never_accidentally_sends_full_body(gateway):
    payload = gateway.payload(content_mode='summary')
    payload['content']['body'] = 'FULL_BODY_MUST_NOT_BE_SENT'
    text = card_text(gateway.render(payload))
    assert '已核实的摘要' in text
    assert 'FULL_BODY_MUST_NOT_BE_SENT' not in text


def test_notification_failed_summary_sends_status_and_explicit_notice(gateway):
    payload = gateway.payload(content_mode='summary')
    payload['content'].update(summary='', summary_status='failed',
                              body='FULL_BODY_MUST_NOT_BE_SENT')
    text = card_text(gateway.render(payload))
    assert '成功' in text
    assert '摘要暂不可用' in text
    assert 'FULL_BODY_MUST_NOT_BE_SENT' not in text


def test_notification_renders_images_and_reports_as_native_parts(gateway):
    payload = gateway.payload()
    payload['content']['artifacts'] = [
        {'artifact_id': 'image-1', 'kind': 'image', 'name': 'chart.png',
         'mime_type': 'image/png', 'size_bytes': 120, 'revision': 1},
        {'artifact_id': 'file-1', 'kind': 'file', 'name': 'report.pdf',
         'mime_type': 'application/pdf', 'size_bytes': 300, 'revision': 1},
    ]
    parts = gateway.render(payload)
    assert {'card', 'image', 'file'} <= {p['kind'] for p in parts}
    names = json.dumps(parts, ensure_ascii=False)
    assert 'chart.png' in names and 'report.pdf' in names


@pytest.mark.parametrize('kind,limit,name', [
    ('image', 10 * 1024 * 1024, 'large.png'),
    ('file', 30 * 1024 * 1024, 'large.pdf'),
])
def test_notification_oversized_artifact_does_not_drop_good_content(gateway, kind, limit, name):
    payload = gateway.payload()
    payload['content']['artifacts'] = [
        {'artifact_id': 'oversized', 'kind': kind, 'name': name,
         'size_bytes': limit + 1, 'revision': 1},
        {'artifact_id': 'good', 'kind': 'file', 'name': 'small.txt',
         'size_bytes': 30, 'revision': 1},
    ]
    parts = gateway.render(payload)
    text = card_text(parts)
    assert '最后一项结论' in text
    assert name in text and 'LazyMind' in text
    assert any(p['kind'] == 'file' and 'small.txt' in json.dumps(p) for p in parts)
    assert not any(p.get('artifact_id') == 'oversized'
                   and p.get('kind') in ('image', 'file') for p in parts)


def test_notification_protocol_blocks_are_not_exposed(gateway):
    payload = gateway.payload()
    payload['content']['body'] = (
        '<think>INTERNAL_REASONING</think><tool_result>INTERNAL_RESULT</tool_result>'
        '可以公开的结论'
    )
    text = card_text(gateway.render(payload))
    assert '可以公开的结论' in text
    assert 'INTERNAL_REASONING' not in text and 'INTERNAL_RESULT' not in text


@pytest.mark.parametrize('size', [28 * 1024 - 1, 28 * 1024, 28 * 1024 + 1, 90 * 1024])
def test_notification_single_paragraph_escaping_and_card_budget(gateway, size):
    payload = gateway.payload()
    # Escaping quotes/backslashes increases wire bytes beyond visible text size.
    body = ('引号"反斜杠\\🙂' * size)[:size]
    payload['content']['body'] = body
    parts = gateway.render(payload)
    cards = [p['card'] for p in parts if p['kind'] == 'card']
    assert cards
    for card in cards:
        assert len(json.dumps(card, ensure_ascii=False).encode()) <= 28 * 1024
    # A long indivisible paragraph may split into chunks or become a file.
    text = card_text(parts)
    if not any(p['kind'] == 'file' for p in parts):
        assert text.count('🙂') == body.count('🙂')
        assert text.count('反斜杠') == body.count('反斜杠')


@pytest.mark.parametrize('kind,limit', [('image', 10 * 1024 ** 2), ('file', 30 * 1024 ** 2)])
@pytest.mark.parametrize('delta', [-1, 0])
def test_notification_artifact_within_limit_is_retained(gateway, kind, limit, delta):
    payload = gateway.payload()
    payload['content']['artifacts'] = [
        {'artifact_id': 'boundary', 'kind': kind, 'name': 'boundary.bin',
         'size_bytes': limit + delta, 'source': '/static-files/boundary.bin', 'revision': 1}]
    parts = gateway.render(payload)
    assert any(p['kind'] == kind for p in parts)


def test_notification_empty_file_reports_reason_and_keeps_body(gateway):
    payload = gateway.payload()
    payload['content']['artifacts'] = [
        {'artifact_id': 'empty', 'kind': 'file', 'name': 'empty.txt', 'size_bytes': 0, 'revision': 1}]
    parts = gateway.render(payload)
    assert 'empty.txt' in card_text(parts) and 'LazyMind' in card_text(parts)
    assert '最后一项结论' in card_text(parts)
    assert not any(p['kind'] == 'file' for p in parts)


def test_notification_empty_result_still_has_status_card(gateway):
    payload = gateway.payload()
    payload['content']['body'] = ''
    text = card_text(gateway.render(payload))
    assert '行业日报' in text and '成功' in text and '2026-09-10' in text


def test_notification_pause_includes_pending_action_without_unfinished_answer(gateway):
    payload = gateway.payload(event='paused')
    payload['content'].update(reason='等待用户选择', pending_actions=['确认输出范围'],
                              body='UNFINISHED_OUTPUT_MUST_NOT_BE_SENT')
    text = card_text(gateway.render(payload))
    assert '等待用户选择' in text and '确认输出范围' in text
    assert 'UNFINISHED_OUTPUT_MUST_NOT_BE_SENT' not in text
