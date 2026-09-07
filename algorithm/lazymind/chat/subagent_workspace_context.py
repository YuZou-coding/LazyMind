from __future__ import annotations

from copy import deepcopy
from typing import Any


def trusted_subagent_parent_config(config: dict[str, Any]) -> dict[str, Any]:
    workspace_sources = task_workspace_sources(config.get('local_fs_sources'))
    trusted = {
        'user_id': str(config.get('user_id') or '').strip(),
        'conversation_id': str(config.get('conversation_id') or '').strip(),
        'workspace_permission_mode': str(
            config.get('workspace_permission_mode') or 'ask_as_needed'
        ),
        'workspace_permission_version': int(
            config.get('workspace_permission_version') or 1
        ),
        'local_fs_sources': workspace_sources,
    }
    return trusted if workspace_sources else {}


def task_workspace_sources(sources: Any) -> list[dict[str, Any]]:
    return [
        deepcopy(source)
        for source in (sources or [])
        if (
            isinstance(source, dict) and source.get('workspace_id') and
            source.get('relative_paths') is True
        )
    ]
