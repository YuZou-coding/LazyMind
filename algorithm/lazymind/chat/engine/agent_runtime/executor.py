from __future__ import annotations

import json
import time
import types
import uuid
from typing import Any, AsyncIterator, Optional, Tuple

import lazyllm
import lazyllm.module.stream_helper as _sh
import lazyllm.tools.agent as _agent_mod
from lazyllm.tools.agent.toolError import tool_failure
from lazyllm.tools.agent.base import _write_agent_data
from lazymind.chat.engine.tools.infra import CitationResultMiddleware
from lazymind.chat.engine.tools.session_env import redact_session_env_arguments
from lazymind.config import config as _cfg

from .context_estimator import estimate_non_history_tokens
from .models import AgentRole, AgentRunPlan
from .pruner import estimate_history_tokens, make_history_compactor
from .telemetry import (
    append_event,
    emit_tool_call,
    emit_tool_result,
    make_runtime_observer,
    sid,
    telemetry_enabled,
)
from .tool_call_guard import (
    ExactRepeatMonitor,
    FailureRetryPolicy,
    OneShotNoticeBuffer,
    ToolExecutionMiddleware,
    _log_tool_call,
    _requires_expanded_budget,
    _summarize_tool_result,
)
from .tool_limit_control import tool_limit_decision_coordinator


def _sanitize_tools(tools: list[Any]) -> list[Any]:
    """Drop invalid tool entries (e.g. partially-imported modules) before ReactAgent."""
    cleaned: list[Any] = []
    for tool in tools:
        if isinstance(tool, types.ModuleType):
            lazyllm.LOG.error(
                '[AgentExecutor] dropping invalid tool module '
                f'name={getattr(tool, "__name__", None)} file={getattr(tool, "__file__", None)}'
            )
            continue
        if isinstance(tool, dict):
            children = tool.get('tools')
            if isinstance(children, list):
                kept = []
                for child in children:
                    if isinstance(child, types.ModuleType):
                        lazyllm.LOG.error(
                            '[AgentExecutor] dropping invalid ToolGroup child module '
                            f'group={tool.get("name")} name={getattr(child, "__name__", None)} '
                            f'file={getattr(child, "__file__", None)}'
                        )
                        continue
                    kept.append(child)
                tool = {**tool, 'tools': kept}
        cleaned.append(tool)
    return cleaned


def _parse_tool_arguments(function: dict[str, Any]) -> Any:
    arguments = function.get('arguments', {})
    if isinstance(arguments, str):
        try:
            return json.loads(arguments)
        except Exception:
            return arguments
    return arguments


