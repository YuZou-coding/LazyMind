from __future__ import annotations

import ntpath

import pytest

from lazymind.chat.engine.tools import workspace_win32


def test_win32_path_parts_accept_only_descendants():
    assert workspace_win32._path_parts(
        r'C:\Users\alice\workspace\docs\note.txt',
        r'C:\Users\alice\workspace',
        ntpath,
    ) == ['docs', 'note.txt']
    with pytest.raises(workspace_win32.WorkspaceWin32Error, match='outside'):
        workspace_win32._path_parts(
            r'C:\Users\alice\outside\secret.txt',
            r'C:\Users\alice\workspace',
            ntpath,
        )


def test_win32_directory_handles_disallow_delete_sharing(monkeypatch, tmp_path):
    child = tmp_path / 'docs'
    child.mkdir()
    shares = []
    closed = []

    class FakeCtypes:
        class c_void_p:
            def __init__(self, value):
                self.value = value

        @staticmethod
        def get_last_error():
            return 0

    def create_file(_path, _access, share, *_args):
        shares.append(share)
        return len(shares)

    def close_handle(handle):
        closed.append(handle)

    def get_attributes(_path):
        return 0x00000010

    monkeypatch.setattr(
        workspace_win32,
        '_win32_api',
        lambda: (FakeCtypes, create_file, close_handle, get_attributes),
    )

    with workspace_win32.lock_workspace_path(
        str(child), str(tmp_path), include_leaf=True,
    ):
        assert len(shares) == 2

    assert all(share == 0x00000001 | 0x00000002 for share in shares)
    assert closed == [2, 1]


def test_win32_final_path_prefixes_are_normalized():
    assert workspace_win32._normal_win32_final_path(
        r'\\?\C:\Users\alice\workspace\note.txt',
    ) == r'C:\Users\alice\workspace\note.txt'
    assert workspace_win32._normal_win32_final_path(
        r'\\?\UNC\server\share\note.txt',
    ) == r'\\server\share\note.txt'
