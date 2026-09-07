# Copyright (c) 2026 LazyAGI. All rights reserved.
"""Tools for reading, searching, and safely editing local text files.

Provides local directory listing, filename search, text search, file reading,
and file metadata lookup for local files made available to the current request.

Backend: prefers ripgrep_ (``rg``) for ``grep`` and ``glob`` when available;
falls back to Python stdlib otherwise.  The two backends differ in edge-case
behaviour (hidden files, .gitignore, binary-file detection, regex dialect) —
rg is the primary path and Python is a best-effort fallback.

.. _ripgrep: https://github.com/BurntSushi/ripgrep
"""
from __future__ import annotations

import datetime
from dataclasses import dataclass
import errno
import fnmatch
import glob as _glob
import hashlib
import json
import os
import re
import secrets
import shutil
import stat
import subprocess
import threading
from typing import Any, Dict, List, Optional

import lazyllm
import requests
from lazyllm.tools.agent import ToolExecutionError

from lazymind.config import config as _cfg
from lazymind.chat.engine.tools.text_edit import (
    build_exact_replacement,
    replace_exact_text_file,
    write_file_atomically,
)
from lazymind.chat.engine.tools.workspace_win32 import (
    WorkspaceWin32Error,
    lock_workspace_path as lock_win32_workspace_path,
    lock_workspace_parent as lock_win32_workspace_parent,
    open_workspace_file as open_win32_workspace_file,
)

_RG_BINARY = shutil.which('rg') or ''
_RG_TIMEOUT = 30
_MAX_TEXT_BYTES = 20 * 1024 * 1024
_MAX_READ_WINDOW_BYTES = 10 * 1024
_SENSITIVE_EXACT = {
    '.env', '.npmrc', '.pypirc', '.netrc', '.git-credentials', 'credentials.json',
    'kubeconfig', 'id_rsa', 'id_dsa', 'id_ecdsa', 'id_ed25519',
}
_SENSITIVE_SUFFIXES = ('.pem', '.key', '.p12', '.pfx')
_SENSITIVE_ENV_EXAMPLES = {'.env.example', '.env.sample', '.env.template'}
_WRITE_LOCKS_GUARD = threading.Lock()
_WRITE_LOCKS: Dict[str, threading.RLock] = {}


def _write_lock(path: str) -> threading.RLock:
    key = os.path.normcase(os.path.realpath(path))
    with _WRITE_LOCKS_GUARD:
        return _WRITE_LOCKS.setdefault(key, threading.RLock())


@dataclass(frozen=True)
class LocalFSScope:
    source_id: str
    roots: tuple[str, ...]
    file_extensions: frozenset[str]
    relative_paths: bool = True
    workspace_id: Optional[str] = None
    workspace_version: Optional[int] = None
    workspace_permission_mode: str = 'ask_as_needed'
    workspace_permission_version: Optional[int] = None


