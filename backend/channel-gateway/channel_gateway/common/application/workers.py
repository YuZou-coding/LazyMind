from __future__ import annotations

import logging
import threading
import time
import uuid
import datetime as dt
from dataclasses import replace
from typing import Callable

from channel_gateway.common.application.messages import ChannelMessageService
from channel_gateway.common.application.task_artifacts import (
    TASK_ARTIFACT_MONITOR_VERSION,
)
from channel_gateway.common.domain.channel import (
    ClaimedInbound,
    OutboundMessage,
    welcome_message,
)
from channel_gateway.common.domain.chat import (
    delivery_provider_context,
    inbox_provider_context,
)
from channel_gateway.common.errors import (
    GatewayError,
    LazyMindHTTPError,
    RetryableLazyMindError,
    RetryableProviderSideEffectError,
)
from channel_gateway.common.ports.messaging import MessageWorkerRepository
from channel_gateway.common.ports.messaging import (
    DeliveryProviderRegistry,
    OutboxWorkRepository,
    ReplyStreamProviderRegistry,
)


_logger = logging.getLogger(__name__)
_INBOUND_LEASE_SECONDS = 120
_OUTBOUND_LEASE_SECONDS = 120
_MAX_INBOUND_ATTEMPTS = 1
_MAX_RETRYABLE_CORE_ATTEMPTS = 3
_MAX_PROVIDER_SIDE_EFFECT_ATTEMPTS = 5
_MAX_OUTBOUND_ATTEMPTS = 5


def _failure_message() -> str:
    return 'LazyMind 暂时无法处理这条消息，请稍后重试。'


def _inbound_attempt_limit(exc: Exception) -> int:
    if isinstance(exc, RetryableLazyMindError):
        return _MAX_RETRYABLE_CORE_ATTEMPTS
    if isinstance(exc, LazyMindHTTPError) and exc.retryable:
        return _MAX_RETRYABLE_CORE_ATTEMPTS
    return _MAX_INBOUND_ATTEMPTS


class LeaseLostError(RuntimeError):
    pass


class _LeaseHeartbeat:
    def __init__(
        self,
        renew: Callable[[], bool],
        *,
        name: str,
        interval_seconds: int = 30,
    ):
        self._renew = renew
        self._interval_seconds = interval_seconds
        self._stop = threading.Event()
        self._lost = threading.Event()
        self._thread = threading.Thread(
            target=self._run,
            name=name,
            daemon=True,
        )

    def __enter__(self):
        self._thread.start()
        return self

    def __exit__(self, _exc_type, _exc_value, _traceback):
        self._stop.set()
        self._thread.join(timeout=1.0)

    def ensure_owned(self) -> None:
        if self._lost.is_set():
            raise LeaseLostError('Channel work lease was lost')

    def _run(self) -> None:
        while not self._stop.wait(self._interval_seconds):
            try:
                if not self._renew():
                    self._lost.set()
                    return
            except Exception:
                _logger.exception('channel_lease_renewal_failed')
                self._lost.set()
                return


