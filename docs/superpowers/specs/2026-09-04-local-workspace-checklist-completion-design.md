# Local Workspace Checklist Completion Design

## Goal

Complete the Local/Desktop Work task workspace acceptance checklist against the
current working tree, while keeping product logic in `backend/` whenever that
layer can satisfy the requirement.

## Scope And Ownership

- `backend/` is the default implementation location for authorization, task
  binding, revocation, permission modes, optimistic versions, deployment gates,
  migrations, OpenAPI contracts, and stable errors.
- `algorithm/lazyllm` is read-only. Its remote URL, pinned gitlink, and clean
  state may be verified, but no file, commit, patch, or generated artifact may
  be changed or added there.
- `algorithm/lazymind/`, `local/`, `desktop/`, and `frontend/` are audited and
  tested before modification. They may receive only the smallest change that
  cannot be implemented in Backend, such as native directory selection, Agent
  tool enforcement, Desktop preload bridging, or visible user interaction.
- `docs/plan/.DS_Store` is unrelated user/system state and must not be edited,
  restored, removed, staged, or committed.
- The feature must not add a general command product, command broker, resource
  scheduler, long-lived approval queue, archive expansion approval system, or
  global Office conversion changes.

## Architecture

Core remains the authority for local workspace grants and Work task bindings.
It validates the requesting user, deployment mode, conversation type, directory
identity, grant version, permission mode, and permission version. Local Proxy
converts a native directory choice into a short-lived one-time candidate; the
browser cannot authorize an arbitrary host path. Desktop exposes only the
minimal native selection bridge.

At execution time, LazyMind receives the resolved workspace binding from Core.
Its local file toolkit accepts workspace-relative paths and revalidates the
binding around operations. Its workspace-specific shell adapter runs the
project's existing command capability with a fixed working root and structured
arguments. LazyMind's tool guard owns one-time approval replay and rejects
approval flags supplied by the model. These host-runtime responsibilities are
used only where Backend cannot directly enforce an in-process filesystem or
command operation.

Frontend renders the workspace and permission controls from Core state. It must
hide local workspace access outside eligible Local/Desktop Work tasks, preserve
cancel semantics, keep menus mutually exclusive, and display success or blocked
feedback without creating a parallel authorization model.

## Data And Control Flow

1. The user selects a directory through Local Proxy or Desktop native UI.
2. The trusted host returns a short-lived one-time candidate token.
3. Core consumes the token, records the directory identity, and creates or
   refreshes a user-owned workspace grant.
4. A new Work conversation binds atomically to at most one active grant and
   stores `ask_as_needed` plus its version by default.
5. Before each local operation, the runtime asks Core to resolve the current
   user, conversation, execution, actor, operation class, grant, and permission
   versions.
6. File and command execution remains within the resolved root. Long-running
   commands periodically revalidate and terminate when authorization changes.
7. Revocation invalidates the grant. Old tasks remain bound to the revoked grant
   and cannot regain access through a later grant for the same path.

## Security And Errors

- Public file inputs are workspace-relative; absolute paths, parent traversal,
  null bytes, drive or UNC paths, and symbolic-link escapes are rejected.
- Directory identity and authorization are checked before operations and again
  before mutable commits. File replacement uses optimistic content versions and
  same-directory atomic replacement.
- File APIs do not expose delete, move, rename, links, permissions, or ownership
  changes.
- `allow_all` skips approvable prompts only. It never bypasses workspace bounds,
  revocation, privilege escalation, destructive system operations, or permanent
  command denials.
- Errors are stable and do not disclose host paths. UI feedback distinguishes
  cancellation, authorization failure, revocation, stale versions, and blocked
  paths.

## Permission Modes

- `always_ask`: ask before file mutations, network effects, connected-app side
  effects, and other configured risky operations.
- `ask_as_needed`: default; ask only when existing risk classification requires
  approval.
- `allow_all`: enabled only after explicit risk confirmation; skips eligible
  prompts but preserves permanent prohibitions.
- A permission change does not approve an already pending operation. One-time
  approval is scoped to one replay and cannot be supplied by model arguments.

## Verification Strategy

The checklist is converted into an evidence matrix linking every item to a
test, static inspection, or manual scenario. Existing tests establish the
current baseline. Every discovered behavioral gap is fixed test-first: add a
focused failing test, observe the expected failure, implement the smallest
change, then rerun the focused and neighboring suites.

Automated verification covers Core Go tests and migrations, Local Proxy and
Runtime Manager Go tests, Desktop contract tests, Frontend lint/contract/OpenAPI
checks, Algorithm workspace and approval tests, submodule integrity, and scans
for explicitly excluded architecture. The user performs the final packaged
Local/Desktop native file loop using exact steps supplied after automated
verification. Manual results and any environment blockers are recorded
separately from automated results.

## Completion Criteria

- Every checklist item has direct evidence or is explicitly marked as awaiting
  the agreed user-run native E2E step.
- All relevant automated suites pass on the final working tree.
- Any required non-Backend change includes a documented reason why Backend
  cannot implement that behavior.
- LazyLLM remains pinned to the official repository and the submodule working
  tree remains clean.
- The implementation and handoff documents describe the final code rather than
  the earlier intermediate state.
