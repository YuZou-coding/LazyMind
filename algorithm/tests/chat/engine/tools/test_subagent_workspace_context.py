from __future__ import annotations

from lazymind.chat.subagent_workspace_context import (
    task_workspace_sources,
    trusted_subagent_parent_config,
)


def test_trusted_subagent_context_keeps_only_bound_relative_workspace(tmp_path):
    config = {
        'thinking_depth': 'medium',
        'user_id': 'user-1',
        'conversation_id': 'conversation-1',
        'run_id': 'run-1',
        'workspace_permission_mode': 'ask_as_needed',
        'local_fs_sources': [{
            'workspace_id': 'workspace-1',
            'workspace_version': 3,
            'workspace_permission_mode': 'ask_as_needed',
            'workspace_permission_version': 2,
            'source_id': 'local-workspace:workspace-1',
            'paths': [str(tmp_path)],
            'file_extensions': ['*'],
            'relative_paths': True,
        }, {
            'source_id': 'forged-legacy',
            'paths': ['/etc'],
            'file_extensions': ['*'],
        }],
    }

    parent = trusted_subagent_parent_config(config)

    assert parent['local_fs_sources'][0]['workspace_id'] == 'workspace-1'
    assert parent['local_fs_sources'][0]['paths'] == [str(tmp_path)]
    assert all(source.get('workspace_id') for source in parent['local_fs_sources'])


def test_trusted_subagent_context_rejects_legacy_only_sources():
    assert trusted_subagent_parent_config({
        'local_fs_sources': [{'paths': ['/etc'], 'file_extensions': ['*']}],
    }) == {}


def test_bound_work_uses_only_workspace_source(tmp_path):
    sources = [
        {'source_id': 'watcher', 'paths': ['/etc'], 'file_extensions': ['*']},
        {
            'source_id': 'local-workspace:one', 'workspace_id': 'one',
            'relative_paths': True, 'paths': [str(tmp_path)], 'file_extensions': ['*'],
        },
    ]

    assert task_workspace_sources(sources) == [sources[1]]
