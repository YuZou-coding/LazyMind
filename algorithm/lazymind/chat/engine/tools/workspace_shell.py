from __future__ import annotations

import json
import os
import re
import signal
import shlex
import shutil
import stat
import subprocess
import sys
import tempfile
import threading
import time
from typing import Optional
from urllib import error as urllib_error
from urllib import request as urllib_request

import lazyllm
from lazyllm.tools.agent import ToolExecutionError


_PERMANENTLY_DENIED = {
    'sudo', 'su', 'chmod', 'chown', 'mkfs', 'dd', 'shutdown', 'reboot',
    'poweroff', 'kill', 'killall', 'pkill', 'ssh', 'scp', 'ln', 'link',
    'mount', 'umount', 'diskutil', 'launchctl',
}
_APPROVAL_REQUIRED = {
    'apt', 'apt-get', 'yum', 'dnf', 'brew', 'pip', 'pip3', 'conda',
    'curl', 'wget', 'npm', 'pnpm', 'yarn',
}
_MUTATIONS = {'rm', 'mv', 'rename', 'del', 'move-item', 'remove-item', 'rename-item'}
_READ_ONLY_COMMANDS = {
    'cat', 'head', 'tail', 'less', 'more', 'wc', 'find', 'rg', 'grep',
    'ls', 'dir', 'pwd', 'echo', 'printf', 'stat', 'file', 'which', 'where',
}
_OUTPUT_LIMIT = 1024 * 1024
_LINK_SCAN_LIMIT = 100_000


def _workspace_link_snapshot(root: str) -> tuple[set[str], dict[tuple[int, int], set[str]]]:
    symlinks: set[str] = set()
    identities: dict[tuple[int, int], set[str]] = {}
    seen = 0
    for current, directories, files in os.walk(root, followlinks=False):
        for name in [*directories, *files]:
            seen += 1
            if seen > _LINK_SCAN_LIMIT:
                raise ToolExecutionError('Workspace command containment is unavailable')
            path = os.path.join(current, name)
            relative = os.path.relpath(path, root)
            try:
                item_stat = os.lstat(path)
            except OSError:
                continue
            if stat.S_ISLNK(item_stat.st_mode):
                symlinks.add(relative)
            elif stat.S_ISREG(item_stat.st_mode):
                identities.setdefault((item_stat.st_dev, item_stat.st_ino), set()).add(relative)
    return symlinks, identities


def _remove_new_workspace_links(
    root: str,
    before: tuple[set[str], dict[tuple[int, int], set[str]]],
) -> bool:
    before_symlinks, before_identities = before
    after_symlinks, after_identities = _workspace_link_snapshot(root)
    forbidden = set(after_symlinks - before_symlinks)
    for identity, paths in after_identities.items():
        prior = before_identities.get(identity, set())
        if len(paths) > 1:
            forbidden.update(paths - prior if prior else paths)
    for relative in sorted(forbidden, reverse=True):
        candidate = os.path.join(root, relative)
        try:
            item_stat = os.lstat(candidate)
            if stat.S_ISLNK(item_stat.st_mode) or stat.S_ISREG(item_stat.st_mode):
                os.unlink(candidate)
        except OSError:
            pass
    return bool(forbidden)


