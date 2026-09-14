# 飞书首期后端任务拆解

状态：2026-09-14，T0—T8 已按获批范围实施，T9 后端自动化链路通过。剩余 Core 基线失败仅需修正旧测试的 UTC 夹具，现完整 Core 2500 通过、0 失败、8 项可选集成／夹具跳过。真实飞书及目录外部署接入仍未验收。当前证据见 [最新验证](asyncjob-timezone-correction-20260914.md)，仓库改动仅限 backend。

下列任务覆盖已批准需求中适用于飞书的全部后端能力；后续阶段仅扩展其他渠道，不承接被裁剪的飞书功能。实施计划及本批测试契约已批准；不建立新的服务或独立架构层。

## T0 冻结范围、行为与测试矩阵

- 依赖：按用户已明确的完整飞书后端范围补齐技术契约与阈值，之后批准三份计划；不再将原先六项建议作为功能裁剪审批。
- 影响：本目录三份文档。
- 参考：完整飞书需求、根目录 AGENTS.md、`backend/core/migrations/AGENTS.md`。
- 工作：逐项映射原需求的全部飞书后端能力，冻结事件映射、摘要／全文／产物呈现、目标类型、超限策略、重试与超时、错误码映射；记录目录外接入依赖。不得擅加仅截取摘要、仅发最终答复、不处理产物或首期单目标等缩减条件。
- 验证：需求 R1—R6、任务、验收项一一对应，无未冻结的实施前置项。

## T1 编写首期测试，等待人工 Review

- 依赖：T0 获批。
- 影响：`backend/core/scheduler/*_test.go`、`backend/core/taskcenter/*_test.go`、`backend/core/userprefs/*_test.go`、`backend/core/common/*_test.go`、`backend/core/migrations/` 相关测试、`backend/channel-gateway/tests/`（新增）。
- 参考：`backend/core/scheduler/scheduler_test.go`、`backend/core/scheduler/automation_test.go`、`backend/core/taskcenter/taskcenter_test.go`、`backend/channel-gateway/test_security.py`。
- 工作：新增规则、权限、真实双数据库、并发／幂等、发送恢复、飞书渲染及主流程契约测试；使用可控 Clock，不能用长 sleep 等待任务。
- 验证：报告每项需求对应的测试、命令、预期失败、异常失败和环境缺口。测试阶段不编写生产逻辑；此处必须停止并等待本批测试批准。

## T2 规则模型与持久化

- 依赖：T1 测试批准后。
- 影响：`backend/core/common/orm/`、`backend/core/scheduler/`、`backend/core/userprefs/`、`backend/core/migrations/`。
- 参考：`backend/core/common/orm/taskcenter_models.go`、`backend/core/userprefs/settings_overview.go`。
- 工作：用户默认设置、计划配置版本、执行规则快照、通知事件及目标身份；保留旧任务和旧执行历史，升级不擅自启用飞书。
- 验证：PostgreSQL／文件 SQLite 的建库、升级、回退、约束、索引、数据保留和 dev／已有 aggregate 路径一致性。

## T3 设置与任务配置接口

- 依赖：T2。
- 影响：`backend/core/userprefs/`、`backend/core/scheduler/`、`backend/core/routes.go`、`backend/core/openapi_manual.go` 及相关 OpenAPI 源文件、`backend/core/common/error_catalog.go`。
- 参考：现有 `/settings/overview`、`/schedules` 与集中权限注册方式。
- 工作：设置读取／更新、关闭影响查询与确认、任务规则读取／保存／恢复默认、配置版本校验；接口仅承担后端行为，不修改 UI。
- 验证：默认值、即时持久化、已有任务不继承新默认、无渠道可保存、非法组合拒绝、并发冲突与跨用户访问。

## T4 网关通知扩展契约

