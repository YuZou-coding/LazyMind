# 飞书任务通知：阶段一测试 Review

日期：2026-09-10。基线：`44c4e038e645cbc3b0336b895ac092517084e11e`。当前分支：`codex/feishu-task-notifications`。

**本批只新增测试、测试夹具及文档，没有实现生产通知功能，没有新增生产依赖，没有编写迁移 SQL，没有提交或推送。** 业务范围仍是已批准的全部飞书后端通知能力，后续只增加其他渠道。

需求映射和具体接口形状见 [test-contract.md](test-contract.md)。新增 HTTP 名称、错误编号及测试存储约定在本批 Review 中一起确认，不是已经发布的生产 API。

## 1. 文件与覆盖

| 文件（相对 backend） | 主要覆盖 |
| --- | --- |
| `core/notification_api_test.go` | 默认设置、规则复制/恢复、多目标和独立状态内容、无效配置、并发版本、所有权、匿名访问、开关确认、配置快照、暂停事件/去重、失败与事务、旧编辑接口和批量创建、OpenAPI |
| `core/scheduler/notification_lifecycle_test.go` | 等待/活跃/失败/取消状态收尾、输出写失败、完整正文和产物、真实调度 tick 与依赖恢复 |
| `core/migrate/notification_migration_test.go` | 通知 SQL 对、真实 PostgreSQL/文件 SQLite 升级/down/up、旧计划保留、无历史补发、aggregate/dev schema 与回退 |
| `core/notification_process_fixture_test.go` | 仅测试二进制入口，调用真实 Core 启动、SQL migration 和后台工作进程 |
| `channel-gateway/tests/notifications/conftest.py` | 独立数据库、真实网关组件、模拟飞书/注册服务和 Core HTTP 边界 |
| `channel-gateway/tests/notifications/test_notification_api.py` | 通知能力、幂等入队、配置验证、账号/目标/引用/影响、内部认证、OpenAPI、错误安全 |
| `channel-gateway/tests/notifications/test_notification_accounts.py` | 同身份重连、不同身份拒绝、孤立账号清理、过期/刷新/取消/网络与拒绝授权、群权限、集中权限 |
| `channel-gateway/tests/notifications/test_notification_content.py` | 卡片内容、状态、全文拆分顺序/完整性、JSON/Unicode 大小、摘要失败、图片/文件/超限/空文件、暂停待办 |
| `channel-gateway/tests/notifications/test_notification_delivery.py` | DB 抢占、租约过期、5 次上限、回执持久化、断开保留历史、逐片授权、部分成功和重试 |
| `channel-gateway/tests/notifications/test_notification_recovery.py` | 响应丢失和稳定 UUID、超窗 unknown、重复风险确认、Retry-After、Core 不可用、并发重试、失败产物后续发送 |
| `channel-gateway/tests/notifications/test_notification_protocol.py` | SDK 60 秒超时/稳定 UUID、30 秒续租/失租隔离、Core 集中权限提取 |
| `channel-gateway/tests/notifications/test_notification_reply_regression.py` | 原飞书卡片回复和微信 context_token 回复 |
| `channel-gateway/tests/notifications/test_notification_e2e.py` | 真实 Core→网关链路，摘要模型与多目标、长文分段、摘要失败/超时补发、Core 重启复用内容、失败状态、跨用户历史/重试、交接中断和响应丢失 |

生产逻辑没有放进 Fake。Fake 仅控制远程聊天执行、模型、飞书 SDK/注册和 HTTP 故障；规则、状态、事件、渲染、数据库、队列、重试均要求真实代码负责。业务时间测试通过受控 Event、Future 或持久化到期时间推进，不等待真实 30/60/120/3600 秒。

## 2. 执行结果

统计按 Go 顶层测试 / pytest 参数化用例计数，不把 Go 父测试与子测试重复相加。续接时发现临时环境被清理，已恢复独立 Python 环境和隔离 PostgreSQL，并重新执行当前完整用例集；下表采用最后一轮结果。

| 验证范围 | 数据库 | 通过 | 失败 | 环境阻塞/跳过 |
| --- | --- | ---: | ---: | --- |
| Core 通知 API + Scheduler 通知测试 | 文件 SQLite | 2 | 26 | 0 |
| Core 通知 API + Scheduler 通知测试 | PostgreSQL | 2 | 26 | 0 |
| 新通知 migration 测试（含两个数据库分支） | 两者 | 0 | 3 | 0；SQL 尚未实现，后续 SQL 断言尚未执行 |
| 当前 main 的 PostgreSQL 迁移路径基线 | PostgreSQL | 1 | 0 | 未配置 TARGET_MIGRATIONS_DIR，额外的指定旧分支升级对照未验证 |
| 当前 main 的 SQLite dev/aggregate、新建/升级基线 | 文件 SQLite | 2 | 0 | 0 |
| 网关通知、账号、内容、投递、协议、回复及现有安全回归，排除 E2E | 两者 | 31 | 143 | 0 |
| 后端 E2E（最终用例集） | 两者 | 2 | 20 | 0 |
| 既有 Core 任务中心/调度聚焦回归 | 文件 SQLite | 14 | 0 | 0 |
| 新增 Python flake8、Go gofmt | 不适用 | 通过 | 0 | 0 |

Python 测试无收集/夹具初始化错误；飞书 SDK/uvicorn 依赖有弃用警告，不影响本次执行。默认系统 Python 缺依赖的问题已通过 `/tmp/lazymind-notification-tests-venv` 独立测试环境解决，未改生产 requirements。