class LocalFileToolkit:
    """Tools for listing, searching, reading, and safely editing local text files.

    The tools can access only the local files and directories made available
    for the current request.
    """

    __public_apis__ = [
        'ls', 'glob', 'grep', 'read', 'make_dir', 'write', 'append',
        'string_replace', 'info',
    ]

    def _get_scopes(self) -> List[LocalFSScope]:
        config = lazyllm.globals.get('agentic_config') or {}
        sources = config.get('local_fs_sources') or []
        if not isinstance(sources, list):
            return []

        scopes: List[LocalFSScope] = []
        for source in sources:
            if not isinstance(source, dict):
                continue
            source_id = source.get('source_id')
            paths = source.get('paths')
            file_extensions = source.get('file_extensions')
            if not isinstance(source_id, str) or not isinstance(paths, list) or not isinstance(file_extensions, list):
                continue
            roots = tuple(path for path in paths if isinstance(path, str) and path.strip())
            extensions = frozenset(ext for ext in file_extensions if isinstance(ext, str) and ext.strip())
            if roots and extensions:
                relative_paths = bool(source.get('workspace_id') or source.get('relative_paths'))
                scopes.append(LocalFSScope(
                    source_id=source_id, roots=roots, file_extensions=extensions,
                    relative_paths=relative_paths,
                    workspace_id=str(source.get('workspace_id') or '').strip() or None,
                    workspace_version=(
                        int(source['workspace_version'])
                        if source.get('workspace_version') is not None else None
                    ),
                    workspace_permission_mode=str(
                        source.get('workspace_permission_mode') or 'ask_as_needed'
                    ),
                    workspace_permission_version=(
                        int(source['workspace_permission_version'])
                        if source.get('workspace_permission_version') is not None else None
                    ),
                ))
        return scopes

    @staticmethod
    def _authorize_scope(scope: LocalFSScope, operation_class: str) -> None:
        """Revalidate a task workspace immediately before each host operation."""
        if not scope.workspace_id:
            return
        config = lazyllm.globals.get('agentic_config') or {}
        token = os.environ.get('LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN', '').strip()
        core_url = str(_cfg['core_api_url'] or '').strip().rstrip('/')
        user_id = str(config.get('user_id') or '').strip()
        conversation_id = str(config.get('conversation_id') or '').strip()
        execution_id = str(config.get('run_id') or config.get('session_id') or '').strip()
        is_subagent = bool(config.get('is_subagent'))
        actor_type = 'sub_agent' if is_subagent else 'main_agent'
        actor_id = str(
            config.get('subagent_task_id') or config.get('agent_type') or actor_type
        ).strip()
        if not all((token, core_url, user_id, conversation_id, execution_id, actor_id)):
            raise ToolExecutionError('Workspace authorization context is unavailable')
        try:
            with requests.sessions.Session() as session:
                session.trust_env = False
                response = session.post(
                    f'{core_url}/internal/local-workspaces:resolve',
                    json={
                        'conversation_id': conversation_id,
                        'execution_id': execution_id,
                        'actor_type': actor_type,
                        'actor_id': actor_id,
                        'operation_class': operation_class,
                    },
                    headers={
                        'X-User-Id': user_id,
                        'X-LazyMind-Local-Workspace-Token': token,
                    },
                    timeout=min(5, int(_cfg['core_api_timeout'])),
                )
                payload = response.json() if response.ok else {}
        except (requests.RequestException, ValueError) as exc:
            raise ToolExecutionError('Workspace authorization could not be verified') from exc
        data = payload.get('data') if isinstance(payload, dict) else None
        if not response.ok or not isinstance(data, dict):
            raise ToolExecutionError('Workspace authorization is no longer active')
        if (
            str(data.get('workspace_id') or '') != scope.workspace_id or
            int(data.get('workspace_version') or 0) != int(scope.workspace_version or 0) or
            str(data.get('permission_mode') or '') != scope.workspace_permission_mode or
            int(data.get('permission_version') or 0) != int(scope.workspace_permission_version or 0) or
            os.path.realpath(str(data.get('root_path') or '')) != os.path.realpath(scope.roots[0])
        ):
            raise ToolExecutionError('Workspace authorization changed; start a new operation')

    def __key_source__(self) -> Any:
        return self._get_scopes()

    def _resolve_with_scope(
        self, target: str, *, allow_internal_absolute: bool = False,
    ) -> tuple[str, LocalFSScope]:
        """Resolve *target* to an absolute path within a configured source.

        Raises:
            PermissionError: if *target* is outside the allowed set.
        """
        scopes = self._get_scopes()
        if not scopes:
            raise ToolExecutionError('No local filesystem paths are configured')
        target = str(target or '').strip()
        if not target:
            target = '.'
        if '\x00' in target:
            raise ToolExecutionError('Workspace path is invalid')
        portable_target = target.replace('\\', '/')
        relative_only = all(scope.relative_paths for scope in scopes)
        if relative_only and not allow_internal_absolute:
            if (
                os.path.isabs(target) or portable_target.startswith('//') or
                re.match(r'^[A-Za-z]:($|/)', portable_target)
            ):
                raise ToolExecutionError('Workspace paths must be relative')
            if any(part == '..' for part in portable_target.split('/')):
                raise ToolExecutionError('Path escapes the authorized workspace')
        if os.path.isabs(target):
            if not allow_internal_absolute and all(scope.relative_paths for scope in scopes):
                raise ToolExecutionError('Workspace paths must be relative')
            candidates = [
                (target, scope) for scope in scopes
                if allow_internal_absolute or not scope.relative_paths
            ]
        else:
            if target == '..' or target.startswith('../'):
                raise ToolExecutionError('Path escapes the authorized workspace')
            ordered_scopes = sorted(scopes, key=lambda item: not item.relative_paths)
            candidates = [
                (os.path.join(root, target), scope)
                for scope in ordered_scopes for root in scope.roots
            ]
        for candidate, scope in candidates:
            target_path = os.path.realpath(candidate)
            for root in scope.roots:
                base = os.path.realpath(root)
                try:
                    if os.path.commonpath([base, target_path]) == base:
                        return target_path, scope
                except ValueError:
                    continue
        raise ToolExecutionError('Path is outside the authorized workspace')

    @staticmethod
    def _relative(path: str, scope: LocalFSScope) -> str:
        if not scope.relative_paths:
            return path
        for root in scope.roots:
            base = os.path.realpath(root)
            try:
                if os.path.commonpath([base, path]) == base:
                    relative = os.path.relpath(path, base)
                    return '.' if relative == '.' else relative
            except ValueError:
                continue
        raise ToolExecutionError('Path is outside the authorized workspace')

    @staticmethod
    def _open_workspace_parent(path: str, scope: LocalFSScope) -> tuple[int, str]:
        if not scope.relative_paths:
            return os.open(os.path.dirname(path), os.O_RDONLY), os.path.basename(path)
        if os.name != 'posix':
            raise ToolExecutionError('Workspace path cannot be opened safely on this platform')
        root = next((
            os.path.realpath(candidate) for candidate in scope.roots
            if os.path.commonpath([os.path.realpath(candidate), path]) == os.path.realpath(candidate)
        ), None)
        if not root:
            raise ToolExecutionError('Path is outside the authorized workspace')
        relative = os.path.relpath(path, root)
        parts = relative.split(os.sep)
        if relative in ('', '.') or any(part in ('', '.', '..') for part in parts):
            raise ToolExecutionError('Workspace path cannot be opened safely')
        flags = os.O_RDONLY | getattr(os, 'O_DIRECTORY', 0) | getattr(os, 'O_NOFOLLOW', 0)
        opened = []
        try:
            current = os.open(root, flags)
            opened.append(current)
            for part in parts[:-1]:
                current = os.open(part, flags, dir_fd=current)
                opened.append(current)
            opened.pop()
            return current, parts[-1]
        except OSError as exc:
            raise ToolExecutionError('Workspace path changed or contains a symlink') from exc
        finally:
            for descriptor in reversed(opened):
                try:
                    os.close(descriptor)
                except OSError:
                    pass

    @staticmethod
    def _open_workspace_fd(path: str, scope: LocalFSScope, flags: int, mode: int = 0o644) -> int:
        if scope.relative_paths and os.name == 'nt':
            try:
                return open_win32_workspace_file(path, scope.roots[0], flags, mode)
            except WorkspaceWin32Error as exc:
                raise ToolExecutionError('Workspace path changed or contains a link') from exc
        parent, name = LocalFileToolkit._open_workspace_parent(path, scope)
        try:
            return os.open(name, flags | getattr(os, 'O_NOFOLLOW', 0), mode, dir_fd=parent)
        except OSError as exc:
            raise ToolExecutionError('Workspace path changed or contains a symlink') from exc
        finally:
            os.close(parent)

    @staticmethod
    def _open_workspace_dir(path: str, scope: LocalFSScope) -> int:
        if scope.relative_paths and os.name == 'nt':
            raise ToolExecutionError('Workspace directory requires a guarded path operation')
        flags = os.O_RDONLY | getattr(os, 'O_DIRECTORY', 0) | getattr(os, 'O_NOFOLLOW', 0)
        roots = [os.path.realpath(candidate) for candidate in scope.roots]
        if path in roots:
            try:
                return os.open(path, flags)
            except OSError as exc:
                raise ToolExecutionError('Workspace path changed or contains a symlink') from exc
        parent, name = LocalFileToolkit._open_workspace_parent(path, scope)
        try:
            return os.open(name, flags, dir_fd=parent)
        except OSError as exc:
            raise ToolExecutionError('Workspace path changed or contains a symlink') from exc
        finally:
            os.close(parent)

    def _atomic_write_workspace(
        self, path: str, scope: LocalFSScope, content: bytes, expected_version: str,
    ) -> None:
        if os.name == 'nt':
            self._atomic_write_workspace_windows(path, scope, content, expected_version)
            return
        parent, name = self._open_workspace_parent(path, scope)
        temp_name = f'.{name}.{secrets.token_hex(8)}.tmp'
        temp_fd = None
        try:
            try:
                current_fd = os.open(name, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0), dir_fd=parent)
            except OSError as exc:
                raise ToolExecutionError('Workspace target changed before commit') from exc
            try:
                current_stat = os.fstat(current_fd)
                digest = hashlib.sha256()
                while chunk := os.read(current_fd, 1024 * 1024):
                    digest.update(chunk)
                if digest.hexdigest() != expected_version:
                    raise ToolExecutionError('File version conflict; read the file again before modifying it')
            finally:
                os.close(current_fd)
            temp_fd = os.open(
                temp_name,
                os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0),
                0o600,
                dir_fd=parent,
            )
            view = memoryview(content)
            while view:
                written = os.write(temp_fd, view)
                view = view[written:]
            os.fsync(temp_fd)
            os.fchmod(temp_fd, stat.S_IMODE(current_stat.st_mode))
            os.close(temp_fd)
            temp_fd = None
            self._authorize_scope(scope, 'write')
            verify_fd = os.open(name, os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0), dir_fd=parent)
            try:
                verify_stat = os.fstat(verify_fd)
                verify_digest = hashlib.sha256()
                while chunk := os.read(verify_fd, 1024 * 1024):
                    verify_digest.update(chunk)
                if (
                    verify_digest.hexdigest() != expected_version or
                    (verify_stat.st_dev, verify_stat.st_ino) != (current_stat.st_dev, current_stat.st_ino)
                ):
                    raise ToolExecutionError(
                        'File version conflict; read the file again before modifying it'
                    )
            finally:
                os.close(verify_fd)
            os.replace(temp_name, name, src_dir_fd=parent, dst_dir_fd=parent)
            os.fsync(parent)
            temp_name = ''
        except ToolExecutionError:
            raise
        except OSError as exc:
            raise ToolExecutionError('Workspace file could not be committed safely') from exc
        finally:
            if temp_fd is not None:
                try:
                    os.close(temp_fd)
                except OSError:
                    pass
            if temp_name:
                try:
                    os.unlink(temp_name, dir_fd=parent)
                except OSError:
                    pass
            os.close(parent)

    def _atomic_write_workspace_windows(
        self, path: str, scope: LocalFSScope, content: bytes, expected_version: str,
    ) -> None:
        temp_path = os.path.join(
            os.path.dirname(path), f'.{os.path.basename(path)}.{secrets.token_hex(8)}.tmp',
        )
        try:
            with lock_win32_workspace_parent(path, scope.roots[0]):
                if self._version_for_scope(path, scope) != expected_version:
                    raise ToolExecutionError(
                        'File version conflict; read the file again before modifying it'
                    )
                descriptor = open_win32_workspace_file(
                    temp_path, scope.roots[0], os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600,
                )
                with os.fdopen(descriptor, 'wb') as handle:
                    handle.write(content)
                    handle.flush()
                    os.fsync(handle.fileno())
                self._authorize_scope(scope, 'write')
                if self._version_for_scope(path, scope) != expected_version:
                    raise ToolExecutionError(
                        'File version conflict; read the file again before modifying it'
                    )
                os.replace(temp_path, path)
                temp_path = ''
        except ToolExecutionError:
            raise
        except (OSError, WorkspaceWin32Error) as exc:
            raise ToolExecutionError('Workspace file could not be committed safely') from exc
        finally:
            if temp_path:
                try:
                    os.unlink(temp_path)
                except OSError:
                    pass

    def _atomic_create_workspace(
        self, path: str, scope: LocalFSScope, content: bytes, mode: int = 0o644,
    ) -> None:
        if os.name == 'nt':
            self._atomic_create_workspace_windows(path, scope, content, mode)
            return
        parent, name = self._open_workspace_parent(path, scope)
        temp_name = f'.{name}.{secrets.token_hex(8)}.tmp'
        temp_fd = None
        try:
            temp_fd = os.open(
                temp_name,
                os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0),
                0o600,
                dir_fd=parent,
            )
            view = memoryview(content)
            while view:
                written = os.write(temp_fd, view)
                view = view[written:]
            os.fsync(temp_fd)
            os.fchmod(temp_fd, mode)
            os.close(temp_fd)
            temp_fd = None
            self._authorize_scope(scope, 'write')
            os.link(
                temp_name, name,
                src_dir_fd=parent, dst_dir_fd=parent, follow_symlinks=False,
            )
            os.unlink(temp_name, dir_fd=parent)
            temp_name = ''
            os.fsync(parent)
        except FileExistsError as exc:
            raise ToolExecutionError(
                'File already exists; set overwrite=true and provide its version to replace it'
            ) from exc
        except ToolExecutionError:
            raise
        except OSError as exc:
            raise ToolExecutionError('Workspace file could not be committed safely') from exc
        finally:
            if temp_fd is not None:
                try:
                    os.close(temp_fd)
                except OSError:
                    pass
            if temp_name:
                try:
                    os.unlink(temp_name, dir_fd=parent)
                except OSError:
                    pass
            os.close(parent)

    def _atomic_create_workspace_windows(
        self, path: str, scope: LocalFSScope, content: bytes, mode: int,
    ) -> None:
        temp_path = os.path.join(
            os.path.dirname(path), f'.{os.path.basename(path)}.{secrets.token_hex(8)}.tmp',
        )
        try:
            with lock_win32_workspace_parent(path, scope.roots[0]):
                if os.path.lexists(path):
                    raise FileExistsError(path)
                descriptor = open_win32_workspace_file(
                    temp_path, scope.roots[0], os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600,
                )
                with os.fdopen(descriptor, 'wb') as handle:
                    handle.write(content)
                    handle.flush()
                    os.fsync(handle.fileno())
                os.chmod(temp_path, mode)
                self._authorize_scope(scope, 'write')
                if os.path.lexists(path):
                    raise FileExistsError(path)
                os.rename(temp_path, path)
                temp_path = ''
        except FileExistsError as exc:
            raise ToolExecutionError(
                'File already exists; set overwrite=true and provide its version to replace it'
            ) from exc
        except ToolExecutionError:
            raise
        except (OSError, WorkspaceWin32Error) as exc:
            raise ToolExecutionError('Workspace file could not be committed safely') from exc
        finally:
            if temp_path:
                try:
                    os.unlink(temp_path)
                except OSError:
                    pass

    def _read_workspace_bytes(self, path: str, scope: LocalFSScope) -> bytes:
        descriptor = self._open_workspace_fd(path, scope, os.O_RDONLY)
        try:
            content = bytearray()
            while len(content) <= _MAX_TEXT_BYTES:
                chunk = os.read(descriptor, min(1024 * 1024, _MAX_TEXT_BYTES + 1 - len(content)))
                if not chunk:
                    break
                content.extend(chunk)
            if len(content) > _MAX_TEXT_BYTES:
                raise ToolExecutionError('Text file exceeds the 20 MiB limit')
            return bytes(content)
        finally:
            os.close(descriptor)

    def _version_for_scope(self, path: str, scope: LocalFSScope) -> str:
        if scope.workspace_id:
            return hashlib.sha256(self._read_workspace_bytes(path, scope)).hexdigest()
        return self._version(path)

    def _make_workspace_dir(self, path: str, scope: LocalFSScope) -> None:
        if not scope.relative_paths:
            os.makedirs(path, mode=0o755, exist_ok=True)
            return
        if os.name == 'nt':
            root = os.path.realpath(scope.roots[0])
            relative = os.path.relpath(path, root)
            parts = relative.replace('\\', '/').split('/')
            if relative in ('', '.'):
                return
            if any(part in ('', '.', '..') for part in parts):
                raise ToolExecutionError('Workspace directory cannot be created safely')
            current = root
            for part in parts:
                candidate = os.path.join(current, part)
                try:
                    with lock_win32_workspace_parent(candidate, root):
                        if os.path.lexists(candidate):
                            if os.path.islink(candidate) or not os.path.isdir(candidate):
                                raise ToolExecutionError(
                                    'Workspace path changed or contains a link'
                                )
                        else:
                            self._authorize_scope(scope, 'write')
                            os.mkdir(candidate, mode=0o755)
                    current = candidate
                except WorkspaceWin32Error as exc:
                    raise ToolExecutionError(
                        'Workspace path changed or contains a link'
                    ) from exc
            return
        if os.name != 'posix':
            raise ToolExecutionError('Workspace directory cannot be created safely on this platform')
        root = next((
            os.path.realpath(candidate) for candidate in scope.roots
            if os.path.commonpath([os.path.realpath(candidate), path]) == os.path.realpath(candidate)
        ), None)
        if not root:
            raise ToolExecutionError('Path is outside the authorized workspace')
        relative = os.path.relpath(path, root)
        if relative in ('', '.'):
            return
        parts = relative.split(os.sep)
        if any(part in ('', '.', '..') for part in parts):
            raise ToolExecutionError('Workspace directory cannot be created safely')
        flags = os.O_RDONLY | getattr(os, 'O_DIRECTORY', 0) | getattr(os, 'O_NOFOLLOW', 0)
        current = os.open(root, flags)
        try:
            for part in parts:
                try:
                    next_fd = os.open(part, flags, dir_fd=current)
                except OSError as exc:
                    if exc.errno != errno.ENOENT:
                        raise ToolExecutionError('Workspace path changed or contains a symlink') from exc
                    self._authorize_scope(scope, 'write')
                    os.mkdir(part, mode=0o755, dir_fd=current)
                    next_fd = os.open(part, flags, dir_fd=current)
                os.close(current)
                current = next_fd
        finally:
            os.close(current)

    def _resolve_dir(self, path: str) -> tuple[str, LocalFSScope]:
        resolved, scope = self._resolve_with_scope(path)
        if not os.path.isdir(resolved):
            raise ToolExecutionError(f'Path is not a directory: {path}')
        return resolved, scope

    def _iter_roots(self, path: Optional[str]) -> list[tuple[str, LocalFSScope]]:
        scopes = self._get_scopes()
        if not scopes:
            raise ToolExecutionError('No local filesystem paths are configured')
        if path is None or str(path).strip() in ('', '.'):
            roots: list[tuple[str, LocalFSScope]] = []
            for scope in scopes:
                for root in scope.roots:
                    resolved = os.path.realpath(root)
                    if os.path.isdir(resolved):
                        roots.append((resolved, scope))
            return roots
        return [self._resolve_dir(str(path))]

    @staticmethod
    def _file_extension(path: str) -> str:
        return os.path.splitext(path)[1].lower().lstrip('.')

    def _is_visible_file(self, scope: LocalFSScope, path: str) -> bool:
        return '*' in scope.file_extensions or self._file_extension(path) in scope.file_extensions

    def _ensure_visible_file(self, scope: LocalFSScope, path: str) -> None:
        if not self._is_visible_file(scope, path):
            raise ToolExecutionError(f'File extension is not allowed: {path}')

    @staticmethod
    def _is_sensitive(path: str) -> bool:
        name = os.path.basename(path).lower()
        if name in _SENSITIVE_ENV_EXAMPLES:
            return False
        return (
            name in _SENSITIVE_EXACT or name.startswith('.env.') or
            name.startswith('service-account') and name.endswith('.json') or
            name.endswith(_SENSITIVE_SUFFIXES)
        )

    def _sensitive_read_allowed(self, path: str, scope: LocalFSScope) -> bool:
        if self._permission_mode(scope) == 'allow_all':
            return True
        config = lazyllm.globals.get('agentic_config') or {}
        approved = config.get('approved_sensitive_paths') or []
        relative = self._relative(path, scope)
        return relative in approved

    @staticmethod
    def _permission_mode(scope: LocalFSScope) -> str:
        config = lazyllm.globals.get('agentic_config') or {}
        mode = str(
            config.get('workspace_permission_mode') or
            scope.workspace_permission_mode or
            'ask_as_needed'
        )
        return mode if mode in {'always_ask', 'ask_as_needed', 'allow_all'} else 'ask_as_needed'

    def _require_write_approval(self, path: str, scope: LocalFSScope) -> None:
        if self._permission_mode(scope) != 'always_ask':
            return
        config = lazyllm.globals.get('agentic_config') or {}
        approved = config.get('approved_workspace_write_paths') or []
        relative = self._relative(path, scope)
        if relative not in approved:
            raise ToolExecutionError.approval_required(
                f'Changing workspace path {relative!r} requires approval for this operation.'
            )

    @staticmethod
    def _ensure_write_target_allowed(path: str, scope: LocalFSScope) -> None:
        relative = LocalFileToolkit._relative(path, scope).replace('\\', '/')
        if relative == '.git' or relative.startswith('.git/'):
            raise ToolExecutionError('Git internal files are read-only')
        if LocalFileToolkit._is_sensitive(path):
            raise ToolExecutionError('Sensitive files are read-only')

    @staticmethod
    def _version(path: str) -> str:
        digest = hashlib.sha256()
        with open(path, 'rb') as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b''):
                digest.update(chunk)
        return digest.hexdigest()

    def _resolve_visible_file(self, path: str) -> Optional[tuple[str, LocalFSScope]]:
        try:
            resolved, scope = self._resolve_with_scope(path, allow_internal_absolute=True)
            if os.path.isfile(resolved) and self._is_visible_file(scope, resolved):
                return resolved, scope
        except OSError:
            return None
        return None

    def _resolve_visible_file_for_scope(self, path: str, scope: LocalFSScope) -> Optional[str]:
        visible = self._resolve_visible_file(path)
        if not visible:
            return None
        resolved, resolved_scope = visible
        if resolved_scope != scope:
            return None
        return resolved

    def _entry(self, path: str, scope: LocalFSScope) -> Dict[str, Any]:
        st = os.stat(path, follow_symlinks=False)
        return {
            'name': os.path.basename(path),
            'path': self._relative(path, scope),
            'type': 'directory' if os.path.isdir(path) else 'file',
            'source_id': scope.source_id,
            'size': st.st_size,
            'mtime': datetime.datetime.fromtimestamp(st.st_mtime).isoformat(),
        }

    @staticmethod
    def _has_rg() -> bool:
        return bool(_RG_BINARY)

    @staticmethod
    def _run_rg(args: List[str], cwd: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            [_RG_BINARY] + args,
            capture_output=True, text=True, timeout=_RG_TIMEOUT, cwd=cwd,
        )

    def ls(self, path: Optional[str] = None, max_entries: int = 200) -> Dict[str, Any]:
        """List available local directories or one directory level.

        Args:
            path: Directory path. When omitted, lists available local root directories.
            max_entries: Maximum entries to return, default 200.

        Returns:
            A directory listing with entry paths, types, sizes, update times,
            and pagination metadata.
        """
        entries: List[Dict[str, Any]] = []
        limit = min(200, max(1, max_entries))

        if path is None or str(path).strip() in ('', '.'):
            roots = self._iter_roots(None)
            if len(roots) == 1:
                safe_dir, scope = roots[0]
                self._authorize_scope(scope, 'read')
            else:
                for root, scope in roots:
                    self._authorize_scope(scope, 'read')
                    entries.append(self._entry(root, scope))
                    if len(entries) >= limit:
                        break
                return {
                    'path': None,
                    'entry_count': len(entries),
                    'truncated': len(entries) >= limit,
                    'max_entries': limit,
                    'entries': entries,
                }
        else:
            safe_dir, scope = self._resolve_dir(str(path))
            self._authorize_scope(scope, 'read')

        if scope.workspace_id and os.name == 'nt':
            try:
                with lock_win32_workspace_path(safe_dir, scope.roots[0], include_leaf=True):
                    with os.scandir(safe_dir) as iterator:
                        for entry in sorted(iterator, key=lambda item: item.name):
                            try:
                                entry_stat = entry.stat(follow_symlinks=False)
                                if entry.is_symlink():
                                    continue
                                entry_path = os.path.join(safe_dir, entry.name)
                                if stat.S_ISDIR(entry_stat.st_mode) or (
                                    stat.S_ISREG(entry_stat.st_mode) and self._is_visible_file(scope, entry_path)
                                ):
                                    entries.append({
                                        'name': entry.name,
                                        'path': self._relative(entry_path, scope),
                                        'type': 'directory' if stat.S_ISDIR(entry_stat.st_mode) else 'file',
                                        'source_id': scope.source_id,
                                        'size': entry_stat.st_size,
                                        'mtime': datetime.datetime.fromtimestamp(entry_stat.st_mtime).isoformat(),
                                    })
                            except OSError:
                                continue
                            if len(entries) >= limit:
                                break
            except WorkspaceWin32Error as exc:
                raise ToolExecutionError('Workspace path changed or contains a link') from exc
        elif scope.workspace_id:
            descriptor = self._open_workspace_dir(safe_dir, scope)
            try:
                with os.scandir(descriptor) as iterator:
                    for entry in sorted(iterator, key=lambda item: item.name):
                        try:
                            entry_stat = entry.stat(follow_symlinks=False)
                            if stat.S_ISLNK(entry_stat.st_mode):
                                continue
                            entry_path = os.path.join(safe_dir, entry.name)
                            if stat.S_ISDIR(entry_stat.st_mode) or (
                                stat.S_ISREG(entry_stat.st_mode) and self._is_visible_file(scope, entry_path)
                            ):
                                entries.append({
                                    'name': entry.name,
                                    'path': self._relative(entry_path, scope),
                                    'type': 'directory' if stat.S_ISDIR(entry_stat.st_mode) else 'file',
                                    'source_id': scope.source_id,
                                    'size': entry_stat.st_size,
                                    'mtime': datetime.datetime.fromtimestamp(entry_stat.st_mtime).isoformat(),
                                })
                        except OSError:
                            continue
                        if len(entries) >= limit:
                            break
            finally:
                os.close(descriptor)
        else:
            with os.scandir(safe_dir) as iterator:
                for entry in sorted(iterator, key=lambda item: item.name):
                    try:
                        entry_path, entry_scope = self._resolve_with_scope(
                            entry.path, allow_internal_absolute=True,
                        )
                        if entry.is_dir(follow_symlinks=True):
                            entries.append(self._entry(entry_path, entry_scope))
                        elif entry.is_file(follow_symlinks=True) and self._is_visible_file(entry_scope, entry_path):
                            entries.append(self._entry(entry_path, entry_scope))
                    except (OSError, ToolExecutionError):
                        continue
                    if len(entries) >= limit:
                        break

        return {
            'path': self._relative(safe_dir, scope),
            'source_id': scope.source_id,
            'entry_count': len(entries),
            'truncated': len(entries) >= limit,
            'max_entries': limit,
            'entries': entries,
        }

    def glob(self, pattern: str, path: Optional[str] = None) -> Dict[str, Any]:
        """Find local files whose names match a glob pattern.

        Args:
            pattern: Glob pattern, e.g. ``**/*.pdf`` or ``*.csv``.
            path: Optional directory returned by ls. Omit path to search all
                available local directories; do not pass a shared parent directory.

        Returns:
            A list of matching local file paths.
        """
        matches: List[str] = []
        for safe_dir, scope in self._iter_roots(path):
            self._authorize_scope(scope, 'read')
            if self._has_rg():
                proc = self._run_rg(['--files', '--no-ignore', '--hidden', '--glob', pattern], cwd=safe_dir)
                if proc.returncode > 1:
                    raise ToolExecutionError(
                        f'ripgrep glob failed: {proc.stderr.strip() or "unknown error"}'
                    )
                raw = [os.path.join(safe_dir, p) for p in proc.stdout.splitlines() if p.strip()]
            else:
                py_pattern = pattern if '**' in pattern else f'**/{pattern}'
                raw = [os.path.join(safe_dir, p) for p in _glob.glob(py_pattern, root_dir=safe_dir, recursive=True)]
            for fpath in raw:
                resolved = self._resolve_visible_file_for_scope(fpath, scope)
                if resolved:
                    matches.append(self._relative(resolved, scope))
        matches.sort()
        return {
            'pattern': pattern,
            'path': path,
            'match_count': len(matches),
            'matches': matches[:200],
        }

    def grep(
        self,
        pattern: str,
        path: Optional[str] = None,
        glob: str = '*',
        max_results: int = 50,
    ) -> Dict[str, Any]:
        """Search text within available local files.

        Args:
            pattern: Regex search pattern.
            path: Optional directory returned by ls. Omit path to search all
                available local directories; do not pass a shared parent directory.
            glob: Filename filter (only search matching files), default ``*``.
            max_results: Maximum results to return, default 50.

        Returns:
            Matching lines with file path, line number, and text snippet.
        """
        max_results = min(200, max(1, max_results))
        matches: List[Dict[str, Any]] = []
        for safe_dir, scope in self._iter_roots(path):
            self._authorize_scope(scope, 'read')
            if self._has_rg():
                result = self._grep_rg(pattern, safe_dir, scope, glob, max_results - len(matches))
            else:
                result = self._grep_py(pattern, safe_dir, scope, glob, max_results - len(matches))
            matches.extend(result.get('matches', []))
            if len(matches) >= max_results:
                break
        return {
            'pattern': pattern,
            'path': path,
            'match_count': len(matches),
            'matches': matches,
        }

    def _grep_rg(
        self, pattern: str, safe_dir: str, scope: LocalFSScope, glob_filter: str, max_results: int,
    ) -> Dict[str, Any]:
        if max_results <= 0:
            return {'matches': []}
        args = ['--json', '--no-heading', '--no-ignore', '--hidden', '-g', glob_filter, '--', pattern]
        try:
            proc = self._run_rg(args, cwd=safe_dir)
        except subprocess.TimeoutExpired:
            raise ToolExecutionError(
                f'Search timed out after {_RG_TIMEOUT} seconds.'
            )

        if proc.returncode > 1:
            raise ToolExecutionError(
                f'ripgrep search failed: {proc.stderr.strip() or "unknown error"}'
            )

        matches: List[Dict[str, Any]] = []
        for line in proc.stdout.splitlines():
            if not line.strip():
                continue
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue
            if entry.get('type') != 'match':
                continue
            data = entry.get('data', {})
            fpath = os.path.join(safe_dir, data.get('path', {}).get('text', ''))
            resolved = self._resolve_visible_file_for_scope(fpath, scope)
            if not resolved:
                continue
            if self._is_sensitive(resolved) and not self._sensitive_read_allowed(resolved, scope):
                continue
            content = data.get('lines', {}).get('text', '').rstrip()
            lineno = data.get('line_number', 0)
            matches.append({
                'file': self._relative(resolved, scope),
                'source_id': scope.source_id,
                'line': lineno,
                'content': content[:500],
            })
            if len(matches) >= max_results:
                break

        return {
            'pattern': pattern,
            'path': safe_dir,
            'match_count': len(matches),
            'matches': matches,
        }

    def _grep_py(
        self, pattern: str, safe_dir: str, scope: LocalFSScope, glob_filter: str, max_results: int,
    ) -> Dict[str, Any]:
        if max_results <= 0:
            return {'matches': []}
        try:
            regex = re.compile(pattern)
        except re.error as exc:
            raise ToolExecutionError(f'Invalid regex: {exc}') from exc

        matches: List[Dict[str, Any]] = []
        for root, _dirs, files in os.walk(safe_dir):
            for fn in files:
                if not fnmatch.fnmatch(fn, glob_filter):
                    continue
                fpath = os.path.join(root, fn)
                resolved = self._resolve_visible_file_for_scope(fpath, scope)
                if not resolved:
                    continue
                if self._is_sensitive(resolved) and not self._sensitive_read_allowed(resolved, scope):
                    continue
                try:
                    with open(resolved, 'r', encoding='utf-8', errors='replace') as fh:
                        for lineno, line in enumerate(fh, 1):
                            if regex.search(line):
                                matches.append({
                                    'file': self._relative(resolved, scope),
                                    'source_id': scope.source_id,
                                    'line': lineno,
                                    'content': line.rstrip()[:500],
                                })
                                if len(matches) >= max_results:
                                    break
                except OSError:
                    continue
                if len(matches) >= max_results:
                    break
            if len(matches) >= max_results:
                break

        return {
            'pattern': pattern,
            'path': safe_dir,
            'match_count': len(matches),
            'matches': matches,
        }

    def read(
        self,
        filepath: str,
        start_line: int = 0,
        max_lines: int = 2000,
    ) -> Dict[str, Any]:
        """Read text content from an available local file.

        Args:
            filepath: Local file path to read.
            start_line: Starting line number (0-based), default 0.
            max_lines: Maximum lines to read, default 500.

        Returns:
            File content plus line range and total line count metadata.
        """
        safe_path, scope = self._resolve_with_scope(filepath)
        self._authorize_scope(scope, 'read')
        if not os.path.isfile(safe_path):
            raise ToolExecutionError(f'File not found: {filepath}')
        self._ensure_visible_file(scope, safe_path)
        if self._is_sensitive(safe_path) and not self._sensitive_read_allowed(safe_path, scope):
            raise ToolExecutionError.approval_required(
                f'Reading sensitive file {self._relative(safe_path, scope)!r} requires one-time approval.'
            )
        if os.path.getsize(safe_path) > _MAX_TEXT_BYTES:
            raise ToolExecutionError('Text file exceeds the 20 MiB limit')

        start_line = max(0, int(start_line))
        max_lines = min(4000, max(1, int(max_lines)))

        try:
            chunk: List[str] = []
            if scope.workspace_id:
                snapshot = self._read_workspace_bytes(safe_path, scope)
                lines = snapshot.decode('utf-8', errors='replace').splitlines(keepends=True)
                total = len(lines)
                window_bytes = 0
                for line in lines[start_line:start_line + max_lines]:
                    line_bytes = len(line.encode('utf-8'))
                    if window_bytes + line_bytes > _MAX_READ_WINDOW_BYTES:
                        break
                    chunk.append(line)
                    window_bytes += line_bytes
                version = hashlib.sha256(snapshot).hexdigest()
            else:
                descriptor = self._open_workspace_fd(safe_path, scope, os.O_RDONLY)
                with os.fdopen(descriptor, 'r', encoding='utf-8', errors='replace') as fh:
                    total = 0
                    for index, line in enumerate(fh):
                        total += 1
                        if start_line <= index < start_line + max_lines:
                            candidate = ''.join(chunk) + line
                            if len(candidate.encode('utf-8')) > _MAX_READ_WINDOW_BYTES:
                                break
                            chunk.append(line)
                version = self._version_for_scope(safe_path, scope)
        except OSError as exc:
            raise ToolExecutionError(f'Cannot read file: {exc}') from exc

        return {
            'path': self._relative(safe_path, scope),
            'filepath': self._relative(safe_path, scope),
            'source_id': scope.source_id,
            'total_lines': total,
            'start_line': start_line,
            'end_line': start_line + len(chunk),
            'content': ''.join(chunk),
            'version': version,
        }

    def string_replace(
        self,
        filepath: str,
        old_string: str,
        new_string: str,
        expected_replacements: int = 1,
        encoding: str = 'utf-8',
        expected_version: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Replace an exact string in an available local text file.

        The file is changed only when the number of exact matches equals
        ``expected_replacements``. Use a multiline old_string with enough
        surrounding context to make a local edit unambiguous.

        Args:
            filepath: Local text file path to edit.
            old_string: Exact literal text to replace; must not be empty.
            new_string: Replacement text, which may be empty.
            expected_replacements: Required number of exact matches, default 1.
            encoding: Text encoding used to decode and encode the file, default utf-8.
            expected_version: Version returned by read/info; required for workspace edits.

        Returns:
            Replacement count and updated file metadata. On mismatch or any
            error, the original file remains unchanged.
        """
        safe_path, scope = self._resolve_with_scope(filepath)
        self._authorize_scope(scope, 'write')
        self._require_write_approval(safe_path, scope)
        if not os.path.isfile(safe_path):
            raise ToolExecutionError(f'File not found: {filepath}')
        self._ensure_visible_file(scope, safe_path)
        self._ensure_write_target_allowed(safe_path, scope)
        if os.path.getsize(safe_path) > _MAX_TEXT_BYTES:
            raise ToolExecutionError('Text file exceeds the 20 MiB limit')

        with _write_lock(safe_path):
            if scope.workspace_id and not expected_version:
                raise ToolExecutionError('expected_version is required when modifying a workspace file')
            if expected_version and self._version_for_scope(safe_path, scope) != expected_version:
                raise ToolExecutionError('File version conflict; read the file again before modifying it')
            self._authorize_scope(scope, 'write')
            try:
                if scope.workspace_id:
                    replacement = build_exact_replacement(
                        self._read_workspace_bytes(safe_path, scope),
                        old_string,
                        new_string,
                        expected_replacements=expected_replacements,
                        encoding=encoding,
                    )
                    self._atomic_write_workspace(
                        safe_path, scope, replacement.content, expected_version or '',
                    )
                else:
                    replacement = replace_exact_text_file(
                        safe_path,
                        old_string,
                        new_string,
                        expected_replacements=expected_replacements,
                        encoding=encoding,
                    )
            except ValueError as exc:
                raise ToolExecutionError(str(exc)) from exc

        return {
            'path': self._relative(safe_path, scope),
            'filepath': self._relative(safe_path, scope),
            'source_id': scope.source_id,
            'replacements': replacement.replacements,
            'encoding': replacement.encoding,
            'bytes': len(replacement.content),
            'version': self._version_for_scope(safe_path, scope),
        }

    def info(self, path: Optional[str] = None) -> Dict[str, Any]:
        """Get metadata for an available local file or directory.

        Args:
            path: Local file or directory path. When omitted, returns metadata for
                available local root directories.

        Returns:
            File or directory metadata such as path, type, size, and update time.
        """
        if path is None or str(path).strip() in ('', '.'):
            entries = []
            for root, scope in self._iter_roots(None):
                self._authorize_scope(scope, 'read')
                entries.append(self._entry(root, scope))
            return {'path': None, 'entries': entries}
        else:
            safe_path, scope = self._resolve_with_scope(str(path))
            self._authorize_scope(scope, 'read')
            if os.path.isfile(safe_path):
                self._ensure_visible_file(scope, safe_path)

        try:
            if scope.workspace_id:
                if os.name == 'nt' and os.path.isdir(safe_path):
                    with lock_win32_workspace_path(
                        safe_path, scope.roots[0], include_leaf=True,
                    ):
                        st = os.stat(safe_path, follow_symlinks=False)
                else:
                    descriptor = (
                        self._open_workspace_dir(safe_path, scope)
                        if os.path.isdir(safe_path)
                        else self._open_workspace_fd(safe_path, scope, os.O_RDONLY)
                    )
                    try:
                        st = os.fstat(descriptor)
                        if stat.S_ISREG(st.st_mode):
                            digest = hashlib.sha256()
                            while chunk := os.read(descriptor, 1024 * 1024):
                                digest.update(chunk)
                            version = digest.hexdigest()
                    finally:
                        os.close(descriptor)
            else:
                st = os.stat(safe_path)
        except (OSError, WorkspaceWin32Error) as exc:
            raise ToolExecutionError('Cannot get file info safely') from exc

        result = {
            'path': self._relative(safe_path, scope),
            'type': 'directory' if stat.S_ISDIR(st.st_mode) else 'file',
            'source_id': scope.source_id,
            'size': st.st_size,
            'mtime': datetime.datetime.fromtimestamp(st.st_mtime).isoformat(),
        }
        if stat.S_ISREG(st.st_mode):
            result['version'] = version if scope.workspace_id else self._version_for_scope(safe_path, scope)
        return result

    def make_dir(self, path: str) -> Dict[str, Any]:
        """Create a directory and parents inside the authorized workspace."""
        safe_path, scope = self._resolve_with_scope(path)
        self._authorize_scope(scope, 'write')
        self._require_write_approval(safe_path, scope)
        self._ensure_write_target_allowed(safe_path, scope)
        try:
            self._make_workspace_dir(safe_path, scope)
        except OSError as exc:
            raise ToolExecutionError(f'Cannot create directory: {exc}') from exc
        return {'path': self._relative(safe_path, scope), 'source_id': scope.source_id}

    def write(
        self,
        filepath: str,
        content: str,
        overwrite: bool = False,
        encoding: str = 'utf-8',
        expected_version: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Create a text file, or explicitly overwrite an existing one.

        Args:
            filepath: Workspace-relative text file path.
            content: Complete text content to write.
            overwrite: Set true only to replace an existing file.
            encoding: Text encoding, default utf-8.
            expected_version: Version returned by read/info; required for workspace overwrite.

        Returns:
            Created flag, byte size, and the new opaque file version.
        """
        safe_path, scope = self._resolve_with_scope(filepath)
        self._authorize_scope(scope, 'write')
        self._require_write_approval(safe_path, scope)
        self._ensure_visible_file(scope, safe_path)
        self._ensure_write_target_allowed(safe_path, scope)
        if len(content.encode(encoding)) > _MAX_TEXT_BYTES:
            raise ToolExecutionError('Text write exceeds the 20 MiB limit')
        parent = os.path.dirname(safe_path)
        if not os.path.isdir(parent):
            raise ToolExecutionError('Parent directory does not exist')
        encoded = content.encode(encoding)
        created = False
        with _write_lock(safe_path):
            try:
                if overwrite:
                    if not os.path.isfile(safe_path):
                        raise ToolExecutionError('File not found; overwrite only applies to an existing file')
                    if scope.workspace_id and not expected_version:
                        raise ToolExecutionError(
                            'expected_version is required when overwriting a workspace file'
                        )
                    if expected_version and self._version_for_scope(safe_path, scope) != expected_version:
                        raise ToolExecutionError(
                            'File version conflict; read the file again before overwriting it'
                        )
                    self._authorize_scope(scope, 'write')
                    if scope.workspace_id:
                        self._atomic_write_workspace(
                            safe_path, scope, encoded, expected_version or '',
                        )
                    else:
                        write_file_atomically(safe_path, encoded)
                else:
                    if expected_version:
                        raise ToolExecutionError('expected_version is not valid when creating a new file')
                    self._authorize_scope(scope, 'write')
                    if scope.workspace_id:
                        self._atomic_create_workspace(safe_path, scope, encoded)
                    else:
                        descriptor = self._open_workspace_fd(
                            safe_path,
                            scope,
                            os.O_WRONLY | os.O_CREAT | os.O_EXCL,
                            0o644,
                        )
                        with os.fdopen(descriptor, 'wb') as handle:
                            handle.write(encoded)
                            handle.flush()
                            os.fsync(handle.fileno())
                    created = True
            except FileExistsError as exc:
                raise ToolExecutionError(
                    'File already exists; set overwrite=true and provide its version to replace it'
                ) from exc
            except OSError as exc:
                raise ToolExecutionError('Cannot write file') from exc
        return {
            'path': self._relative(safe_path, scope), 'source_id': scope.source_id,
            'bytes': os.path.getsize(safe_path), 'created': created,
            'version': self._version_for_scope(safe_path, scope),
        }

    def append(
        self,
        filepath: str,
        content: str,
        encoding: str = 'utf-8',
        expected_version: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Append text to an existing file inside the authorized workspace.

        Args:
            filepath: Workspace-relative existing text file path.
            content: Text to append.
            encoding: Text encoding, default utf-8.
            expected_version: Version returned by read/info; required for workspace append.

        Returns:
            Updated byte size and opaque file version.
        """
        safe_path, scope = self._resolve_with_scope(filepath)
        self._authorize_scope(scope, 'write')
        self._require_write_approval(safe_path, scope)
        if not os.path.isfile(safe_path):
            raise ToolExecutionError(f'File not found: {filepath}')
        self._ensure_visible_file(scope, safe_path)
        self._ensure_write_target_allowed(safe_path, scope)
        with _write_lock(safe_path):
            if scope.workspace_id and not expected_version:
                raise ToolExecutionError('expected_version is required when appending to a workspace file')
            if expected_version and self._version_for_scope(safe_path, scope) != expected_version:
                raise ToolExecutionError('File version conflict; read the file again before appending')
            try:
                if scope.workspace_id:
                    current = self._read_workspace_bytes(safe_path, scope)
                else:
                    with open(safe_path, 'rb') as handle:
                        current = handle.read(_MAX_TEXT_BYTES + 1)
                addition = content.encode(encoding)
                if len(current) + len(addition) > _MAX_TEXT_BYTES:
                    raise ToolExecutionError('Text write exceeds the 20 MiB limit')
                self._authorize_scope(scope, 'write')
                if scope.workspace_id:
                    self._atomic_write_workspace(
                        safe_path, scope, current + addition, expected_version or '',
                    )
                else:
                    write_file_atomically(safe_path, current + addition)
            except OSError as exc:
                raise ToolExecutionError('Cannot append file') from exc
        return {
            'path': self._relative(safe_path, scope), 'source_id': scope.source_id,
            'bytes': os.path.getsize(safe_path), 'version': self._version_for_scope(safe_path, scope),
        }
