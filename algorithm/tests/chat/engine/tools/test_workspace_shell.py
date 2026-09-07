from __future__ import annotations

import subprocess
import sys

import lazyllm
import pytest
from lazyllm.tools.agent import ToolExecutionError

from lazymind.chat.engine.tools import workspace_shell


def _configure(root, mode='ask_as_needed'):
    previous = lazyllm.globals.get('agentic_config')
    lazyllm.globals['agentic_config'] = {
        'workspace_permission_mode': mode,
        'local_fs_sources': [{
            'workspace_id': 'workspace-1', 'paths': [str(root)],
            'workspace_permission_mode': mode,
        }],
    }
    return previous


def _restore(previous):
    if previous is None:
        lazyllm.globals.pop('agentic_config', None)
    else:
        lazyllm.globals['agentic_config'] = previous


def test_workspace_shell_uses_structured_argv(monkeypatch, tmp_path):
    calls = []
    previous = _configure(tmp_path)
    monkeypatch.setattr(workspace_shell, '_revalidate_task_workspace', lambda _root: None)
    monkeypatch.setattr(
        workspace_shell,
        '_run_workspace_process',
        lambda *args, **kwargs: calls.append((args, kwargs)) or subprocess.CompletedProcess(args, 0, 'ok', ''),
    )
    try:
        result = workspace_shell.shell_tool('git status')
    finally:
        _restore(previous)
    assert result['stdout'] == 'ok'
    assert calls[0][0][0] == ['git', 'status']
    assert calls[0][0][1] == str(tmp_path)


def test_workspace_shell_rejects_outside_path(tmp_path):
    previous = _configure(tmp_path)
    try:
        with pytest.raises(ToolExecutionError, match='outside'):
            workspace_shell.shell_tool('cat ../secret.txt')
    finally:
        _restore(previous)


def test_workspace_shell_rejects_absolute_cwd_even_inside_workspace(tmp_path):
    previous = _configure(tmp_path)
    try:
        with pytest.raises(ToolExecutionError, match='relative'):
            workspace_shell.shell_tool('git status', cwd=str(tmp_path))
    finally:
        _restore(previous)


def test_subagent_workspace_shell_is_available_only_for_ordinary_bound_tasks(tmp_path):
    bound = {
        'agent_type': 'research',
        'local_fs_sources': [{'workspace_id': 'workspace-1', 'paths': [str(tmp_path)]}],
    }

    assert workspace_shell.subagent_workspace_shell_tools(bound, tools_only=False) == [
        workspace_shell.shell_tool,
    ]
    assert workspace_shell.subagent_workspace_shell_tools(
        {**bound, 'local_fs_sources': []}, tools_only=False,
    ) == []
    assert workspace_shell.subagent_workspace_shell_tools(
        {**bound, 'agent_type': 'workflow_step'}, tools_only=False,
    ) == []
    assert workspace_shell.subagent_workspace_shell_tools(bound, tools_only=True) == []


def test_linux_workspace_shell_uses_bubblewrap_with_only_workspace_writable(monkeypatch, tmp_path):
    root = str(tmp_path)
    monkeypatch.setattr(
        workspace_shell.shutil,
        'which',
        lambda name: '/usr/bin/bwrap' if name in {'bwrap', 'bubblewrap'} else '/usr/bin/git',
    )

    command = workspace_shell._linux_sandbox_argv(
        ['git', 'status'], root, root, root, allow_network=False,
    )

    assert command[0] == '/usr/bin/bwrap'
    assert ['--bind', root, '/workspace'] == command[
        command.index('--bind'):command.index('--bind') + 3
    ]
    assert '--unshare-net' in command
    assert command[-2:] == ['/usr/bin/git', 'status']


def test_linux_workspace_shell_fails_closed_without_bubblewrap(monkeypatch, tmp_path):
    monkeypatch.setattr(workspace_shell.shutil, 'which', lambda _name: None)

    with pytest.raises(ToolExecutionError, match='containment is unavailable'):
        workspace_shell._linux_sandbox_argv(
            ['git', 'status'], str(tmp_path), str(tmp_path), str(tmp_path), allow_network=False,
        )


@pytest.mark.skipif(sys.platform != 'darwin', reason='macOS sandbox contract')
def test_workspace_shell_contains_code_run_from_inside_workspace(monkeypatch, tmp_path):
    workspace = tmp_path / 'workspace'
    outside = tmp_path / 'outside'
    workspace.mkdir()
    outside.mkdir()
    (outside / 'secret.txt').write_text('outside-secret', encoding='utf-8')
    (workspace / 'probe.py').write_text(
        'from pathlib import Path\nprint(Path("../outside/secret.txt").read_text())\n',
        encoding='utf-8',
    )
    previous = _configure(workspace)
    monkeypatch.setattr(workspace_shell, '_revalidate_task_workspace', lambda _root: None)
    try:
        result = workspace_shell.shell_tool('/usr/bin/python3 probe.py')
    finally:
        _restore(previous)

    assert result['exit_code'] != 0
    assert 'outside-secret' not in result['stdout']