端到端的两个通过项是 SQLite/PostgreSQL 下真实 Core 的启动、迁移和用户模型测试数据初始化。**完整通知 E2E 尚未通过**：业务用例在当前缺失的通知规则 API 处失败，后面的摘要、发送、重启、历史及重试断言尚未获得运行证据。

## 3. 失败分类与已复现问题

1. **缺少通知业务 API/Schema**：Core `/user/notification-settings`、任务规则/历史/重试和网关通知 API 当前不存在；相关测试收到 404/405。新通知 migration 文件缺失。这些属于阶段一的预期失败，不能通过跳过或 Stub 返回成功消除。
2. **任务收尾判断错误**：已持久化 waiting、Workflow waiting/active 等执行可被标为 succeeded；既有 failed 状态也可能被后续收尾覆盖；输出写入失败时仍可能标记成功。取消状态保留、完整正文、产物等有现有通过证据，但不能因此宣布收尾逻辑整体通过。
3. **账号与历史会被删除**：原 DELETE 断开路径删除账号，随后相同身份连接得到新 ID；已有通知 outbox 历史会被级联删除。已通过真实数据库用例复现。
4. **SQLite 续租语法错误**：`GatewayStore.renew_outbound_lease` 经 SQLite SQL 翻译后报 `sqlite3.OperationalError: near ">": syntax error`。PostgreSQL 对应租约用例通过。这是现有实现兼容性问题，不是没有测试数据库。
5. **当前会话卡片尚不是通知卡片**：标题、执行时间、状态、摘要/全文和产物的通知语义未实现；新内容用例预期失败。
6. **权限和接口契约缺失**：飞书注册权限未包含群读取权限，新通知公开接口尚未进入集中权限与 OpenAPI；新协议测试失败。现有 SDK 的 60 秒超时、稳定 UUID、30 秒续租隔离，以及原飞书/微信会话回复已有通过证据。

已修复的测试问题：切换 main 后端到端 Core 目录定位、测试格式、重复运行夹具的会话身份、外部 HTTP 故障边界。上述问题修复后重新运行，未发现剩余的编译、导入或夹具启动阻塞。

## 4. 可复现命令

先准备独立 Python 环境，安装现有生产依赖及测试工具；这些安装不修改仓库依赖文件：

```sh
python3.12 -m venv /tmp/lazymind-notification-tests-venv
/tmp/lazymind-notification-tests-venv/bin/python -m pip install -r backend/channel-gateway/requirements.txt pytest flake8 PyYAML
```

本次恢复了隔离容器 `lazymind-notification-review-pg`（PostgreSQL 16），仅绑定 `127.0.0.1:32768`；测试自己创建临时 database/schema 并清理，不连接用户业务库。以下 DSN 仅用于该隔离测试服务，无生产凭据。

```sh
cd backend/core
go test . ./scheduler -run '^TestNotification' -count=1 -json
TEST_DB_DRIVER=postgres TEST_DB_DSN=postgresql://postgres@127.0.0.1:32768/postgres go test . ./scheduler -run '^TestNotification' -count=1 -json
MIGRATION_TEST_POSTGRES_DSN=postgresql://postgres@127.0.0.1:32768/postgres go test ./migrate -run '^Test(Notification|Repository.*MigrationPaths)' -count=1 -json
go test ./migrate -run '^TestRepositorySQLite(ReleaseAndDevPathsMatch|FreshAndUpgradePaths)$' -count=1 -json
go test ./taskcenter ./scheduler -run '^Test(UpdateTask|CreateTask|CreateSchedule|RunNow|Fire|Finalize|Dependency)' -count=1 -json
```

从仓库根目录执行网关测试与格式验证：

```sh
PYTHONPATH=backend/channel-gateway NOTIFICATION_TEST_POSTGRES_DSN=postgresql://postgres@127.0.0.1:32768/postgres /tmp/lazymind-notification-tests-venv/bin/python -m pytest backend/channel-gateway/tests/notifications backend/channel-gateway/test_security.py --tb=short -q
PYTHONPATH=backend/channel-gateway /tmp/lazymind-notification-tests-venv/bin/python -m flake8 backend/channel-gateway/tests/notifications --max-line-length=120
gofmt -l backend/core/notification_api_test.go backend/core/notification_process_fixture_test.go backend/core/migrate/notification_migration_test.go backend/core/scheduler/notification_lifecycle_test.go
```

最后一轮原始结果在 `/tmp/notification-core-sqlite-final.json`、`/tmp/notification-core-postgres-final.json`、`/tmp/notification-migrations-final.json`、`/tmp/notification-sqlite-migration-baseline.json`、`/tmp/notification-existing-core-regression.json`、`/tmp/notification-gateway-final.xml`。逐项结果已持久保存为同目录 [test-results.json](test-results.json)，不依赖临时目录长期保留。

## 5. Review 后的实施边界

- 用户尚未批准本批测试，生产实现保持未开始。依据根 AGENTS.md 第 5 节：“只有用户明确批准本批测试后，才进入阶段二”。
- 测试批准后按 T2—T9 实现；当前失败的首个断言修复后仍须执行后续所有断言，不能以接口存在或状态字段变绿替代内容、权限与双数据库验收。
- 长文的真实模型 token 预算、真实飞书渲染/权限/限频、Windows Desktop 环境、完整生产部署都没有通过本次本地模拟测试验证。摘要接口超时响应以模拟 408 覆盖，未等待真实模型超时。
- 通知 migration 未实现，因此新增通知 schema 的真实升级/回退与业务约束当前无通过证据。现有 main 的基础迁移通过不代表通知迁移通过。
- 真实飞书送达未执行；仍需用户明确授权测试账号、接收对象及消息。目录外前端、Desktop、部署、i18n 和契约发布未接入，下一阶段仅在 backend 内写交接说明。
