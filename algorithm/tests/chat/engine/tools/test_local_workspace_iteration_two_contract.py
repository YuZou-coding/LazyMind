from __future__ import annotations

import subprocess

import pytest

from lazyllm.tools.agent import ToolExecutionError
from lazymind.chat.engine.tools.local_fs import LocalFSScope, LocalFileToolkit
from lazyllm.tools.agent.shell_tool import shell_tool
import lazyllm.tools.agent.shell_tool as shell_tool_module


class WorkspaceToolkit(LocalFileToolkit):
    def __init__(self, root: str):
        self._scope = LocalFSScope("workspace-1", (root,), frozenset({"txt", ""}))

    def _get_scopes(self):
        return [self._scope]


class _Response:
    def __init__(self, ok, data=None):
        self.ok = ok
        self._data = data or {}

    def json(self):
        return self._data


class _Session:
    def __init__(self, response, calls):
        self.response = response
        self.calls = calls
        self.trust_env = True

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False

    def post(self, url, **kwargs):
        self.calls.append((url, kwargs, self.trust_env))
        return self.response


def test_model_paths_are_relative_and_results_do_not_disclose_absolute_root(tmp_path):
    (tmp_path / "note.txt").write_text("hello", encoding="utf-8")
    toolkit = WorkspaceToolkit(str(tmp_path))

    with pytest.raises(ToolExecutionError):
        toolkit.read(str(tmp_path / "note.txt"))

    result = toolkit.read("note.txt")
    assert result["path"] == "note.txt"
    assert str(tmp_path) not in repr(result)


def test_directory_limit_is_capped_at_200_even_when_caller_requests_more(tmp_path):
    toolkit = WorkspaceToolkit(str(tmp_path))
    result = toolkit.ls(".", max_entries=1000)
    assert result["max_entries"] == 200
    assert result["entry_count"] <= 200


def test_stale_version_cannot_overwrite_a_newer_workspace_file(tmp_path):
    target = tmp_path / "note.txt"
    target.write_text("first", encoding="utf-8")
    toolkit = WorkspaceToolkit(str(tmp_path))
    old_version = toolkit.read("note.txt")["version"]
    target.write_text("newer", encoding="utf-8")

    with pytest.raises(ToolExecutionError, match="version conflict"):
        toolkit.write(
            "note.txt",
            "stale replacement",
            overwrite=True,
            expected_version=old_version,
        )

    assert target.read_text(encoding="utf-8") == "newer"


def test_create_and_append_return_new_versions(tmp_path):
    toolkit = WorkspaceToolkit(str(tmp_path))
    created = toolkit.write("note.txt", "first")
    appended = toolkit.append(
        "note.txt",
        " second",
        expected_version=created["version"],
    )

    assert created["version"] != appended["version"]
    assert (tmp_path / "note.txt").read_text(encoding="utf-8") == "first second"


def test_workspace_delete_executes_without_per_operation_approval(monkeypatch, tmp_path):
    calls = []

    def fake_run(*args, **kwargs):
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args=args, returncode=0, stdout="", stderr="")

    monkeypatch.setattr(shell_tool_module, "_run_workspace_process", fake_run)
    monkeypatch.setattr(shell_tool_module, "_task_workspace_root", lambda: str(tmp_path))
    result = shell_tool("rm -f disposable.txt", cwd=str(tmp_path))
    assert result["status"] == "ok"
    assert calls[0][0][0] == ["rm", "-f", "disposable.txt"]


def test_workspace_delete_cannot_request_approval_for_outside_target(monkeypatch, tmp_path):
    calls = []

    def fake_run(*args, **kwargs):
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args=args, returncode=0, stdout="", stderr="")

    monkeypatch.setattr(shell_tool_module.subprocess, "run", fake_run)
    monkeypatch.setattr(shell_tool_module, "_task_workspace_root", lambda: str(tmp_path))
    with pytest.raises(ToolExecutionError) as exc_info:
        shell_tool("rm ../outside.txt", cwd=str(tmp_path))
    assert exc_info.value.needs_approval is False
    assert "outside the authorized workspace" in str(exc_info.value)
    assert calls == []


def test_workspace_rename_executes_exact_argv_without_approval(monkeypatch, tmp_path):
    calls = []

    def fake_run(*args, **kwargs):
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args=args, returncode=0, stdout="", stderr="")

    monkeypatch.setattr(shell_tool_module, "_run_workspace_process", fake_run)
    monkeypatch.setattr(shell_tool_module, "_task_workspace_root", lambda: str(tmp_path))
    shell_tool("mv old.txt new.txt", cwd=str(tmp_path))
    assert calls[0][0][0] == ["mv", "old.txt", "new.txt"]
    assert calls[0][0][1] == str(tmp_path)


