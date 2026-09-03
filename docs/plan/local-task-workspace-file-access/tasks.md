# Local/Desktop 任务工作区文件访问（第二迭代）任务计划草案

> 本计划适用两阶段人工 Review 门禁。三份文档获批后先完成任务 1～6 的测试并停止；只有用户明确批准该批测试，才进入任务 7～12 的生产实现。

## 1. 固化第二迭代契约与测试矩阵

- 影响：Core、Chat 算法、Local/Desktop 宿主、前端契约测试和 Fixture；阶段一不修改生产契约、错误目录或生成客户端。
- 依赖：获批的 `spec.md`。
- 工作：先把用户提供的五张 UI 基准图片以原始字节和 SHA-256 固化到本计划的 `assets/`，再用失败测试固定文件工具输入/输出、游标与版本契约、30 秒短期任务能力、相对路径、操作策略、危险命令分类、一次性批准、资源限制和模式门禁；固定 Core 错误目录作为唯一错误语义来源。基准图片只作为视觉参考，不把其中水印或系统窗口内容实现为产品 UI。
- 验证：契约测试因生产文件能力仍返回 `LOCAL_FILE_ACCESS_NOT_ENABLED` 而按预期失败。

## 2. 编写 Core 授权解析、路径和撤销安全测试

- 影响：`backend/core/localworkspace/` 就近测试、`tests/backend/core/`，必要时迁移测试。
- 依赖：任务 1。
- 工作：覆盖 Work/Chat、Local/Desktop/Cloud/LAN、所有权、任务唯一绑定、能力签发/30 秒过期/actor 绑定/重放、长命令 5 秒授权复核、撤销竞态、路径身份变化、相对路径规范化、Unix/Windows 路径、符号链接逃逸、新文件最近存在父目录和外部绝对路径拒绝；用可控同步点覆盖检查后替换父目录的 TOCTOU 场景。
- 验证：SQLite 文件数据库和 PostgreSQL/SQLite 迁移路径；并发撤销与写提交使用真实数据库/可控同步点。

## 3. 编写文件读取、创建、修改与资源限制测试

- 影响：`algorithm/lazymind/chat/engine/tools/`、`algorithm/lazyllm/lazyllm/tools/agent/` 及对应测试。
- 依赖：任务 1～2。
- 工作：覆盖目录 200 项与分页游标、深度 5、搜索 50/200、固定忽略集合、敏感文件精确匹配、文本 20 MiB、窗口 10 KiB/2,000～4,000 行、二进制 100 MiB、创建/覆盖/追加语义、原子写入、版本冲突、跨任务写锁、并发 4/2/1、写入故障和路径脱敏；用目录 descriptor/Handle 验证并发链接替换不能逃逸；Agent 文件 API 的删除、移动和重命名保持永久拒绝。
- 验证：Python 单元/集成测试，真实临时目录、符号链接和并发写，不使用生产环境识别分支。

## 4. 编写 Agent、子 Agent、Skill 与命令策略测试

- 影响：Chat 工具注册、Agent runtime、LazyLLM file/shell tools 及其测试。
- 依赖：任务 1～3。
- 工作：证明主 Agent、子 Agent、内置 Skill 和第三方 Skill 共享同一任务工作区能力；证明 Trusted Local 任意绝对路径、DummySandbox、解释器子进程和模型提供 `allow_unsafe` 不能绕过；覆盖现有危险 token/phrase/重定向检测、普通命令结构化 argv/`shell=false`、工作区内删除/移动/重命名直接执行及越界永久拒绝、受控构建/测试/格式化/包管理在范围内的必要删除移动副作用、`.git/**` 受保护写入、未批准敏感文件在命令/Skill/子进程视图中不可读、多风险合并批准、cwd、环境 allowlist、后台进程、流式输出硬上限与路径/Secret 脱敏、超时和撤销终止。
- 验证：现有 ToolManager/FunctionCall 执行流测试及受控命令 Fake；macOS/Linux/Windows containment 适配器增加真实读取/写入/链接/子进程逃逸测试，但不执行真实破坏命令。适配器不可用时验证安全失败。

## 5. 编写高风险命令与敏感文件批准闭环测试

- 影响：Core/Chat 批准状态、SSE/事件、前端工具卡片及测试。
- 依赖：任务 1、4。
- 工作：复用现有 `tool_limit_control`、Agent control 队列、Core 所有权校验端点、SSE 与 `ToolLimitCard` 测试链；覆盖单任务单 active、并发请求 FIFO、pending → approved/denied/expired/consumed 状态、从服务端提升为 active 并发布事件时开始的 10 分钟可控 Clock、重复决定幂等/冲突、刷新重连恢复、一次性消费、命令/参数/cwd/授权版本绑定、模型伪造拒绝、拒绝后 Agent 继续、重启安全失效、授权撤销失效、永久禁止项无批准按钮、敏感文件读取同一批准语义；覆盖压缩处理超过默认 10,000 条目/500 MiB 总量/100 MiB 单项后的精确上限批准、拒绝、过期、文件变化和再次超限；用失败测试固定现有决定端点不得把数据库/投递原始错误返回客户端。
- 验证：Go/Python/前端契约测试；不用真实 10 分钟 `sleep`。

