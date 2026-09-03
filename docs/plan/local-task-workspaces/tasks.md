# Local/Desktop 任务工作区（第一迭代）任务计划

> 本计划适用两阶段门禁。批准本文档后先只编写测试并等待人工 Review；测试获批后才实现生产代码。

## 1. 在测试中固化预期契约与测试矩阵（已完成）

- 影响：后端/前端契约测试及测试 Fixture；阶段一不修改 `api/backend/`、`i18n/errors/`、Core OpenAPI 或生成客户端。
- 依赖：批准的 `spec.md`。
- 工作：用测试表达工作区、候选选择 token、首次授权、任务绑定、撤销和状态查询的预期数据结构、稳定错误语义、服务端部署/模式门禁及可信宿主登记边界；固定授权范围、`allow` 读取策略和 `ask_before_write` 修改策略。稳定错误编号只在阶段报告中提出分配建议，阶段一不写入生产错误目录。
- 验证：预期契约和请求/响应边界测试因生产契约与实现尚不存在而按预期失败。

## 2. 为工作区授权与任务绑定编写后端测试（已完成）

- 影响：`backend/core/` 就近测试、`tests/backend/core/`，必要时迁移测试。
- 依赖：任务 1。
- 工作：覆盖授权、列表、最近排序、精确工作区根、唯一任务绑定、首条消息原子绑定、所有权、撤销即时影响全部任务、发送前并发撤销、重新授权不恢复旧绑定、归档/永久清除、并发绑定和第一迭代文件访问拒绝。
- 验证：SQLite 文件数据库测试；若新增 Core migration，同时覆盖 PostgreSQL/SQLite 建库、升级、回退和约束一致性。阶段一预期因生产行为尚未实现而失败。

## 3. 为 Local 原生目录选择边界编写测试（已完成）

- 影响：`local/local-proxy/`、`local/local-runtime-manager/` 相关测试与配置测试。
- 依赖：任务 1。
- 工作：覆盖 localhost 成功、系统选择取消、候选 token 单次使用与 5 分钟过期、授权弹窗取消、LAN/非 loopback/错误 Origin/无会话/CSRF 拒绝、任意路径和 token 伪造拒绝、无效目录、精确根与真实路径规范化、系统选择器失败、参数注入防护和输出脱敏。
- 验证：Go 单元与 HTTP handler 测试；平台调用使用可注入执行边界，不在单元测试中实际弹窗。

## 4. 为 Desktop 选择与授权桥接编写测试（已完成）

- 影响：`desktop/electron/src/`、`desktop/scripts/*.test.mjs`、`frontend/src/runtime/desktopBridge.test.ts`。
- 依赖：任务 1。
- 工作：覆盖 Electron IPC 参数校验、选择取消、目录真实路径解析、候选 token、授权确认后的可信宿主登记、授权状态读取、撤销同步和 preload 最小暴露面；证明 renderer 不能登记任意路径，且任务工作区不会写入 file-watcher `allowedRoots`。
- 验证：Node 测试和前端 bridge 测试；不得读取授权范围外文件。

## 5. 为图片对应的前端交互编写测试（已完成）

- 影响：`frontend/src/modules/chat/components/ChatInput/`、`frontend/src/modules/chat/pages/newChat/` 及相关测试、i18n。
- 依赖：任务 1。
- 工作：覆盖 Work-only 展示、控件顺序、下拉搜索、最近 8 项、悬停/聚焦管理与撤销确认、打开本地文件夹、首次授权弹窗的精确结构和文案、关闭/取消/允许访问、不使用工作区、系统选择取消、token 过期、错误、待选状态、发送前被撤销、既有 Work 任务只读状态、首条消息后锁定及 Chat/Cloud/LAN 隐藏。
- 验证：Vitest/Testing Library；建立稳定 DOM/无障碍断言，避免只断言内部状态。

## 人工 Review 门禁：阶段一结束

- 汇报需求到测试映射、测试文件、命令、预期失败、异常失败及未覆盖环境。
- 明确生产功能尚未实现。
- 等待用户明确批准本批测试，不自动进入后续任务。

## 6. 实现并同步 Core 契约、工作区与绑定领域边界（已完成）

- 影响：`backend/core/`、必要的新 migration、`api/backend/`、`i18n/errors/`、API 注册、错误映射及前端契约类型。
- 依赖：阶段一测试获批。
- 工作：按已批准测试同步公开契约和稳定错误编号，实现本机授权元数据、精确根、`allow`/`ask_before_write` 策略、任务唯一绑定、事务撤销、派生失效状态、状态查询、所有权和服务端部署/任务类型门禁；实现第二迭代复用的内部解析与策略执行边界，但保持文件能力关闭。
- 验证：任务 2 测试通过；迁移按目录规则验证 PostgreSQL 与 SQLite。

## 7. 实现 Local 原生目录选择与本机保护（已完成；真实 macOS 选择器操作待人工验收）

- 影响：`local/local-proxy/`、`local/local-runtime-manager/`、`Makefile` 和必要的 Local 配置/文档。
- 依赖：阶段一测试获批、任务 6 的授权契约。
- 工作：在现有 Local Gateway 常驻边界中实现 loopback-only 系统选择、单次候选 token 和确认后的可信登记流程及跨平台选择器适配；验证本机会话、精确 Origin、CSRF/一次性凭据、网络配置和真实目录；使用参数化进程调用，不新增独立服务、不上传目录内容。
- 验证：任务 3 测试、`make local-up` 手工选择、`local-up-lan` 拒绝验证。

## 8. 实现 Desktop IPC 与授权状态协同（已完成；Desktop 真实界面闭环待人工验收）

- 影响：`desktop/electron/src/main.js`、`preload.js`、`local-folder-access.js` 及对应测试；必要的 Desktop 运行配置。
- 依赖：阶段一测试获批、任务 6。
- 工作：只复用现有无副作用的目录选择与路径规范化能力，另行接入 Core 工作区授权来源；确保 renderer 不能提交未由 main process 验证的任意路径，且不改变目录发现状态或 file-watcher `allowedRoots`。
- 验证：任务 4 测试及 Desktop 手工流程。

## 9. 实现工作区选择 UI（已完成）

- 影响：`frontend/src/modules/chat/components/ChatInput/`、`pages/newChat/`、运行时桥接、样式与中英文 i18n。
- 依赖：任务 6～8 的契约。
- 工作：按样例图实现 LazyMind 自有的工作区控件、下拉层和首次授权弹窗，不仿制 macOS 系统选择器；管理操作仅在行悬停/聚焦时出现且不改变默认视觉；接入 Local/Desktop 原生选择适配器；把待选 `workspace_id` 带入首次 Work 任务创建；已有 Work 任务显示只读绑定/失效状态并允许查看或撤销，但不能切换或补绑。
- 验证：任务 5 测试、键盘操作和两种语言检查。

## 10. 接入主流程并完成端到端验证（自动化与 Local 启动验证已完成；原生选择器和 Desktop 闭环待人工验收）

- 影响：上述批准范围内的集成测试、Local/Desktop 运行文档。
- 依赖：任务 6～9。
- 工作：验证选择—可信登记—创建任务—重启恢复—多任务复用—撤销—全部任务失效闭环；验证发送前并发撤销、重新授权不恢复旧任务、Chat、Cloud、LAN、跨站/伪造路径和第一迭代文件能力关闭。
- 验证：相关 Go、Node、Vitest、Core 测试；Local/Desktop 手工 E2E；按样例图进行截图对比或人工视觉 Review。
