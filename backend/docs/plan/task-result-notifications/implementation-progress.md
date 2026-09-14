# 实现进展与测试修正 Review

## 当前状态：2026-09-14

剩余 Core 失败已定位为旧测试夹具未统一 UTC，本轮只修正测试一行，生产逻辑不变。北京时间环境的 asyncjob 模块两库各 27 通过，完整 Core 2500 通过、0 失败、8 跳过。最新证据见 [时区修正报告](asyncjob-timezone-correction-20260914.md) 和 [结果](asyncjob-timezone-results-20260914.json)。通知分支及 backend 范围保持不变，未提交、推送、真实外发或实施目录外接入。

## 2026-09-13 历史状态

用户对 SDK 拒绝测试及三处旧测试适配的具体审阅方案回复“继续”，本批已完成实现和验证。明确拒绝记录安全失败并继续后续产物；人工只补失败部分；先前请求曾丢失响应时保留 unknown 和重复确认。旧测试按批准补丁应用，SDK 复现文件未修改。

最新结果：网关 178 通过；E2E 22 通过；三处适配及错误登记两库各 4 通过；最后 SDK／恢复 16 通过，额外双库恢复检查 4 项通过。完整 Core 为 2499 通过、8 跳过、1 项基线已有 asyncjob 失败，其中迁移模块 47 项通过；未宣称全量测试全绿。

详情见 [验收报告](delivery-verification-20260913.md)、[验证证据](sdk-correction-results-20260913.json) 和 [交接说明](backend-handoff.md)。当前工作目录 `/Users/zouyu/Downloads/LazyMind-notifications`，通知分支，仓库改动仅 backend；原目录无关改动和保留 stash 未触碰。没有真实飞书外发、目录外接入改动、提交或推送。

以下为历史记录，早期“尚未实现／待 Review”状态由上面的当前状态和集成报告更新。

2026-09-10 用户批准阶段一测试，开始阶段二。在 `codex/feishu-task-notifications` 上开发，改动仅在 backend；未提交或推送。原测试报告仍作为阶段一记录保留。

## 已落地但尚未完整交付

- Core 通知模型、全局设置、任务规则、版本冲突、关闭影响确认、执行快照、事件与状态事务、历史与重试意图、内部授权路由。
- 创建与批量创建计划携带规则，旧编辑保留配置。
- 执行收尾检查等待和会话状态；结果写入错误不再产生成功。
- 新增 PostgreSQL/SQLite 通知迁移和既有 v0.3 聚合增补。
- 尚未实现网关目标/账号/渲染/投递、Core 摘要与交接工作进程、完整 OpenAPI 请求响应定义和全部状态路径接入。当前重试只有持久化意图，不能完成发送。

## 已批准并完成的测试修正

2026-09-10 用户回复“批准，先进行修正”。三处补丁已应用并验证；具体差异见 [test-correction-proposal.patch](test-correction-proposal.patch)。保持原业务断言和覆盖范围：

1. `notification_api_test.go`：OpenAPI 查询使用 `apiPrefix + path`。仓库 `openapi_gen.go` 的 apiPrefix 为 `/api/core`，现有覆盖测试也如此；新增断言漏此前缀导致六项路径误报。
2. `scheduler/notification_lifecycle_test.go`：模拟聊天持久化使用长度最多 36 的新 ID。当前 `history-` 拼接会话 ID 超过 `chat_histories.id` 的 PostgreSQL VARCHAR(36)，触发 SQLSTATE 22001；不能扩大生产字段来适配错误测试数据。
3. `scheduler/automation_test.go`：建库夹具补建 SubAgentTask 和 SubAgentArtifact 两张实际会被收尾读取的表。原实现忽略查询错误而掩盖了缺表；现在不能通过忽略错误来遗漏产物。

本次授权已解除测试修改门禁。仅修改上述三处测试及验证文档，本轮没有新增生产改动；阶段一另 11 份测试的 SHA256 保持不变。原始阶段一结果继续保留。

## 修正后的验证

命令从 backend/core 执行，三组进程均退出 0，详细用例与最新测试 SHA256 见 [test-correction-results.json](test-correction-results.json)。

| 检查 | 通过 | 失败 | 跳过 |
| --- | --- | --- | --- |
| 文件 SQLite：通知测试 + 既有 OpenAPI 路由覆盖 | 29 | 0 | 0 |
| 真实 PostgreSQL：同批测试 | 29 | 0 | 0 |
| Core / Scheduler 受影响回归 | 14 | 0 | 0 |