class ToolCallGuard:
    """Stop selected tools from looping after failures without limiting successful work."""

    def __init__(
        self,
        manager: Any,
        failure_limits: dict[str, int] | None = None,
        expanded_round_limit: int | None = None,
        repeated_call_limit: int = 3,
        cancel_check: Any = None,
    ):
        self._manager = manager
        self._failure_limits = dict(failure_limits or {})
        self._consecutive_failures: dict[str, int] = {}
        self._expanded_round_limit = expanded_round_limit
        self._repeated_call_limit = max(2, int(repeated_call_limit))
        self._signature_calls: dict[str, int] = {}
        self._failed_signatures: set[str] = set()
        self._cancel_check = cancel_check

    def __getattr__(self, name: str) -> Any:
        return getattr(self._manager, name)

    @staticmethod
    def _signature(tool_call: dict[str, Any]) -> str:
        function = tool_call.get('function') or {}
        arguments = function.get('arguments', {})
        if isinstance(arguments, str):
            try:
                arguments = json.loads(arguments)
            except Exception:
                arguments = arguments.strip()
        try:
            normalized = json.dumps(
                arguments, ensure_ascii=False, sort_keys=True, separators=(',', ':'),
            )
        except (TypeError, ValueError):
            normalized = str(arguments)
        return f"{function.get('name', '')}:{normalized}"

    @staticmethod
    def _failed(result: Any) -> bool:
        return isinstance(result, dict) and result.get('ok') is False

    @staticmethod
    def _blocked(name: str, message: str) -> dict[str, Any]:
        message = f'[Repeated Tool Failure] {name}: {message}'
        return tool_failure(message)

    @staticmethod
    def _loop_blocked(name: str, message: str) -> dict[str, Any]:
        message = f'[Repeated Tool Call] {name}: {message}'
        return tool_failure(message)

    @staticmethod
    def _permission_mode() -> str:
        config = lazyllm.globals.get('agentic_config') or {}
        mode = str(config.get('workspace_permission_mode') or '').strip()
        if not mode:
            source = next((
                item for item in (config.get('local_fs_sources') or [])
                if isinstance(item, dict) and item.get('workspace_id')
            ), {})
            mode = str(source.get('workspace_permission_mode') or 'ask_as_needed')
        return mode if mode in {'always_ask', 'ask_as_needed', 'allow_all'} else 'ask_as_needed'

    @staticmethod
    def _strip_model_permissions(tool_call: dict[str, Any]) -> dict[str, Any]:
        sanitized = dict(tool_call)
        function = dict(sanitized.get('function') or {})
        arguments = _parse_tool_arguments(function)
        if isinstance(arguments, dict) and 'allow_unsafe' in arguments:
            arguments = dict(arguments)
            arguments['allow_unsafe'] = False
            function['arguments'] = arguments
        sanitized['function'] = function
        return sanitized

    def _resolve_approval(
        self,
        tool_call: dict[str, Any],
        result: Any,
        *,
        verbose: bool,
        allowed_tool_names: set[str] | None,
        permission_mode: str,
    ) -> Any:
        if not (
            isinstance(result, dict) and result.get('ok') is False and
            result.get('needs_approval') is True
        ):
            return result
        function = tool_call.get('function') or {}
        arguments = _parse_tool_arguments(function)
        if not isinstance(arguments, dict):
            return result
        name = str(function.get('name') or '')
        config = lazyllm.globals.get('agentic_config')
        if not isinstance(config, dict):
            config = {}
            lazyllm.globals['agentic_config'] = config
        coordinator = tool_limit_decision_coordinator
        decision_id = uuid.uuid4().hex
        sid_value = lazyllm.globals._sid
        auto_allow = permission_mode == 'allow_all'
        if not auto_allow:
            coordinator._register(
                sid_value, decision_id, str(config.get('conversation_id') or ''),
            )
        try:
            command = str(arguments.get('command') or arguments.get('cmd') or '')
            file_path = str(arguments.get('filepath') or arguments.get('path') or '')
            if auto_allow:
                action = 'allow_once'
            else:
                _write_agent_data(
                    'tool_limit_pending',
                    decision_id=decision_id,
                    approval_kind='tool',
                    tool_name=name,
                    command=command,
                    path=file_path,
                    cwd=str(arguments.get('cwd') or '.'),
                    reason=str(result.get('value') or ''),
                    used_rounds=0,
                    round_limit=0,
                    expanded_max_rounds=0,
                    timeout_seconds=600,
                )
                action = coordinator._wait_for_action(decision_id, 600)
            if action != 'allow_once':
                return tool_failure('The user denied or did not approve this operation.')

            replay = dict(tool_call)
            replay_function = dict(function)
            replay_arguments = dict(arguments)
            prior_sensitive = list(config.get('approved_sensitive_paths') or [])
            prior_writes = list(config.get('approved_workspace_write_paths') or [])
            prior_apps = list(config.get('approved_connected_app_tools') or [])
            if command:
                replay_arguments['allow_unsafe'] = True
            elif file_path:
                config['approved_sensitive_paths'] = prior_sensitive + [file_path]
                config['approved_workspace_write_paths'] = prior_writes + [file_path]
            else:
                config['approved_connected_app_tools'] = prior_apps + [name]
            replay_function['arguments'] = replay_arguments
            replay['function'] = replay_function
            try:
                return self._manager(
                    [replay], verbose=verbose, allowed_tool_names=allowed_tool_names,
                )[0]
            finally:
                config['approved_sensitive_paths'] = prior_sensitive
                config['approved_workspace_write_paths'] = prior_writes
                config['approved_connected_app_tools'] = prior_apps
        finally:
            if not auto_allow:
                coordinator._unregister(sid_value, decision_id)

    def __call__(self, tools: Any, verbose: bool = False,
                 allowed_tool_names: set[str] | None = None) -> Any:
        if self._cancel_check is not None:
            self._cancel_check(None)
        tool_calls = [tools] if isinstance(tools, dict) else list(tools or [])
        tool_calls = [self._strip_model_permissions(tool_call) for tool_call in tool_calls]
        permission_mode = self._permission_mode()
        results: list[Any] = [None] * len(tool_calls)
        pending: list[dict[str, Any]] = []
        pending_indices: list[int] = []
        pending_signatures: dict[str, int] = {}
        duplicate_indices: dict[int, int] = {}
        for index, tool_call in enumerate(tool_calls):
            function = tool_call.get('function') or {}
            name = str(function.get('name') or '')
            config = lazyllm.globals.get('agentic_config') or {}
            connected_tools = set(config.get('connected_app_tool_names') or [])
            network_tools = set(config.get('network_tool_names') or [])
            approved_apps = set(config.get('approved_connected_app_tools') or [])
            if (
                name in connected_tools | network_tools and permission_mode == 'always_ask'
                and name not in approved_apps
            ):
                emit_tool_call(tool_call)
                result = self._resolve_approval(
                    tool_call,
                    tool_failure(
                        f'Connected app operation {name!r} requires approval.',
                        needs_approval=True,
                    ),
                    verbose=verbose,
                    allowed_tool_names=allowed_tool_names,
                    permission_mode=permission_mode,
                )
                results[index] = result
                emit_tool_result(tool_call, result)
                continue
            if _requires_expanded_budget(name):
                workspace = lazyllm.locals.get('_lazyllm_agent', {}).get('workspace')
                if (
                    isinstance(workspace, dict)
                    and self._expanded_round_limit is not None
                    and workspace.get('_react_round_limit') != self._expanded_round_limit
                ):
                    workspace['_react_round_limit'] = self._expanded_round_limit
                    lazyllm.LOG.info(
                        f'ChatAgent used tool={name}; automatically expanding '
                        f'tool round limit to {self._expanded_round_limit}.'
                    )
            signature = self._signature(tool_call)
            arguments = _parse_tool_arguments(function)
            signature_calls = self._signature_calls.get(signature, 0)
            if signature_calls >= self._repeated_call_limit:
                results[index] = self._loop_blocked(
                    name,
                    f'the exact same call was already made {self._repeated_call_limit} times; '
                    'stop retrying it and synthesize from existing results or choose another tool.',
                )
                _log_tool_call(
                    'blocked', name, reason='repeated_call',
                    args=redact_session_env_arguments(name, arguments),
                )
                continue
            guarded = name in self._failure_limits
            if guarded and signature in self._failed_signatures:
                results[index] = self._blocked(
                    name, 'this exact call already failed; do not retry it with the same arguments.',
                )
                emit_tool_call(tool_call, blocked=True, reason='repeated_failed_signature')
                emit_tool_result(tool_call, results[index])
                _log_tool_call(
                    'blocked', name, reason='repeated_failure',
                    args=redact_session_env_arguments(name, arguments),
                )
                continue
            if guarded and signature in pending_signatures:
                duplicate_indices[index] = pending_signatures[signature]
                emit_tool_call(tool_call, blocked=True, reason='duplicate_merged')
                _log_tool_call(
                    'merged', name, reason='duplicate_in_batch',
                    args=redact_session_env_arguments(name, arguments),
                )
                continue
            failures = self._consecutive_failures.get(name, 0)
            limit = self._failure_limits.get(name)
            if limit is not None and failures >= limit:
                results[index] = self._blocked(
                    name,
                    f'{failures} consecutive attempts failed. Stop changing parameters and use '
                    'another grounded source or explain that the evidence is unavailable.',
                )
                emit_tool_call(tool_call, blocked=True, reason='consecutive_failure_limit')
                emit_tool_result(tool_call, results[index])
                _log_tool_call(
                    'blocked', name, reason='consecutive_failures',
                    failures=failures,
                    args=redact_session_env_arguments(name, arguments),
                )
                continue
            emit_tool_call(tool_call)
            self._signature_calls[signature] = signature_calls + 1
            pending.append(tool_call)
            pending_indices.append(index)
            if guarded:
                pending_signatures[signature] = index
        if pending:
            for tool_call in pending:
                function = tool_call.get('function') or {}
                _log_tool_call(
                    'start',
                    str(function.get('name') or ''),
                    args=redact_session_env_arguments(
                        str(function.get('name') or ''),
                        _parse_tool_arguments(function),
                    ),
                )
            started_at = time.perf_counter()
            pending_results = self._manager(
                pending,
                verbose=verbose,
                allowed_tool_names=allowed_tool_names,
            )
            elapsed = time.perf_counter() - started_at
            for index, tool_call, result in zip(pending_indices, pending, pending_results):
                result = self._resolve_approval(
                    tool_call,
                    result,
                    verbose=verbose,
                    allowed_tool_names=allowed_tool_names,
                    permission_mode=permission_mode,
                )
                results[index] = result
                emit_tool_result(tool_call, result)
                name = str((tool_call.get('function') or {}).get('name') or '')
                _log_tool_call(
                    'done', name, elapsed=f'{elapsed:.3f}s', **_summarize_tool_result(result),
                )
                if name in self._failure_limits:
                    if self._failed(result):
                        self._consecutive_failures[name] = (
                            self._consecutive_failures.get(name, 0) + 1
                        )
                        self._failed_signatures.add(self._signature(tool_call))
                    else:
                        self._consecutive_failures[name] = 0
                        prefix = f'{name}:'
                        self._failed_signatures = {
                            item for item in self._failed_signatures if not item.startswith(prefix)
                        }
        for duplicate_index, original_index in duplicate_indices.items():
            results[duplicate_index] = results[original_index]
            if results[duplicate_index] is not None:
                emit_tool_result(tool_calls[duplicate_index], results[duplicate_index])
        return results

