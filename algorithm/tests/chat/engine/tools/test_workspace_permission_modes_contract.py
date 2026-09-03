from __future__ import annotations

import subprocess

import lazyllm
import pytest

import lazyllm.tools.agent.shell_tool as shell_tool_module
from lazyllm.tools.agent import ToolExecutionError
from lazyllm.tools.agent.shell_tool import shell_tool
from lazymind.chat.engine.tools.local_fs import LocalFSScope, LocalFileToolkit


class _WorkspaceToolkit(LocalFileToolkit):
    def __init__(self, root: str):
        self._scope = LocalFSScope('workspace-policy', (root,), frozenset({'txt', ''}))

    def _get_scopes(self):
        return [self._scope]


def _set_workspace_mode(root, mode):
    previous = lazyllm.globals.get('agentic_config')
    lazyllm.globals['agentic_config'] = {
        'workspace_permission_mode': mode,
        'local_fs_sources': [{
            'workspace_id': 'workspace-policy',
            'workspace_version': 1,
            'paths': [str(root)],
        }],
    }
    return previous


def _restore_config(previous):
    if previous is None:
        lazyllm.globals.pop('agentic_config', None)
    else:
        lazyllm.globals['agentic_config'] = previous


def test_always_ask_requires_approval_before_creating_a_workspace_file(tmp_path):
    previous = _set_workspace_mode(tmp_path, 'always_ask')
    try:
        with pytest.raises(ToolExecutionError) as exc_info:
            _WorkspaceToolkit(str(tmp_path)).write('created.txt', 'content')
        assert exc_info.value.needs_approval is True
        assert not (tmp_path / 'created.txt').exists()
    finally:
        _restore_config(previous)


def test_always_ask_requires_approval_for_workspace_delete(monkeypatch, tmp_path):
    calls = []
    previous = _set_workspace_mode(tmp_path, 'always_ask')
    monkeypatch.setattr(shell_tool_module, '_revalidate_task_workspace', lambda _root: None)
    monkeypatch.setattr(
        shell_tool_module,
        '_run_workspace_process',
        lambda *args, **kwargs: calls.append((args, kwargs)) or subprocess.CompletedProcess(
            args=args, returncode=0, stdout='', stderr='',
        ),
    )
    try:
        with pytest.raises(ToolExecutionError) as exc_info:
            shell_tool('rm -f disposable.txt', cwd=str(tmp_path))
        assert exc_info.value.needs_approval is True
        assert calls == []
    finally:
        _restore_config(previous)


def test_allow_all_skips_approval_for_approvable_risky_command(monkeypatch, tmp_path):
    calls = []
    previous = _set_workspace_mode(tmp_path, 'allow_all')
    monkeypatch.setattr(shell_tool_module, '_revalidate_task_workspace', lambda _root: None)
    monkeypatch.setattr(
        shell_tool_module,
        '_run_workspace_process',
        lambda *args, **kwargs: calls.append((args, kwargs)) or subprocess.CompletedProcess(
            args=args, returncode=0, stdout='', stderr='',
        ),
    )
    try:
        result = shell_tool('npm install', cwd=str(tmp_path))
        assert result['status'] == 'ok'
        assert calls
    finally:
        _restore_config(previous)


def test_allow_all_never_bypasses_permanently_denied_command(monkeypatch, tmp_path):
    previous = _set_workspace_mode(tmp_path, 'allow_all')
    monkeypatch.setattr(shell_tool_module, '_revalidate_task_workspace', lambda _root: None)
    try:
        with pytest.raises(ToolExecutionError) as exc_info:
            shell_tool('sudo touch forbidden.txt', cwd=str(tmp_path), allow_unsafe=True)
        assert exc_info.value.needs_approval is False
        assert 'permanently denied' in str(exc_info.value)
    finally:
        _restore_config(previous)