gofmt、git diff --check 通过。没有重跑未改动的迁移和网关测试；先前已知的 v0.3 SQLite 聚合迁移问题仍待修复，不能将本批通过等同于完整通知功能完成。

## 修正前的验证记录

命令均从 backend/core 执行；详细用例结果持久化在 [implementation-results.json](implementation-results.json)。

| 检查 | 结果 |
| --- | --- |
| `go test . ./scheduler -run '^TestNotification' -count=1 -json`，文件 SQLite | 27 个顶层用例通过、1 个失败（OpenAPI 前缀） |
| 同模块 PostgreSQL，额外包含既有 OpenAPI 全路由测试 | 27 通过、2 失败（OpenAPI 前缀、测试 ID 超长） |
| `go test ./migrate -run '^TestNotification' -count=1 -json`，真实 PostgreSQL 与文件 SQLite | 顶层 2 通过、1 失败；通知增量 up/down/旧数据保留在两库均通过；PG 聚合/dev 一致性及回退通过；SQLite 聚合被既有 v0.3 SQL 语法阻断 |
| `go test . -run '^TestOpenAPISpecCoversAllRegisteredRoutes$' -count=1 -json` | 1 通过，说明新路由已按项目路径规则导出 |
| `go test ./taskcenter ./scheduler -run '^Test(UpdateTask|CreateTask|CreateSchedule|RunNow|Fire|Finalize|Dependency)' -count=1 -json` | 11 通过、3 失败（旧测试夹具缺少产物表） |
| gofmt、git diff --check | 通过 |

PG 使用隔离测试服务 `postgresql://postgres@127.0.0.1:32768/postgres`；SQLite 为文件测试库（既有 automation 回归仍使用其内存夹具）。无真实飞书发送。

## 接续工作

