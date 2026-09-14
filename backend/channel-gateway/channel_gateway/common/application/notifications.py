"""Task notifications through existing provider adapters and outbox workers."""

import hmac
import os
import datetime as dt
from typing import Annotated, Literal
from urllib.parse import quote

import httpx
from pydantic import BaseModel, ConfigDict, Field, StrictInt, StrictStr, field_validator

from channel_gateway.common.domain.channel import OutboundMessage
from channel_gateway.common.errors import GatewayError


Identifier = Annotated[StrictStr, Field(min_length=1, max_length=256)]


class NotificationArtifact(BaseModel):
    model_config = ConfigDict(extra='forbid')
    artifact_id: Identifier
    kind: Literal['image', 'file']
    name: Annotated[StrictStr, Field(min_length=1, max_length=512)]
    mime_type: Annotated[StrictStr, Field(max_length=128)] = ''
    size_bytes: Annotated[StrictInt, Field(ge=0)]
    revision: Annotated[StrictInt, Field(ge=1)]
    source: Annotated[StrictStr, Field(max_length=4096)] = ''
    inline_text: Annotated[StrictStr, Field(max_length=8 * 1024 * 1024)] = ''


class NotificationContent(BaseModel):
    model_config = ConfigDict(extra='forbid')
    title: Annotated[StrictStr, Field(max_length=512)]
    executed_at: Annotated[StrictStr, Field(min_length=1, max_length=64)]
    body: Annotated[StrictStr, Field(max_length=8 * 1024 * 1024)] = ''
    summary: Annotated[StrictStr, Field(max_length=128 * 1024)] = ''
    summary_status: Literal['ready', 'pending', 'failed']
    reason: Annotated[StrictStr, Field(max_length=4096)] = ''
    pending_actions: Annotated[list[Annotated[StrictStr, Field(max_length=4096)]], Field(max_length=100)] = []
    artifacts: Annotated[list[NotificationArtifact], Field(max_length=100)] = []

    @field_validator('executed_at')
    @classmethod
    def timestamp_has_zone(cls, value):
        parsed = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
        if parsed.tzinfo is None:
            raise ValueError('A timezone is required')
        return value


