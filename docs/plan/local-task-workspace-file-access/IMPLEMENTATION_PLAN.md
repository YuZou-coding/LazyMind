# Local Workspace Checklist Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Update `HANDOFF.md` and `checklist.md` after each verified phase.

**Goal:** Complete every automatable Local/Desktop Work workspace checklist item and prepare exact user-run Local/Desktop native E2E steps.

**Architecture:** Core is the authority for grants, bindings, permission state, versions, deployment gates, and public contracts. Host-specific layers are changed only for behavior Backend cannot perform: native selection, in-process Agent filesystem and command enforcement, Desktop bridging, and visible UI interactions.

**Tech Stack:** Go/GORM/SQLite/PostgreSQL, Python/pytest/LazyMind, Electron/Node test runner, React/TypeScript/Vitest, OpenAPI generation.

## Global Constraints

- Prefer implementation in `backend/`.
- Never modify or add content under `algorithm/lazyllm`.
- Preserve all existing staged, unstaged, and untracked user work.
- Never edit, restore, remove, stage, or commit `docs/plan/.DS_Store`.
- Do not add a command broker, write-lock broker, resource scheduler, durable approval queue, archive expansion approvals, or global Office changes.
- Use the current feature branch and working tree because the implementation being completed is uncommitted there.
- Write a focused failing test before each new behavioral fix and observe the expected failure before implementation.

---

### Task 1: Reconstruct Current Truth And Baseline

**Files:**
- Modify: `docs/plan/local-task-workspace-file-access/HANDOFF.md`
- Modify: `docs/plan/local-task-workspace-file-access/checklist.md`
- Create: `docs/plan/local-task-workspace-file-access/EVIDENCE.md`

**Interfaces:**
- Consumes: current Git index/worktree, `IMPLEMENTATION_DESIGN.md`, existing test suites.
- Produces: one checklist-to-evidence table with `automated`, `static`, `manual`, or `blocked` status for every checklist item.

- [x] **Step 1: Capture protected state**

Run:

```bash
git status --short
git submodule status algorithm/lazyllm
git -C algorithm/lazyllm status --short
git config -f .gitmodules --get submodule.algorithm/lazyllm.url
```

Expected: branch worktree changes remain visible; LazyLLM points to the official repository, gitlink `084d4905...`, and its own worktree is clean.

- [x] **Step 2: Run the focused baseline suites**

Run the exact commands listed in Task 5, recording exit codes and test counts. A failing suite is baseline evidence, not permission to edit unrelated code.

- [x] **Step 3: Build the evidence matrix**

For every checkbox in `checklist.md`, record the proving test name, static code location, or exact manual scenario in `EVIDENCE.md`. Leave items unverified when no direct evidence exists.

- [x] **Step 4: Record the reconstructed state**

Update `HANDOFF.md` with the difference between its previous claims and the current worktree, including all failing tests and untracked source files.

---

### Task 2: Verify And Complete Backend Authority

**Files:**
- Modify if a gap exists: `backend/core/localworkspace/service.go`
- Modify if a gap exists: `backend/core/localworkspace/handlers.go`
- Modify if a gap exists: `backend/core/chat/local_workspace.go`
- Modify if a contract gap exists: `backend/core/openapi_manual.go`
- Test: `backend/core/localworkspace/iteration_two_capability_contract_test.go`
- Test: `backend/core/localworkspace/workspace_permission_mode_contract_test.go`
- Test: `backend/core/chat/local_workspace_mode_contract_test.go`
- Test: `backend/core/chat/local_workspace_permission_mode_contract_test.go`
- Test: `backend/core/local_workspace_openapi_test.go`
- Test: `backend/core/migrate/*_test.go`

**Interfaces:**
- Consumes: trusted native candidate token, authenticated user ID, Work conversation ID, execution and actor identity.
- Produces: active grant and immutable task binding resolution containing `workspace_id`, `workspace_version`, `permission_mode`, `permission_version`, `root_path`, and a short-lived actor-bound capability.

- [x] **Step 1: Add the first missing Backend contract as a failing test**

