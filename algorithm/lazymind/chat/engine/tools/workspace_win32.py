from __future__ import annotations

from contextlib import contextmanager
import os
from typing import Iterator


class WorkspaceWin32Error(OSError):
    pass


def _inside(root: str, candidate: str) -> bool:
    try:
        return os.path.commonpath([os.path.normcase(root), os.path.normcase(candidate)]) == os.path.normcase(root)
    except ValueError:
        return False


def _path_parts(path: str, root: str, path_module=os.path) -> list[str]:
    root = path_module.realpath(root)
    path = path_module.abspath(path)
    try:
        inside = path_module.commonpath([
            path_module.normcase(root), path_module.normcase(path),
        ]) == path_module.normcase(root)
    except ValueError:
        inside = False
    if not inside:
        raise WorkspaceWin32Error('path is outside the workspace')
    relative = path_module.relpath(path, root)
    if relative in ('', '.'):
        return []
    parts = relative.replace('\\', '/').split('/')
    if any(part in ('', '.', '..') for part in parts):
        raise WorkspaceWin32Error('path is not workspace-relative')
    return parts


def _win32_api():
    if os.name != 'nt':
        raise WorkspaceWin32Error('Win32 workspace APIs are unavailable')
    import ctypes
    from ctypes import wintypes

    kernel32 = ctypes.WinDLL('kernel32', use_last_error=True)
    create_file = kernel32.CreateFileW
    create_file.argtypes = [
        wintypes.LPCWSTR, wintypes.DWORD, wintypes.DWORD, wintypes.LPVOID,
        wintypes.DWORD, wintypes.DWORD, wintypes.HANDLE,
    ]
    create_file.restype = wintypes.HANDLE
    close_handle = kernel32.CloseHandle
    close_handle.argtypes = [wintypes.HANDLE]
    close_handle.restype = wintypes.BOOL
    get_attributes = kernel32.GetFileAttributesW
    get_attributes.argtypes = [wintypes.LPCWSTR]
    get_attributes.restype = wintypes.DWORD
    return ctypes, create_file, close_handle, get_attributes


def _win32_file_api():
    if os.name != 'nt':
        raise WorkspaceWin32Error('Win32 workspace APIs are unavailable')
    import ctypes
    import msvcrt
    from ctypes import wintypes

    class ByHandleFileInformation(ctypes.Structure):
        _fields_ = [
            ('dwFileAttributes', wintypes.DWORD),
            ('ftCreationTime', wintypes.FILETIME),
            ('ftLastAccessTime', wintypes.FILETIME),
            ('ftLastWriteTime', wintypes.FILETIME),
            ('dwVolumeSerialNumber', wintypes.DWORD),
            ('nFileSizeHigh', wintypes.DWORD),
            ('nFileSizeLow', wintypes.DWORD),
            ('nNumberOfLinks', wintypes.DWORD),
            ('nFileIndexHigh', wintypes.DWORD),
            ('nFileIndexLow', wintypes.DWORD),
        ]

    kernel32 = ctypes.WinDLL('kernel32', use_last_error=True)
    create_file = kernel32.CreateFileW
    create_file.argtypes = [
        wintypes.LPCWSTR, wintypes.DWORD, wintypes.DWORD, wintypes.LPVOID,
        wintypes.DWORD, wintypes.DWORD, wintypes.HANDLE,
    ]
    create_file.restype = wintypes.HANDLE
    close_handle = kernel32.CloseHandle
    get_information = kernel32.GetFileInformationByHandle
    get_information.argtypes = [wintypes.HANDLE, ctypes.POINTER(ByHandleFileInformation)]
    get_information.restype = wintypes.BOOL
    get_final_path = kernel32.GetFinalPathNameByHandleW
    get_final_path.argtypes = [wintypes.HANDLE, wintypes.LPWSTR, wintypes.DWORD, wintypes.DWORD]
    get_final_path.restype = wintypes.DWORD
    return (
        ctypes, msvcrt, ByHandleFileInformation,
        create_file, close_handle, get_information, get_final_path,
    )


def _normal_win32_final_path(path: str) -> str:
    if path.startswith('\\\\?\\UNC\\'):
        return '\\\\' + path[8:]
    if path.startswith('\\\\?\\'):
        return path[4:]
    return path