def _revalidate_task_workspace(root: str) -> None:
    config = lazyllm.globals.get('agentic_config') or {}
    source = next((
        item for item in (config.get('local_fs_sources') or [])
        if isinstance(item, dict) and item.get('workspace_id')
    ), None)
    if not source:
        return
    core_url = str(
        os.environ.get('LAZYMIND_CORE_API_URL') or
        os.environ.get('LAZYMIND_CORE_SERVICE_URL') or ''
    ).strip().rstrip('/')
    token = os.environ.get('LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN', '').strip()
    user_id = str(config.get('user_id') or '').strip()
    conversation_id = str(config.get('conversation_id') or '').strip()
    execution_id = str(config.get('run_id') or config.get('session_id') or '').strip()
    actor_type = 'sub_agent' if config.get('is_subagent') else 'main_agent'
    actor_id = str(config.get('subagent_task_id') or config.get('agent_type') or actor_type).strip()
    if not all((core_url, token, user_id, conversation_id, execution_id, actor_id)):
        raise ToolExecutionError('Workspace authorization context is unavailable')
    request = urllib_request.Request(
        f'{core_url}/internal/local-workspaces:resolve',
        data=json.dumps({
            'conversation_id': conversation_id,
            'execution_id': execution_id,
            'actor_type': actor_type,
            'actor_id': actor_id,
            'operation_class': 'command',
        }).encode('utf-8'),
        headers={
            'Content-Type': 'application/json',
            'X-User-Id': user_id,
            'X-LazyMind-Local-Workspace-Token': token,
        },
        method='POST',
    )
    try:
        with urllib_request.urlopen(request, timeout=5) as response:
            body = json.loads(response.read(64 * 1024).decode('utf-8'))
    except (urllib_error.URLError, OSError, ValueError) as exc:
        raise ToolExecutionError('Workspace authorization is no longer active') from exc
    data = body.get('data') if isinstance(body, dict) else None
    if not isinstance(data, dict) or (
        str(data.get('workspace_id') or '') != str(source.get('workspace_id') or '') or
        int(data.get('workspace_version') or 0) != int(source.get('workspace_version') or 0) or
        str(data.get('permission_mode') or '') != str(
            source.get('workspace_permission_mode') or 'ask_as_needed'
        ) or
        int(data.get('permission_version') or 0) != int(source.get('workspace_permission_version') or 0) or
        os.path.realpath(str(data.get('root_path') or '')) != root
    ):
        raise ToolExecutionError('Workspace authorization changed; start a new operation')


def _stop_process_tree(process: subprocess.Popen) -> None:
    try:
        if os.name == 'posix':
            os.killpg(process.pid, signal.SIGKILL)
        else:
            process.kill()
    except (OSError, ProcessLookupError):
        pass


def _drain(pipe, sink: bytearray, state: dict[str, bool]) -> None:
    while True:
        chunk = pipe.read(64 * 1024)
        if not chunk:
            return
        if isinstance(chunk, str):
            chunk = chunk.encode('utf-8', errors='replace')
        remaining = _OUTPUT_LIMIT - len(sink)
        if remaining > 0:
            sink.extend(chunk[:remaining])
        if len(chunk) > remaining:
            state['truncated'] = True


def _sandbox_literal(value: str) -> str:
    return json.dumps(os.path.realpath(value))


def _macos_sandbox_argv(
    argv: list[str], root: str, temp_root: str, *, allow_network: bool,
) -> list[str]:
    sandbox = shutil.which('sandbox-exec')
    executable = shutil.which(argv[0]) if not os.path.isabs(argv[0]) else argv[0]
    if not sandbox or not executable:
        raise ToolExecutionError('Workspace command containment is unavailable')
    protected_read_roots = {
        os.path.expanduser('~'), '/Users', '/Volumes', '/private/tmp',
        '/private/etc', '/private/var', '/opt',
    }
    read_denials = []
    for protected in sorted(os.path.realpath(path) for path in protected_read_roots):
        exceptions = [root, temp_root]
        if protected == '/private/var':
            exceptions.append('/private/var/db')
        executable_path = os.path.realpath(executable)
        if protected == '/opt' and executable_path.startswith('/opt/'):
            parts = executable_path.split(os.sep)
            exceptions.append(os.sep.join(parts[:3]))
        read_denials.append(' '.join([
            '(deny file-read* (require-all',
            f'(subpath {_sandbox_literal(protected)})',
            *(
                f'(require-not (subpath {_sandbox_literal(path)}))'
                for path in exceptions
            ),
            '))',
        ]))
    write_exceptions = ' '.join([
        f'(require-not (subpath {_sandbox_literal(root)}))',
        f'(require-not (subpath {_sandbox_literal(temp_root)}))',
        '(require-not (literal "/dev/null"))',
    ])
    profile = ' '.join([
        '(version 1)',
        '(allow default)',
        *read_denials,
        '(deny file-read* (literal "/private/etc/hosts"))',
        '(deny file-write-mode)',
        '(deny file-write-owner)',
        '(deny file-link)',
        f'(deny file-write* (require-all {write_exceptions}))',
        '' if allow_network else '(deny network*)',
    ])
    return [sandbox, '-p', profile, executable, *argv[1:]]


