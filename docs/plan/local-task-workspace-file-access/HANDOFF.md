# Local/Desktop 工作区与本地文件权限——开发交接（2026-09-03）

## 1. 给接手 Agent 的首要说明

- 仓库：`/Users/zouyu/Downloads/LazyMind-main`
- 当前工作树包含本需求此前多轮产生的大量未提交改动，也可能包含用户原有改动。**不得 reset、checkout、clean、覆盖或批量格式化无关文件。**
- 先完整阅读仓库根目录 `AGENTS.md`。涉及 `algorithm/lazyllm` 时，还要按其目录规则完整阅读对应 `AGENTS.md`、`base.py`、`functionCall.py` 等必读文件。
- 本需求属于权限、安全、API、迁移和跨部署改动，已执行两阶段门禁。阶段一测试已经提交用户 Review，用户已明确批准进入阶段二，最近又明确要求“完成代码实现”。接手后可继续阶段二，无需重新询问是否开始；如果发现会改变已确认产品行为的新歧义，必须先问用户。
- 当前实现**尚未完成**，不要向用户宣称已完成或全部验证通过。
- 主仓库不能直接携带 `algorithm/lazyllm` 子模块的未发布 commit，因此本次交付附带 `lazyllm-workspace-permissions.patch`。另一个账号拉取并初始化子模块后，在主仓库根目录执行：

  ```bash
  git -C algorithm/lazyllm apply ../../docs/plan/local-task-workspace-file-access/lazyllm-workspace-permissions.patch
  ```

  补丁包含 `functionCall.py`、`shell_tool.py`、`tool_adaptor.py` 的完整当前差异；应用后再继续开发和验证。不要重复应用。

## 2. 已确认的最终产品语义

### 模式与范围

- Work 模式对应“新建任务”；Chat 模式对应“快速问答”。
- 本地工作区能力只在 Local（`make local-up`）和 Desktop 的 Work 任务中开放。
- Chat、Cloud、Docker Compose 服务模式、LAN 模式不得取得本地文件权限。
- 授权记录属于本机用户；每个 Work 任务绑定一个授权工作区。一个工作区授权可被多个 Work 任务绑定。
- 撤销某工作区后，所有绑定它的任务立即失效；重新授权同一路径必须创建新的授权记录，旧任务不得自动恢复。

### 三档任务级权限

- 权限模式保存在“Work 任务—工作区绑定”上，而不是工作区全局设置。
- 新任务及升级前已有 Work 任务默认 `ask_as_needed`（按需确认）。
- `always_ask`（始终询问）：普通文件读取和安全只读本地命令直接执行；文件创建、修改、删除、移动、重命名，以及联网、已连接应用操作逐次确认。
- `ask_as_needed`（按需确认）：授权工作区内读取、创建、修改、删除、移动、重命名无需再次授权；沿用项目已有危险命令识别与 `needs_approval`，合理的高风险操作逐次确认。
- `allow_all`（全部允许）：跳过“可批准”的文件、命令、联网和已连接应用操作确认；不得绕过永久禁止项、越界、提权或系统破坏。
- 模式切换只影响后续操作，不能自动批准或消费切换前已经 pending 的决定。
- 主 Agent、子 Agent、Skill 都使用同一任务绑定与权限模式。

### 命令边界

- 沿用项目已有危险命令识别和 `needs_approval` 机制。
- 普通工作区命令直接执行。
- 工作区内删除、移动、重命名在默认“按需确认”下不需要批准；在“始终询问”下需要逐次批准。
- 越界访问、提权、权限/所有权修改、系统破坏等永久禁止，任何权限模式和 `allow_unsafe` 都不能放行。

### 前端交互（必须与用户图片保持一致）

- 新建 Work 默认显示“选择工作区”和“按需确认”；不默认选中任何目录；菜单和弹窗默认收起。
- 工作区入口可展开/收起；按名称和路径实时搜索。
- 点击有效授权：直接切换并关闭菜单。
- 点击失效/未授权历史工作区：弹“首次使用时需要授权”；取消、关闭、遮罩、`Esc` 不授权也不切换；允许后授权并切换。
- “打开本地文件夹”必须调用 macOS 原生目录选择器；选择后仍弹 LazyMind 首次授权框；原生选择器取消则保持当前工作区。
- “不使用本地工作区”清除当前任务选择，并禁用权限入口。
- 权限菜单含“始终询问 / 按需确认 / 全部允许”。前两项立即切换；“全部允许”必须先弹风险框，展示文件、命令、联网、已连接应用四类风险。
- 风险框取消、关闭、遮罩、`Esc` 保持原权限；“确认开启”才切换。
- 工作区菜单与权限菜单互斥；空白处或 `Esc` 关闭菜单和弹窗。
- 工作区切换、授权、权限修改成功后显示短暂提示；越界访问显示权限阻止提示。
- 授权弹窗无需明确列出删除/移动/重命名，现有范围说明可保留。
- Windows 人工验收本轮可以不执行。