def _tool_name(tool: Any) -> str:
    if isinstance(tool, tuple) and len(tool) == 2:
        return _tool_name(tool[0])
    if isinstance(tool, dict):
        return str(tool.get('name') or '')
    return str(getattr(tool, '__name__', '') or '') or tool.__class__.__name__


def _deduplicate_tools(tools: list[Any]) -> list[Any]:
    result, seen = [], set()
    for tool in tools:
        name = _tool_name(tool)
        if name and name in seen:
            continue
        if name:
            seen.add(name)
        result.append(tool)
    return result


class AgentExecutor:
    """Create and drive ReactAgent instances from a fully assembled run plan."""

    def create_agent(self, llm: Any, plan: AgentRunPlan) -> Any:
        from lazymind.chat.lazyllm_tool_docs import ensure_lazyllm_tool_docs

        options = plan.execution_options
        keep_full_turns = options.keep_full_turns
        if keep_full_turns is None:
            keep_full_turns = int(_cfg['agentic_keep_full_turns'])
        history_compactor = options.history_compactor
        if not _cfg['context_compression_enabled']:
            history_compactor = None
        elif history_compactor is None:
            history_compactor = make_history_compactor(
                max_input_tokens=options.max_input_tokens,
                llm_config=options.llm_config,
                keep_recent=keep_full_turns,
                trigger='mid_turn',
                llm=llm,
                workspace=options.workspace,
            )
        run_id = uuid.uuid4().hex[:12]
        observer = (
            make_runtime_observer(
                role=getattr(plan.role, 'value', str(plan.role)),
                run_id=run_id,
            )
            if telemetry_enabled() else None
        )
        repeat_monitor = ExactRepeatMonitor()
        notice_buffer = OneShotNoticeBuffer()
        kwargs = {
            'stream': True,
            'max_retries': options.max_retries or _cfg['max_retries'],
            'enable_builtin_tools': (
                bool(_cfg['trusted_local_mode'])
                if options.enable_builtin_tools is None else options.enable_builtin_tools
            ),
            'force_summarize': True,
            'force_summarize_context': plan.force_summarize_context,
            'on_max_retries': (
                tool_limit_decision_coordinator.on_max_retries
                if plan.role == AgentRole.CHAT else None
            ),
        }
        optional = {
            'skills': options.skills,
            'workspace': options.workspace,
            'keep_full_turns': keep_full_turns,
            'history_compactor': history_compactor,
            'fs': options.fs,
            'skills_dir': options.skills_dir,
            'extra_stop_condition': options.extra_stop_condition,
            'runtime_observer': observer,
            'model_context_provider': notice_buffer.take,
        }
        kwargs.update({key: value for key, value in optional.items() if value is not None})
        tools = _sanitize_tools(_deduplicate_tools(plan.tools))
        ensure_lazyllm_tool_docs(tools)
        agent = _agent_mod.ReactAgent(
            llm=llm,
            tools=tools,
            prompt=plan.prompt.system_prompt,
            **kwargs,
        )
        agent._tools_manager = ToolCallGuard(
            ToolExecutionMiddleware(
                CitationResultMiddleware(agent._tools_manager),
                failure_policy=FailureRetryPolicy(options.tool_failure_limits),
                expanded_round_limit=max(2, int(_cfg['agentic_expanded_max_rounds'])),
                cancel_check=options.extra_stop_condition,
                repeat_monitor=repeat_monitor,
                notice_buffer=notice_buffer,
            ),
        )
        agent._agent_lab_run_id = run_id
        agent._exact_repeat_monitor = repeat_monitor
        agent._runtime_notice_buffer = notice_buffer
        # Restore lazy Toolkit activation before the streaming helper takes over.
        # Relying only on ReactAgent._pre_process makes restoration dependent on
        # llm_chat_history surviving the helper/framework call path.
        agent._prepare_tool_context(plan.prompt.current_input, plan.history)
        prefix = agent._model_facing_prefix()
        estimated = (
            estimate_non_history_tokens(prefix, plan.prompt.current_input)
            + estimate_history_tokens(plan.history or [])
        )
        if telemetry_enabled():
            append_event(
                'run_prepare',
                role=getattr(plan.role, 'value', str(plan.role)),
                compression_enabled=bool(_cfg['context_compression_enabled']),
                history_len=len(plan.history or []),
                estimated_tokens=estimated,
                sid=sid(),
            )
        agent.set_stop_tools(plan.stop_tools)
        return agent

    async def stream(
        self,
        llm: Any,
        plan: AgentRunPlan,
    ) -> AsyncIterator[Tuple[str, Any]]:
        agent = self.create_agent(llm, plan)
        async for item in self.stream_agent(agent, plan):
            yield item

    async def stream_agent(
        self,
        agent: Any,
        plan: AgentRunPlan,
    ) -> AsyncIterator[Tuple[str, Any]]:
        history = plan.history if plan.history else None
        run_id = getattr(agent, '_agent_lab_run_id', '')
        repeat_monitor = getattr(agent, '_exact_repeat_monitor', None)
        notice_buffer = getattr(agent, '_runtime_notice_buffer', None)
        if repeat_monitor is not None:
            repeat_monitor.reset()
        if notice_buffer is not None:
            notice_buffer.clear()
        if telemetry_enabled():
            append_event(
                'run_start',
                role=getattr(plan.role, 'value', str(plan.role)),
                run_id=run_id,
                history_len=len(history or []),
                estimated_tokens=estimate_history_tokens(history or []),
                input_preview=(plan.prompt.current_input or '')[:240],
                sid=sid(),
            )
        helper = _sh.StreamCallHelper(agent, init_sid=False)
        kwargs = {'llm_chat_history': history} if history is not None else {}
        finished_model_calls: set[str] = set()
        failed = False
        try:
            async for item in helper.astream(plan.prompt.current_input, **kwargs):
                self._record_finished_model_call(item, finished_model_calls)
                yield 'event', item
            try:
                result = helper.future.result()
            except Exception as exc:
                failed = True
                terminal = self._find_model_terminal(exc)
                model_call_id = str((terminal or {}).get('model_call_id') or '')
                if terminal and model_call_id not in finished_model_calls:
                    yield 'event', {
                        'tag': 'runtime_event',
                        'runtime_event': {
                            'schema_version': 1,
                            'event_id': uuid.uuid4().hex,
                            'type': 'model_call_finished',
                            'data': terminal,
                        },
                    }
                lazyllm.LOG.exception(
                    f'[AgentExecutor] agent future raised: {type(exc).__name__}: {exc}'
                )
                raise
            yield 'final', result
        finally:
            if repeat_monitor is not None:
                repeat_monitor.reset()
            if notice_buffer is not None:
                notice_buffer.clear()
            if telemetry_enabled():
                append_event(
                    'run_end',
                    role=getattr(plan.role, 'value', str(plan.role)),
                    run_id=run_id,
                    ok=not failed,
                    sid=sid(),
                )

    @staticmethod
    def _record_finished_model_call(item: Any, seen: set[str]) -> None:
        if not isinstance(item, dict) or item.get('tag') != 'runtime_event': return
        event = item.get('runtime_event')
        if not isinstance(event, dict) or event.get('type') != 'model_call_finished': return
        data = event.get('data')
        if isinstance(data, dict) and data.get('model_call_id'):
            seen.add(str(data['model_call_id']))

    @staticmethod
    def _find_model_terminal(exc: Exception) -> Optional[dict[str, Any]]:
        seen = set()
        while exc is not None and id(exc) not in seen:
            seen.add(id(exc))
            terminal = getattr(exc, 'terminal', None)
            if terminal is not None:
                public_dict = getattr(terminal, 'public_dict', None)
                return public_dict() if callable(public_dict) else terminal
            exc = exc.__cause__ or exc.__context__
        return None

    def run(self, llm: Any, plan: AgentRunPlan) -> Any:
        """Run a one-shot agent while preserving ReactAgent's synchronous API."""
        agent = self.create_agent(llm, plan)
        return agent(plan.prompt.current_input)