def _linux_sandbox_argv(
    argv: list[str], root: str, cwd: str, temp_root: str, *, allow_network: bool,
) -> list[str]:
    bubblewrap = shutil.which('bwrap') or shutil.which('bubblewrap')
    executable = shutil.which(argv[0]) if not os.path.isabs(argv[0]) else argv[0]
    if not bubblewrap or not executable:
        raise ToolExecutionError('Workspace command containment is unavailable')
    relative_cwd = os.path.relpath(cwd, root)
    sandbox_cwd = '/workspace' if relative_cwd == '.' else '/workspace/' + relative_cwd.replace(os.sep, '/')
    command = [
        bubblewrap,
        '--die-with-parent', '--new-session',
        '--cap-drop', 'ALL',
        '--unshare-pid', '--unshare-ipc', '--unshare-uts',
        '--proc', '/proc', '--dev', '/dev', '--tmpfs', '/tmp',
    ]
    if not allow_network:
        command.append('--unshare-net')
    for system_path in ('/usr', '/bin', '/sbin', '/lib', '/lib64', '/etc', '/opt'):
        if os.path.exists(system_path):
            command.extend(['--ro-bind', system_path, system_path])
    empty_file = os.path.join(temp_root, 'empty-host-file')
    with open(empty_file, 'wb'):
        pass
    for host_path in ('/etc/hosts', '/etc/hostname', '/etc/passwd', '/etc/shadow'):
        if os.path.exists(host_path):
            command.extend(['--ro-bind', empty_file, host_path])
    command.extend([
        '--bind', root, '/workspace',
        '--chdir', sandbox_cwd,
        executable, *argv[1:],
    ])
    return command


def _run_workspace_process(
    argv: list[str], cwd: str, timeout: int, root: str, *, allow_network: bool = False,
) -> subprocess.CompletedProcess:
    if sys.platform not in {'darwin', 'linux'}:
        raise ToolExecutionError('Workspace command containment is unavailable')
    links_before = _workspace_link_snapshot(root)
    temp_parent = root if os.access(root, os.W_OK) else None
    with tempfile.TemporaryDirectory(prefix='.lazymind-command-', dir=temp_parent) as temp_root:
        contained_argv = (
            _macos_sandbox_argv(argv, root, temp_root, allow_network=allow_network)
            if sys.platform == 'darwin'
            else _linux_sandbox_argv(
                argv, root, cwd, temp_root, allow_network=allow_network,
            )
        )
        result = _run_contained_process(contained_argv, argv, cwd, timeout, root, temp_root)
    if _remove_new_workspace_links(root, links_before):
        raise ToolExecutionError('Command is permanently denied because it created a link')
    return result