The test must exercise one uncovered checklist behavior through the real handler/service. Examples of acceptable focused assertions are:

```go
if response.Code != http.StatusForbidden {
    t.Fatalf("status=%d body=%s, want disabled mode rejection", response.Code, response.Body.String())
}
if strings.Contains(response.Body.String(), workspace.CanonicalPath) {
    t.Fatalf("forbidden response leaked host path: %s", response.Body.String())
}
```

Run only the named test with `go test ./localworkspace -run TestName -count=1` or `go test ./chat -run TestName -count=1`, and confirm it fails for the missing behavior.

- [x] **Step 2: Implement the smallest Backend fix**

Keep authorization and version checks inside Core transactions. Return project error-catalog messages rather than raw database or host-path errors. Do not move host filesystem execution into Core.

- [x] **Step 3: Verify focused and neighboring Backend tests**

Run:

```bash
cd backend/core
GOCACHE=/private/tmp/lazymind-go-build go test ./localworkspace ./chat ./migrate -count=1
GOCACHE=/private/tmp/lazymind-go-build go test . -count=1
```

Expected: all packages exit 0.

- [x] **Step 4: Verify migration and OpenAPI parity**

Run:

```bash
cd backend/core
GOCACHE=/private/tmp/lazymind-go-build go test ./migrate -count=1
GOCACHE=/private/tmp/lazymind-go-build go test . -run 'TestOpenAPI.*LocalWorkspace' -count=1
cd ../../frontend
pnpm gen:openapi:check
```

Expected: PostgreSQL/SQLite migration contracts and generated-client stale checks exit 0. If a schema change is required, add a new UTC-named dev migration with up/down files and update the existing `v0_3` aggregate without modifying merged dev migrations.

---

### Task 3: Complete Agent File And Command Enforcement

**Files:**
- Modify if a gap exists: `algorithm/lazymind/chat/engine/tools/local_fs.py`
- Modify if a gap exists: `algorithm/lazymind/chat/engine/tools/workspace_shell.py`
- Modify if a gap exists: `algorithm/lazymind/chat/engine/agent_runtime/executor.py`
- Modify if a gap exists: `algorithm/lazymind/chat/service/chat_service.py`
- Test: `algorithm/tests/chat/engine/tools/test_local_workspace_iteration_two_contract.py`
- Test: `algorithm/tests/chat/engine/tools/test_workspace_permission_modes_contract.py`
- Test: `algorithm/tests/chat/engine/tools/test_workspace_shell.py`
- Test: `algorithm/tests/chat/engine/agent_runtime/test_function_call_approval_contract.py`

**Interfaces:**
- Consumes: Core-confirmed workspace source and permission versions in `lazyllm.globals['agentic_config']`.
- Produces: relative-path-only file tools and a workspace-rooted `shell_tool`; each mutable or long-running operation revalidates Core state.

- [x] **Step 1: Run current runtime contract tests**

```bash
LAZYLLM_LOG_FILE_MODE=split PYTHONPATH=algorithm/lazyllm:algorithm \
.venv/bin/python -m pytest \
  algorithm/tests/chat/engine/tools/test_workspace_permission_modes_contract.py \
  algorithm/tests/chat/engine/tools/test_local_workspace_iteration_two_contract.py \
  algorithm/tests/chat/engine/tools/test_workspace_shell.py \
  algorithm/tests/chat/engine/agent_runtime/test_function_call_approval_contract.py \
  -q --tb=short
```

Expected: all tests pass. Treat collection errors, mock/API mismatches, and hangs as implementation defects.

- [x] **Step 2: Add a failing test for each uncovered runtime invariant**

Required invariant assertions include: model-supplied `allow_unsafe=True` is cleared; one-time approval replays exactly once; `allow_all` cannot execute permanent denials; public file APIs omit command/delete/move/rename/link/permission operations; parent replacement and symlink races cannot write outside the workspace; permission or grant version changes reject subsequent operations; revoked long-running commands are terminated.

