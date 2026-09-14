# 通知后端集成：Review 历史记录（2026-09-13）

当前更新：用户在本方案后回复“继续”，本批测试适配与 SDK 拒绝修复已获确认并完成。最新结果见 [修复验收报告](delivery-verification-20260913.md)。下文保留审批前的缺口、预期失败和补丁说明，不再表示当前待审状态。

已获批的工作流停止／恢复测试、旧状态断言和迁移计数修正已实施。当前等待 Review 的是本文件列出的 **2 个新增测试（双库共 4 项）及 3 处既有测试适配**。尚未实现新测试对应的 SDK 拒绝分类修复，既有测试适配补丁也尚未应用；不能据此宣称通知功能已完整交付。

工作目录为 `/Users/zouyu/Downloads/LazyMind-notifications`，分支 `codex/feishu-task-notifications`，基线 `44c4e038`。从原工作目录保留的 stash 恢复了通知改动，stash 未删除；原目录的其他分支及无关改动保持原样。仓库改动全部限于 `backend/`，本轮未提交、推送或真实外发。

## 1. 本次需要确认的内容

### A. 新增 SDK 拒绝场景测试及后续修复

测试文件：[test_notification_sdk_failures.py](../../../channel-gateway/tests/notifications/test_notification_sdk_failures.py)。测试运行真实网关服务、出站工作进程和 SDK 结果转换，仅模拟平台传输返回值 `success=false`、`retryable=false`；没有真实发送。

| 需求 | 新测试 | 预期行为 | 当前结果（SQLite／PostgreSQL 相同） |
| --- | --- | --- | --- |
| R4、R5：永久失败产物不阻塞后续内容 | `test_notification_explicit_sdk_rejection_does_not_block_later_artifact` | 拒绝的文件记 failed，继续发送后一文件及包含文件名和 LazyMind 查看提示的说明，整体 partial | queued，未继续处理后续内容 |
| R5：区分明确拒绝与结果未知 | `test_notification_explicit_sdk_rejection_has_no_ambiguous_send_window` | 明确拒绝记 failed，清除该次不确定请求窗口，返回安全错误；只能人工重试 | queued，被当作不确定结果继续退避 |

4 项均为预期功能失败，0 项环境跳过。失败发生在首个状态断言，因此其后关于文件名提示、安全错误、请求窗口的断言暂未执行通过。没有修改或跳过旧断言，也没有为这些新增用例实现生产分支。

原永久失败测试由 Fake 发送器直接抛出已分类的 `GatewayError`，没有经过实际 SDK 的错误转换。实际 SDK 对明确拒绝抛出普通 `FeishuRuntimeError`，工作进程的通用异常路径将它当成响应丢失，因此现有测试通过仍遗漏了这一边界。

获批后修复范围：保留 SDK 的明确拒绝分类，在通知发送边界映射为稳定安全错误（拟用 `FEISHU_SEND_REJECTED`）；记录失败分片，文件失败追加可恢复的具名说明并继续后续内容。仅在确认未成功发送时清除不确定窗口；超时、连接中断和响应丢失继续使用原稳定去重标识与 unknown 处理。保留原飞书／微信会话回复行为，补齐此错误的后端契约说明，并运行受影响回归。

### B. 三处既有测试适配

完整差异：[integration-review-proposal.patch](integration-review-proposal.patch)。已验证补丁可应用，尚未应用。

| 文件 | 原因 | 拟调整 |
| --- | --- | --- |
| `backend/core/scheduler/scheduler_test.go` | 创建计划已保存独立通知规则，旧夹具只建原有 3 张表 | 补建 NotificationSettings、ScheduleNotificationRule；保持创建和取消断言 |
| `backend/core/workflow/store/host_session_test.go` | 工作流状态与任务中心投影已在同一事务更新，旧夹具未建任务表 | 补建 TaskCenterTask；保持命令幂等、版本和中断断言 |
| `backend/core/common/error_catalog_test.go` | 旧测试只读目录外 i18n 文件，本任务只允许修改 backend；新增翻译已作为 backend 源嵌入真实响应 | 合并读取 backend 翻译源；继续检查中英文完整性，新增重复文案冲突及实际响应翻译一致性断言 |

第三项不会删除或跳过翻译校验，也不会将真实运行未使用的数据作为测试替代品。目录外文案发布仍是交接事项。