- 依赖：T2、T3 契约稳定。
- 影响：`backend/channel-gateway/channel_gateway/common/domain/`、`common/ports/providers.py`、`common/ports/messaging.py`、`common/application/providers.py`、`app.py`、`bootstrap.py`；Core 内部调用接入处。
- 参考：现有 ProviderRegistry、DeliveryProvider、OutboundMessage 和账号服务。
- 工作：增加任务通知用途、能力查询、账号／目标验证、幂等交接与结果查询契约；首期只开放飞书定时任务发送能力。
- 验证：通知无需入站消息；未实现渠道返回不支持；现有微信／飞书会话回复回归。测试中可用 Fake 渠道验证接口独立性，不在生产注册虚假适配器。

## T5 飞书稳定账号身份与接收对象

- 依赖：T4。
- 影响：`backend/channel-gateway/channel_gateway/feishu/accounts.py`、`connection.py`、`ports.py`、`sdk.py`，`common/infrastructure/postgres.py`、`sqlite.py` 及相关结构初始化／升级代码。
- 参考：现有断开调用与 GatewayStore 删除账号逻辑、注册失败清理逻辑。
- 工作：保留用户主动断开后的账号身份／引用；安全重连；提供账号状态、任务引用和可验证接收目标，区分缺权限与未连接。
- 验证：不改绑其他账号、同名账号不混淆、重连身份不匹配拒绝、清理孤立注册账号仍有效、目标过期及跨用户访问拒绝；双数据库验证。

## T6 任务事件与可靠交接

- 依赖：T2、T4。
- 影响：`backend/core/scheduler/scheduler.go`、`dependency_runtime.go`、`backend/core/taskcenter/taskcenter.go`、必要的 `backend/core/workflow/` 状态写入处、`backend/core/main.go`；网关通知入口与持久化代码。
- 参考：定时触发创建执行、finalizeTaskOutput、任务状态／失败更新及等待状态解析；`backend/core/workflow/hooks.go` 中用户停止后的可恢复等待，`backend/core/workflow/eventloop.go` 中 dynamic_pause／interrupted 状态转换。
- 工作：单次执行规则快照、输出就绪事件、被暂停／等待确认事件、各状态独立通知开关与内容、持久化幂等交接和总开关发送前检查；覆盖所有相关状态写入路径。不得把暂停仅映射为等待审批，也不得按操作名称排除实际可恢复暂停。
- 验证：状态与内容对应；重复回调、进程重启、Core→网关中断、关闭开关竞态不丢失应发送事件或无意重复投递；任务失败不依赖通知服务是否可用。

## T7 飞书内容与实际投递

- 依赖：T5、T6。
- 影响：`backend/channel-gateway/channel_gateway/feishu/presentation.py`、`delivery.py`、`sdk.py`、必要的 `common/domain/outbound.py`。
- 参考：已有飞书文本、卡片、图片／文件发送；原需求的结果摘要／完整内容及平台展示能力。
- 工作：按状态独立配置生成成功摘要／完整内容、失败安全原因、暂停原因与待处理事项；覆盖原需求涉及的正文与产物呈现，冻结平台限制与提示／转换策略；复用消息分片和幂等发送标识，不以截取开头替代结果摘要。
- 验证：正常文本、中文边界、空结果、长内容、平台拒绝／限流、发送成功但响应丢失；测试与真实飞书验收分别记录。

## T8 记录查询、重试与恢复

- 依赖：T6、T7。
- 影响：`backend/core/taskcenter/`、`backend/core/routes.go`、OpenAPI 与错误源文件；`backend/channel-gateway/channel_gateway/common/application/workers.py`、持久化及错误映射代码。
- 工作：按执行查询记录与目标状态、关联重试尝试、失败分级、历史快照保留、有限重试与恢复；人工重试再次校验所有权、目标和总开关。
- 验证：重试不改变任务执行次数；多个目标相互独立；同一失败记录并发重试不重复排队；已发送分片不重发；未知发送结果可观察。

## T9 主流程、双数据库和端到端验收