def _run_contained_process(
    contained_argv: list[str], display_argv: list[str], cwd: str, timeout: int,
    root: str, temp_root: str,
) -> subprocess.CompletedProcess:
    environment = _environment()
    environment['TMPDIR'] = temp_root
    environment['TMP'] = temp_root
    environment['TEMP'] = temp_root
    kwargs = {
        'cwd': cwd,
        'env': environment,
        'shell': False,
        'text': False,
        'stdout': subprocess.PIPE,
        'stderr': subprocess.PIPE,
    }
    if os.name == 'posix':
        kwargs['start_new_session'] = True
    process = subprocess.Popen(contained_argv, **kwargs)
    stdout, stderr = bytearray(), bytearray()
    stdout_state, stderr_state = {'truncated': False}, {'truncated': False}
    readers = [
        threading.Thread(target=_drain, args=(process.stdout, stdout, stdout_state), daemon=True),
        threading.Thread(target=_drain, args=(process.stderr, stderr, stderr_state), daemon=True),
    ]
    for reader in readers:
        reader.start()
    deadline = time.monotonic() + timeout
    next_check = time.monotonic() + 5
    while True:
        if process.poll() is not None:
            for reader in readers:
                reader.join()
            result = subprocess.CompletedProcess(
                display_argv, process.returncode,
                bytes(stdout).decode('utf-8', errors='replace'),
                bytes(stderr).decode('utf-8', errors='replace'),
            )
            result.stdout_truncated = stdout_state['truncated']
            result.stderr_truncated = stderr_state['truncated']
            return result
        now = time.monotonic()
        if now >= deadline:
            _stop_process_tree(process)
            process.wait()
            raise subprocess.TimeoutExpired(display_argv, timeout)
        if now >= next_check:
            try:
                _revalidate_task_workspace(root)
            except ToolExecutionError:
                _stop_process_tree(process)
                process.wait()
                raise
            next_check = now + 5
        time.sleep(0.1)


def _workspace_context() -> tuple[str, str]:
    config = lazyllm.globals.get('agentic_config') or {}
    sources = [
        source for source in (config.get('local_fs_sources') or [])
        if isinstance(source, dict) and source.get('workspace_id')
    ]
    roots = {
        os.path.realpath(path)
        for source in sources for path in (source.get('paths') or [])
        if isinstance(path, str) and path.strip()
    }
    if len(sources) != 1 or len(roots) != 1:
        raise ToolExecutionError('Exactly one authorized task workspace is required')
    mode = str(
        config.get('workspace_permission_mode') or
        sources[0].get('workspace_permission_mode') or
        'ask_as_needed'
    )
    if mode not in {'always_ask', 'ask_as_needed', 'allow_all'}:
        mode = 'ask_as_needed'
    return roots.pop(), mode


def _task_workspace_root() -> str:
    return _workspace_context()[0]


def _workspace_permission_mode() -> str:
    config = lazyllm.globals.get('agentic_config') or {}
    mode = str(config.get('workspace_permission_mode') or '').strip()
    if not mode:
        source = next((
            item for item in (config.get('local_fs_sources') or [])
            if isinstance(item, dict) and item.get('workspace_id')
        ), {})
        mode = str(source.get('workspace_permission_mode') or 'ask_as_needed')
    return mode if mode in {'always_ask', 'ask_as_needed', 'allow_all'} else 'ask_as_needed'


def _inside(root: str, candidate: str) -> bool:
    try:
        return os.path.commonpath([root, os.path.realpath(candidate)]) == root
    except ValueError:
        return False


def _validate_paths(argv: list[str], root: str, cwd: str) -> None:
    for token in argv[1:]:
        if not token or token == '--' or token.startswith('-') or '://' in token:
            continue
        looks_like_path = os.path.isabs(token) or token in {'.', '..'} or '/' in token or '\\' in token
        if looks_like_path and (os.path.isabs(token) or not _inside(root, os.path.join(cwd, token))):
            raise ToolExecutionError('Command path is outside the authorized workspace')


def _environment() -> dict[str, str]:
    allowed = {'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TERM', 'TMPDIR', 'TEMP', 'TMP', 'SYSTEMROOT', 'WINDIR'}
    return {name: value for name, value in os.environ.items() if name.upper() in allowed}


def _is_read_only_command(argv: list[str]) -> bool:
    executable = os.path.basename(argv[0]).lower()
    if executable in _READ_ONLY_COMMANDS:
        return not any(token in {'>', '>>', '-delete', '--delete'} for token in argv[1:])
    if executable in {'git', 'git.exe'}:
        index = 1
        while index < len(argv) and argv[index].startswith('-'):
            index += 1
        return index < len(argv) and argv[index] in {
            'status', 'diff', 'log', 'show', 'rev-parse', 'ls-files', 'ls-tree',
            'cat-file', 'grep', 'describe', 'name-rev', 'for-each-ref', 'branch',
        }
    return False


