# Local/Desktop Work 任务本地工作区开发交接（范围收敛版）

更新时间：2026-09-04

## 0. 最新接手状态（本节优先于下方旧摘要）

- 用户已批准继续开发到满足 `checklist.md`，采用 Backend-first：优先修改
  `backend/`，只有 Backend 无法实现原生选择、进程内文件/命令约束、Desktop bridge
  或可见交互时才最小修改其他自有模块。
- `algorithm/lazyllm` 绝对只读，不得修改或增加内容。当前 URL 为官方仓库，gitlink 为
  `084d49054d5b44d3ebde6911514660354aaf440d`，子模块工作树 clean。
- 下方“聚焦回归 26/26”等数字已经过时。对最终工作树重新执行的结果为：Core/Chat/
  LocalWorkspace/Migrate/SubAgent 631、Local Proxy 80、Local Runtime Manager 130、Desktop
  8、Algorithm 80、Frontend 相关 30，均通过；Frontend 受影响文件 ESLint、生产构建、
  OpenAPI stale check 和 Python 编译通过。
- Frontend 全仓 `tsc --noEmit` 存在大量既有生成客户端和跨模块错误，本功能不把它作为
  完成门槛。`frontend/node_modules` 是本地构建依赖的符号链接，当前无 TTY 的 `pnpm
  exec` 会在清理询问处退出；验证使用已有 `node_modules/.bin/*`，不改共享依赖。
- 已补：Core 撤销/重授权/乐观版本 handler 契约、三类 actor 撤销边界、文件
  read/version 同快照、创建提交前复核、跨平台相对路径、Windows reparse/父目录句柄
  guard、macOS/Linux 命令 containment、Shell 执行前复核、联网审批、pending 权限快照、
  明确权限阻止提示，以及 Frontend 搜索/取消/成功反馈和四类 allow-all 风险。
- 独立 reviewer 发现并已修复：主/子 Agent legacy 绝对路径越界、子 Agent 伪造上下文与
  批准事件丢失、命令读取宿主配置、always_ask 写命令漏判、Win32 leaf-swap、覆盖版本
  复核窗口、Chat/Work 切换隐藏旧 workspace ID、撤销取消污染当前选择。
- 平台命令支持：macOS 使用系统 `sandbox-exec`；Linux 使用 `bwrap`，缺少时安全失败；
  Windows 暂无可验证的进程文件 containment，命令安全失败而不降级为裸 Shell。Windows
  文件 API 使用 Win32 目录句柄锁和 reparse point 检查，真机行为仍由 CI/人工验证。
- 真实 Local 与打包 Desktop E2E 由用户人工执行；Agent 负责提供精确步骤并根据反馈修复。
- PostgreSQL release/dev/schema/data/down 集成路径已使用一次性本地实例执行并通过；SQLite
  对应路径通过。外部 `TARGET_MIGRATIONS_DIR` 未提供，因此仅该目标分支对比未执行。
- 当前工作树 Local Runtime 已重启并在 `http://localhost:8090` 就绪。`make local-up` 的
  Docker skill-bundler 前置因 Docker Desktop 容器停留 Created 而阻塞；最小 Alpine
  容器同样复现。本轮使用已有已物化 Skill 和 `local-runtime-manager down/up` 启动，未清数据。
- 稳定设计见 `IMPLEMENTATION_DESIGN.md`，执行计划见 `IMPLEMENTATION_PLAN.md`，逐项证据
  见 `EVIDENCE.md`，人工步骤见 `MANUAL_ACCEPTANCE.md`。阅读顺序：本文件 ->
  `checklist.md` -> `EVIDENCE.md` -> 设计/计划。

## 1. 当前仓库状态

