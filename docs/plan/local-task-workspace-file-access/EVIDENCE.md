# Local/Desktop Work 本地工作区验收证据

更新时间：2026-09-04

本文档是 `checklist.md` 的证据索引。代码与重新执行的测试优先于历史文档。

## 最新自动验证

| 层 | 结果 | 命令/证据 |
| --- | --- | --- |
| Core | 通过 | `go test . ./chat ./localworkspace ./migrate ./subagent -count=1`，631 tests |
| Local Proxy | 通过 | `go test ./... -count=1`，80 tests |
| Local Runtime Manager | 通过 | `go test ./... -count=1`，130 tests |
| Desktop | 通过 | preload/workspace Node contracts，8 tests |
| Algorithm | 通过 | workspace/permission/shell/Win32/SubAgent/Agent runtime，80 tests |
| Frontend | 通过 | 受影响文件 ESLint；workspace/TaskCenter Vitest，30 tests；Vite production build |
| OpenAPI | 通过 | auth/core/scan/channel-gateway 均为 `fresh` |
| Python 编译 | 通过 | 新增和受影响的 8 个 Python 文件 `py_compile` |
| Windows Go | 通过 | Runtime Manager 四个包 `GOOS=windows GOARCH=amd64 go test -c` |

## 环境说明

- PostgreSQL 使用一次性本地实例执行 `TestRepositoryPostgresMigrationPaths` 并通过；SQLite
  release/dev、fresh/legacy upgrade、down 和数据保留路径也实际执行通过。仅外部
  `TARGET_MIGRATIONS_DIR` 分支对比因未提供目标仓库而跳过。
- Frontend 全仓 `tsc --noEmit` 在生成客户端和多个既有模块存在大量基线错误，不作为本
  功能门槛。受影响文件 ESLint、实际 Vitest 与 OpenAPI stale check 均通过。
- `frontend/node_modules` 指向 `local/build/deps/node/frontend`。无 TTY 的 `pnpm exec`
  会触发依赖清理询问，本轮直接使用已有 `node_modules/.bin/*`，未修改共享依赖。
- Frontend 测试仍有既有 React `act(...)`、Node localStorage 和 Sass 弃用提示。

## 核心与授权证据

- 部署/任务门禁：`TestChatModeCannotBindLocalWorkspace`、
  `TestCloudRuntimeCannotBindLocalWorkspaceEvenForBackgroundTask`、
  `TestIterationTwoResolveRejectsQuickQuestion`、`TestWorkspaceSelectionStaysDisabledForLANProfile`。
- 单任务单工作区：`TestExistingWorkTaskCannotSwitchWorkspace`、
  `TestExistingWorkTaskWithoutWorkspaceCannotBeBackfilled` 和 schema unique contract。
- 所有权与原子创建：`TestFirstWorkMessageCannotBindAnotherUsersWorkspace`、
  `TestFirstWorkMessageRejectsRevokedWorkspaceWithoutLeavingConversation`。
- 最近授权和搜索：`TestListSearchesOwnedRecentWorkspacesByNameAndPath`。
- 撤销任务数及三种 actor：`TestRevokeReturnsAffectedTaskCountAndBlocksEveryActor`。
- 同路径重授权不恢复旧任务：`TestRegisterAfterRevocationCreatesNewGrantWithoutRestoringOldTask`。
- 子 Agent 参数由 Core `authoritativeSubagentParams` 从当前用户、Work 和绑定重建，模型
  伪造的 `parent_agentic_config` 与 legacy 绝对路径被删除。
- 权限乐观版本：`TestUpdateConversationPermissionUsesOptimisticVersion`。
- 目录身份变化：`TestCurrentDirectoryIdentityChangesWhenDirectoryIsReplaced`、
  `TestValidateCurrentDirectoryPermanentlyMarksReplacementUnavailable`。

## 文件与命令证据

- 文件 API 精确集合：`test_workspace_file_toolkit_exposes_only_read_create_and_modify_apis`。
- 列表、glob、grep、read：`test_workspace_lists_searches_and_reads_relative_files`。
- 创建、覆盖、追加、精确替换和版本：create/append/stale-version/exact-replace contracts。
- 读内容和版本来自同一快照：`test_workspace_read_version_matches_returned_content`。
- POSIX symlink/父目录替换：read/list/search/info/create/mkdir/overwrite race contracts。
- Windows 路径：`workspace_win32.py` 使用不含 `FILE_SHARE_DELETE` 的目录句柄锁定祖先，
  拒绝 reparse point；纯路径和 share-mode contracts 通过，Windows 真机行为待人工/CI。
