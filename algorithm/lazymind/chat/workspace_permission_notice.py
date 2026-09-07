from __future__ import annotations

from typing import Any, Optional


_WORKSPACE_TOOL_MARKERS = ('LocalFileToolkit_', 'shell_tool')
_BOUNDARY_FAILURE_MARKERS = (
    'outside the authorized workspace',
    'workspace paths must be relative',
    'path escapes the authorized workspace',
    'workspace path is invalid',
    'workspace authorization is no longer active',
    'workspace authorization changed',
    'workspace path changed',
    'workspace command containment is unavailable',
    'command is permanently denied',
)


def workspace_permission_notice(
    tool_name: str, result: Any, language: str,
) -> Optional[str]:
    if not any(marker in str(tool_name) for marker in _WORKSPACE_TOOL_MARKERS):
        return None
    if not isinstance(result, dict) or result.get('ok') is not False:
        return None
    reason = str(result.get('value') or '').strip()
    if not any(marker in reason.lower() for marker in _BOUNDARY_FAILURE_MARKERS):
        return None
    safe_reason = reason[:500]
    if language == 'zh':
        return f'工作区权限已阻止此操作：{safe_reason}\n'
    return f'Workspace permissions blocked this operation: {safe_reason}\n'
