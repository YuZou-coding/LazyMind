# 飞书任务通知后端接入说明

日期：2026-09-14。后端主流程及 SDK 明确拒绝发送修复已完成自动化验证，三处旧测试适配已按用户确认应用，见 [验收报告](delivery-verification-20260913.md)。本文交接部署和调用要求，不代表已经部署或真实飞书送达验收完成。

## 1. 服务与环境

复用现有 Core、渠道网关、业务数据库、模型服务和出站工作进程。没有新增服务、中间件或生产依赖。部署时由既有 Secret 管理方式注入认证信息，禁止把真实 Token、数据库密码或平台凭据写入本目录。

| 服务 | 环境项 | 接入要求 |
| --- | --- | --- |
| Core（新增） | `LAZYMIND_CHANNEL_GATEWAY_URL` | Core 可访问的渠道网关服务根地址；不带 `/api/channel-gateway/v1` 或 `/internal`，由调用代码追加内部路径。缺失时不交接通知 |
| 网关（沿用） | `LAZYMIND_CHANNEL_GATEWAY_CORE_BASE_URL` | 网关可访问的 Core 服务根地址；不带 `/api/core`，通知内部路由直接位于 `/internal/...` |
| 两服务 | `LAZYMIND_AUTH_SERVICE_INTERNAL_TOKEN` | 配置相同的既有内部认证 Token；请求使用 `X-LazyMind-Internal-Token`，缺失或错误不能放行 |
| Core（沿用） | `LAZYMIND_BACKGROUND_JOBS_ENABLED` | 运行调度、通知交接与恢复需要开启后台工作进程；不要以仅 API 测试模式部署 |
| 网关（沿用） | `LAZYMIND_CHANNEL_GATEWAY_DATABASE_DSN` | 使用本部署已有状态数据库；PostgreSQL 和文件 SQLite 分别测试。升级保留账号、通知、出站回执及限频记录 |
| 网关（沿用） | `LAZYMIND_CHANNEL_GATEWAY_CREDENTIAL_KEY_PATH` | 保留既有加密主密钥和持久卷；仅保留数据库而丢失密钥不能恢复平台凭据 |
| Core／网关（沿用） | 现有模型和产物存储配置 | 摘要使用用户模型配置及现有纯模型接口；文件依赖原有产物读取路径和服务连通性 |

Compose、Desktop 和 Local 必须分别把服务根地址配置为该运行环境实际可达的地址，不能将某一种环境的服务名直接用于另一种环境。此任务没有修改根 Compose、Kong 或桌面启动／打包配置。

## 2. 公共接口、权限与错误

Core 公共路径前缀为 `/api/core`，网关为 `/api/channel-gateway/v1`。继续使用 Kong／auth-service 集中身份和权限体系，读取使用 `qa.read`，写入使用 `qa.write`；这些权限组已存在。Core 和网关同时校验资源所有权。内部路由只能由服务认证调用，不应作为无需用户身份的公开代理入口。

- Core 8 条公开路由：全局设置读取／更新、关闭影响查询、计划规则读取／保存／恢复默认、执行历史、失败重试。
- 网关 5 条公开路由：通知能力、账号接收对象、账号任务引用、断开影响、按原身份重连。
- 完整方法／路径／权限清单：[notification-api-permissions.json](notification-api-permissions.json)。后端源中已声明，目录外部署的权限导出及代理接入需按既有发布流程同步。
- Core OpenAPI 源：[openapi_notifications.go](../../../core/openapi_notifications.go)；网关源是 FastAPI 请求／响应模型，通知相关导出：[gateway-notification-openapi.json](gateway-notification-openapi.json)。网关导出包含 5 个公共和 4 个内部路径。
- Core 新错误源：[notification_errors.go](../../../core/common/notification_errors.go)、[notification_error_translations.json](../../../core/common/notification_error_translations.json)。六个通知错误码为 `2002701/2002702/2002705/2002706/2002707/2002710`；后端嵌入中英文并用于实际错误响应。

Core 成功响应直接返回相应 JSON 对象；错误为 `code/message/request_id`。网关错误使用其既有 `error` 对象（稳定 `code`、安全 `message`、`retryable`、`request_id`）。不能把平台异常原文用于界面提示。SDK 明确拒绝的分片错误 `FEISHU_SEND_REJECTED` 已实施；先前请求曾丢失响应时，后次拒绝仍保留 unknown 和重复风险确认。