- 公开路径：绝对、父级、空字节、drive/UNC 和反斜杠逃逸参数化 contracts。
- 创建与覆盖：同目录临时文件、`fsync`、提交前 Core 复核和原子 rename/replace。
- macOS 命令：真实 sandbox 正向写入和工作区外读取拒绝 tests。
- Linux 命令：`bwrap` 只写绑定 `/workspace` 和私有 `/tmp`，默认断网；缺少隔离器时安全失败。
- Windows 命令：没有可验证的进程文件 containment，因此安全失败，不降级到裸 Shell。
- 永久禁止：提权、权限/所有权、链接、挂载、进程终止、破坏性 Git 和全局 Git 配置。
- 撤销：命令启动前复核，长命令每 5 秒复核并终止进程树。
- 子 Agent：权威工作区继承、Task SID 决定路由、Task SSE pending 事件和 TaskCenter
  `ToolLimitCard` 使用同一短期批准链；不新增长期批准状态。
- bound Work 的文件工具只接收 workspace source，不再混入 file-watcher legacy 绝对路径。

## 三档权限证据

- Core schema/migration 保存 `permission_mode` 和 `permission_version`，默认 `ask_as_needed`。
- `always_ask`：文件修改、联网工具和连接应用均有 ToolCallGuard contract。
- `ask_as_needed`：安全文件/命令直接执行，npm 等已有风险操作要求一次批准。
- `allow_all`：跳过可批准请求，但 permanent-denial tests 始终拒绝。
- 模型伪造 `allow_unsafe` 被清除，且不会向其他工具 schema 注入该参数。
- 权限切换使用调用开始时的快照，不能自动批准已经 pending 的操作。
- `always_ask` 只有显式只读命令白名单免确认，解释器和其他潜在写命令均先批准。
- LazyLLM URL 为 `https://github.com/LazyAGI/LazyLLM.git`，gitlink 为 `084d4905...`，
  子模块工作树 clean；所有批准和命令适配均位于 LazyMind。

## Frontend 与宿主证据

- Frontend contracts 覆盖 Work/Chat/部署模式显示、菜单展开收起、名称/路径搜索、有效
  切换、失效重授权、原生取消、不使用工作区、互斥菜单和成功反馈。
- 授权框的取消、关闭、backdrop、Escape 均无授权副作用。
- allow-all 风险框独立显示文件、命令、互联网、连接应用四类风险；确认后才生效。
- 工作区边界、撤销和 containment 失败通过既有 tool result preview 显示明确的
  “工作区权限已阻止此操作”或英文等价提示。
- Local Proxy contracts 覆盖 loopback、Origin、forwarded caller、LAN、任意浏览器路径、
  token 篡改、五分钟及单次消费。
- Desktop contracts 覆盖原生 `openDirectory`、五分钟 token、只提交 token 和最小 preload
  bridge，且不修改 watcher/知识库根。
- Frontend 额外覆盖 Chat/Work 切换清理隐藏 workspace ID，以及取消撤销其他最近工作区
  不改变当前选择。

## 排除项

- 生产代码扫描无 `workspace-write-locks`、`workspace-commands:run` 或
  `LAZYMIND_LOCAL_WORKSPACE_BROKER_URL`。
- `backend/office-convert-service` 相对 `origin/main` 无差异。
- `lazyllm-workspace-permissions.patch` 不存在。
- `.DS_Store` 是用户/系统改动，未处理。

## 待人工

按 `MANUAL_ACCEPTANCE.md` 执行 Local 和打包 Desktop 闭环，并核对视觉、焦点、键盘、
中英文及权限阻止提示。人工结果回填本文和 `checklist.md`。

## 当前 Local 运行状态

- 当前工作树已通过 `local-runtime-manager down/up` 完成无数据清理重启。
- Frontend：`http://localhost:8090`；Local Proxy health：`http://127.0.0.1:5024/_local/healthz`。
- `make local-up` 的前置 Docker skill-bundler 在本机 Docker Desktop 中停留于 Created 状态；
  最小 Alpine 容器同样无法启动，确认是 Docker 容器启动层环境问题。本次使用仓库已有已物化
  Skill，重建 Runtime Manager/CLI 后启动，所有 Local 服务健康。