class NotificationEnvelope(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    schema_version: Literal[1]
    notification_id: Identifier
    user_id: Identifier
    task_id: Identifier
    event: Literal['succeeded', 'failed', 'paused']
    event_instance_id: Identifier
    rule_version: Annotated[StrictInt, Field(ge=1)]
    settings_generation: Annotated[StrictInt, Field(ge=1)]
    provider: Annotated[StrictStr, Field(min_length=1, max_length=32)]
    account_id: Identifier
    recipient_id: Identifier
    content_mode: Literal['summary', 'full']
    content: NotificationContent


class NotificationRetry(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    expected_revision: Annotated[StrictInt, Field(ge=1)]
    confirm_duplicate_risk: bool = False
    summary: Annotated[StrictStr, Field(min_length=1, max_length=128 * 1024)] | None = None


class NotificationTarget(BaseModel):
    model_config = ConfigDict(extra='forbid')
    account_id: Identifier
    recipient_id: Identifier


class NotificationTargetValidation(BaseModel):
    model_config = ConfigDict(extra='forbid')
    user_id: Identifier
    provider: Identifier
    targets: Annotated[list[NotificationTarget], Field(min_length=1, max_length=100)]


class NotificationLimitsView(BaseModel):
    card_bytes: int
    image_bytes: int
    file_bytes: int


class NotificationCapabilityView(BaseModel):
    provider: str
    task_notifications: bool
    proactive_send: bool
    formats: list[Literal['card', 'image', 'file']]
    recipient_types: list[Literal['group', 'verified_person']]
    limits: NotificationLimitsView


class NotificationCapabilitiesView(BaseModel):
    items: list[NotificationCapabilityView]


class NotificationRecipientView(BaseModel):
    recipient_id: str
    name: str
    type: Literal['group', 'verified_person']


class NotificationRecipientsView(BaseModel):
    items: list[NotificationRecipientView]
    next_page_token: str
    verified_person: NotificationRecipientView | None = None


class NotificationReferenceView(BaseModel):
    schedule_id: str
    name: str
    enabled: bool | None = None


class NotificationReferencesView(BaseModel):
    items: list[NotificationReferenceView]


class NotificationPartView(BaseModel):
    part_id: str
    kind: Literal['card', 'image', 'file']
    artifact_id: str | None = None
    name: str = ''
    status: Literal['pending', 'sending', 'sent', 'failed', 'unknown', 'skipped']
    error_code: str | None = None
    attempt_count: int = 0
    message_id: str | None = None
    sent_at: str | None = None


class NotificationAttemptView(BaseModel):
    retry_of: int
    requested_at: str


class NotificationRecordView(BaseModel):
    notification_id: str
    status: Literal['queued', 'sent', 'partial', 'failed', 'unknown', 'skipped']
    account_id: str
    recipient_id: str
    revision: int
    parts: list[NotificationPartView]
    attempts: list[NotificationAttemptView]


class NotificationValidatedTarget(NotificationTarget):
    valid: bool


class NotificationValidationView(BaseModel):
    valid: bool
    provider: str
    targets: list[NotificationValidatedTarget]


class NotificationErrorDetail(BaseModel):
    code: str
    message: str
    retryable: bool
    request_id: str


class NotificationErrorView(BaseModel):
    error: NotificationErrorDetail


NOTIFICATION_ERROR_RESPONSES = {
    status: {'model': NotificationErrorView, 'description': 'Safe notification business error'}
    for status in (400, 401, 403, 404, 409, 422, 500, 503)
}


class NotificationService:
    def __init__(self, *, store, providers, core_base_url):
        self.store = store
        self.providers = providers
        self.core_base_url = core_base_url.rstrip('/')
        self.internal_token = (os.getenv('LAZYMIND_AUTH_SERVICE_INTERNAL_TOKEN') or '').strip()

    def authenticate(self, token):
        if not self.internal_token or not hmac.compare_digest(self.internal_token, token or ''):
            raise GatewayError(401, 'UNAUTHORIZED', '内部认证失败')

    def core_request(self, method, path, body=None, owner=''):
        if not self.internal_token:
            raise GatewayError(503, 'NOTIFICATION_CORE_UNAVAILABLE', '暂时无法确认通知权限', True)
        try:
            response = httpx.request(method, self.core_base_url + path, json=body,
                                     headers={'X-LazyMind-Internal-Token': self.internal_token,
                                              'X-User-Id': owner}, timeout=10, follow_redirects=False)
            response.raise_for_status()
            result = response.json()
            return result.get('data', result)
        except (httpx.HTTPError, ValueError) as exc:
            raise GatewayError(503, 'NOTIFICATION_CORE_UNAVAILABLE', '暂时无法确认通知权限', True) from exc

    def account(self, owner, account_id):
        account = self.store.get_account(owner, account_id)
        if not account:
            raise GatewayError(404, 'ACCOUNT_NOT_FOUND', '账号不存在')
        return account

    def capabilities(self):
        return {'items': self.providers.notification_capabilities()}

    def validate_target(self, owner, provider, account_id, recipient_id):
        adapter = self.providers.notifications(provider)
        if adapter is None:
            raise GatewayError(400, 'NOTIFICATION_PROVIDER_UNSUPPORTED', '该渠道暂不支持任务通知')
        account = self.account(owner, account_id)
        if account['provider'] != provider:
            raise GatewayError(404, 'ACCOUNT_NOT_FOUND', '账号不存在')
        if account['status'] != 'connected':
            raise GatewayError(409, 'ACCOUNT_UNAVAILABLE', '账号已断开，请重连')
        adapter.validate_notification_target(account_id, recipient_id)
        return {'account_id': account_id, 'recipient_id': recipient_id, 'valid': True}

    def recipients(self, owner, account_id, page_size, page_token):
        account = self.account(owner, account_id)
        if account['status'] != 'connected':
            raise GatewayError(409, 'ACCOUNT_UNAVAILABLE', '账号已断开，请重连')
        adapter = self.providers.notifications(account['provider'])
        if adapter is None:
            raise GatewayError(400, 'NOTIFICATION_PROVIDER_UNSUPPORTED', '该渠道暂不支持任务通知')
        return adapter.notification_recipients(account_id, page_size, page_token)

    def references(self, owner, account_id):
        self.account(owner, account_id)
        return self.core_request('GET', '/internal/channel-accounts/' + quote(account_id, safe='')
                                 + '/notification-references', owner=owner)

    def submit(self, envelope):
        payload = envelope.model_dump(exclude_unset=True)
        self.validate_target(payload['user_id'], payload['provider'], payload['account_id'], payload['recipient_id'])
        message = OutboundMessage(provider=payload['provider'], account_id=payload['account_id'],
                                  recipient_id=payload['recipient_id'],
                                  order_key='notification:' + payload['recipient_id'][:100],
                                  provider_context={'chat_id': payload['recipient_id']},
                                  text=payload['content'].get('body', ''), purpose='task_notification',
                                  intent_kind='task_notification', metadata={'task_notification': payload})
        with self.store._connect() as connection:
            self.store._insert_outbound(connection, None, [message])
        return self.store.notification_record(payload['notification_id'])

    def authorize(self, payload):
        result = self.core_request('POST', '/internal/task-notifications/'
                                   + quote(payload['notification_id'], safe='') + ':authorize',
                                   {'task_id': payload['task_id']}, owner=payload['user_id'])
        if not isinstance(result.get('allowed'), bool) or not isinstance(result.get('generation'), int):
            raise GatewayError(503, 'NOTIFICATION_CORE_UNAVAILABLE', '暂时无法确认通知权限', True)
        if not result['allowed'] or result['generation'] != payload['settings_generation']:
            return False
        self.validate_target(payload['user_id'], payload['provider'], payload['account_id'], payload['recipient_id'])
        return True

    def retry(self, notification_id, body):
        # Revalidation also occurs at each actual send. Here it prevents a
        # disconnected or foreign target from silently entering a retry queue.
        with self.store._connect() as connection:
            row = self.store._notification_row(connection, notification_id)
        from channel_gateway.common.infrastructure.notification_store import decoded
        payload = decoded(row['metadata'], {})['task_notification']
        if not self.authorize(payload):
            raise GatewayError(409, 'NOTIFICATION_CONFLICT', '通知已失效，不能重新发送')
        supplemental_parts = None
        if body.summary is not None:
            if payload['content_mode'] != 'summary' or payload['content'].get('summary_status') != 'failed':
                raise GatewayError(409, 'NOTIFICATION_CONFLICT', '已生成的摘要不能替换')
            from copy import deepcopy
            from dataclasses import replace
            amended = deepcopy(payload)
            amended['content'].update(summary=body.summary, summary_status='ready')
            outbound = self.store._claimed_outbound(row)
            supplemental_parts = self.providers.delivery(payload['provider']).render(
                replace(outbound, metadata={'task_notification': amended}))
        return self.store.retry_notification(notification_id, body.expected_revision, body.confirm_duplicate_risk,
                                             summary=body.summary, supplemental_parts=supplemental_parts)