- 仓库：`/Users/theone/Downloads/lazymind`
- 分支：`feature/newWorkZone`
- 当前远程基线：`c07096c7 feat: update workspace support and LazyLLM submodule`，本次范围收敛会反向恢复其中的子模块改动。
- 工作树存在未提交的范围收敛改动，禁止 reset、clean 或覆盖无关文件。
- `docs/plan/.DS_Store` 是用户/系统产生的独立改动，本轮没有处理，后续也不要纳入本功能提交。
- LazyLLM 已恢复官方仓库 `https://github.com/LazyAGI/LazyLLM.git` 和固定版本 `084d49054d5b44d3ebde6911514660354aaf440d`，子模块工作树 clean。
- `lazyllm-workspace-permissions.patch` 已删除；本需求不再修改 LazyLLM。
- 用户确认的代码边界：业务改动尽量位于 `backend/` 和 `frontend/`；`algorithm/lazymind/`、`local/`、`desktop/` 仅保留原生选择、Agent 文件执行和本地启动所需的最小改动。Office、其他业务模块和外部依赖保持 `origin/main`。

## 2. 最新需求边界

产品范围由用户提供的最初需求和后续交互图共同确定。

必须实现：

- Local/Desktop 的 Work 任务选择并授权一个本地文件夹；
- 授权范围内读取、创建和修改文件；
- 工作区外访问拒绝；
- 查看和撤销授权，撤销后不能继续访问；
- Chat 和未启用部署模式不提供本地文件权限；
- 工作区搜索、有效授权直接切换、失效授权重新授权、原生目录选择器和“不使用本地工作区”；
- “始终询问 / 按需确认 / 全部允许”三档权限；
- 全部允许风险确认框，覆盖文件、命令、联网和已连接应用；
- 菜单互斥、空白/遮罩/关闭/`Esc` 取消、成功提示和越界阻止提示。

必要安全实现可以保留：路径规范化、符号链接防逃逸、目录身份、操作/提交前复核、版本冲突、原子写、永久禁止项和子 Agent/Skill 不扩大范围。

## 3. 已确认的非目标

以下被确认为过度开发，应删除或不再继续：

- 新的通用命令执行产品；只需让项目已有命令能力遵守权限模式。
- 固定 4 读/2 命令/1 写的资源调度系统。
- 跨请求读取/写入租约及复杂租约调度。
- 多请求 FIFO、长期批准持久化或复杂恢复状态机。
- 为本需求改造全局 Office 转换服务。
- 通用压缩包条目/解包预算和精确扩容批准体系。
- 与当前工作区授权平行的新权限表、授权来源或服务。

## 4. 本次范围收敛已做的工作

- 撤销了审计过程中误删三档权限 UI 的部分改动，三档权限必须保留。
- 将以下文件恢复到 `b27a0e19` 版本，以移除最近增加的调度复杂度：
  - `algorithm/lazymind/chat/engine/agent_runtime/tool_limit_control.py`
  - `algorithm/lazymind/chat/engine/tools/local_fs.py`
  - 对应的工作区批准/文件测试
  - Local Proxy 和 Runtime Manager 的 Broker 环境接入点
- 删除最近新增的：
  - `workspace_command*.go`
  - `workspace_lock*.go`
  - 临时 `task_plan.md`、`findings.md`、`progress.md`
- 全局 Office 转换服务已恢复，没有保留超范围改造。
- 已重写本目录的 `spec.md`、`tasks.md` 和 `checklist.md`。
- 一次性批准重放、模型权限参数清洗和已连接应用确认已迁移到 LazyMind `ToolCallGuard`。
- 工作区任务使用 LazyMind 自有同名 `shell_tool` 适配器覆盖 LazyLLM 内置 Shell；适配器固定工作区并使用结构化参数执行。
- 新增后端适配文件：`algorithm/lazymind/chat/engine/tools/workspace_shell.py`。
- 关键后端改动：
  - `algorithm/lazymind/chat/engine/agent_runtime/executor.py`
  - `algorithm/lazymind/chat/service/chat_service.py`
  - `algorithm/lazymind/chat/engine/tools/local_fs.py`
- 新增/更新测试：
  - `algorithm/tests/chat/engine/tools/test_workspace_shell.py`
  - `algorithm/tests/chat/engine/agent_runtime/test_function_call_approval_contract.py`
  - `algorithm/tests/chat/engine/tools/test_workspace_permission_modes_contract.py`
  - `algorithm/tests/chat/engine/tools/test_local_workspace_iteration_two_contract.py`

范围收敛后的聚焦回归已通过，但真实 Local/Desktop E2E 尚未执行，不能据此宣称全部验收完成。

已通过的回归：