1. 三处测试修正已按本轮授权完成，复验通过。继续实现时保持已批准业务断言。
2. v0.3 聚合 SQLite 分支既有 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` 无法执行。应按迁移目录规则把已在本版创建的 vocabulary_review_sessions 的最终列合并到 CREATE TABLE，保持 dev 文件不变，再核对两库聚合/dev/回退。这是生产迁移问题，不能弱化测试跳过。
3. 补齐 Core 摘要生成与持久交接工作进程、网关实现、全部事件入口、错误/权限/OpenAPI 源、后端部署交接；最后跑完整通知与受影响回归。
4. 保持仅 backend 范围；真实飞书送达和目录外部署未验证。


## 2026-09-11 接续实现与第二次测试数据修正

本轮未改动已批准测试。新代码仍仅限 backend；未提交、推送或真实飞书发送。

### 本轮推进

- 网关新增通知能力、目标校验、接收对象、账号引用/断开影响、稳定账号重连及内部交接/历史/重试接口。
- 既有出站队列增加通知业务身份、持久幂等、分片结果、部分成功、失败重试、未知发送结果和重复风险确认；每片检查 Core 开关与飞书权限。
- 飞书卡片使用 2.0 结构并限制序列化大小；长段落转文本附件，图片/文件原生投递，失败产物有安全提示。
- SDK 接入官方群查询、机器人在群判断和发言权限查询；注册补充群读取权限。
- 修复 SQLite 出站租约续租的 SQL 方言转换；用户主动断开保留账号行；重连身份不匹配拒绝；清理失去租约后安全停止。
- Core 增加摘要、交接及结果同步工作进程，内部账号引用查询。摘要走 mode=llm 的既有接口、用户选定模型、分段归纳和 30 秒总超时；摘要补发使用原投递身份追加缺失内容。
- 聚合迁移中 vocabulary_review_sessions 的新列已并入 CREATE TABLE，解决 SQLite 的 ADD COLUMN IF NOT EXISTS 语法错误。继续验证发现其他既有聚合/dev 差异，尚未修完。

### 已执行验证

详细结果见 phase2-results-20260911.json。

- 网关通知 API、内容、交接、队列和恢复：138 通过（文件 SQLite + 真 PostgreSQL）。
- 账号、协议、既有飞书/微信回复及安全回归：36 通过；曾捕获重连清理失租约线程告警，已修复，再跑重连/心跳 5 项通过，无该线程告警。
- 新 Core 工作进程代码编译及通知/OpenAPI 回归：文件 SQLite 29 通过。
- 通知迁移顶层 2 通过、1 失败：PG 聚合/dev/回退以及双库增量迁移通过；SQLite 聚合/dev 指纹仍有差异。
- 端到端：2 个启动夹具通过，首个摘要用例失败后按 -x 停止；其余未验证。失败源头是测试写入不存在的聊天历史时间列。
- 新增三个 Python 模块 flake8（max-line-length=120）、compileall 和 git diff --check 已验证。尚未执行最终完整 lint/回归。

### 必须先 Review 的夹具修正

`test_notification_e2e.py` 模拟聊天结果 INSERT 使用 `created_at,updated_at`，实际 ChatHistory.TimeMixin 为 `create_time,update_time`。SQLite 实际报 `table chat_histories has no column named created_at`，因此测试任务失败且没有触发摘要模型。先前测试阶段此断言被更早缺失接口失败遮挡。

建议只替换这两个列名，保留 CURRENT_TIMESTAMP、模拟结果正文和全部端到端业务断言。当时差异写入 e2e-fixture-correction-proposal.patch 等待批准；后续用户已批准并应用，22 项端到端测试全部通过。

### 后续检查重点

- 应用获批夹具补丁后跑端到端双数据库，核对摘要补发、服务重启、响应丢失、重复交接。
- SQLite 聚合与 dev 当前差异包括外部 Agent 表的 host_id 默认、TIMESTAMP/DATETIME、命名唯一索引/表内约束表示，知识市场主键 NOT NULL，以及多张表列顺序和 DDL 空白。不得仅忽略实际约束差异；依照迁移规则完成 final-state 聚合，保留已合并 dev 文件。
- Core 成功产物快照尚需完整补齐 kind/source/size，全部 workflow 暂停/恢复入口尚需核对；网关失败重连时不得删除已有身份，仍需检查全部孤立清理路径。
- Core 事件租约失效后目标更新的 fencing、总开关失效队列、权限失效及限流的生产边界需完整审阅，不能仅依赖当前测试通过。
- 公开 OpenAPI 目前路由可见，仍需完整请求/响应 schema、后端集中权限提取和环境变量交接文档。
- 现有 app 账号列表已读取 Core 引用信息，需检查 Core 不可用时对既有会话入口的兼容性。

## 2026-09-11 本轮接续结果

- 已确认获批 E2E 夹具修正后的完整运行结果：22 通过，6 项既有依赖弃用告警。真实 Core 和网关进程，模型及飞书边界模拟；没有真实外发。
- 修正现有 v0.3 SQLite 聚合的最终表定义、既有表追加列顺序、索引和回退重建。约束和默认值对齐已合并 dev 的最终效果，不修改任何已合并 dev 文件。回退显式保留用户聊天设置的数据列。
- 通知迁移顶层 3 项通过，内部同时验证 PostgreSQL 与文件 SQLite，涵盖增量 up/down/up、旧数据、不补发、聚合/dev 相同以及回退。
- 扩大迁移回归：46 通过、1 失败。唯一失败是 mode_test.go 写死 v0.3 有 66 个 dev migration；通知新增后实际 67。未修改该测试，拟修正计数并增加通知迁移版本存在断言。
- 发现实际工作流停止/恢复入口尚未通知：SetSessionStopped 保存 stopped，可通过 resume 恢复；任务中心却投影 canceled。旧回归明确期待 canceled，原通知测试仅模拟 waiting，因此没有暴露实际入口缺口。此处涉及既有接口行为及测试调整，按根 AGENTS.md 第 1、5 节暂停扩展生产实现。
- 新增 notification_workflow_lifecycle_test.go，调用真实 HTTP stop/resume、引擎 UpdateSessionStatus、取消入口及真实数据库，覆盖命令重放、同次暂停去重、恢复再暂停、失败、事务回滚、彻底取消。两库分别 3 项预期功能失败、1 项取消回归通过，无环境跳过；尚未实现本批新增测试对应的工作流生产改动。
- 待审阅内容集中在 workflow-review.md、workflow-review-proposal.patch；后者两处既有测试补丁尚未应用。详细结果在 phase2-workflow-review-results-20260911.json。

其余已记录缺口仍未宣称完成：正式结果产物元数据、Core 交接租约写入隔离、目标独立交接、账号失败重连保留、账号列表降级、平台排队限流、完整 OpenAPI/错误源和部署交接。所有变动仅 backend；未提交或推送。