- 依赖：T2—T8 全部完成。
- 影响：仅 `backend/` 下测试、测试夹具、必要后端启动适配和本目录交付记录。
- 工作：从后端配置现有飞书账号和通知目标，创建定时计划，运行调度，保存结果，投递飞书，查询记录，模拟失败并单独重试。
- 验证：PostgreSQL 与文件 SQLite 分别执行，不以单一数据库代替另一部署模式；现有会话任务、账号连接和微信回复回归。
- 真实外发：仅在用户明确提供或授权测试账号、接收对象及测试消息后执行；此计划和阅读需求不视为向任意真实群发送消息的授权。
- 交付：报告后端范围完成度、验证证据与未验证项，提供目录外契约发布／错误文案／前端／部署接入说明。不得宣称已完成 Desktop 或其他渠道。

## 验证命令安排

阶段一执行新行为测试并保留预期失败，不修改生产功能。实际文件、命令、结果与覆盖缺口见本目录 test-review.md。

测试阶段按模块运行聚焦用例；实现阶段在聚焦用例通过后运行受影响回归，例如：

```sh
cd backend/core
go test ./scheduler ./taskcenter ./userprefs ./common/...
go test ./...
```

```sh
cd backend/channel-gateway
python3 -m pytest tests test_security.py -v --tb=short
python3 -m flake8 channel_gateway tests test_security.py
```

迁移、PostgreSQL 集成与文件 SQLite 运行命令在 T1 随已批准测试固定。测试 Fixture 不修改现有用户数据库、根配置或目录外文件。缺数据库／凭据／依赖时区分环境失败与代码失败，保留未验证状态。

## 阶段二启动记录

2026-09-10 用户明确批准本批测试，开始生产实现。阶段一结果保留在 test-review.md 和 test-results.json；已批准测试保持原样。

## 测试修正完成记录

2026-09-10 用户批准三处测试修正，已应用并通过文件 SQLite 29 项、真实 PostgreSQL 29 项及受影响回归 14 项验证。此次仅修改测试与验证文档，详情见 test-correction-results.json；下一项仍为既有 v0.3 SQLite 聚合迁移问题及后续功能实现。

## 2026-09-13 集成验收与下一批 Review

- 工作流真实停止／恢复、原子状态与通知、最终产物、Core 交接租约隔离、目标独立交接、账号重连保留及列表降级已接入。v0.3 聚合问题已处理，迁移模块 47 个顶层用例通过。
- 已批准网关及安全测试 174 通过；Core 两库各 73 通过；独立进程 E2E 22 通过。13 条后端公开权限注册、OpenAPI 请求／响应源和 backend 文案源已整理，部署交接已写入 backend。
- 新增 `test_notification_sdk_failures.py` 覆盖明确拒绝不阻塞后续产物及不进入未知结果窗口，两库共 4 项预期失败。只增加测试，分类修复尚未实施。
- `integration-review-proposal.patch` 提议补齐 scheduler／workflow store 两处旧建库夹具，以及错误文案测试的 backend 来源校验。补丁可应用，旧测试内容未改变。
- 获批后依次应用测试适配、修复 SDK 拒绝分类、运行新旧网关回归和受影响 Core 检查。完整 Core 的基线异步任务失败、可选集成环境跳过继续单独报告，不作为通知实现已通过的证据。
- 未执行真实飞书外发和目录外部署；未提交或推送。

## 本批确认与完成

用户在上述具体审阅方案后回复“继续”，已应用三处测试适配并修复 SDK 明确拒绝分类，保留先前丢失响应的重复风险。网关 178 项及 E2E 22 项通过；最后的恢复回归 16 项及额外双库恢复检查通过。完整 Core 2499 通过、8 跳过、1 项基线原有失败。本批不再等待 Review，下一验收边界为指定账号的真实送达和目录外接入；未执行真实外发或扩大文件范围。

## 2026-09-14 基线失败修正

用户要求修复剩余失败。已确认该用例直接保存本地时间，与生产写入 UTC 不一致，仅将旧测试一行改为 UTC，保持全部原断言。北京时间下 asyncjob 两库各 27 项通过，完整 Core 2500 通过、0 失败、8 跳过；生产代码未改。详情见 asyncjob-timezone-correction-20260914.md。