目录外 `api/backend/`、`i18n/errors/` 及部署权限发布产物未修改；发布方须从这些后端源同步，不能仅替换二进制就认为所有外部接口已经接入。根目录文案校验测试的 backend 来源适配已按本批批准补丁应用。

## 3. 飞书账号与调用方行为

注册除既有机器人消息和资源权限外，新增申请 `im:chat:read`、`im:chat.members:read`。旧账号缺权限时需对原应用补授权，并用原身份重连；不能通过创建另一账号来自动替换原任务绑定。实际权限审批及受限制群的可见／可发送范围仍需真实测试账号验收。

个人接收对象仅允许已验证身份，群接收对象来自机器人可访问且具备发送权限的群。调用方保存稳定账号 ID 和返回的接收对象 ID，不能把任意用户输入当作已验证对象。账号主动断开保留身份、绑定和历史；注册失败且从未连接的孤立记录按原清理流程处理。

调用方应使用以下约定：

1. 读取能力与账号状态，显式开启飞书并选择目标；已连接机器人不会自动开启通知。
2. 新建计划可以传 `notification_rule`，省略则复制当前默认。保存已有规则须提交读取的版本；冲突后重新读取，不能覆盖。普通变更从下一执行的快照生效。
3. 关闭总开关前读取影响并提交当前确认信息。关闭保留配置和历史，但使未发送部分失效；重新开启不补发旧队列。已经进入平台请求的分片可能完成。
4. 历史按事件、目标和分片展示，区分 queued、sent、partial、failed、unknown 等状态；交接返回接受不表示平台送达。
5. 手工补发只处理失败部分；摘要生成失败先显示“摘要暂不可用”，补发不重跑任务。unknown 的手工重试必须明确确认可能重复。

## 4. 投递与运行边界

单卡序列化上限 28 KiB、图片 10 MiB、文件 30 MiB；正文按段落分卡，无法放入卡片的内容转文件。无法发送的产物应有具名安全说明并继续其他内容；明确 SDK 拒绝已修复为安全失败并继续其余内容，失败文件和已发送说明均可在历史中追踪。

发送请求 60 秒超时，出站租约 120 秒、30 秒续租，最多自动尝试 5 次。网关按账号与接收目标共享数据库限频预算，使用保守的每秒 4 次间隔并尊重 Retry-After；实际平台限制始终优先。分片使用稳定 UUID，响应丢失时沿用；超过 1 小时去重窗口仍无法确认则记录 unknown，停止自动重发。

Core 保留事件、内容及待交接记录，网关保留实际结果。运维排查应通过通知身份和 request_id 关联，勿记录真实凭据或完整 Provider 错误。数据库／服务不可达时暂缓，并保留可恢复状态；Core 正常退出以独立有界清理上下文释放租约，异常退出依靠租约到期恢复。

## 5. 升级与验收状态

Core 新迁移为 `20260910080428_task_notifications` 的 up/down，同步已有 v0.3 aggregate；没有改动已合并 dev migration。迁移模块 47 个顶层测试通过，通知相关用例内部覆盖真实 PostgreSQL 和文件 SQLite 的建库、升级、回退、旧数据及路径一致性。网关使用既有初始化机制追加通知表／列和索引，无需额外迁移服务。升级不会启用旧任务飞书通知或追溯补发历史执行。

| 验收项 | 状态 |
| --- | --- |
| 模拟外部边界的自动化 | 网关 178 通过；前批 Core 两库各 73 通过、本批旧测试适配两库各 4 通过；迁移 47 通过；E2E 22 通过；最终 SDK／恢复 16 通过 |
| 完整 Core 回归 | 当前 2500 通过／0 失败／8 跳过；原基线失败已修正测试夹具时区，见 [修正报告](asyncjob-timezone-correction-20260914.md) |
| 真实飞书送达 | 未执行，尚未获得明确的测试账号／接收对象／消息外发授权 |
| 目录外部署、代理、契约／文案发布、前端 | 未修改、未验证；本文提供接入要求 |
| 微信／企业微信任务通知、桌面提醒 | 不在首期发送范围，不能据已有会话回复能力宣称已接入 |

详细命令和证据：[通知验证](sdk-correction-results-20260913.json)、[最新 Core 验证](asyncjob-timezone-results-20260914.json)。后续真实测试须单独获得指定账号、接收对象和消息的授权，本说明不替代授权。