## 6. 编写 Local/Desktop 和前端端到端边界测试

- 影响：`local/local-proxy/`、`local/local-runtime-manager/`、`desktop/electron/`、`frontend/src/modules/chat/` 测试。
- 依赖：任务 1～5。
- 工作：覆盖可信运行环境和任务能力注入、preload/renderer 最小暴露、Local loopback、Desktop IPC、Chat/Cloud/LAN 隐藏与拒绝、授权弹窗精确新文案及受控工具内部清理说明、工作区状态、命令/资源确认卡片、批准/拒绝和错误提示；证明 file-watcher 权限不变；覆盖 Office/PDF broker staging、绝对路径日志脱敏及原始转换错误不外泄。
- 验证：Go、Node、Vitest；对 LazyMind 自有工作区控件、展开面板和授权弹窗做图片结构/截图回归；真实 macOS 系统目录选择器和 Desktop 闭环留到最终人工 E2E。

## 人工 Review 门禁：阶段一结束

- 汇报需求到测试映射、测试文件、执行命令、预期失败、异常失败、环境阻塞和未覆盖平台。
- 明确生产文件能力仍未启用。
- 等待用户明确批准本批测试，不自动进入实现。

## 7. 同步授权策略、稳定错误和必要迁移

- 影响：Core ORM/迁移、错误目录、OpenAPI、前端生成客户端和第一迭代授权弹窗契约。
- 依赖：阶段一测试获批。
- 工作：把最终未发布授权语义同步为读取/创建/修改 `allow`；新增第二迭代错误；必须新增独立 up/down migration 并同步 PostgreSQL、SQLite 和现有 v0_3 aggregate，不修改已存在的 dev migration；up 将已有 `ask_before_write` 映射为 `allow`，down 在恢复旧约束前反向映射。批准状态沿用本机短期状态与 Chat 状态缓存，不新增长期批准表。
- 验证：任务 2、5；PostgreSQL 与 SQLite 建库、升级、回退、数据和默认值一致性；SQLite 表重建显式验证数据、主键、外键、唯一/普通索引、默认值和 CHECK 约束完整保留。

## 8. 实现任务工作区能力与双重执行校验

- 影响：`backend/core/localworkspace/`、内部路由、`local/local-proxy/` broker 边界和 Local/Desktop 运行配置。
- 依赖：任务 7。
- 工作：把第一迭代只验证后返回 501 的解析边界升级为可信任务执行能力；由 Local Proxy 提供 Local/Desktop 共用的受控 loopback broker，验证运行实例凭据、用户/Work/绑定/授权/目录身份及 actor 上下文，签发默认 30 秒、单运行实例和操作类别绑定、不可转让/重放的能力；长操作每 5 秒复核，操作前和提交前重新解析；不向算法、浏览器或模型暴露绝对根路径，Desktop main 不复制执行逻辑。
- 验证：任务 2 及撤销/并发/重放安全测试。

## 9. 接入允许的文件工具与安全落盘

- 影响：Chat 本地文件工具、LazyLLM agent file tools、工具注册与文档处理适配。
- 依赖：任务 8。
- 工作：按已固化的 `list_directory`、`search_workspace`、`read_text_file`、`create_directory`、`write_text_file`、`replace_text` 和文档 staging/commit 契约接入任务工作区；实现 descriptor/Handle 锚定解析、文本/二进制限制、原子写、不透明版本冲突、可信 broker 集中跨任务路径锁、固定忽略集合、敏感文件检测和脱敏；删除、移动、重命名工具不注册或明确拒绝。
- 验证：任务 3；相关现有文件工具和聊天产物回归。

## 10. 接入子 Agent、Skill 和受控命令执行

- 影响：Chat Agent runtime、子 Agent/Skill 上下文、LazyLLM shell tool 及 Local/Desktop 平台执行边界。
- 依赖：任务 8～9。
- 工作：沿用 ToolManager/FunctionCall 和现有危险检测；将实际进程创建委托给 Local/Desktop runner；普通检查命令用结构化 argv 和 `shell=false`，已识别的项目格式化/测试/构建及工作区范围内的删除/移动/重命名可在 containment 中直接执行，任意解释器内联代码和现有逻辑判定的其他高风险命令逐次批准，越界删除/移动/重命名永久拒绝；固定 cwd、环境 allowlist、流式 stdout/stderr 各 1 MiB、路径/Secret 脱敏、进程树、超时和并发；原始批准载荷仅短期保存在 broker 内存；内置与第三方 Skill 脚本只获得只读 Skill 源、受限工作区和专用临时目录；关闭绑定 Work 任务的 Trusted Local 任意绝对路径行为并禁止 DummySandbox 降级。
- 验证：任务 4；macOS/Linux/Windows 平台适配器真实逃逸测试和交叉构建；不可验证的平台安全失败。

## 11. 实现批准状态、任务暂停恢复与前端确认卡片