def subagent_workspace_shell_tools(config: dict, *, tools_only: bool) -> list:
    if tools_only or str(config.get('agent_type') or '') == 'workflow_step':
        return []
    has_workspace = any(
        isinstance(source, dict) and source.get('workspace_id')
        for source in (config.get('local_fs_sources') or [])
    )
    return [shell_tool] if has_workspace else []


def shell_tool(
    cmd: str,
    cwd: Optional[str] = None,
    timeout: int = 30,
    allow_unsafe: bool = False,
) -> dict:
    """Run an existing project command inside the current authorized Work workspace."""
    root = _task_workspace_root()
    permission_mode = _workspace_permission_mode()
    try:
        argv = shlex.split(str(cmd or '').strip())
    except ValueError as exc:
        raise ToolExecutionError(f'Invalid command syntax: {exc}') from exc
    if not argv:
        raise ToolExecutionError('cmd cannot be empty')
    executable = os.path.basename(argv[0]).lower()
    normalized_command = ' '.join(argv).lower()
    destructive_git = executable in {'git', 'git.exe'} and (
        ' reset --hard' in f' {normalized_command}' or
        ' clean -' in f' {normalized_command}' or
        (' config ' in f' {normalized_command}' and '--global' in argv)
    )
    if executable in _PERMANENTLY_DENIED or destructive_git:
        raise ToolExecutionError('Command is permanently denied')
    requested_cwd = str(cwd or '.').strip()
    portable_cwd = requested_cwd.replace('\\', '/')
    if (
        '\x00' in requested_cwd or os.path.isabs(requested_cwd) or
        portable_cwd.startswith('//') or re.match(r'^[A-Za-z]:($|/)', portable_cwd)
    ):
        raise ToolExecutionError('Command cwd must be workspace-relative')
    if any(part == '..' for part in portable_cwd.split('/')):
        raise ToolExecutionError('Command cwd is outside the authorized workspace')
    effective_cwd = os.path.realpath(os.path.join(root, requested_cwd))
    if not _inside(root, effective_cwd) or not os.path.isdir(effective_cwd):
        raise ToolExecutionError('Command cwd is outside the authorized workspace')
    _validate_paths(argv, root, effective_cwd)
    risks = []
    if executable in _APPROVAL_REQUIRED or re.search(r'(^|\s)(>|>>)(\s|$)', cmd):
        risks.append(executable or 'redirection')
    if permission_mode == 'always_ask' and not _is_read_only_command(argv):
        risks.append(executable)
    if risks and permission_mode != 'allow_all' and not allow_unsafe:
        raise ToolExecutionError.approval_required(
            f'Command requires approval: {", ".join(dict.fromkeys(risks))}'
        )
    timeout = min(600, max(1, int(timeout)))
    _revalidate_task_workspace(root)
    try:
        completed = _run_workspace_process(
            argv,
            effective_cwd,
            timeout,
            root,
            allow_network=executable in _APPROVAL_REQUIRED,
        )
    except subprocess.TimeoutExpired as exc:
        raise ToolExecutionError(f'Command timed out after {timeout} seconds') from exc
    stdout = (completed.stdout or '').encode('utf-8', errors='replace')
    stderr = (completed.stderr or '').encode('utf-8', errors='replace')
    return {
        'status': 'ok',
        'stdout': stdout[:_OUTPUT_LIMIT].decode('utf-8', errors='replace').replace(root, '.'),
        'stderr': stderr[:_OUTPUT_LIMIT].decode('utf-8', errors='replace').replace(root, '.'),
        'stdout_truncated': bool(getattr(completed, 'stdout_truncated', len(stdout) > _OUTPUT_LIMIT)),
        'stderr_truncated': bool(getattr(completed, 'stderr_truncated', len(stderr) > _OUTPUT_LIMIT)),
        'exit_code': completed.returncode,
        'cwd': os.path.relpath(effective_cwd, root),
    }