- Core：`go test . ./chat ./localworkspace ./migrate`
- Local Proxy：`go test ./...`
- Local Runtime Manager：本机测试与 Windows amd64 交叉测试
- Desktop：原生工作区与 preload 契约 8/8
- Frontend：受影响文件 ESLint、工作区交互契约 15/15、OpenAPI stale check
- Python：工作区文件、权限模式、批准、LazyMind Shell 和 Agent runtime 聚焦测试 26/26

前端 Vitest 仍输出一条既有 React `act(...)` 告警和 Node localStorage/Sass 提示，但测试通过；后续若处理，只能调整测试，不要扩大产品范围。

尚未执行：Local `make local-up` 与打包 Desktop 的真实授权—读—建—改—撤销闭环。Windows 人工验收已由用户豁免。

## 5. 应保留并继续核对的现有实现

- Core 工作区授权、列表、撤销、任务绑定和目录身份验证；
- Local Proxy 原生目录选择与一次性候选 token；
- Desktop main/preload 原生选择桥；
- 前端工作区菜单、授权弹窗、撤销和三档权限交互；
- Work/Chat 和部署模式门禁；
- Algorithm 工作区文件读取、创建、覆盖、追加和文本替换；
- 三档权限持久化、修改接口和现有一次性批准链；
- OpenAPI 和生成客户端中的工作区、绑定及权限字段。

## 6. 下一位 Agent 的首要任务

1. 运行 `git status --short`，确认只处理本功能文件并排除 `docs/plan/.DS_Store`；该文件当前同时存在 staged/unstaged 系统改动，不得恢复、删除或提交。
2. 搜索残留的固定并发配额、租约、压缩扩容和全局 Office 改造；当前生产代码扫描已无残留。
3. 检查三档权限及前端交互未因范围收敛回退而受损。
4. 核对 `LocalFileToolkit`：只公开文件读/建/改能力；若仍公开新的通用 `run_command` 文件工具，应移除该公开入口，但保留项目原有 shell/命令体系的权限约束。
5. 确认 `.gitmodules` 指向官方 LazyLLM、gitlink 为 `084d4905`、子模块 clean 且仓库中不存在补丁文件。
6. Core OpenAPI stale check 当前已通过；仅当规格再次变化时重新生成。
7. 自动回归当前全部通过；修改后按下方命令重跑。
8. 下一优先级是按新 `checklist.md` 执行 Local/Desktop 真实 E2E；Windows 人工验收已豁免。

## 7. 建议验证命令

```bash
cd backend/core
GOCACHE=/private/tmp/lazymind-go-build go test . ./chat ./localworkspace ./migrate
```

```bash
cd local/local-proxy
GOCACHE=/private/tmp/lazymind-go-build go test ./...
```

```bash
cd local/local-runtime-manager
GOCACHE=/private/tmp/lazymind-go-build go test ./...
```

```bash
cd desktop
node --test scripts/preload-bridge.test.mjs scripts/local-workspace-contract.test.mjs
```

```bash
cd frontend
pnpm exec eslint \
  src/modules/chat/components/ChatInput/LocalWorkspaceControl.tsx \
  src/modules/chat/components/ChatInput/index.tsx \
  src/modules/chat/components/ChatInput/types.ts \
  src/modules/chat/pages/chatLayout/index.tsx \
  src/modules/chat/components/newChatContainer/hooks/useChatConversation.ts
pnpm exec vitest run src/modules/chat/components/ChatInput/LocalWorkspace.contract.test.tsx
pnpm gen:openapi:check
```

```bash
LAZYLLM_LOG_FILE_MODE=split \
PYTHONPATH=algorithm/lazyllm:algorithm \
.venv/bin/python -m pytest \
  algorithm/tests/chat/engine/tools/test_workspace_permission_modes_contract.py \
  algorithm/tests/chat/engine/tools/test_local_workspace_iteration_two_contract.py \
  algorithm/tests/chat/engine/agent_runtime -q --tb=short
```

## 8. 完成标准

只以新 `checklist.md` 为准。不得继续旧 checklist 中的压缩资源审批、固定并发配额、通用命令 Broker 或全局 Office 改造。所有自动测试、环境阻塞和未执行人工项必须分别报告；Windows 人工验收按用户要求豁免。