class MessageWorker:
    def __init__(
        self,
        *,
        store: MessageWorkerRepository,
        messages: ChannelMessageService,
        streams: ReplyStreamProviderRegistry,
        worker_count: int = 2,
    ):
        self._store = store
        self._messages = messages
        self._streams = streams
        self._worker_count = max(1, worker_count)
        self._stop = threading.Event()
        self._threads: list[threading.Thread] = []

    def start(self) -> None:
        if self._threads:
            return
        for index in range(self._worker_count):
            thread = threading.Thread(
                target=self._run,
                args=(f'message_{uuid.uuid4().hex}',),
                name=f'channel-message-{index}',
                daemon=True,
            )
            self._threads.append(thread)
            thread.start()

    def stop(self) -> None:
        self._stop.set()
        for thread in self._threads:
            thread.join(timeout=2.0)
        self._threads.clear()

    def _run(self, claim_owner: str) -> None:
        while not self._stop.is_set():
            try:
                inbound = self._store.claim_next_inbound(
                    claim_owner,
                    lease_seconds=_INBOUND_LEASE_SECONDS,
                )
                if inbound is None:
                    self._stop.wait(0.5)
                    continue
                self._process(inbound, claim_owner)
            except Exception:
                _logger.exception('channel_message_worker_failed')
                self._stop.wait(1.0)

    def _process(self, inbound: ClaimedInbound, claim_owner: str) -> None:
        inbox_context = inbox_provider_context(
            inbound.provider_context
        )
        delivery_context = delivery_provider_context(
            inbound.provider_context
        )
        fallback = OutboundMessage(
            provider=inbound.provider,
            account_id=inbound.account_id,
            order_key=inbound.order_key,
            recipient_id=inbound.recipient_id,
            provider_context=delivery_context,
            text='LazyMind 暂时无法处理这条消息，请稍后重试。',
            intent_kind='failed',
        )
        stream = None
        try:
            with _LeaseHeartbeat(
                lambda: self._store.renew_inbound_lease(
                    inbound.inbox_id,
                    claim_owner,
                    lease_seconds=_INBOUND_LEASE_SECONDS,
                ),
                name='channel-inbound-lease',
            ) as lease:
                lease.ensure_owned()
                stream_provider = self._streams.streaming(
                    inbound.provider
                )
                stream = (
                    stream_provider.open_stream(inbound)
                    if stream_provider is not None
                    else None
                )
                result = self._messages.process(
                    account_id=inbound.account_id,
                    external_address_hash=inbound.external_address_hash,
                    owner_user_id=inbound.owner_user_id,
                    text=inbound.text,
                    request_id=f'channel_{inbound.message_key}',
                    provider_context=inbound.provider_context,
                    on_stream=(
                        stream.update
                        if stream is not None
                        else None
                    ),
                )
                streamed_text = (
                    stream.finish(result.text)
                    if stream is not None
                    else False
                )
                stream = None
                lease.ensure_owned()
                has_task = any(
                    presentation.kind == 'task'
                    for presentation in result.presentations
                )
                outbound = [
                    replace(
                        fallback,
                        text=result.text,
                        intent_kind=result.intent_kind.value,
                        metadata={
                            'core_events': list(result.core_events),
                            'sources': list(result.sources),
                            'presentations': [
                                presentation.to_dict()
                                for presentation
                                in result.presentations
                            ],
                            'task_monitor': has_task,
                            'task_artifact_monitor_version': (
                                TASK_ARTIFACT_MONITOR_VERSION
                                if has_task
                                else 0
                            ),
                            'streamed_text': streamed_text,
                        },
                    )
                ]
                if self._store.welcome_pending(inbound.account_id):
                    outbound.append(
                        replace(
                            outbound[0],
                            text=welcome_message(inbound.provider),
                            intent_kind='welcome',
                            purpose='welcome',
                            metadata={},
                        )
                    )
                if not self._store.complete_inbound(
                    inbound.inbox_id,
                    claim_owner,
                    outbound,
                    inbox_context,
                ):
                    _logger.warning(
                        'channel_inbound_completion_fenced inbox_id=%s',
                        inbound.inbox_id,
                    )
                    return
            _logger.info(
                'channel_inbound_completed inbox_id=%s intent=%s',
                inbound.inbox_id,
                result.intent_kind.value,
            )
        except LeaseLostError:
            if stream is not None:
                stream.abort()
            _logger.warning(
                'channel_inbound_lease_lost inbox_id=%s',
                inbound.inbox_id,
            )
        except RetryableProviderSideEffectError as exc:
            if stream is not None:
                stream.abort()
            _logger.warning(
                'channel_provider_side_effect_uncertain '
                'inbox_id=%s attempt=%s',
                inbound.inbox_id,
                inbound.attempt_count,
            )
            self._store.record_inbound_failure(
                inbound.inbox_id,
                claim_owner,
                error=exc.__class__.__name__,
                fallback=fallback,
                max_attempts=_MAX_PROVIDER_SIDE_EFFECT_ATTEMPTS,
                retained_provider_context=inbox_context,
            )
        except Exception as exc:
            if stream is not None:
                stream.abort()
            fallback = replace(
                fallback,
                text=_failure_message(),
            )
            _logger.exception(
                'channel_inbound_processing_failed inbox_id=%s attempt=%s',
                inbound.inbox_id,
                inbound.attempt_count,
            )
            self._store.record_inbound_failure(
                inbound.inbox_id,
                claim_owner,
                error=exc.__class__.__name__,
                fallback=fallback,
                max_attempts=_inbound_attempt_limit(exc),
                retained_provider_context=inbox_context,
            )


