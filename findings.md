# Findings

- Remote branch is `feature/newWorkZone`, current base `b27a0e19`.
- UI reference confirms a three-mode permission menu and an explicit Allow-all risk modal. Watermarks/background are not product UI.
- User confirmed the modal should show three visual rows, combining network and connected apps while preserving separate underlying permission semantics.
- User waived Windows manual validation; automated Windows coverage and cross-compilation are still in scope.
- Frontend contract failures were caused by stale i18n mocks and immediate assertion of an asynchronous reauthorization flow.
- Core OpenAPI generation succeeds locally; the client contains the workspace-permission PUT operation and `include_inactive` list parameter.
- LazyLLM changes are shipped as `lazyllm-workspace-permissions.patch` because the parent repo cannot reference an unpublished submodule commit.
- Focused workspace permission Python tests pass after applying the patch.
