# 飞书任务通知测试契约

基线：`44c4e038e645cbc3b0336b895ac092517084e11e`，分支 `codex/feishu-task-notifications`。业务计划与此前测试批次已获批并实施。2026-09-13 新增 SDK 拒绝场景测试和三处旧测试适配已获用户确认并实施；结果见 [验收报告](delivery-verification-20260913.md)，其余契约沿用获批范围。

## 1. 业务规则映射

| 需求 | 主要可执行测试 | 可观察结果 |
| --- | --- | --- |
| R1 默认值、立即保存、独立用户 | Core `DefaultsAreSafeAndPersisted`、`GlobalDefaultsMustKeepOneEvent` | 默认总开关开启，成功/失败摘要开启，暂停摘要关闭，飞书关闭；重建路由可读取保存版本 |
| R1 总开关影响与确认 | Core `DisableImpactRequiresFreshConfirmation`、`GlobalDisableConfirmAndReenableRetainConfiguration` | 新执行使旧影响确认失效；配置保留 |
| R2 默认复制、恢复、老接口兼容 | Core `DefaultsOnlyApplyToNewSchedulesAndReset`、`LegacyEditDoesNotClearRule`、`CreateAndBatchPersistExplicitRules` | 新建复制默认，旧任务不变，恢复为当前默认；省略通知字段不清空规则 |
| R2 并发、验证、状态内容、多目标 | Core `RuleRetainsDisabledSelectionsAndMultipleTargets`、`RejectsInvalidRules`、`ConcurrentRuleWritersHaveOneWinner`、`GatewayValidationFailsClosed` | 成功/失败/暂停各自保留模式；两个写者仅一个成功；校验失败不保存 |
| R3 身份、权限、接收对象 | Gateway `test_notification_api.py`、`test_notification_accounts.py` | 同名不同账号独立、跨用户隐藏、未知个人不可用、群失效/缺权限明确失败 |
| R3 断开和重连 | Gateway `reconnect_same_identity_preserves_id`、`reconnect_cannot_replace_identity`、`disconnect_*` | 断开保留身份/历史，重连保留 ID，错误身份不能替换，孤立注册记录仍可清理 |
| R3 连接过程 | Gateway `connection_expiry_refresh_and_cancel`、`connection_failure_is_safe_and_keeps_existing_accounts` | 过期可刷新，二维码版本增加，取消终止，网络/拒绝授权错误不泄露原文 |
| R4 状态与原子性 | Core `PauseEpisodesAndExecutionSnapshot`、`NonEventsDoNotNotify`、`FailureIsDeduplicatedAndSafe`、`TransactionRollbackDoesNotPublishEvent`、`EventInsertFailureRollsBackTaskStatus` | 暂停重复上报去重，恢复再暂停是新事件；状态与事件共同提交/回滚 |
| R4 收尾与实际调度 | Scheduler `notification_lifecycle_test.go` | 等待/活跃执行不误判成功；输出写失败不成功；标准调度及依赖恢复产生成功事件 |
| R4 摘要与模型 | E2E `summary_is_generated_once_for_two_targets`、`summary_failure_and_retry_never_reruns_task`、`long_summary_covers_tail_with_bounded_model_inputs`、`delivery_retry_reuses_summary_after_core_restart` | 用户所选纯模型无工具调用；跨目标生成一次；缺摘要部分成功、补发不重跑；已有摘要跨进程复用 |
| R4 全文、卡片、产物 | Gateway `test_notification_content.py`、`permanent_bad_artifact_does_not_block_later_file` | 标题/时间/事件、段落顺序和完整性、Unicode/JSON 转义预算、图片文件、空文件/超限说明及部分成功 |
| R4、R5 实际 SDK 明确拒绝（2026-09-13 已验证） | Gateway `test_notification_sdk_failures.py` | 真实 SDK 结果转换；永久失败文件不阻塞后续文件及具名提示；明确拒绝不进入未知请求窗口，历史不含 Provider 原文。双库 4 项通过，安全码 `FEISHU_SEND_REJECTED` 已实施；若此前请求响应丢失，后次拒绝仍保留 unknown 和重复确认 |
| R5 持久交接与去重 | Gateway `acceptance_is_durable_and_not_delivery_success`、`duplicate_handoff_has_one_business_record`、`different_transport_id_cannot_duplicate_business_event` | 交接没有伪造入站消息；入队状态不报 sent；并发重复仅一条投递 |
| R5 工作进程恢复 | Gateway `test_notification_delivery.py`、`test_notification_recovery.py` | 真实 DB 租约抢占/旧写者隔离、5 次上限、分片恢复、响应丢失复用 UUID、超窗 unknown、Retry-After、并发人工重试 |
| R5 开关与服务故障 | Gateway `disable_before_send_skips_queue_and_never_replays`、`permission_is_checked_before_each_part`、`core_unavailable_defers_without_sending` | 每片重新授权；无法确认就暂缓；关闭后的旧通知不因重开复活 |
| R6 扩展及会话兼容 | Gateway `capabilities_only_feishu_is_sendable`、`unimplemented_provider_rejected`、`test_notification_reply_regression.py` | 只有飞书支持任务通知，原飞书/微信回复继续使用原投递路径 |
| 契约、权限、安全 | Core `RoutesHaveOpenAPIContracts`、`InternalAuthorizationRejectsMissingCredentials`；Gateway API/protocol/account 测试 | OpenAPI 存在；集中权限提取；缺失内部认证安全失败；稳定错误与 request_id |
| 双数据库及迁移 | Core `notification_migration_test.go`、全部 Core DB 测试双跑、Gateway `sqlite/postgres` 参数化、E2E 双跑 | 文件 SQLite + 真 PostgreSQL；迁移对、升级/down/up、旧数据、全新建库、dev/aggregate schema 一致性 |

