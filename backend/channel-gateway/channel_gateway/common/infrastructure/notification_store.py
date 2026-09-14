"""Notification operations on the gateway's existing transactional outbox."""

import datetime as dt
import hashlib
import json
import uuid

from channel_gateway.common.errors import GatewayError


def decoded(value, default):
    if isinstance(value, str):
        return json.loads(value)
    return value if value is not None else default


class NotificationStoreMixin:
    def initialize_notifications(self, connection):
        connection.execute('''
            CREATE TABLE IF NOT EXISTS channel_notifications (
                notification_id TEXT PRIMARY KEY,
                business_key VARCHAR(64) NOT NULL UNIQUE,
                outbox_id TEXT NOT NULL UNIQUE REFERENCES channel_outbox(id),
                payload_hash VARCHAR(64) NOT NULL,
                revision INTEGER NOT NULL DEFAULT 1,
                attempts TEXT NOT NULL DEFAULT '[]'
            )
        ''')

        connection.execute('''CREATE TABLE IF NOT EXISTS channel_notification_reconnects (
            session_id TEXT PRIMARY KEY REFERENCES channel_connection_sessions(id),
            account_id TEXT NOT NULL REFERENCES channel_accounts(id))''')

        connection.execute('''CREATE TABLE IF NOT EXISTS channel_notification_rates (
            rate_key VARCHAR(64) PRIMARY KEY,
            next_allowed_at TIMESTAMPTZ NOT NULL)''')

    @staticmethod
    def _notification_rate_keys(provider, account_id, recipient_id):
        # A group budget is shared by all bots managed by this gateway.
        return sorted(hashlib.sha256(value.encode()).hexdigest() for value in (
            f'{provider}:account:{account_id}', f'{provider}:recipient:{recipient_id}'))

    def reserve_notification_send(self, provider, account_id, recipient_id):
        """Reserve a conservative four-requests/second slot across processes.

        Short waits retain the worker lease; provider cooldowns return to the
        existing queue without consuming a send attempt. Both databases lock
        the same keys in the same order.
        """
        now = dt.datetime.now(dt.timezone.utc)
        keys = self._notification_rate_keys(provider, account_id, recipient_id)
        with self._connect() as connection:
            due = now
            connection.execute('INSERT INTO channel_notification_rates(rate_key,next_allowed_at) '
                               'VALUES(%s,%s),(%s,%s) ON CONFLICT(rate_key) DO NOTHING',
                               (keys[0], now, keys[1], now))
            rows = connection.execute('SELECT next_allowed_at FROM channel_notification_rates '
                                      'WHERE rate_key IN (%s,%s) ORDER BY rate_key FOR UPDATE', keys).fetchall()
            for row in rows:
                value = row['next_allowed_at']
                if isinstance(value, str):
                    value = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
                if value.tzinfo is None:
                    value = value.replace(tzinfo=dt.timezone.utc)
                due = max(due, value)
            now = dt.datetime.now(dt.timezone.utc)
            due = max(due, now)
            delay = (due - now).total_seconds()
            if delay <= 1:
                connection.execute('UPDATE channel_notification_rates SET next_allowed_at=%s '
                                   'WHERE rate_key IN (%s,%s)', (due + dt.timedelta(seconds=0.25), *keys))
            return delay

    def defer_notification_provider(self, provider, account_id, recipient_id, delay):
        due = dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=max(0, delay))
        with self._connect() as connection:
            for key in self._notification_rate_keys(provider, account_id, recipient_id):
                connection.execute('''INSERT INTO channel_notification_rates(rate_key,next_allowed_at)
                    VALUES(%s,%s) ON CONFLICT(rate_key) DO UPDATE SET next_allowed_at=CASE
                    WHEN channel_notification_rates.next_allowed_at < excluded.next_allowed_at
                    THEN excluded.next_allowed_at ELSE channel_notification_rates.next_allowed_at END''', (key, due))

    def reserve_notification_reconnect(self, session_id, account_id):
        with self._connect() as connection:
            connection.execute('INSERT INTO channel_notification_reconnects(session_id,account_id) VALUES(%s,%s)',
                               (session_id, account_id))

    def notification_reconnect_account(self, session_id):
        with self._connect() as connection:
            return connection.execute('SELECT a.* FROM channel_notification_reconnects r '
                                      'JOIN channel_accounts a ON a.id=r.account_id WHERE r.session_id=%s',
                                      (session_id,)).fetchone()

    def insert_notification(self, connection, message):
        payload = message.metadata['task_notification']
        business_key = hashlib.sha256(self._json([
            payload['user_id'], payload['task_id'], payload['event_instance_id'],
            payload['account_id'], payload['recipient_id'],
        ]).encode()).hexdigest()
        digest = hashlib.sha256(json.dumps(payload, sort_keys=True, ensure_ascii=False).encode()).hexdigest()
        # Serialize retries and concurrent duplicate handoffs before either
        # insert; SQLite already owns its database write transaction.
        connection.execute('SELECT pg_advisory_xact_lock(%s)',
                           (int(business_key[:15], 16),))
        existing = connection.execute('''
            SELECT * FROM channel_notifications
            WHERE notification_id=%s OR business_key=%s
        ''', (payload['notification_id'], business_key)).fetchone()
        if existing:
            if existing['payload_hash'] != digest:
                raise GatewayError(409, 'NOTIFICATION_CONFLICT', '该通知已存在，不能替换内容或目标')
            return
        outbox_id = 'co_' + uuid.uuid4().hex
        connection.execute('''
            INSERT INTO channel_outbox(id,account_id,dedupe_key,provider,order_key,
                recipient_id,provider_context,text,intent_kind,purpose,metadata)
            VALUES(%s,%s,%s,%s,%s,%s,%s::jsonb,%s,%s,%s,%s::jsonb)
        ''', (outbox_id, message.account_id, business_key, message.provider,
              message.order_key, message.recipient_id, self._json(message.provider_context),
              message.text, message.intent_kind, message.purpose, self._json(message.metadata)))
        connection.execute('''
            INSERT INTO channel_notifications(notification_id,business_key,outbox_id,payload_hash)
            VALUES(%s,%s,%s,%s)
        ''', (payload['notification_id'], business_key, outbox_id, digest))

    def notification_record(self, notification_id):
        with self._connect() as connection:
            row = self._notification_row(connection, notification_id)
            return self._notification_view(row)

    @staticmethod
    def _notification_row(connection, notification_id, lock=False):
        sql = '''SELECT o.*,n.notification_id,n.revision,n.attempts
                 FROM channel_notifications n JOIN channel_outbox o ON o.id=n.outbox_id
                 WHERE n.notification_id=%s'''
        row = connection.execute(sql + (' FOR UPDATE' if lock else ''), (notification_id,)).fetchone()
        if not row:
            raise GatewayError(404, 'NOTIFICATION_NOT_FOUND', '通知记录不存在')
        return row

    @staticmethod
    def _notification_view(row):
        metadata = decoded(row['metadata'], {})
        payload = metadata['task_notification']
        states = decoded(row['provider_state'], {})
        rendered = decoded(row['rendered_parts'], [])
        parts = []
        for index, part in enumerate(rendered):
            state = states.get(str(index), {})
            parts.append({'part_id': str(index), 'kind': part['kind'],
                          'artifact_id': part.get('artifact_id'),
                          'name': part.get('name', ''), 'status': state.get('status', 'pending'),
                          'error_code': state.get('error_code'),
                          'attempt_count': state.get('attempt_count', 0),
                          'message_id': state.get('message_id'),
                          'sent_at': state.get('sent_at')})
        statuses = {p['status'] for p in parts}
        if row['status'] in {'pending', 'sending', 'retry_wait'}:
            status = 'queued'
        elif 'unknown' in statuses:
            status = 'unknown'
        elif 'skipped' in statuses:
            status = 'partial' if 'sent' in statuses else 'skipped'
        elif 'failed' in statuses or row['status'] == 'dead':
            status = 'partial' if 'sent' in statuses else 'failed'
        elif payload['content_mode'] == 'summary' and payload['content'].get('summary_status') == 'failed':
            status = 'partial'
        else:
            status = 'sent'
        return {'notification_id': row['notification_id'], 'status': status,
                'account_id': row['account_id'], 'recipient_id': row['recipient_id'],
                'revision': row['revision'], 'parts': parts,
                'attempts': decoded(row['attempts'], [])}

    def retry_notification(self, notification_id, expected_revision, confirm_duplicate_risk=False,
                           *, summary=None, supplemental_parts=None):
        with self._connect() as connection:
            row = self._notification_row(connection, notification_id, lock=True)
            record = self._notification_view(row)
            if record['revision'] != expected_revision or record['status'] not in {'failed', 'partial', 'unknown'}:
                raise GatewayError(409, 'NOTIFICATION_CONFLICT', '通知状态已变化或无需重试')
            if record['status'] == 'unknown' and not confirm_duplicate_risk:
                raise GatewayError(409, 'NOTIFICATION_DUPLICATE_RISK', '上次发送结果未知，重试可能重复，请确认')
            if summary is not None:
                metadata = decoded(row['metadata'], {})
                content = metadata['task_notification']['content']
                if content.get('summary_status') != 'failed' or not supplemental_parts:
                    raise GatewayError(409, 'NOTIFICATION_CONFLICT', '已生成的摘要不能替换')
                content.update(summary=summary, summary_status='ready')
                parts = decoded(row['rendered_parts'], []) + supplemental_parts
                connection.execute('UPDATE channel_outbox SET metadata=%s::jsonb,rendered_parts=%s::jsonb WHERE id=%s',
                                   (self._json(metadata), self._json(parts), row['id']))
            states = decoded(row['provider_state'], {})
            for state in states.values():
                if state.get('status') in {'failed', 'unknown'} and not state.get('permanent_failure'):
                    state['status'] = 'pending'
                    if confirm_duplicate_risk:
                        # Explicitly accepted duplicate risk starts a new
                        # dedupe window, with a fresh stable request identity.
                        state.pop('first_request_at', None)
                        state['delivery_generation'] = int(state.get('delivery_generation', 0)) + 1
            attempts = decoded(row['attempts'], [])
            attempts.append({'retry_of': row['revision'], 'requested_at': dt.datetime.now(dt.timezone.utc).isoformat()})
            connection.execute('''UPDATE channel_notifications SET revision=revision+1,attempts=%s
                                  WHERE notification_id=%s''', (self._json(attempts), notification_id))
            connection.execute('''UPDATE channel_outbox SET status='pending',attempt_count=0,
                next_part_index=0,provider_state=%s::jsonb,next_attempt_at=NULL,lease_owner=NULL,
                lease_until=NULL,last_error=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=%s''',
                               (self._json(states), row['id']))
        return self.notification_record(notification_id)

    def finish_notification(self, outbox_id, claim_owner, *, status, delay=None, consume_attempt=True):
        now = dt.datetime.now(dt.timezone.utc)
        due = now + dt.timedelta(seconds=delay) if delay is not None else None
        with self._connect() as connection:
            row = connection.execute('''UPDATE channel_outbox SET status=%s,next_attempt_at=%s,
                lease_owner=NULL,lease_until=NULL,updated_at=CURRENT_TIMESTAMP,
                attempt_count=attempt_count-%s
                WHERE id=%s AND status='sending' AND lease_owner=%s
                AND lease_until>=CURRENT_TIMESTAMP RETURNING id''',
                                     (status, due, 0 if consume_attempt else 1, outbox_id, claim_owner)).fetchone()
            if not row:
                return False
            connection.execute('''UPDATE channel_notifications SET revision=revision+1
                                  WHERE outbox_id=%s''', (outbox_id,))
            return True

    def fail_notification_artifact(self, outbox_id, claim_owner, index, state, notice):
        with self._connect() as connection:
            row = connection.execute('''SELECT provider_state,rendered_parts FROM channel_outbox
                WHERE id=%s AND status='sending' AND lease_owner=%s AND lease_until>=CURRENT_TIMESTAMP
                FOR UPDATE''', (outbox_id, claim_owner)).fetchone()
            if not row:
                return None
            states, parts = decoded(row['provider_state'], {}), decoded(row['rendered_parts'], [])
            states[str(index)] = state
            if not any(part.get('failure_for') == str(index) for part in parts):
                parts.append({**notice, 'failure_for': str(index)})
            connection.execute('''UPDATE channel_outbox SET provider_state=%s::jsonb,rendered_parts=%s::jsonb,
                updated_at=CURRENT_TIMESTAMP WHERE id=%s''', (self._json(states), self._json(parts), outbox_id))
            return parts

    def disconnect_retaining_account(self, owner_user_id, account_id):
        with self._connect() as connection:
            row = connection.execute('''UPDATE channel_accounts SET status='disconnected',
                runtime_status='stopped',updated_at=CURRENT_TIMESTAMP
                WHERE id=%s AND owner_user_id=%s RETURNING id''',
                                     (account_id, owner_user_id)).fetchone()
            return row is not None
