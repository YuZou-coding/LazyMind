# 真实工作流通知入口补充 Review

2026-09-11。在已有阶段二实现上补充验证，不改变已确认的通知范围。本批对应 R4/R5、T6、C3/C4；新测试尚未实现对应生产行为。

## 已确认的行为与实际差异

用户批准的方案要求可恢复暂停发送 paused，彻底取消不发送通知。现有工作流 `:stop` 把会话保存为 stopped，`:resume` 可以恢复为 active；任务中心目前把 stopped 投影成 canceled，且旧回归明确期待这个结果。

首批通知测试中的 user-stopped-resumable 使用的是模拟 waiting 状态，只证明任务处于 waiting 时不会误判成功，不能证明实际 stop/resume 入口已经接通。真实引擎 UpdateSessionStatus 也尚未同步任务通知状态。

拟修正后，任务中心对可恢复 stopped 返回 waiting，任务记录不设置终态完成时间；恢复返回 running。变化适用于关联工作流的任务中心状态投影，包含既有非定时工作流任务。彻底取消仍为 canceled，迟到回调不能复活取消任务。通知仍只面向配置启用的定时执行。

## 需批准的测试内容

1. 新增 [notification_workflow_lifecycle_test.go](../../../core/notification_workflow_lifecycle_test.go)：真实 HTTP 停止/恢复、同命令重放、重复停止、恢复后再暂停；引擎暂停/失败；通知写入失败回滚会话、任务和命令；彻底取消不产生暂停事件。
2. 将既有 taskcenter/workflow_task_test.go 的 stopped 期望值从 canceled 改为 waiting，其余状态断言保留。
3. 迁移模块扩大回归发现 mode_test.go 写死 66 个 v0.3 增量迁移。新增通知迁移后改为 67，并增加通知迁移时间戳存在断言，保留其他目录检查。

第 2、3 项的完整差异在 [workflow-review-proposal.patch](workflow-review-proposal.patch)，仅生成补丁，尚未修改两份既有测试。补丁可应用性已检查。新测试已写入并执行；本批不含工作流生产修正。

## 运行结果

所有 Go 命令从 backend/core 运行。PostgreSQL 使用隔离测试数据库，SQLite 使用文件库。

| 验证 | 结果 |
| --- | --- |
| 获批 E2E 夹具修正后，两库真实 Core/网关进程、模拟平台/模型 | 22 通过，6 项依赖弃用告警 |
| 通知迁移专项，双库 | 顶层 3 项通过，升级、回退、数据保留、路径一致性通过 |
| 整个 migrate 模块，双库 | 46 通过，1 项旧迁移计数预期需更新 |
| 新工作流入口测试，文件 SQLite | 3 项预期失败，1 项彻底取消通过 |
| 新工作流入口测试，PostgreSQL | 3 项预期失败，1 项彻底取消通过 |

预期失败均来自尚未实现的真实入口状态/事件同步与事务约束；无编译、夹具或环境异常。没有使用跳过、xfail 或测试专用生产分支。

```sh
go test . -run '^TestNotificationWorkflow' -count=1 -json
TEST_DB_DRIVER=postgres TEST_DB_DSN=<隔离测试DSN> go test . -run '^TestNotificationWorkflow' -count=1 -json
MIGRATION_TEST_POSTGRES_DSN=<隔离测试DSN> go test ./migrate -run '^TestNotification' -count=1 -json
MIGRATION_TEST_POSTGRES_DSN=<隔离测试DSN> go test ./migrate -count=1 -json
```

详细用例、结果与 SHA256 见 [phase2-workflow-review-results-20260911.json](phase2-workflow-review-results-20260911.json)。未真实飞书发送，未修改 backend 外文件，未提交、推送或修改已合并 dev migration。

## 门禁依据与后续

根目录 AGENTS.md 第 5 节要求：“修改已 Review 的测试前重新获得批准。”第 1 节要求影响产品行为、兼容性或验收的差异等待确认。本次是实际可恢复停止与既有 canceled 映射的行为差异，不是需要再次确认已批准的完整飞书功能范围。

批准后应用两处既有测试修正，接通工作流状态与事件事务，处理 SQLite 命令路径的事务边界，再运行新测试和受影响工作流回归，继续完成其他已批准后端实现。