- [x] **Step 3: Implement minimal host-runtime fixes**

Use descriptor-relative POSIX traversal for workspace files where available, same-directory atomic replacement, structured argv with `shell=False`, sanitized environment variables, bounded output, and periodic Core revalidation. Explain every non-Backend modification in `EVIDENCE.md` as an in-process host responsibility.

- [x] **Step 4: Re-run the focused suite and the full Agent runtime directory**

```bash
LAZYLLM_LOG_FILE_MODE=split PYTHONPATH=algorithm/lazyllm:algorithm \
.venv/bin/python -m pytest \
  algorithm/tests/chat/engine/tools/test_workspace_permission_modes_contract.py \
  algorithm/tests/chat/engine/tools/test_local_workspace_iteration_two_contract.py \
  algorithm/tests/chat/engine/tools/test_workspace_shell.py \
  algorithm/tests/chat/engine/agent_runtime -q --tb=short
```

Expected: exit 0 with no hangs.

---

### Task 4: Verify Host Selection, Desktop Bridge, And Frontend Interaction

**Files:**
- Modify if a native-host gap exists: `local/local-proxy/internal/server/workspace.go`
- Modify if a native-host gap exists: `desktop/electron/src/main.js`
- Modify if a bridge gap exists: `desktop/electron/src/preload.js`
- Modify if an interaction gap exists: `frontend/src/modules/chat/components/ChatInput/LocalWorkspaceControl.tsx`
- Modify if a wiring gap exists: `frontend/src/modules/chat/components/ChatInput/index.tsx`
- Modify if a state gap exists: `frontend/src/modules/chat/pages/chatLayout/index.tsx`
- Test: `local/local-proxy/internal/server/workspace_selection_contract_test.go`
- Test: `desktop/scripts/local-workspace-contract.test.mjs`
- Test: `desktop/scripts/preload-bridge.test.mjs`
- Test: `frontend/src/modules/chat/components/ChatInput/LocalWorkspace.contract.test.tsx`
- Test: `frontend/src/modules/chat/components/ChatInput/LocalWorkspaceIterationTwo.contract.test.ts`

**Interfaces:**
- Consumes: native folder selection result and Core workspace APIs.
- Produces: one-time trusted candidate flow and Work-only workspace/permission controls with cancel-safe UI state.

- [x] **Step 1: Run host and UI contracts unchanged**

```bash
cd local/local-proxy
GOCACHE=/private/tmp/lazymind-go-build go test ./... -count=1
cd ../../desktop
node --test scripts/preload-bridge.test.mjs scripts/local-workspace-contract.test.mjs
cd ../frontend
pnpm exec vitest run \
  src/modules/chat/components/ChatInput/LocalWorkspace.contract.test.tsx \
  src/modules/chat/components/ChatInput/LocalWorkspaceIterationTwo.contract.test.ts
```

Expected: all commands exit 0.

- [x] **Step 2: Add a failing contract test for each uncovered interaction**

Cover Work/Chat and deployment-mode visibility, search, valid direct switching, invalid reauthorization, native picker cancellation, no-workspace selection, menu mutual exclusion, overlay/close/Escape cancellation, `allow_all` risk confirmation, success notification, and blocked-path feedback.

- [x] **Step 3: Implement only irreducible host/UI fixes**

Native directory picking remains in Local Proxy/Desktop, and visual state remains in Frontend. Do not create a client-side authorization source or accept arbitrary browser-submitted paths.

- [x] **Step 4: Verify lint, contracts, and generated client**

```bash
cd frontend
pnpm exec eslint \
  src/modules/chat/components/ChatInput/LocalWorkspaceControl.tsx \
  src/modules/chat/components/ChatInput/index.tsx \
  src/modules/chat/components/ChatInput/types.ts \
  src/modules/chat/pages/chatLayout/index.tsx \
  src/modules/chat/components/newChatContainer/hooks/useChatConversation.ts
pnpm exec vitest run \
  src/modules/chat/components/ChatInput/LocalWorkspace.contract.test.tsx \
  src/modules/chat/components/ChatInput/LocalWorkspaceIterationTwo.contract.test.ts
pnpm gen:openapi:check
```