def test_long_command_is_stopped_when_workspace_is_revoked(monkeypatch, tmp_path):
    class FakeProcess:
        pid = 12345
        returncode = None

        def __init__(self):
            self.communications = 0

        def communicate(self, timeout=None):
            self.communications += 1
            if timeout is not None:
                raise subprocess.TimeoutExpired(["build"], timeout)
            return "", ""

    process = FakeProcess()
    stopped = []
    monkeypatch.setattr(shell_tool_module.subprocess, "Popen", lambda *_args, **_kwargs: process)
    monkeypatch.setattr(shell_tool_module, "_stop_process_tree", lambda item: stopped.append(item))

    def revoked(_root):
        raise ToolExecutionError("Workspace authorization is no longer active")

    monkeypatch.setattr(shell_tool_module, "_revalidate_task_workspace", revoked)
    with pytest.raises(ToolExecutionError, match="no longer active"):
        shell_tool_module._run_workspace_process(
            ["build"], str(tmp_path), {}, 30, str(tmp_path),
        )

    assert stopped == [process]
    assert process.communications == 2


def test_safe_command_uses_argument_execution_without_shell(monkeypatch, tmp_path):
    calls = []

    def fake_run(*args, **kwargs):
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args=args, returncode=0, stdout="ok\n", stderr="")

    monkeypatch.setattr(shell_tool_module.subprocess, "run", fake_run)
    shell_tool("python -m pytest -q", cwd=str(tmp_path))
    assert calls[0][1]["shell"] is False
    assert calls[0][1]["cwd"] == str(tmp_path)


def test_each_workspace_operation_revalidates_the_bound_authorization(monkeypatch, tmp_path):
    (tmp_path / "note.txt").write_text("hello", encoding="utf-8")
    toolkit = WorkspaceToolkit(str(tmp_path))
    toolkit._scope = LocalFSScope(
        "workspace-1",
        (str(tmp_path),),
        frozenset({"txt"}),
        workspace_id="workspace-1",
        workspace_version=3,
    )
    calls = []
    response = _Response(True, {"data": {
        "workspace_id": "workspace-1",
        "workspace_version": 3,
        "root_path": str(tmp_path),
    }})
    monkeypatch.setattr(
        "lazymind.chat.engine.tools.local_fs.requests.sessions.Session",
        lambda: _Session(response, calls),
    )
    monkeypatch.setenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN", "host-token")
    import lazyllm
    previous_config = lazyllm.globals.get("agentic_config")
    lazyllm.globals["agentic_config"] = {
        "user_id": "user-1",
        "conversation_id": "conversation-1",
        "run_id": "execution-1",
    }
    try:
        assert toolkit.read("note.txt")["content"] == "hello"
        assert calls[0][0].endswith("/internal/local-workspaces:resolve")
        assert calls[0][1]["json"]["operation_class"] == "read"
        assert calls[0][1]["headers"]["X-LazyMind-Local-Workspace-Token"] == "host-token"
        assert calls[0][2] is False
    finally:
        if previous_config is None:
            lazyllm.globals.pop("agentic_config", None)
        else:
            lazyllm.globals["agentic_config"] = previous_config


def test_revoked_workspace_fails_before_reading_host_file(monkeypatch, tmp_path):
    target = tmp_path / "note.txt"
    target.write_text("secret", encoding="utf-8")
    toolkit = WorkspaceToolkit(str(tmp_path))
    toolkit._scope = LocalFSScope(
        "workspace-1",
        (str(tmp_path),),
        frozenset({"txt"}),
        workspace_id="workspace-1",
        workspace_version=3,
    )
    calls = []
    response = _Response(False)
    monkeypatch.setattr(
        "lazymind.chat.engine.tools.local_fs.requests.sessions.Session",
        lambda: _Session(response, calls),
    )
    monkeypatch.setenv("LAZYMIND_LOCAL_WORKSPACE_HOST_TOKEN", "host-token")
    import lazyllm
    previous_config = lazyllm.globals.get("agentic_config")
    lazyllm.globals["agentic_config"] = {
        "user_id": "user-1",
        "conversation_id": "conversation-1",
        "run_id": "execution-1",
    }
    original_open = open

    def guarded_open(path, *args, **kwargs):
        if str(path) == str(target):
            raise AssertionError("revoked workspace file must not be opened")
        return original_open(path, *args, **kwargs)

    monkeypatch.setattr("builtins.open", guarded_open)
    try:
        with pytest.raises(ToolExecutionError, match="no longer active"):
            toolkit.read("note.txt")
    finally:
        if previous_config is None:
            lazyllm.globals.pop("agentic_config", None)
        else:
            lazyllm.globals["agentic_config"] = previous_config