完整需求与验收矩阵见同目录：`spec.md`、`tasks.md`、`checklist.md`。这些文档包含比当前代码覆盖范围更广的安全与资源限制要求，接手人必须逐项核对，不能只以现有聚焦测试变绿作为完成标准。

## 3. 当前已经实现的主要内容

### 第一迭代及第二迭代前半部分（工作树内已有）

- Core 工作区模型、授权/列表/撤销/任务绑定、Local/Desktop 模式门禁、稳定错误目录。
- Local Proxy 原生目录选择与一次性候选 token；Desktop Electron 原生目录选择 IPC/preload 桥。
- 工作区路径注入、本地文件读取/创建/修改工具、受控命令基础策略、敏感文件与若干资源限制。
- `tool_limit_control` / `ToolLimitCard` 一次性批准链的部分扩展。
- Local/Desktop 启动环境变量和宿主 token 传递。
- PostgreSQL/SQLite v0_3 工作区创建迁移、写策略迁移及 aggregate 同步。

### 本次阶段二已经新增/修改

- `conversation_workspace_bindings` 增加：
  - `permission_mode`，默认 `ask_as_needed`
  - `permission_version`，默认 1
  - `updated_at`
- 新 migration：
  - `backend/core/migrations/dev_mode/v0_3/20260903023152_add_workspace_permission_mode.up.sql`
  - `backend/core/migrations/dev_mode/v0_3/20260903023152_add_workspace_permission_mode.down.sql`
- v0_3 aggregate 已同步权限字段。
- 创建 Work 任务时校验并写入 `workspace_permission_mode`；未知值拒绝。
- 新接口：`PUT /api/core/conversations/{conversation_id}:workspace-permission`，校验所有权、Work 类型、绑定、枚举和乐观版本。
- 任务绑定查询及内部 workspace resolve 会返回权限模式与版本。
- 工作区执行源包含 `workspace_permission_mode` / `workspace_permission_version`。
- 工作区执行前会同时复核工作区授权版本和任务权限版本，模式在执行中被修改时旧操作安全失败。
- 已增加失效历史工作区重授权准备入口：
  - Core trusted-host：`POST /internal/local-workspaces/{workspace_id}:select`
  - Local Proxy：`POST /_local/workspaces:reauthorize`
  - Desktop preload：`reauthorizeLocalWorkspace(workspaceId)`
- 前端已开始实现任务级权限控件、互斥菜单、四风险弹窗、提示、未授权历史工作区重授权、请求参数传递。
- 文件/命令权限语义已开始接入：
  - `always_ask` 在文件写入副作用前返回一次性批准。
  - `always_ask` 对工作区内 `rm/mv/rename` 等要求批准。
  - `allow_all` 跳过 shell 中可批准风险，但永久禁止检查仍先执行。
  - FunctionCall 在人工批准后以一次性内部许可重放文件、命令或已连接应用调用。
  - `allow_all` 对既有 `needs_approval` 结果自动进行一次受控重放。
  - MCP 已连接应用在 `always_ask` 下要求一次性确认。
- Agent 提示词会根据三档模式给出不同操作规则。
- OpenAPI 源规格已经从 Core 重新导出，包含权限字段、新 PUT 接口和 `include_inactive`。

## 4. 已运行并通过的验证

- Core 聚焦权限/OpenAPI/迁移测试通过：

  ```bash
  cd backend/core
  GOCACHE=/private/tmp/lazymind-go-build go test . ./chat ./localworkspace ./migrate -run 'Test(OpenAPILocalWorkspace|.*WorkspacePermission|IterationTwoWorkspaceWritePolicy|RepositoryStructuredMigrationCatalogLoads|ChatRequest.*Workspace|LocalWorkspace)' -count=1
  ```

- Local Proxy 全部 Go 测试通过：

  ```bash
  cd local/local-proxy
  GOCACHE=/private/tmp/lazymind-go-build go test ./...
  ```

- Desktop 两个 Node 契约测试通过（8/8）：

  ```bash
  cd desktop
  node --test scripts/preload-bridge.test.mjs scripts/local-workspace-contract.test.mjs
  ```