## 2. 已完成实现及自动化证据

已完成设置／计划规则、执行快照、真实暂停／恢复入口、结果产物、摘要与持久交接、目标独立发送、稳定账号重连、历史与重试、限频、后端权限和 OpenAPI 源。最后一次端到端运行还验证了 Core 正常停止后释放交接租约，避免重启等待旧租约到期。SDK 明确拒绝的缺口仍如上保留。

详细计数、命令、测试文件指纹和历史失败分类见 [integration-results-20260913.json](integration-results-20260913.json)。各组包含重叠覆盖，不应简单相加为独立测试数量。

| 验证组 | 结果 | 范围 |
| --- | --- | --- |
| 已批准网关测试及安全回归 | 174 通过，4 项依赖弃用告警 | 文件 SQLite＋真实 PostgreSQL，含飞书／微信回复；不包含新增待审 SDK 测试及单列的 E2E |
| Core 通知／OpenAPI／受影响状态回归 | 文件 SQLite 73 通过；PostgreSQL 73 通过 | 无跳过，含真实工作流停止、恢复、失败、原子回滚 |
| 完整迁移模块 | 47 个顶层用例通过 | 通知增量和聚合的两库 up/down/up、旧数据、路径一致性 |
| 独立 Core＋网关进程 E2E | 22 通过，6 项依赖弃用告警 | 两库的配置、执行、摘要、投递、历史、补发、重启和响应丢失；平台及模型模拟 |
| 新增待审 SDK 测试 | 4 项预期失败，4 项依赖弃用告警 | 上述 A 的缺口，无真实平台调用 |
| 静态检查 | 通过 | Python flake8、Go 格式、差异空白、补丁可应用；13 条公开权限注册、网关 9 个通知相关路径响应 schema |

扩大 Core 回归的原始结果为 **2495 通过、8 跳过、5 失败**，没有把它写成全量通过。其中：

- 3 项是上述待审旧夹具／翻译来源适配。
- 1 项新错误构造未登记，已改为既有安全错误；后续两库 73 项回归包含该检查并通过。
- `asyncjob.TestRunnerRecoversLeaseThatExpiresAfterStartup` 为范围外失败；在未包含通知改动的 `44c4e038` 临时基线副本上也稳定复现。未修改该模块，也未删除测试。

8 项跳过分别为通知子进程夹具（已由 E2E 实际调用）、可选浏览器夹具、真实 MCP 服务、真实模型、currentmemory PostgreSQL 专用集成、Redis 取消集成、episode PostgreSQL 专用集成和 AnkiConnect 集成。后四项缺少各自的可选环境变量；不将通知双库测试代替这些独立集成。

验证曾遇到本机 libpq GSS 协商带来的连接延迟，Python 测试改用测试进程环境变量 `PGGSSENCMODE=disable` 后恢复；没有放宽测试时间阈值或修改生产数据库配置。不能在共用 DSN 中添加该参数，Go pgx 会将它作为不支持的服务端参数。先前一次重启失败在修复租约释放后，完整 22 项 E2E 重新通过。

## 3. 交接与下一步

[backend-handoff.md](backend-handoff.md) 已列出内部 URL／认证、数据库和凭据文件、补授权、13 条权限注册、OpenAPI 和文案发布要求。真实飞书可见送达、目录外部署配置／契约发布尚未执行；本批不新增外发授权请求。

`test-contract.md` 早期写成成功字段位于 `data`，与已批准测试及现有计划 API 的直接 JSON 响应不一致。本轮仅纠正文档为根对象字段，接口实现和已 Review 测试未因此变化。

批准本批后，先应用三处测试适配，再修复 SDK 拒绝分类并验证 4 项复现、174 项网关回归和受影响 Core 测试；涉及主流程变动时重跑端到端。异步任务基线失败及可选环境缺口继续如实报告。

暂停依据：[用户提供的 AGENTS.md 第 5 节](/Users/zouyu/Downloads/LazyMind-main/AGENTS.md:80) 要求“高风险缺陷修复必须先增加稳定复现测试并等待 Review”；同节规定“修改已 Review 的测试前重新获得批准”。这次确认只针对新增 SDK 测试及三处既有测试适配，不重复请求已批准范围的授权。
