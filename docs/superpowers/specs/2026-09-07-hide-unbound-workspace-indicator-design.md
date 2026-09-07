# Hide the Unbound Workspace Indicator

## Goal

Remove the persistent local-workspace control from the chat input when an
existing Work task has no bound local workspace. The UI must no longer show
“不使用本地工作区” (or its localized equivalent) in this state.

## Scope

The change is limited to the chat input's local workspace control. It does not
change workspace authorization, selection, revocation, permissions, task
binding, localization strings, or API behavior.

## UI Behavior

- A new temporary Work task with no workspace continues to show the workspace
  selector so the user can bind a workspace before sending the first message.
- An existing Work task with an active or unavailable workspace continues to
  show the bound workspace name and its current read-only details.
- An existing Work task with no bound workspace renders no workspace trigger.
  The disabled permission-mode control remains unchanged.
- The “不使用本地工作区” action inside the editable workspace menu remains
  available for clearing a selection before a new task is sent.

## Implementation

`LocalWorkspaceControl` will return no workspace trigger when the task is an
existing task and the workspace lookup resolves to the `none` state. Other
states retain the existing rendering and event behavior.

## Testing

Update the component contract test first so the existing-task-without-workspace
case expects the persistent trigger to be absent. Run that test to confirm it
fails against the current implementation, then make the minimal conditional
rendering change and rerun the focused test suite. Finally run frontend type
checking and the relevant contract tests.

## Error Handling

Workspace lookup failures continue to follow the existing fallback behavior.
They are treated as having no selected workspace, so an existing task does not
show a misleading workspace control.