- 本次前端受影响文件的 ESLint 通过：

  ```bash
  cd frontend
  pnpm exec eslint \
    src/modules/chat/components/ChatInput/LocalWorkspaceControl.tsx \
    src/modules/chat/components/ChatInput/index.tsx \
    src/modules/chat/components/ChatInput/types.ts \
    src/modules/chat/pages/chatLayout/index.tsx \
    src/modules/chat/components/newChatContainer/hooks/useChatConversation.ts
  ```

## 5. 当前明确未完成/未通过的项目

### A. 前端契约测试目前 10 通过、5 失败

运行环境 Node 20 缺少 `worker_threads.markAsUncloneable`，需 Node 22；临时用 `NODE_OPTIONS` polyfill 后测试可运行：

```bash
cd frontend
NODE_OPTIONS='-r /private/tmp/lazymind-node20-worker-polyfill.cjs' \
  node node_modules/vitest/vitest.mjs run \
  src/modules/chat/components/ChatInput/LocalWorkspace.contract.test.tsx
```

当前失败原因：

1. 测试里的 i18n mock 尚未认识动态 key，例如 `chat.workspace.permission.ask_as_needed`，因此按钮文本/accessible name 显示 key，导致 4 个权限 UI 断言失败。生产 `zh-CN.ts`/`en-US.ts` 已有文案。应优先让组件使用稳定 label 映射，或在不改变已批准行为的前提下补齐测试 mock；不要简单删断言。
2. 点击失效历史工作区后的重授权是 Promise 异步流程，测试立即 `getByRole(dialog)`，当前尚未等到弹窗。应检查是否可以让交互状态可靠地在异步完成后出现，并用 `findByRole`/`waitFor`验证；同时确保真实 UI 有 loading 与错误反馈。
3. `message.success` 在多测试间留下 DOM/异步告警，需要清理或使用统一 mock，避免干扰。

### B. OpenAPI 生成客户端未完整再生成

- Core OpenAPI 源已更新：
  - `api/backend/core/openapi.yml`
  - `api/backend/core/swagger.json`
  - `frontend/scripts/openapi/specs/core.yaml`
- 执行 `pnpm gen:openapi -- core --skip-cache` 失败：当前环境无法访问 Maven，且没有 Java Runtime。
- `frontend/src/api/generated/core-client/api.ts` 只手工补了新 schema 类型和 `LocalWorkspace.write_policy=allow`，**尚未完整生成新 PUT 方法和 `include_inactive` 参数**。
- `.openapi-cache.json` 曾手工更新，但之后 `api.ts` 又有修改，因此当前 output hash 很可能已过期。接手人必须在有 Java/生成器的环境正式运行：

  ```bash
  cd frontend
  node scripts/openapi/generate-api.mjs core --skip-cache
  pnpm gen:openapi:check
  ```

  不要用伪造 cache hash代替正式生成。

### C. Python 测试尚未实际运行

- 系统 Python 没有 pytest，执行报 `No module named pytest`。
- 至少需要运行：

  ```bash
  PYTHONPATH=algorithm/lazyllm:algorithm python3 -m pytest \
    algorithm/tests/chat/engine/tools/test_workspace_permission_modes_contract.py \
    algorithm/tests/chat/engine/tools/test_local_workspace_iteration_two_contract.py \
    algorithm/tests/chat/engine/agent_runtime -v --tb=short
  ```

- 特别检查：文件批准一次性重放、`allow_all` 自动重放、MCP connected-app 批准、子 Agent/Skill 权限继承、模式版本变化后旧操作失效。

### D. Core 全量测试存在环境阻塞与一个已修正契约差异

- `go test . ./chat ./localworkspace ./migrate` 中需要监听本机端口的 shutdown/httptest 用例被当前 sandbox 以 `bind: operation not permitted` 阻止；这不是已确认的代码失败，应在允许 loopback socket 的环境重跑。
- 旧 OpenAPI 测试原先期待 `ask_before_write`，已按最终确认语义更新为 `allow`。
- migration catalog 的 v0_3 dev 数量已从 46 更新为 47。

### E. 大范围安全/资源要求尚未证明完成

`checklist.md` 中仍有大量项目未逐项实现和验证，重点包括：