表中 Core/Scheduler 名称省略 `TestNotification` 前缀；Gateway 名称省略 `test_notification_`。文件和实际执行结果见 [测试报告](test-review.md)。用例失败时，失败断言之后的步骤未获得执行证据，不能算功能已验证。

## 2. Core HTTP 契约

所有公开接口继续由 Kong/auth-service 集中鉴权；Core 校验当前用户对资源的所有权。通知接口成功响应直接返回对应 JSON 对象，业务字段在根对象，与已批准测试及既有计划接口一致。早期文档所写 `data` 封装为说明偏差，本轮仅纠正文档，不改变接口或测试。`version` 为已读版本，保存成功严格递增；冲突返回 409。时间为带时区的 RFC3339。

| 接口 | 请求/响应关键字段 |
| --- | --- |
| `GET/PUT /user/notification-settings` | `enabled, version, defaults`；关闭时附 `impact_revision` |
| `GET /user/notification-settings/disable-impact` | 当前用户的受影响执行和 `impact_revision` |
| `GET/PUT /schedules/{schedule_id}/notification-rule` | `{version, rule}` |
| `POST /schedules/{schedule_id}/notification-rule:reset` | `{version}`；返回当前默认规则的独立副本 |
| 原创建计划、批量创建 | 可选 `notification_rule`；无字段时复制当前默认 |
| 原编辑计划 | 无通知字段时保留原规则；通知专用写接口使用版本控制 |
| `GET /task-center/tasks/{task_id}/notifications` | `items` 为事件记录；含 `id,event,status,rule_version,revision,targets`；目标包含其分片、错误、时间、尝试历史 |
| `POST /task-center/notifications/{notification_id}:retry` | `expected_revision`；unknown 附 `confirm_duplicate_risk:true`；接受返回 202，仅重试失败部分 |
| `POST /internal/task-notifications/{notification_id}:authorize` | 认证后根据执行/投递身份返回 `allowed,generation`，每片调用；不相信客户端自报所有者 |

规则结构示例：

```json
{
  "events": {
    "succeeded": {"enabled": true, "content_mode": "summary"},
    "failed": {"enabled": true, "content_mode": "full"},
    "paused": {"enabled": false, "content_mode": "summary"}
  },
  "channels": [{
    "provider": "feishu", "enabled": true,
    "targets": [
      {"account_id": "account-a", "recipient_id": "group-a"},
      {"account_id": "account-b", "recipient_id": "group-b"}
    ]
  }]
}
```

同一事件的目标状态汇总为事件状态；网关每个接收目标有独立投递身份，Core 事件 ID 与网关投递 ID 不能混用。Core history 的 `targets` 既保留业务绑定，也展示该目标的投递结果。

## 3. 网关 HTTP 与交接契约

公开接口前缀 `/api/channel-gateway/v1`，公开错误为 `{error:{code,message,retryable,request_id}}`。

| 接口 | 行为 |
| --- | --- |
| `GET /notification-capabilities` | provider、task_notifications、proactive_send、formats、限制和目标类型 |
| `GET /channel-accounts` | 原字段加头像、连接状态/时间、引用数量；不返回平台凭据 |
| `GET /channel-accounts/{account_id}/notification-recipients` | 所有权校验、分页；`items,next_page_token`，缺权限提示补授权 |
| `GET /channel-accounts/{account_id}/notification-references` | 引用计划 `items` |
| `GET /channel-accounts/{account_id}/disconnect-impact` | 当前账号断开影响 |
| 原 `DELETE /channel-accounts/{account_id}` | 204，保留身份、绑定和历史 |
| `POST /channel-accounts/{account_id}:reconnect` | 返回连接会话（200 或异步接受 202），授权结束必须匹配原身份 |
| `POST /internal/notification-targets:validate` | `{user_id,provider,targets}` → `{valid,provider,targets}` |
| `POST /internal/task-notifications` | 幂等接受单目标投递，202；不能直接返回 sent |
| `GET /internal/task-notifications/{notification_id}` | `status,revision,parts,attempts` 和目标快照 |
| `POST /internal/task-notifications/{notification_id}:retry` | `expected_revision`；并发只有一项接受；未知结果要求重复风险确认 |