@pytest.mark.skipif(sys.platform != 'darwin', reason='macOS sandbox contract')
def test_workspace_shell_runs_code_that_stays_inside_workspace(monkeypatch, tmp_path):
    (tmp_path / 'probe.py').write_text(
        'from pathlib import Path\nPath("result.txt").write_text("ok")\nprint("ok")\n',
        encoding='utf-8',
    )
    previous = _configure(tmp_path)
    monkeypatch.setattr(workspace_shell, '_revalidate_task_workspace', lambda _root: None)
    try:
        result = workspace_shell.shell_tool('/usr/bin/python3 probe.py')
    finally:
        _restore(previous)

    assert result['exit_code'] == 0, result['stderr']
    assert result['stdout'].strip() == 'ok'
    assert (tmp_path / 'result.txt').read_text(encoding='utf-8') == 'ok'


def test_workspace_shell_revalidates_before_starting_process(monkeypatch, tmp_path):
    calls = []
    previous = _configure(tmp_path)
    monkeypatch.setattr(
        workspace_shell,
        '_revalidate_task_workspace',
        lambda _root: (_ for _ in ()).throw(
            ToolExecutionError('Workspace authorization is no longer active')
        ),
    )
    monkeypatch.setattr(
        workspace_shell,
        '_run_workspace_process',
        lambda *args, **kwargs: calls.append((args, kwargs)),
    )
    try:
        with pytest.raises(ToolExecutionError, match='no longer active'):
            workspace_shell.shell_tool('git status')
    finally:
        _restore(previous)
    assert calls == []


def test_workspace_shell_applies_permission_modes(monkeypatch, tmp_path):
    previous = _configure(tmp_path, 'ask_as_needed')
    monkeypatch.setattr(workspace_shell, '_revalidate_task_workspace', lambda _root: None)
    monkeypatch.setattr(
        workspace_shell,
        '_run_workspace_process',
        lambda *args, **kwargs: subprocess.CompletedProcess(args, 0, '', ''),
    )
    try:
        with pytest.raises(ToolExecutionError) as exc_info:
            workspace_shell.shell_tool('npm install')
        assert exc_info.value.needs_approval is True
        assert workspace_shell.shell_tool('npm install', allow_unsafe=True)['status'] == 'ok'
    finally:
        _restore(previous)


@pytest.mark.parametrize('command', [
    'touch created.txt',
    'mkdir generated',
    'cp source.txt copy.txt',
    'tee output.txt',
    'sed -i s/a/b/ source.txt',
    'git commit -m update',
    '/usr/bin/python3 probe.py',
])
def test_always_ask_requires_approval_for_any_non_read_only_command(tmp_path, command):
    previous = _configure(tmp_path, 'always_ask')
    try:
        with pytest.raises(ToolExecutionError) as exc_info:
            workspace_shell.shell_tool(command)
    finally:
        _restore(previous)

    assert exc_info.value.needs_approval is True


@pytest.mark.skipif(sys.platform != 'darwin', reason='macOS sandbox contract')
def test_workspace_shell_blocks_host_etc_files(monkeypatch, tmp_path):
    (tmp_path / 'probe.py').write_text(
        'from pathlib import Path\nprint(Path("/etc/hosts").read_text())\n',
        encoding='utf-8',
    )
    previous = _configure(tmp_path)
    monkeypatch.setattr(workspace_shell, '_revalidate_task_workspace', lambda _root: None)
    try:
        result = workspace_shell.shell_tool('/usr/bin/python3 probe.py')
    finally:
        _restore(previous)

    assert result['exit_code'] != 0
    assert 'localhost' not in result['stdout']


@pytest.mark.skipif(sys.platform != 'darwin', reason='macOS sandbox contract')
@pytest.mark.parametrize('body', [
    'from pathlib import Path\nPath("target.txt").chmod(0o777)\n',
    'import os\nos.symlink("target.txt", "linked.txt")\n',
])
def test_workspace_shell_blocks_scripted_permission_and_link_changes(
    monkeypatch, tmp_path, body,
):
    (tmp_path / 'target.txt').write_text('content', encoding='utf-8')
    (tmp_path / 'probe.py').write_text(body, encoding='utf-8')
    before_mode = (tmp_path / 'target.txt').stat().st_mode
    previous = _configure(tmp_path, 'allow_all')
    monkeypatch.setattr(workspace_shell, '_revalidate_task_workspace', lambda _root: None)
    try:
        if 'symlink' in body:
            with pytest.raises(ToolExecutionError, match='permanently denied'):
                workspace_shell.shell_tool('/usr/bin/python3 probe.py')
            result = None
        else:
            result = workspace_shell.shell_tool('/usr/bin/python3 probe.py')
    finally:
        _restore(previous)

    if result is not None:
        assert result['exit_code'] != 0
    assert (tmp_path / 'target.txt').stat().st_mode == before_mode
    assert not (tmp_path / 'linked.txt').exists()


@pytest.mark.parametrize('command', [
    'sudo touch file.txt',
    'chmod 777 file.txt',
    'ln -s file.txt linked.txt',
    'mount /dev/disk1 mounted',
    'git reset --hard HEAD',
    'git clean -fd',
    'git config --global user.name Agent',
])
def test_workspace_shell_never_allows_permanent_denials(tmp_path, command):
    previous = _configure(tmp_path, 'allow_all')
    try:
        with pytest.raises(ToolExecutionError, match='permanently denied'):
            workspace_shell.shell_tool(command, allow_unsafe=True)
    finally:
        _restore(previous)
