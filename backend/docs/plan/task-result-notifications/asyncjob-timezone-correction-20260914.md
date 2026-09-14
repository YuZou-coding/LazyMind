# Core 基线测试失败：时区夹具修正

2026-09-14，用户要求继续修复剩余的 Core 测试失败。本次定位为测试数据使用了本地时区，与生产代码统一写入 UTC 的约定不一致。只修正 `backend/core/asyncjob/runner_test.go` 一行，不改变生产 SQL、租约恢复、接口、迁移或运行配置。该修改范围局部、可直接回退，使用现有失败测试验证，不新增依赖。

## 根因及证据

`TestRunnerRecoversLeaseThatExpiresAfterStartup` 用 `time.Now().Add(200 * time.Millisecond)` 直接写入数据库，模拟上一进程留下的租约。本机北京时间使该值保存为带 `+08:00` 的本地时间。生产领取和续租路径都使用 UTC；恢复路径也把比较时间转为 UTC。

文件 SQLite 对这类日期字段的比较没有自动换算时区。同一个到期瞬间，`2026-09-14 08:00:00+08:00` 与 `2026-09-14 00:00:00+00:00` 的存储文本不同。比较 UTC 的 `00:00:01` 时，前一条仍被视为未到期，后一条正常恢复为 pending。诊断使用真实文件 SQLite、现有 ORM 和实际 `RecoverStaleJobs`，没有依赖等待或替换恢复逻辑。

未改代码时，该测试在北京时间的文件 SQLite 上失败，在 PostgreSQL 上通过；仅将测试进程时区设为 UTC 后，原测试连续 3 次通过。此前报告确认的是“基线也存在测试失败”，现在已进一步定位为夹具时区问题，不能据原失败认定生产恢复功能缺失。

## 修改

唯一测试代码差异：

```diff
- until := time.Now().Add(200 * time.Millisecond)
+ until := time.Now().UTC().Add(200 * time.Millisecond)
```

租约仍为 200 毫秒，工作进程配置、5 秒等待上限和全部断言保持原样。修正后的验证显式使用 `TZ=Asia/Shanghai`，不会通过把运行环境永久改成 UTC 来掩盖问题。既有通知实现和已 Review 的通知测试均保持原样。

## 验证

- 原问题复现：文件 SQLite 1 项失败；真实 PostgreSQL 1 项通过。
- 时区诊断：未改测试在 UTC 环境连续 3 次通过；真实 SQLite 同瞬间的两种存储表示得到不同到期判断。
- 修正后的完整 asyncjob 模块：北京时间＋文件 SQLite 27 项通过；北京时间＋PostgreSQL 27 项通过。
- 完整 Core 回归（北京时间）：2500 项通过、0 项失败、8 项可选集成／夹具跳过。详细命令与结果见 [asyncjob-timezone-results-20260914.json](asyncjob-timezone-results-20260914.json)。
- Go 格式、差异空白、仅修改 backend 的检查通过；asyncjob 生产文件与基线相同，通知生产文件和已批准测试指纹与前次报告相同。

本轮没有生产行为变化，因此没有重复执行网关和飞书端到端测试；此前 178 项网关、22 项 E2E 的结果保留在 [上一阶段报告](delivery-verification-20260913.md)。真实飞书送达、目录外部署和发布验收仍未执行；没有提交或推送。
