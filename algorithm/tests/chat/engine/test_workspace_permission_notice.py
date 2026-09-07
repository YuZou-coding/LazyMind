from lazymind.chat.workspace_permission_notice import workspace_permission_notice


def test_workspace_boundary_failure_has_explicit_chinese_block_notice():
    notice = workspace_permission_notice(
        'LocalFileToolkit_read',
        {'ok': False, 'value': 'Path is outside the authorized workspace'},
        'zh',
    )

    assert notice == (
        '工作区权限已阻止此操作：Path is outside the authorized workspace\n'
    )


def test_non_permission_file_failure_has_no_workspace_notice():
    assert workspace_permission_notice(
        'LocalFileToolkit_read',
        {'ok': False, 'value': 'File not found'},
        'zh',
    ) is None


def test_unrelated_tool_failure_has_no_workspace_notice():
    assert workspace_permission_notice(
        'calculator',
        {'ok': False, 'value': 'Path is outside the authorized workspace'},
        'en',
    ) is None