- descriptor/Handle 锚定的 TOCTOU 防护与 Windows containment；
- broker 集中跨任务写锁，而非仅进程内锁；
- 真实命令 runner 的系统调用层隔离、进程树、5 秒撤销复核；
- Skill 脚本与任意解释器子进程的工作区外访问阻断；
- `.git/**`、链接/挂载/设备/ACL/扩展属性等永久禁止；
- stdout/stderr 流式 1 MiB 上限与 Secret/绝对路径脱敏；
- PDF/Office/压缩处理预算、staging/commit 与一次性扩容批准；
- 10 分钟批准生命周期、刷新恢复、重启失效、完整 FIFO/幂等/冲突；
- PostgreSQL 与 SQLite 的真实升级/回退/数据保留双路径；
- Local `make local-up` 与 Desktop 的真实端到端文件闭环。

不能因为当前聚焦测试通过就忽略这些已批准验收项。

## 6. 建议接手顺序

1. 先运行 `git status --short`，阅读本文件与三份计划文档，检查最近修改的代码，不回退工作树。
2. 修复前端 5 个契约失败，使 15/15 通过；确认 UI 顺序、互斥、遮罩、`Esc`、原生选择器和成功提示。
3. 在可用 Java/网络环境正式重新生成 Core TypeScript 客户端并通过 stale check。
4. 使用项目可用 Python 环境运行权限模式和本地文件全部测试，修复执行审批闭环。
5. 给 Core trusted reauthorization 和 Local Proxy reauthorization 增加所有权、active/revoked、路径身份变化、token 一次性/过期测试。
6. 运行 Core、Local Proxy、Local Runtime Manager、Desktop、前端受影响回归。
7. 对照 `checklist.md` 逐项补齐未实现安全边界，特别是 broker/containment/TOCTOU/Skill/文档处理，不要只做 UI。
8. 运行 `make local-up` 做 macOS 人工验收；Windows 人工验收可按用户要求跳过，但代码/交叉构建和安全失败路径仍应验证。
9. 最终报告必须区分：自动测试通过、环境阻塞、未执行人工项、残余风险；只有全部批准范围完成才能声明完成。

## 7. 最近修改的关键文件

- Core：
  - `backend/core/common/orm/local_workspace_models.go`
  - `backend/core/localworkspace/service.go`
  - `backend/core/localworkspace/handlers.go`
  - `backend/core/chat/local_workspace.go`
  - `backend/core/chat/localfs_paths.go`
  - `backend/core/routes.go`
  - `backend/core/openapi_manual.go`
- 迁移：
  - `backend/core/migrations/dev_mode/v0_3/20260903023152_add_workspace_permission_mode.*.sql`
  - `backend/core/migrations/version_mode/v0_3/20260805000000_workflow_runtime_release.*.sql`
- Local/Desktop：
  - `local/local-proxy/internal/server/workspace.go`
  - `local/local-proxy/internal/server/server.go`
  - `desktop/electron/src/main.js`
  - `desktop/electron/src/preload.js`
- 执行策略：
  - `algorithm/lazymind/chat/engine/tools/local_fs.py`
  - `algorithm/lazymind/chat/service/chat_service.py`
  - `algorithm/lazyllm/lazyllm/tools/agent/shell_tool.py`
  - `algorithm/lazyllm/lazyllm/tools/agent/functionCall.py`
  - `algorithm/lazyllm/lazyllm/tools/mcp/tool_adaptor.py`
- 前端：
  - `frontend/src/modules/chat/components/ChatInput/LocalWorkspaceControl.tsx`
  - `frontend/src/modules/chat/components/ChatInput/index.tsx`
  - `frontend/src/modules/chat/components/ChatInput/index.scss`
  - `frontend/src/modules/chat/components/ChatInput/types.ts`
  - `frontend/src/modules/chat/pages/chatLayout/index.tsx`
  - `frontend/src/modules/chat/components/newChatContainer/hooks/useChatConversation.ts`
  - `frontend/src/i18n/locales/zh-CN.ts`
  - `frontend/src/i18n/locales/en-US.ts`
- 阶段一补充测试：
  - `frontend/src/modules/chat/components/ChatInput/LocalWorkspace.contract.test.tsx`
  - `backend/core/chat/local_workspace_permission_mode_contract_test.go`
  - `backend/core/local_workspace_permission_mode_http_contract_test.go`
  - `backend/core/migrate/local_workspace_permission_mode_contract_test.go`
  - `backend/core/localworkspace/workspace_permission_mode_contract_test.go`
  - `algorithm/tests/chat/engine/tools/test_workspace_permission_modes_contract.py`

## 8. 当前停止点

用户因 token 不足明确要求终止任务并生成交接文档。本文件生成后没有继续修复、测试或实现。接手 Agent 应从“前端 5 个契约失败 + 正式 OpenAPI 客户端生成 + Python 执行策略测试”开始，同时以完整 checklist 审核实现充分性。