单目标交接字段：`schema_version:1, notification_id, user_id, task_id, event, event_instance_id, rule_version, settings_generation, provider, account_id, recipient_id, content_mode, content`。`content` 包含 `title,executed_at,body,summary,summary_status,reason,pending_actions,artifacts`。产物带 `artifact_id,kind,name,mime_type,size_bytes,revision`；现有静态文件通过受 Core 保护的 `source` 获取，不接收任意外部 URL/本机路径。

同一次业务身份重复交接不能产生新工作；内容不同返回冲突。同一业务身份但更换传输 ID，可返回已有记录或冲突，均不可新增投递。单片状态及最终 `sent/partial/failed/skipped/unknown` 必须对应实际投递结果；人工重试记录 `retry_of`，已成功的部分不重发。

## 4. 错误码 Review 清单

以下 Core 编号已登记于后端错误源；中英文使用 backend 内嵌源，目录外 i18n 发布留在对应接入范围。沿用 Core 输入错误 400，网关结构验证 422；这属于现有框架的差异。

| Core 编号 | 语义 | HTTP |
| --- | --- | --- |
| 2002701 | NOTIFICATION_CONFIG_INVALID | 400 |
| 2002702 | NOTIFICATION_CONFIG_CONFLICT | 409 |
| 2002705 | NOTIFICATION_PROVIDER_UNSUPPORTED | 400 |
| 2002706 | NOTIFICATION_IMPACT_CHANGED | 409 |
| 2002707 | NOTIFICATION_DUPLICATE_RISK | 409 |
| 2002710 | NOTIFICATION_VALIDATION_UNAVAILABLE | 503 |

不可访问的计划/执行/记录使用已有 404 资源错误；缺失内部认证返回 401。网关沿用 `UNAUTHORIZED/ACCOUNT_NOT_FOUND/INVALID_REQUEST`，新增 `NOTIFICATION_CONFLICT/NOTIFICATION_PROVIDER_UNSUPPORTED/ACCOUNT_UNAVAILABLE/ACCOUNT_IDENTITY_MISMATCH/NOTIFICATION_RECIPIENT_UNVERIFIED/FEISHU_SCOPE_REQUIRED/FEISHU_CHAT_UNAVAILABLE/FEISHU_PERMISSION_DENIED/NOTIFICATION_DUPLICATE_RISK` 等稳定语义。异步分片明确拒绝使用 `FEISHU_SEND_REJECTED`：首次确定拒绝记录 failed，停止自动重发，允许人工只补失败部分；先前已有不确定请求则保留 unknown 和重复风险确认。不得直接序列化数据库异常、平台响应和内部路径。

## 5. 持久化与时间测试约定

- Core 使用 `notification_settings`、`schedule_notification_rules`、`task_notification_events`、`task_notification_deliveries`；任务规则中使用 `schedule_id,version,rule_json`。执行快照、事件内容和待交接状态必须持久化。
- 新 dev migration 后缀 `_task_notifications`，UTC 秒精度时间戳，提供 up/down；同步已有 v0_3 aggregate，不修改已合并 dev SQL。迁移测试使用真实 SQL，不用 AutoMigrate 代替。
- Core API/状态测试通过已有 ORM test helper 建表；文件 SQLite 使用既有真实连接配置，PostgreSQL 为每项测试建独立 schema。它们不替代独立 SQL migration 测试。
- 网关沿用 `channel_outbox`、实际 store 和 worker；每片 `provider_state` 保存 `first_request_at` 和稳定幂等标识。已确认回执必须持久化。
- 最多 5 次自动尝试；发送请求 60 秒超时；租约 120 秒、30 秒续租；卡片序列化上限 28 × 1024 字节，图片/文件为 10/30 × 1024² 字节。1 小时是平台去重窗口，不是无限幂等保证。
- 租约/退避用数据库到期时间推进；worker 用单轮调度夹具，SDK 超时用 Future 边界，续租用可控 Event，不等待真实 30/60/120/3600 秒。
- 端到端只对启动、异步结果进行短周期有界等待；直接调用真实调度 tick 的测试覆盖正常定时与依赖路径，不等待 30 秒调度周期。
- 端到端真实 Core 子进程使用 SQL migration、实际 scheduler/API/网关/队列；只有聊天执行上游、摘要模型、飞书 SDK/注册服务是 Fake。所有模型/账号数据均为明确标记的测试值。

## 6. 交付边界

1. 本批测试及修正已获确认并实施；原始红色结果继续作为历史证据，当前通过／失败／跳过分别报告。
2. 自动化全链路使用模拟外部服务；未获真实账号、接收对象及消息授权，不执行真实飞书送达。
3. 已在 [backend-handoff.md](backend-handoff.md) 交接 `LAZYMIND_CHANNEL_GATEWAY_URL`、`LAZYMIND_CHANNEL_GATEWAY_CORE_BASE_URL` 和 `LAZYMIND_AUTH_SERVICE_INTERNAL_TOKEN` 的部署交接说明；内部请求使用 `X-LazyMind-Internal-Token`，配置缺失安全失败。
4. 前端、Desktop、目录外部署文件/发布契约/i18n 未接入；验收时分别报告，不能用自动化送达替代真实平台验收。