class DeliveryWorker:
    def __init__(
        self,
        *,
        store: OutboxWorkRepository,
        providers: DeliveryProviderRegistry,
        notifications=None,
        worker_count: int = 2,
    ):
        self._store = store
        self._providers = providers
        self._notifications = notifications
        self._worker_count = max(1, worker_count)
        self._stop = threading.Event()
        self._threads: list[threading.Thread] = []

    def start(self) -> None:
        if self._threads:
            return
        for index in range(self._worker_count):
            thread = threading.Thread(
                target=self._run,
                args=(f'delivery_{uuid.uuid4().hex}',),
                name=f'channel-delivery-{index}',
                daemon=True,
            )
            self._threads.append(thread)
            thread.start()

    def stop(self) -> None:
        self._stop.set()
        for thread in self._threads:
            thread.join(timeout=2.0)
        self._threads.clear()

    def _run(self, claim_owner: str) -> None:
        while not self._stop.is_set():
            outbound = None
            try:
                outbound = self._store.claim_next_outbound(
                    claim_owner,
                    lease_seconds=_OUTBOUND_LEASE_SECONDS,
                )
                if outbound is None:
                    self._stop.wait(0.5)
                    continue
                provider = self._providers.delivery(outbound.provider)
                if provider is None:
                    raise RuntimeError(
                        f'No delivery provider for {outbound.provider}'
                    )
                with _LeaseHeartbeat(
                    lambda outbound=outbound:
                    self._store.renew_outbound_lease(
                        outbound.outbox_id,
                        claim_owner,
                        lease_seconds=_OUTBOUND_LEASE_SECONDS,
                    ),
                    name='channel-outbound-lease',
                ) as lease:
                    lease.ensure_owned()
                    parts = outbound.rendered_parts
                    if not parts:
                        parts = provider.render(outbound)
                        lease.ensure_owned()
                        if not self._store.save_rendered_parts(
                            outbound.outbox_id,
                            claim_owner,
                            parts,
                        ):
                            raise RuntimeError(
                                'Cannot persist rendered channel parts'
                            )
                        outbound = replace(outbound, rendered_parts=parts)
                    if outbound.purpose == 'task_notification':
                        self._deliver_notification(outbound, provider, claim_owner, lease)
                        continue
                    self._deliver(
                        outbound,
                        provider,
                        claim_owner,
                        lease,
                    )
                    lease.ensure_owned()
                    if not self._store.complete_outbound(
                        outbound.outbox_id,
                        claim_owner,
                    ):
                        raise RuntimeError(
                            'Channel outbox completion was fenced'
                        )
            except LeaseLostError:
                if outbound is not None:
                    _logger.warning(
                        'channel_outbound_lease_lost outbox_id=%s',
                        outbound.outbox_id,
                    )
            except Exception as exc:
                if outbound is not None:
                    _logger.exception(
                        'channel_outbound_failed outbox_id=%s attempt=%s',
                        outbound.outbox_id,
                        outbound.attempt_count,
                    )
                    self._store.record_outbound_failure(
                        outbound.outbox_id,
                        claim_owner,
                        error=exc.__class__.__name__,
                        max_attempts=_MAX_OUTBOUND_ATTEMPTS,
                    )
                else:
                    _logger.exception('channel_delivery_worker_failed')
                self._stop.wait(1.0)

    def _deliver_notification(self, outbound, provider, claim_owner, lease):
        payload = outbound.metadata['task_notification']
        states = outbound.provider_state

        def save(index, state):
            lease.ensure_owned()
            if not self._store.save_outbound_part_state(outbound.outbox_id, claim_owner, index, state):
                raise LeaseLostError('Notification writer was fenced')
            states[str(index)] = dict(state)

        def finish(status, delay=None, consume_attempt=True):
            lease.ensure_owned()
            if not self._store.finish_notification(outbound.outbox_id, claim_owner, status=status,
                                                   delay=delay, consume_attempt=consume_attempt):
                raise LeaseLostError('Notification completion was fenced')

        for index, part in enumerate(outbound.rendered_parts):
            state = dict(states.get(str(index)) or {})
            if state.get('status') in {'sent', 'skipped'} or state.get('permanent_failure'):
                continue
            if state.get('status') == 'failed' and state.get('retryable') is False:
                # A later part can defer the queue. Keep definite rejections
                # failed until manual retry explicitly resets them to pending.
                continue
            try:
                lease.ensure_owned()
                delay = self._store.reserve_notification_send(
                    outbound.provider, outbound.account_id, outbound.recipient_id)
                if delay > 1:
                    finish('retry_wait', delay=delay, consume_attempt=False)
                    return
                if delay:
                    time.sleep(delay)
                lease.ensure_owned()
                if self._notifications is None:
                    raise GatewayError(503, 'NOTIFICATION_CORE_UNAVAILABLE', '通知授权服务不可用', True)
                if not self._notifications.authorize(payload):
                    for pending_index in range(index, len(outbound.rendered_parts)):
                        pending = dict(states.get(str(pending_index)) or {})
                        if pending.get('status') != 'sent':
                            save(pending_index, {**pending, 'status': 'skipped',
                                                 'error_code': 'NOTIFICATION_DISABLED'})
                    finish('dead')
                    return
            except GatewayError as exc:
                if exc.retryable:
                    finish('retry_wait', delay=2, consume_attempt=False)
                    return
                save(index, {**state, 'status': 'failed', 'error_code': exc.code})
                continue
            now = dt.datetime.now(dt.timezone.utc)
            first_request = state.get('first_request_at')
            if first_request:
                started = dt.datetime.fromisoformat(first_request.replace('Z', '+00:00'))
                if started.tzinfo is None:
                    started = started.replace(tzinfo=dt.timezone.utc)
                if (now - started).total_seconds() >= 3600:
                    save(index, {**state, 'status': 'unknown', 'error_code': 'NOTIFICATION_RESULT_UNKNOWN'})
                    finish('dead')
                    return
            state.update(status='sending', first_request_at=first_request or now.isoformat(),
                         attempt_count=int(state.get('attempt_count', 0)) + 1)
            save(index, state)
            delivery_id = str(uuid.uuid5(uuid.NAMESPACE_URL,
                                         f'lazymind:{outbound.outbox_id}:part:{index}:'
                                         f'{state.get("delivery_generation", 0)}'))
            try:
                lease.ensure_owned()
                delivered = provider.send_part(outbound, part, part_index=index,
                                               idempotency_key=delivery_id, saved_state=state) or state
                delivered = {**state, **delivered, 'status': 'sent', 'sent_at': now.isoformat()}
                if part.get('permanent_failure'):
                    delivered.update(status='failed', permanent_failure=True, error_code=part['error_code'])
                save(index, delivered)
            except LeaseLostError:
                raise
            except GatewayError as exc:
                if first_request and exc.code == 'FEISHU_SEND_REJECTED':
                    # Rejecting this retry cannot disprove an earlier send
                    # whose response was lost. Preserve its duplicate risk.
                    save(index, {**state, 'status': 'unknown', 'error_code': 'NOTIFICATION_RESULT_UNKNOWN'})
                    finish('dead')
                    return
                state.update(status='failed', error_code=exc.code, retryable=exc.retryable)
                # A definite rejection is safe to retry after reauthorization;
                # it does not consume the platform's ambiguity window.
                state.pop('first_request_at', None)
                if exc.code == 'NOTIFICATION_ARTIFACT_UNAVAILABLE':
                    state['permanent_failure'] = True
                if part.get('failure_notice') and (
                    state.get('permanent_failure') or exc.code == 'FEISHU_SEND_REJECTED'
                ):
                    lease.ensure_owned()
                    parts = self._store.fail_notification_artifact(
                        outbound.outbox_id, claim_owner, index, state, part['failure_notice'])
                    if parts is None:
                        raise LeaseLostError('Notification artifact writer was fenced')
                    states[str(index)] = dict(state)
                    outbound.rendered_parts[:] = parts
                else:
                    save(index, state)
                if not exc.retryable:
                    continue
                finish('dead' if outbound.attempt_count >= 5 else 'retry_wait',
                       delay=None if outbound.attempt_count >= 5 else min(300, 2 ** outbound.attempt_count))
                return
            except Exception as exc:
                retry_after = getattr(exc, 'retry_after_seconds', None)
                state.update(status='failed' if outbound.attempt_count >= 5 else 'pending',
                             error_code='NOTIFICATION_SEND_UNCONFIRMED')
                # Explicit rate rejection is not an ambiguous accepted send.
                if retry_after is not None:
                    state.pop('first_request_at', None)
                    self._store.defer_notification_provider(
                        outbound.provider, outbound.account_id, outbound.recipient_id, float(retry_after))
                save(index, state)
                delay = max(float(retry_after or 0), min(300, 2 ** outbound.attempt_count))
                finish('dead' if outbound.attempt_count >= 5 else 'retry_wait',
                       delay=None if outbound.attempt_count >= 5 else delay)
                return
        finish('sent')

    def _deliver(
        self,
        outbound,
        provider,
        claim_owner: str,
        lease: _LeaseHeartbeat,
    ) -> None:
        for part_index in range(
            outbound.next_part_index,
            len(outbound.rendered_parts),
        ):
            lease.ensure_owned()
            part = outbound.rendered_parts[part_index]
            saved_state = dict(
                outbound.provider_state.get(str(part_index)) or {}
            )
            prepared_state = provider.prepare_part(
                outbound,
                part,
                part_index=part_index,
                saved_state=saved_state,
            )
            if prepared_state != saved_state:
                if not self._store.save_outbound_part_state(
                    outbound.outbox_id,
                    claim_owner,
                    part_index,
                    prepared_state,
                ):
                    raise RuntimeError('Cannot persist provider delivery state')
            outbound.provider_state[str(part_index)] = dict(prepared_state)
            lease.ensure_owned()
            delivery_id = str(part.get('delivery_id') or '')
            if not delivery_id or len(delivery_id) > 512:
                delivery_id = str(
                    uuid.uuid5(
                        uuid.NAMESPACE_URL,
                        f'lazymind:{outbound.outbox_id}:part:{part_index}',
                    )
                )
            delivered_state = provider.send_part(
                outbound,
                part,
                part_index=part_index,
                idempotency_key=delivery_id,
                saved_state=prepared_state,
            )
            if (
                delivered_state is not None
                and delivered_state != prepared_state
            ):
                if not self._store.save_outbound_part_state(
                    outbound.outbox_id,
                    claim_owner,
                    part_index,
                    delivered_state,
                ):
                    raise RuntimeError(
                        'Cannot persist provider delivery result'
                    )
            if delivered_state is not None:
                outbound.provider_state[str(part_index)] = dict(
                    delivered_state
                )
            lease.ensure_owned()
            if not self._store.advance_outbound(
                outbound.outbox_id,
                claim_owner,
                part_index + 1,
            ):
                raise RuntimeError('Cannot advance channel outbox')