Expected: every command exits 0; warnings are recorded separately and not described as failures.

---

### Task 5: Full Regression, Documentation, And Manual Acceptance

**Files:**
- Modify: `docs/plan/local-task-workspace-file-access/EVIDENCE.md`
- Modify: `docs/plan/local-task-workspace-file-access/checklist.md`
- Modify: `docs/plan/local-task-workspace-file-access/HANDOFF.md`

**Interfaces:**
- Consumes: final worktree and all automated test outputs.
- Produces: reproducible automated evidence plus exact Local/Desktop manual steps and remaining blockers.

- [x] **Step 1: Run full relevant regression**

```bash
cd backend/core
GOCACHE=/private/tmp/lazymind-go-build go test . ./chat ./localworkspace ./migrate -count=1
cd ../../local/local-proxy
GOCACHE=/private/tmp/lazymind-go-build go test ./... -count=1
cd ../local-runtime-manager
GOCACHE=/private/tmp/lazymind-go-build go test ./... -count=1
GOOS=windows GOARCH=amd64 GOCACHE=/private/tmp/lazymind-go-build go test ./... -count=1
cd ../../desktop
node --test scripts/preload-bridge.test.mjs scripts/local-workspace-contract.test.mjs
cd ../frontend
pnpm exec eslint src/modules/chat/components/ChatInput/LocalWorkspaceControl.tsx src/modules/chat/components/ChatInput/index.tsx src/modules/chat/components/ChatInput/types.ts src/modules/chat/pages/chatLayout/index.tsx src/modules/chat/components/newChatContainer/hooks/useChatConversation.ts
pnpm exec vitest run src/modules/chat/components/ChatInput/LocalWorkspace.contract.test.tsx src/modules/chat/components/ChatInput/LocalWorkspaceIterationTwo.contract.test.ts
pnpm gen:openapi:check
cd ..
LAZYLLM_LOG_FILE_MODE=split PYTHONPATH=algorithm/lazyllm:algorithm .venv/bin/python -m pytest algorithm/tests/chat/engine/tools/test_workspace_permission_modes_contract.py algorithm/tests/chat/engine/tools/test_local_workspace_iteration_two_contract.py algorithm/tests/chat/engine/tools/test_workspace_shell.py algorithm/tests/chat/engine/agent_runtime -q --tb=short
```

Expected: every command exits 0.

- [x] **Step 2: Verify excluded scope and protected files**

```bash
git -C algorithm/lazyllm status --short
git submodule status algorithm/lazyllm
git config -f .gitmodules --get submodule.algorithm/lazyllm.url
test ! -e docs/plan/local-task-workspace-file-access/lazyllm-workspace-permissions.patch
git diff --name-only origin/main...HEAD -- backend/office-convert-service
rg -n 'workspace-write-locks|workspace-commands:run|LAZYMIND_LOCAL_WORKSPACE_BROKER_URL' algorithm/lazymind backend local desktop frontend
```

Expected: LazyLLM is clean and official/pinned, the patch is absent, Office has no feature diff, and excluded broker identifiers have no production matches.

- [x] **Step 3: Update checklist only from direct evidence**

Mark automated/static items complete only when `EVIDENCE.md` names their proof. Keep Local/Desktop native E2E items unchecked until the user reports completion.

- [x] **Step 4: Give the user exact manual scenarios**

Provide steps for Local and packaged Desktop: choose directory, authorize, create Work, read an existing file, create a file, overwrite using the returned version, append or exact-replace, revoke, confirm later access is denied, and confirm Chat has no workspace entry. Include cancel/Escape and `allow_all` risk-confirmation checks.

- [ ] **Step 5: Incorporate manual results**

If the user reports a defect, add a reproducing automated test where possible, observe failure, fix it, and repeat relevant verification. When the user reports success, record the date/platform/result in `EVIDENCE.md`, update `checklist.md`, and refresh `HANDOFF.md` for the next Agent.