@contextmanager
def lock_workspace_path(
    path: str,
    root: str,
    *,
    include_leaf: bool = False,
    allow_missing_leaf: bool = False,
) -> Iterator[None]:
    """Lock existing directories against replacement and reject reparse points.

    Windows does not expose POSIX dir_fd traversal. Directory handles opened
    without FILE_SHARE_DELETE provide the equivalent invariant for the duration
    of a host operation: an ancestor cannot be renamed or replaced while held.
    """
    ctypes, create_file, close_handle, get_attributes = _win32_api()
    root = os.path.realpath(root)
    parts = _path_parts(path, root)
    checked = [root]
    if parts:
        limit = len(parts) if include_leaf else len(parts) - 1
        current = root
        for part in parts[:max(0, limit)]:
            current = os.path.join(current, part)
            checked.append(current)

    invalid_attributes = 0xFFFFFFFF
    reparse_point = 0x00000400
    directory = 0x00000010
    share_read = 0x00000001
    share_write = 0x00000002
    open_existing = 3
    backup_semantics = 0x02000000
    open_reparse_point = 0x00200000
    invalid_handle = ctypes.c_void_p(-1).value
    handles = []
    try:
        for index, candidate in enumerate(checked):
            attributes = get_attributes(candidate)
            if attributes == invalid_attributes:
                if allow_missing_leaf and index == len(checked) - 1:
                    break
                raise WorkspaceWin32Error('workspace path is unavailable')
            if attributes & reparse_point:
                raise WorkspaceWin32Error('workspace path contains a reparse point')
            if not attributes & directory:
                raise WorkspaceWin32Error('workspace parent is not a directory')
            handle = create_file(
                candidate,
                0,
                share_read | share_write,
                None,
                open_existing,
                backup_semantics | open_reparse_point,
                None,
            )
            if handle == invalid_handle:
                raise WorkspaceWin32Error(f'cannot lock workspace path: {ctypes.get_last_error()}')
            handles.append(handle)
            if not _inside(root, os.path.realpath(candidate)):
                raise WorkspaceWin32Error('workspace path changed during validation')
        yield
    finally:
        for handle in reversed(handles):
            close_handle(handle)


def open_workspace_file(path: str, root: str, flags: int, mode: int = 0o644) -> int:
    with lock_workspace_path(path, root):
        (
            ctypes, msvcrt, information_type,
            create_file, close_handle, get_information, get_final_path,
        ) = _win32_file_api()
        generic_read = 0x80000000
        generic_write = 0x40000000
        share_read_write = 0x00000001 | 0x00000002
        create_new = 1
        open_existing = 3
        open_always = 4
        open_reparse_point = 0x00200000
        reparse_point = 0x00000400
        desired_access = generic_read
        if flags & (os.O_WRONLY | os.O_RDWR):
            desired_access = generic_write | (generic_read if flags & os.O_RDWR else 0)
        disposition = open_existing
        if flags & os.O_CREAT:
            disposition = create_new if flags & os.O_EXCL else open_always
        handle = create_file(
            path, desired_access, share_read_write, None, disposition,
            open_reparse_point, None,
        )
        invalid_handle = ctypes.c_void_p(-1).value
        if handle == invalid_handle:
            raise WorkspaceWin32Error(f'cannot open workspace file: {ctypes.get_last_error()}')
        try:
            information = information_type()
            if not get_information(handle, ctypes.byref(information)):
                raise WorkspaceWin32Error(
                    f'cannot inspect workspace file: {ctypes.get_last_error()}'
                )
            if information.dwFileAttributes & reparse_point:
                raise WorkspaceWin32Error('workspace target is a reparse point')
            required = get_final_path(handle, None, 0, 0)
            if not required:
                raise WorkspaceWin32Error(
                    f'cannot resolve workspace file handle: {ctypes.get_last_error()}'
                )
            buffer = ctypes.create_unicode_buffer(required + 1)
            if not get_final_path(handle, buffer, len(buffer), 0):
                raise WorkspaceWin32Error(
                    f'cannot resolve workspace file handle: {ctypes.get_last_error()}'
                )
            final_path = _normal_win32_final_path(buffer.value)
            if not _inside(os.path.realpath(root), os.path.realpath(final_path)):
                raise WorkspaceWin32Error('workspace file handle is outside the workspace')
            descriptor = msvcrt.open_osfhandle(int(handle), flags)
            handle = invalid_handle
            return descriptor
        finally:
            if handle != invalid_handle:
                close_handle(handle)


@contextmanager
def lock_workspace_parent(path: str, root: str) -> Iterator[None]:
    with lock_workspace_path(path, root):
        yield
