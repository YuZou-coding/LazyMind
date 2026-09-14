"""Feishu task cards and native attachments, with a serialized wire budget."""

import base64
import json

from channel_gateway.common.domain.channel import sanitize_channel_text


CARD_BYTES = 28 * 1024
IMAGE_BYTES = 10 * 1024 * 1024
FILE_BYTES = 30 * 1024 * 1024


def render_notification(payload: dict) -> list[dict]:
    content = payload['content']
    event = payload['event']
    status = {'succeeded': '成功', 'failed': '失败', 'paused': '暂停'}[event]
    title = sanitize_channel_text(content['title'])
    heading = f"{title} · {status}"
    executed = content['executed_at']

    def card(text):
        return {'kind': 'card', 'card': {
            'schema': '2.0',
            'config': {'wide_screen_mode': True},
            'header': {'title': {'tag': 'plain_text', 'content': heading},
                       'template': {'succeeded': 'green', 'failed': 'red', 'paused': 'orange'}[event]},
            'body': {'elements': [
                {'tag': 'markdown', 'content': f'执行时间：{executed}'},
                {'tag': 'markdown', 'content': text},
            ]},
        }}

    def fits(text):
        # Use the noncompact serializer too: callers/SDKs may include spaces.
        return len(json.dumps(card(text)['card'], ensure_ascii=False).encode()) <= CARD_BYTES

    def cards(text):
        parts, pending = [], ''
        for paragraph in text.split('\n\n'):
            candidate = pending + ('\n\n' if pending else '') + paragraph
            if fits(candidate):
                pending = candidate
                continue
            if pending:
                parts.append(card(pending))
                pending = ''
            if fits(paragraph):
                pending = paragraph
            else:
                # Preserve an indivisible paragraph as UTF-8, including code,
                # quotes and Unicode clusters. No string slicing loses content.
                raw = paragraph.encode('utf-8')
                if len(raw) <= FILE_BYTES:
                    parts.append(card('本段内容较长，已转换为文本文件，按正文顺序附上。'))
                    parts.append({'kind': 'file', 'name': f'正文片段-{len(parts)}.txt',
                                  'filename': f'正文片段-{len(parts)}.txt',
                                  'inline_base64': base64.b64encode(raw).decode(),
                                  'size_bytes': len(raw), 'mime_type': 'text/plain'})
                else:
                    # Bounded chunks remain valid UTF-8 and keep their order.
                    remaining = paragraph
                    while remaining:
                        lo, hi = 1, len(remaining)
                        while lo < hi:
                            mid = (lo + hi + 1) // 2
                            if fits(remaining[:mid]):
                                lo = mid
                            else:
                                hi = mid - 1
                        parts.append(card(remaining[:lo]))
                        remaining = remaining[lo:]
        if pending or not parts:
            parts.append(card(pending))
        return parts

    if event != 'succeeded':
        text = sanitize_channel_text(content.get('reason', ''))
        if event == 'paused':
            actions = [sanitize_channel_text(v) for v in content.get('pending_actions', [])]
            text += '\n待处理事项：\n' + '\n'.join(actions)
    elif payload['content_mode'] == 'summary':
        text = (sanitize_channel_text(content.get('summary', ''))
                if content.get('summary_status') == 'ready' else '摘要暂不可用，请回 LazyMind 查看完整结果。')
    else:
        text = sanitize_channel_text(content.get('body', ''))
    parts = cards(text)
    if payload['content_mode'] != 'full' or event != 'succeeded':
        return parts
    for artifact in content.get('artifacts', []):
        kind = artifact.get('kind', 'file')
        size = artifact.get('size_bytes', 0)
        name = sanitize_channel_text(artifact.get('name', '未命名产物'))
        limit = IMAGE_BYTES if kind == 'image' else FILE_BYTES
        if size <= 0 or size > limit or kind not in {'image', 'file'}:
            reason = '文件为空或尚不可用' if size <= 0 else '文件超过平台限制或格式不支持'
            warning = card(f'{name}：{reason}，请回 LazyMind 查看。')
            warning.update(artifact_id=artifact['artifact_id'], permanent_failure=True,
                           error_code='NOTIFICATION_ARTIFACT_UNAVAILABLE')
            parts.append(warning)
            continue
        part = {**artifact, 'kind': kind, 'name': name, 'filename': name}
        part['failure_notice'] = card(f'{name}：产物未能发送，请检查访问权限和平台限制，或回 LazyMind 查看。')
        if artifact.get('inline_text'):
            part['inline_base64'] = base64.b64encode(artifact['inline_text'].encode('utf-8')).decode()
            # Conversion is visible before the attachment and ordered with it.
            parts.append(card(f'{name}：该产物已转换为文本文件，完整内容见附件。'))
            part.pop('inline_text', None)
        parts.append(part)
    return parts