- 影响：Core/Chat 事件和状态、前端 AssistantMessage/工具卡片、i18n 与错误提示。
- 依赖：任务 7、10。
- 工作：扩展现有 `tool_limit_control`、Agent control、Core 决定端点、SSE 和 `ToolLimitCard`，实现单 active/FIFO、从服务端发布 active pending 起 10 分钟、重复动作幂等、冲突检测、刷新重连、一次性批准/拒绝/消费及撤销/重启失效；修复决定端点的原始数据库/投递错误泄露；只允许真实用户动作生成内部许可；以原 `execution_id` 暂停和恢复后台 Work 任务；命令卡展示脱敏命令、相对 cwd 和危险原因，敏感文件卡展示精确相对文件，资源卡展示源文件指纹、默认预算和本次精确新上限；永久禁止项不显示允许。
- 验证：任务 5～6；键盘与中英文无障碍检查；工作区控件和授权弹窗按用户图片进行截图或人工视觉核对，macOS 选择窗口只验收原生行为。

## 12. 接入主流程并完成端到端验收

- 影响：上述批准范围内的集成测试、Local/Desktop 运行文档和验收记录。
- 依赖：任务 7～11。
- 工作：完成选择授权—创建 Work—主/子 Agent/Skill 读写—正常命令及范围内内部清理—危险命令批准/拒绝—超预算压缩处理批准/拒绝—文档转换—撤销—立即禁止新访问/提交并在 5 秒内终止命令的闭环；验证 Chat/Cloud/LAN、renderer 伪造、越界、句柄锚定与符号链接竞态、并发冲突、大小/解包限制、敏感文件、转换日志脱敏和永久禁止操作。
- 验证：相关 Go/Python/Node/Vitest 回归，`make local-up` 在可用的 macOS/Linux 主机执行真实目录 E2E，Desktop macOS/Windows 人工或平台 E2E；记录未运行平台和残余风险。

## 补充迭代：任务级三档权限与完整交互

### 13. 补充前端交互契约测试（阶段一）

- 影响：`frontend/src/modules/chat/components/ChatInput/` 的契约测试。
- 工作：固定默认未选工作区/按需确认、菜单互斥、实时搜索、有效与失效授权点击分流、原生选择取消、授权弹窗和全部允许风险框的取消/关闭/遮罩/`Esc`、无工作区禁用、三档切换、成功提示及请求参数。
- 验证：Node 22 + Vitest；生产界面尚未实现的断言必须稳定失败。

### 14. 补充 Core 数据、API 与迁移测试（阶段一）

- 影响：`backend/core/chat/`、`backend/core/localworkspace/`、`backend/core/migrate/` 测试。
- 工作：固定绑定级 `permission_mode`、默认 `ask_as_needed`、三值校验、所有权、已有任务修改、并发版本、能力解析返回当前模式、旧绑定迁移默认值，以及重新授权产生新授权 ID且不恢复旧任务。
- 验证：聚焦 Go 测试；SQLite 文件数据库必须覆盖升级/回退，PostgreSQL 路径独立记录。

### 15. 补充执行审批矩阵测试（阶段一）

- 影响：本地文件工具、Shell/Agent 批准协调器及测试。
- 工作：固定三档对文件写入/删除/移动/重命名、风险命令、联网和已连接应用的批准矩阵；固定 `allow_all` 不能绕过永久禁止，切换模式不能自动消费既有 pending 决定，主 Agent、子 Agent 与 Skill 使用同一绑定模式。
- 验证：Python 聚焦测试使用 Fake，不执行真实联网或破坏操作。

## 补充人工 Review 门禁

- 任务 13～15 完成后报告新增测试、预期失败、异常失败和未覆盖项，并等待用户明确批准。
- 未获批准前不得实施任务 16～18，也不得修改已经 Review 的既有测试来掩盖失败。

### 16. 实现绑定级权限模式与迁移（阶段二）

- 影响：Core ORM、独立 up/down migration、v0_3 aggregate、公开/内部 API、OpenAPI 和前端生成类型。
- 工作：原子保存新任务模式，升级旧绑定为 `ask_as_needed`，提供已有任务查询/修改接口并将当前模式注入执行能力；实现历史工作区重新授权的可信宿主预校验，确保生成新授权记录。
- 验证：任务 14、双数据库迁移与受影响 Core 回归。

### 17. 实现完整前端交互（阶段二）

- 影响：工作区/权限控件、样式、i18n、Desktop/Local 最小桥接。
- 工作：实现三档菜单、全部允许风险框、互斥与键盘/遮罩关闭、有效/失效工作区分流、成功提示和权限阻止状态；保持用户图片的布局与原生选择器边界。
- 验证：任务 13、类型检查、构建、截图与 macOS 人工核对。

### 18. 接入执行策略并完成端到端验证（阶段二）

- 影响：Core 能力解析、Chat/Agent runtime、本地文件与命令工具、联网/已连接应用调用入口。
- 工作：集中执行三档审批矩阵，永久禁止集合始终优先；模式切换不自动放行已有 pending；完成 Local/Desktop 工作区授权—任务创建—权限切换—执行—撤销闭环。
- 验证：任务 15及相关 Go/Python/Node/Vitest 回归；Windows 人工验收按用户决定跳过并披露。
