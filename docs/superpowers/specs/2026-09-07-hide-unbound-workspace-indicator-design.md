# Hide the Workspace Indicator After a Task Starts

## Goal

Remove the persistent local-workspace indicator from the chat input after a
Work task starts, whether or not the task has a bound local workspace. Existing
tasks retain only the permission-mode control when a workspace is bound.

## Scope

The change is limited to the chat input's local workspace control. It does not
change workspace authorization, selection, backend revocation semantics,
permissions, task binding, localization strings, or API behavior. Existing-task
workspace details are no longer opened from the composer because their trigger
is hidden.

## UI Behavior

- A new temporary Work task with no workspace continues to show the workspace
  selector so the user can bind a workspace before sending the first message.
- An existing Work task never renders a workspace trigger, including when it
  has an active, unavailable, or revoked workspace.
- An existing Work task with an active workspace keeps the permission-mode
  control enabled so its permission can be changed.
- An existing Work task with no bound workspace keeps the permission-mode
  control disabled.
- The “不使用本地工作区” action inside the editable workspace menu remains
  available for clearing a selection before a new task is sent.

## Implementation

`LocalWorkspaceControl` will render the workspace trigger only before the task
has a persistent conversation ID. Existing-task workspace metadata is still
loaded because it controls permission availability and the current permission
value, but its display name is not rendered in the composer. Core workspace
requests use the authenticated Axios client so existing-task metadata is not
silently lost to a `401` response.

## Testing

The component contract tests cover both bound and unbound existing tasks. They
assert that neither renders a workspace trigger, while a bound task keeps its
permission selector enabled. The focused contract suite, frontend type checking,
and production build verify the change.

For verification against the running local app, build with
`VITE_LAZYMIND_MODE=local pnpm build`, matching the local runtime manager's
environment. The default cloud build deliberately hides local workspace
controls and must not replace the local app bundle during this check.

## Error Handling

Workspace lookup failures continue to follow the existing fallback behavior.
They are treated as having no selected workspace, so an existing task does not
show a misleading workspace control.
