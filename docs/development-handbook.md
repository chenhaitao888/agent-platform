# Agent Platform 开发手册

这是一份随代码一起演进的开发手册，面向第一次参与 Agent Platform 开发的人。
它记录的不只是“最后代码长什么样”，还记录每一步为什么这样做、如何验证，以及刻意没有做什么。

## 1. 使用方式

开始一个开发步骤前：

1. 先阅读本手册的“当前状态”和“固定开发流程”。
2. 明确这一小步的公共接口、关键行为和范围外事项。
3. 先运行现有测试，确认起点是绿色。

完成一个开发步骤后：

1. 运行对应测试、静态检查和构建。
2. 对跨进程链路做一次真实联调。
3. 更新“开发里程碑”和“当前限制”。

README 负责五分钟内跑起来；本手册负责解释开发过程，避免两处重复维护同一份启动说明。

当前协作约定（2026-10-04）：按用户要求，忽略已知的 GitHub CI 账户账单锁定问题，不重复排查或
提醒，也不把它作为开发和推送的阻塞项。本地测试与静态检查照常执行。

## 2. 当前技术基线

| 范围 | 当前选择 | Java 类比 |
| --- | --- | --- |
| 后端 | Go 1.27.1、标准库 `net/http` | JDK + Servlet/Spring MVC 的最小 HTTP 层 |
| 前端 | React 19、TypeScript、Vite | 浏览器侧表示层；React 组件近似“根据状态生成视图”的函数 |
| 后端测试 | Go `testing`、`httptest` | JUnit + MockMvc 的轻量组合 |
| 前端测试 | Vitest、Testing Library、jsdom | JUnit + 面向用户行为的 UI 测试 |
| 静态检查 | golangci-lint v2.13.2、ESLint 10.12.0、TypeScript | Checkstyle/PMD + 编译期检查 |
| 持久化 | 进程内存 | 临时的 InMemoryRepository，重启即丢失 |

当前代码只覆盖健康检查、持有不可变仓库引用的 Task 幂等创建、查询、第一条状态迁移、Task
事件时间线、GitLab 仓库/commit 只读验证，以及 Workspace 登记、bare clone、detached worktree、
真实 path、固定 base/head 的 diff 读取与预览、不可变 Artifact 归档及 metadata/content 读取，
并具备对应前端闭环；还提供只读 Review 的任务范围触发、执行投影、Findings Artifact 和结果事件。
默认启动未装配 Review Runner。数据库、工作流执行、隔离模型 Worker、Codex app-server、
Credential Broker、鉴权与其他企业 Connector 仍未接入。

当前阶段（2026-10-08）：M13.1 已完成已知交接要求的阶段复核；用户随后确认持续准备 M14，
范围为只读 Codex exec 与结构化 Findings。第 26～28 步已补齐输入贯穿回归、输出契约和环境预检；
第 29 步按用户要求取消 0.154.0 的硬编码限制，改为能力预检与实际版本/摘要记录。
第 30 步修正帮助文本的能力误判，并明确两条检查命令共用 10 秒预算。
具备开始 M14 第一个开发切片的条件，具体入口与验收见第 28 步，版本策略与 Harness 分工见第 29 步，
当前预检边界见第 30 步。第 22～30 步已由用户审阅并提交、推送至 `origin/main`，提交为 `04d9b15`。
第 31 步的 Runner 内部接口与只读进程 adapter 已由用户审阅，2026-10-08 以 `50025c2` 推送至 `origin/main`。
第 32 步完成 HTTP 审阅触发、独立执行投影、Findings Artifact/事件与执行前幂等登记，用户已审阅并批准本次提交推送。
默认 API 启动仍不装配 Runner；真实模型身份、隔离 Worker 和真实模型执行尚未接入。

## 3. 当前目录与职责

```text
agent-platform/
├── backend/
│   ├── cmd/api/                 # Go 进程入口，类似 Java main 启动类
│   ├── cmd/runtimecheck/        # 不调用模型的 Codex 版本与选项预检
│   └── internal/
│       ├── artifact/            # 不可变内容、checksum 与幂等内存 Store
│       ├── codex/               # CLI 版本/摘要预检与只读 exec 进程 adapter
│       ├── connector/gitlab/    # GitLab 验证与可信 clone URL adapter
│       ├── gitworkspace/        # 受控 Git 子进程、bare clone 与 worktree
│       ├── httpapi/             # HTTP 路由和 JSON 适配，类似 Controller 层
│       ├── repository/          # 验证接口与 provider 无关的错误分类
│       ├── review/              # 固定 diff、执行幂等/投影、Findings 契约与 Runner port
│       ├── task/                # Task 模型与内存 Store
│       └── workspace/           # Workspace 登记、准备状态机与 Manager
├── frontend/src/                # React 页面、Task UI 和组件测试
├── scripts/                     # 不调用模型的容器隔离预检
├── CONTEXT.md                   # 领域统一语言
├── docs/development-handbook.md # 本手册
└── README.md                    # 快速启动与使用方式
```

这里暂时没有为了“看起来像企业项目”而预建空层。出现第二种真实实现或复杂业务规则时，
再引入相应接口和分层。

## 4. 固定开发流程

每个功能都按下面的垂直小切片推进：

1. **界定行为**：先写清用户或调用方能观察到什么，以及本步不做什么。
2. **确认基线**：运行当前测试，先排除已有失败。
3. **RED**：只写一条描述公共行为的失败测试。
4. **GREEN**：只增加让这条测试通过的最小实现。
5. **逐条扩展**：对下一个关键行为重复 RED → GREEN，不一次堆完所有测试。
6. **整理**：所有测试绿色后，才消除本步产生的重复或调整模块职责。
7. **完整验证**：运行测试、静态检查、生产构建；跨进程能力再做真实 HTTP 联调。
8. **更新文档**：记录接口、设计取舍、验证结果和仍然存在的限制。

与常见 Java 开发的对应关系是：先用 JUnit/MockMvc 写行为约束，再补 Controller、
Application Service 或 Repository；测试关注 HTTP 响应和用户界面，不直接断言私有方法被调用。

## 5. 测试约定

- 后端优先通过 `http.Handler` 的公开 HTTP 接口测试，不直接测试 Handler 私有方法。
- 前端通过可见文字、label、role 和用户操作测试，不查询 React 内部状态。
- 只模拟系统外部接缝，例如浏览器的 `fetch`；不模拟自己编写的内部模块。
- 每条测试结束后清理 DOM 和全局 `fetch` 替身，防止测试相互污染。
- 并发相关的 Go 代码除普通测试外，还应运行 `go test -race ./...`。

## 6. 开发里程碑

### M0：环境与项目骨架

- 日期：2026-09-21
- 将本机 Go 从 1.19.1 升级到 1.27.1，并确认新终端使用 Homebrew Go。
- 建立 `backend/` 与 `frontend/`，没有覆盖工作目录中的已有文件。
- 选择标准库 HTTP 和小型 React 工程，先避免框架复杂度。

### M1：健康检查与状态页

- 后端增加 `GET /healthz`，返回服务状态和服务名。
- 前端增加加载、健康、不可用三种页面状态。
- Vite 在开发环境中把 `/api/healthz` 转发到 Go 的 `/healthz`。
- 增加 Go Handler 测试和 React 页面测试。

### M2：内存版 Task 创建与查询

- 增加 `Task`、`StatusCreated` 和并发安全的内存 `Store`。
- 增加 `POST /api/v1/tasks` 与 `GET /api/v1/tasks/{id}`。
- 输入会去除首尾空白；空字段、非法 JSON 和不存在的 ID 返回结构化错误。
- Task 仍只存在于当前 Go 进程。

### M3：前端 Task 创建闭环

- 增加受控表单，支持 `PR_REVIEW` 与 `BUG_FIX`。
- 请求期间禁用按钮，成功后显示 Task ID、状态和目标。
- 区分后端业务错误与网络错误。
- 将 Vite 代理拆为健康检查改写规则和 `/api/v1/*` 原样转发规则。
- 真实联调已验证健康检查 200、创建 201、按 ID 查询 200。

### M4：最近 Task 列表

- 状态：完成（2026-09-21）。
- 后端增加 `GET /api/v1/tasks`，响应为 `{"items":[...]}`，最新创建的 Task 排在前面。
- Store 继续用 map 支持按 ID 查询，同时保存创建顺序；`List()` 在读锁内生成结果副本。
- 前端增加 `TaskWorkspace`，统一协调创建表单和最近列表，不引入全局状态管理。
- 页面覆盖加载、空列表、成功和读取失败四种状态。
- `TaskCreator` 通过 `onCreated(task)` 把成功结果通知工作区；工作区按 ID 去重并放到列表顶部。
- 处理了异步竞态：迟到的旧列表成功响应会与本地已确认创建的 Task 合并；迟到的失败也
  不能把已经可用的新 Task 覆盖成错误状态。
- 本步没有加入分页、筛选、自动轮询和数据库持久化。

#### M4 开发过程记录

1. 后端先写“连续创建两个 Task，GET 列表时最新项在前”的 HTTP 测试。测试最初返回
   `405`，随后增加 GET 路由和 Store 的 `List()` 后变绿。
2. 再约束空集合必须返回 `[]` 而不是 `null`；前一轮实现已经自然满足此行为。
3. 前端先用缺失模块测试得到 RED，再实现列表加载与三态渲染。
4. 创建后更新列表的测试最初失败，因为创建结果只存在于 `TaskCreator` 内部；增加
   `onCreated(task)` 接口后变绿。
5. 竞态测试第一次出现了假绿灯，原因是断言早于 Promise 后续状态更新。测试改为先等待
   迟到响应中的旧 Task 出现在 DOM，再检查新 Task，成功复现覆盖问题并推动合并逻辑。
6. 自审时增加了对称的失败竞态测试：如果创建先成功、旧列表请求后失败，应保留已确认的
   Task，而不是切换成错误状态。
7. 页面接入第二个 `role="status"` 后，旧测试定位变得含糊；为 API 状态增加可访问名称，
   测试改用“角色 + 名称”定位。

#### M4 验证结果

- `go test ./...`：通过。
- `go test -race ./...`：通过。
- `go vet ./...`：通过。
- 前端：3 个测试文件、12 条测试全部通过。
- `node --run build`：通过。
- 真实 Vite → Go 联调：两次创建均返回 201，列表返回 200，并按 `task-2`、`task-1` 排序。

### M5：Task 幂等创建契约

- 状态：完成（2026-09-21）。
- 依据：架构文档附录 A 要求所有写请求携带 `requestId` 与 `idempotencyKey`，Task
  以 `(tenantId, idempotencyKey)` 唯一约束去重，所有资源响应携带 `version`。
- 请求体新增必填 `requestId`、`idempotencyKey`、`tenantId`；Task 响应新增
  `tenantId`、`idempotencyKey` 与 `version`。
- HTTP 约定：首次创建返回 201；同内容重放返回 200；同键不同内容返回 409；成功响应
  使用 `X-Request-ID` 回显本次调用标识。
- Store 使用私有 struct 作为 `(tenantId, idempotencyKey)` 复合 map key；检查与创建在同一
  写锁中完成。
- `CreateInput` 类似 Java command record；`CreateResult` 明确区分新建和重放；
  `ErrIdempotencyConflict` 作为普通 Go error 由 Handler 映射为 HTTP 409。
- 前端 Phase 0 固定租户为 `tenant-local`，使用 `crypto.randomUUID()` 生成 request ID
  和 idempotency key。
- 本步不做：数据库唯一索引、自动重试、认证授权、审计持久化和完整 Repository 字段。

#### M5 开发过程记录

1. 先写完全相同请求重放测试，当前实现第二次仍返回 201；增加内存索引后变为 200，
   并返回同一个 Task。
2. 再写同键不同 goal 的测试，当前实现错误返回 200；引入领域冲突 error 后变为 409。
3. 再写不同租户复用同一 key 的测试，字符串索引错误返回 409；改用复合 struct key 后，
   两个租户分别创建成功。
4. 增加缺失 request/tenant 元数据测试，锁定旧版请求必须返回 400。
5. 前端先修改请求契约测试，确认旧 JSON 变红，再加入固定租户和随机请求标识。

#### M5 验证结果

- `go test ./...`、`go test -race ./...`、`go vet ./...`：通过。
- 前端 3 个测试文件、12 条测试：全部通过；生产构建通过。
- 真实 Vite → Go 联调依次得到：首次 201、重放 200、载荷冲突 409、另一租户 201。
- 重放响应保持 `task-1`，另一租户创建 `task-2`；四次响应均正确回显各自 request ID。

### M6：Task 首条状态迁移与乐观并发

- 状态：完成（2026-09-21）。
- 依据：架构文档 4.1.1 定义 `CREATED → QUEUED`；`CREATED` 表示尚未准入，
  `QUEUED` 表示已准入、等待 Workflow 或容量。
- HTTP 接口：`PATCH /api/v1/tasks/{id}`，请求携带 `requestId`、`idempotencyKey`、
  `tenantId`、`expectedVersion` 和目标 `status`。
- 成功迁移返回 200，Task 从 version 1 的 `CREATED` 变为 version 2 的 `QUEUED`。
- 旧 `expectedVersion` 返回 `409 version_conflict`；版本正确但迁移边不允许时返回
  `409 invalid_transition`。
- 相同租户下，同一迁移 `idempotencyKey` 和相同业务内容可安全重放；复用 key 却改变 Task、
  目标状态或期望版本时返回 `409 idempotency_conflict`。
- 前端只为 `CREATED` Task 显示“加入队列”按钮，成功后以服务端返回对象替换旧对象。
- 本步不做：真实消息队列、租户配额、优先级、公平调度、WorkflowRun 或 Temporal。

#### M6 后端代码拆解

1. `StatusQueued` 只是一个领域状态常量，不代表 Go 进程里真的出现了队列。Java 中也可以先有
   `TaskStatus.QUEUED` 枚举值，再由后续 Application Service 接入真正的调度器。
2. `TransitionInput` 是一次迁移命令，作用接近 Java `record TransitionTaskCommand(...)`。
   它把 Task ID、租户、幂等键、期望版本和目标状态放在一起，避免 Store 接收一长串易混参数。
3. `Store.Transition` 在同一把写锁内依次检查迁移重放、Task 归属、version 和合法状态边，最后
   同时更新状态与版本。它相当于内存版的 `UPDATE ... WHERE id = ? AND version = ?`；写锁保证
   “检查后再更新”不会被另一个 goroutine 插进来。
4. 迁移结果按 `(tenantId, idempotencyKey)` 记录。重放时返回第一次成功的结果，不再次增加
   version。这类似一张简化的 `operation_ledger`，目前只是内存 map。
5. HTTP Handler 只负责 JSON、必填字段和错误码映射；“只允许 CREATED 到 QUEUED”的规则放在
   task Store 中。类比 Java，就是 Controller 不自己决定领域状态机，由领域/Application 层判断。

#### M6 前端代码拆解

1. `TaskStatus` 使用 TypeScript 联合类型 `'CREATED' | 'QUEUED'`，类似只包含两个值的 Java enum。
2. 点击“加入队列”时，前端发送 Task 当前的 version；成功后用 `map` 产生一个新数组，并把同 ID
   对象替换成响应对象。React 依赖新的数组引用触发渲染，不能像 Java bean 那样原地调用 setter。
3. 列表加载和按钮更新是两个独立异步请求，返回顺序不保证。`mergeTasks` 先用 `Map` 按 ID 建索引，
   同 ID 保留 version 较大的对象；这类似合并两个 `Map<String, Task>` 时执行乐观版本比较。
4. 更新错误单独放在 `updateError`，因此一次准入失败不会摧毁已经成功加载的 Task 列表。

#### M6 开发过程记录

1. 后端先写 `CREATED → QUEUED` 的 HTTP 测试，旧路由返回 405；增加 PATCH 路由、迁移命令和
   Store 状态更新后变绿。
2. 增加旧 version 测试，确认同一写锁内的版本检查返回 `version_conflict`，且 Task 保持 version 2。
3. 幂等重放测试最初得到 409，因为 Task 已经是 QUEUED；加入迁移结果记录后，同一操作重放返回
   第一次的 version 2 结果。
4. 再锁定两个边界：新 key 发起 `QUEUED → QUEUED` 返回 `invalid_transition`；同 key 改变迁移内容
   返回 `idempotency_conflict`。
5. 前端先写用户点击行为测试，旧页面找不到“加入队列”按钮；补充 PATCH 请求、等待态和不可变
   列表替换后变绿。随后增加 409 测试，确认错误可见且原 Task 不丢失。
6. 最后模拟“旧列表请求迟到”：页面已经拿到 QUEUED/version 2，旧响应却带回 CREATED/version 1。
   测试成功复现回退，再用按 ID、按 version 的合并规则修复。

#### M6 验证结果

- `go test ./...`、`go test -race ./...`、`go vet ./...`：通过。
- 前端 3 个测试文件、15 条测试：全部通过；生产构建通过。
- 真实 HTTP 联调依次得到：创建 201、首次迁移 200、幂等重放 200、旧版本冲突 409、非法迁移 409。
- 首次迁移返回 `QUEUED / version 2`；重放仍返回同一个版本，没有重复递增。

### M7：最小 Task 事件时间线

- 状态：完成（2026-09-22）。
- 依据：架构文档 4 节定义 `PlatformEvent` 作为集成、流式展示和审计的事实载体，附录 A.3
  定义统一事件信封；Phase 0 要跑通执行事件。
- 新增 `GET /api/v1/tasks/{id}/events?tenantId={tenantId}`，响应使用 `{"items":[...]}` 包装。
- 此里程碑当时只产生两类事件：Task 首次创建时的 `task.created`，以及成功准入时的
  `task.queued`；M13.1 第 8 步已扩展 Workspace/Artifact 事件并升级契约。
- 最小信封包含 schema、事件身份、时间、租户/Task 范围、sequence、关联/因果 ID 和状态载荷。
- `correlationId` 使用 Task ID；`causationId` 使用触发该事实的写请求 `requestId`。
- 事件与 Task 变更在同一把内存写锁内完成；幂等重放只返回原结果，不重复产生事件。
- 查询必须显式提供 tenantId；Task 不存在或租户不匹配时统一返回 404。
- 前端按需加载单个 Task 的时间线，避免列表加载时形成一次列表请求加 N 次事件请求。
- 本步不做：数据库事务 Outbox、Event Bus、实时推送、事件消费、分页、长期审计保留。

#### M7 后端代码拆解

1. `Event` 与 `EventPayload` 是事件信封和最小载荷，作用接近 Java record。`EventType` 是字符串
   别名加常量，类似轻量 enum，但 Go 不会自动阻止调用者构造其他字符串，因此合法值仍由模块维护。
2. Store 内部用 `map[string][]Event` 保存每个 Task 的事件列表，近似
   `Map<String, List<Event>>`。`nextEventID` 只保证当前进程内事件 ID 不重复。
3. `appendEventLocked` 统一生成 ID、sequence、关联字段和载荷。名称中的 `Locked` 提醒维护者：
   调用前必须持有 Store 写锁；Go 编译器不会验证这条约定。
4. 创建与迁移都先完成领域检查，再在释放写锁前追加事件。它不是数据库事务，但表达了未来
   “业务状态与 Outbox 同事务提交”的一致性要求。
5. `ListEvents` 在读锁内复制 slice 再返回，防止外部调用者通过共享底层数组修改 Store 内容。
   Java 中相近的意图是返回不可变副本，而不是暴露内部 `ArrayList`。
6. Handler 只解析路径和 tenantId、映射 400/404，并包装列表；事件排序和租户归属仍由 task
   模块负责，因此复杂度不会散落到多个 HTTP 调用方。

#### M7 前端代码拆解

1. 新的 `TaskEvent` 类型对齐后端 JSON 信封。TypeScript 的这些类型只做编译期检查，运行时
   `response.json()` 不会自动验证服务端数据，后续进入外部不可信边界时需要单独 schema 校验。
2. `TaskEventTimeline` 只接受一个 `task` prop，把 URL、请求和渲染状态封装在内部；父级
   `TaskWorkspace` 只增加一行组件调用，形成小接口、深实现。
3. 时间线状态使用 `idle | loading | ready | failed` 联合类型，类似 Java sealed interface 加四个
   record，避免多个 boolean 和 nullable 字段组合出不可能状态。
4. 用户点击后才加载事件，按钮在请求期间禁用；失败只影响该时间线，不会清空 Task 列表。
5. HTML 使用有序列表 `ol` 表达事件先后关系，并为按钮和列表提供可访问名称，测试通过用户可见
   行为定位，不读取 React 内部 state。

#### M7 开发过程记录

1. 后端先写“创建后可查询 `task.created`”的 HTTP 测试，旧实现返回 404；加入嵌套资源路由、
   事件信封和 Store 事件副本查询后变绿。
2. 准入事件测试直接通过，因为第一轮的统一追加路径已覆盖成功迁移；将其记录为特征确认，
   不伪造 RED。
3. 幂等准入重放测试确认两次 PATCH 后仍只有创建、准入两条事件。
4. 增加租户隔离与缺失 tenantId 测试，分别锁定 404 与 400 行为。
5. 前端先导入尚不存在的 `TaskEventTimeline` 得到模块解析失败，再增加类型、联合状态与按需请求
   后变绿。
6. 工作区测试先要求每个 Task 卡片出现事件入口，得到“找不到按钮”的 RED；接入子模块后变绿。
7. 增加失败与重试行为测试，确认事件读取错误不会破坏主列表。

#### M7 验证结果

- `go test ./...`、`go test -race ./...`、`go vet ./...`：通过。
- 前端 4 个测试文件、17 条测试：全部通过；生产构建通过。
- 真实 Vite → Go 联调返回 `task.created / sequence 1` 与 `task.queued / sequence 2`。
- 重放准入请求后再次查询，仍为原来的两条事件，没有重复追加。

### M8：最小 Workspace 元数据

- 状态：完成（2026-09-22）。
- 依据：架构文档第 8 节规定平台 Workspace Manager 是 clone/worktree、base/head SHA 和清理的
  唯一 owner；Phase 0 的 PR Review 必须固定不可变 base/head SHA。
- 新增 `POST /api/v1/tasks/{id}/workspace` 与
  `GET /api/v1/tasks/{id}/workspace?tenantId={tenantId}`。
- Workspace 包含 ID、tenant/task、repository provider/repositoryId、base/head SHA、state、
  version 和 createdAt。
- M8 只定义 `REGISTERED`：元数据已登记，但仓库尚未 clone，路径尚未创建，Runtime 尚未绑定。
- 只有 `QUEUED` Task 可以登记；同一 Task 至多一份 Workspace。
- 首次登记返回 201；同一幂等操作重放返回 200；同键不同内容和第二份 Workspace 分别返回
  `idempotency_conflict` 与 `workspace_already_exists`。
- 前端只为 `QUEUED` Task 提供 Workspace 入口，按需查询；404 时才展示登记表单。
- 新增根目录 `CONTEXT.md`，记录 Task、Workspace、Registered Workspace、Repository Reference
  与 Task Event 的统一语言。
- 本步不做：Git clone/worktree、真实 path、Runtime、容器、挂载、缓存、清理与 Workspace 事件。

#### M8 后端代码拆解

1. 新的 `workspace` package 是独立模块。`Workspace` 与 `Repository` 是数据 struct，`State` 使用
   字符串别名和 `StateRegistered` 常量，类似 Java record 加 enum 的组合。
2. `workspace.Manager` 显式持有 `*task.Store`。登记前先读取 Task 并检查租户与 `QUEUED` 状态；
   它类似模块化单体中的领域服务，但没有引入只有一个实现的 Repository interface。
3. Manager 用自己的 `RWMutex` 保护 Workspace ID、Task 索引和幂等索引。一 Task 一 Workspace
   的检查与写入在同一写锁中完成，两个并发登记请求不能都成功。
4. 幂等检查先于业务唯一约束：同操作重放返回第一次结果；同键不同内容返回幂等冲突；新 key
   再创建才返回 Workspace 已存在。这个顺序避免把安全重试误判成第二次创建。
5. `RegisterResult` 用 `Created` 区分首次和重放，作用类似 Java
   `record RegisterResult(Workspace workspace, boolean created)`；Handler 据此映射 201/200。
6. GET 在读锁中返回 Workspace 值拷贝，并核对 tenantId。错误租户和不存在统一返回 404，减少
   资源枚举信息，但显式 tenantId 仍不能代替真实身份认证。

#### M8 前端代码拆解

1. `workspace.ts` 单独定义 Workspace DTO，不把两个领域对象堆进 `task.ts`。
2. `WorkspaceDetails` 只接受一个 Task prop，并封装 GET、POST、表单和详情展示；父工作区只负责
   在 Task 为 QUEUED 时挂载它。
3. UI 状态使用 `idle | loading | absent | registering | ready | failed` 联合类型。`absent` 是可登记的
   业务状态，不是系统错误；类比 Java sealed interface，它让每种分支携带自己真正需要的数据。
4. 表单使用受控输入，repositoryId、baseSHA、headSHA 都通过 `useState` 保存；提交前去除首尾空白。
5. Phase 0 只有单 Git 平台，前端暂时固定 provider 为 `gitlab`；后端契约仍明确携带 provider。
6. Workspace 查询和事件一样按需触发，不在 Task 列表加载时制造 N+1 请求。

#### M8 开发过程记录

1. 后端先写“QUEUED Task 可登记 Workspace”的 HTTP 测试，旧实现返回 404；增加 workspace
   package、Manager 和 POST 路由后变绿。
2. 未准入测试直接通过，确认 CREATED Task 返回 `task_not_queued`。
3. 幂等重放测试最初返回 `workspace_already_exists`；加入登记记录并调整检查顺序后变为 200。
4. GET 测试最初返回 405；加入 Manager 读取方法和 GET 路由后变绿。
5. 增加同键不同内容、第二份 Workspace 和跨租户读取测试，分别锁定三个冲突/隔离行为。
6. 前端先导入不存在的 `WorkspaceDetails` 得到模块解析 RED，再实现已有 Workspace 的按需读取。
7. 404 场景测试最初只看到“workspace not found”；增加 `absent` 状态和登记表单后变绿。
8. Task 准入测试先要求出现 Workspace 入口，得到找不到按钮的 RED；按 QUEUED 条件接入后变绿。

#### M8 验证结果

- `go test ./...`、`go test -race ./...`、`go vet ./...`：通过。
- 前端 5 个测试文件、19 条测试：全部通过；生产构建通过。
- 真实 Vite → Go 联调依次得到：未登记查询 404、首次登记 201、再次查询 200、幂等重放 200。
- 重放保持 `workspace-1 / REGISTERED / version 1` 以及原 createdAt。

### M9：Repository Reference 前移到 Task

- 以下记录 M9 当时的接口与实现；当前契约已由 M13.1 第 9 步改为服务端固定 master 并计算 base。
- 状态：完成（2026-09-22）。
- 依据：架构文档附录 A.1 在创建 Task 时接收 repository provider/repositoryId/baseSha/headSha；
  分支名不是可复现输入，PR Review 必须固定不可变 base/head SHA。
- `POST /api/v1/tasks` 新增必填 `repository` 对象，Task 响应、按 ID 查询和列表都返回同一份引用。
- Phase 0 只接受 `gitlab`；base/head SHA 必须是 40 或 64 位十六进制对象 ID。
- Task 创建幂等比较现在覆盖 type、goal 和完整 Repository Reference，同 key 改变任一仓库字段均
  返回 `idempotency_conflict`。
- `POST /api/v1/tasks/{id}/workspace` 只接收 requestId、idempotencyKey、tenantId；Workspace
  Manager 从 QUEUED Task 派生仓库和 SHA，调用方不再重复输入。
- Workspace 登记幂等内容缩小为目标 Task：同一 key、同一 Task 可重放；同一租户把 key 用于
  另一个 Task 时冲突。
- 本步不做：连接 GitLab、验证仓库归属或 commit 存在性、解析分支、clone/worktree、准备路径。

#### M9 后端代码拆解

1. `task.RepositoryReference` 把 provider、repositoryId、baseSha、headSha 组成一个值对象，并由
   `Task` 和 `CreateInput` 持有。它近似 Java `record RepositoryReference(...)`；四个字段都是
   可比较字符串，因此 Go struct 可以直接用 `==`/`!=` 做完整值比较。
2. HTTP Handler 在系统边界去除首尾空白并校验必填、provider 和对象 ID 形状。它类似 Java
   Controller DTO 上的 Bean Validation；这里只判断格式，不把网络调用塞进 Controller。
3. `Store.Create` 的幂等检查比较完整 Repository Reference。这样重试时误换 head SHA 不会被
   当作同一个 command 返回旧结果。
4. `workspace.RegisterInput` 不再包含仓库字段。`Manager.Register` 先读取 Task、核对租户与
   QUEUED 状态，再从 `currentTask.Repository` 构造 Workspace；它像 Java Application Service
   通过聚合读取事实，而不是信任第二份 Web 表单。
5. Workspace 登记记录只保存 taskID 和首次结果。同 key 指向另一 Task 才是内容冲突；同一 Task
   的安全重放仍返回第一次的 Workspace。

#### M9 前端代码拆解

1. `RepositoryReference` TypeScript 类型成为 `Task` 的必填字段，作用接近 Java record 的编译期
   契约；`response.json()` 仍不会做运行时 schema 校验。
2. `TaskCreator` 新增 repositoryId、baseSHA、headSHA 三个受控输入，provider 在 Phase 0 固定为
   gitlab。三个 `useState` 类似表单 backing bean 字段，但 React 通过 setter 触发重新渲染。
3. Workspace 404 分支删除了三份输入 state 和登记表单，改为展示 `task.repository` 的只读投影
   和一个按钮。用户现在能看见将使用什么输入，但不能制造第二份不一致值。
4. 测试使用统一合法表单 helper 和 Repository Reference fixture，避免每个竞态/错误测试重复
   关心长 SHA；服务端 mock 若遗漏新必填字段会直接暴露契约不一致。

#### M9 开发过程记录

1. 后端先让创建测试要求响应保存仓库引用；旧实现返回 201，但四个字段为空。补齐请求 DTO、
   CreateInput 与 Task 的连续映射后转绿。
2. 缺少仓库引用的测试先得到 201，再加入必填校验得到 400；随后更新所有本应成功的旧夹具。
3. 分别用 GitHub provider 和 `main` 分支名制造 RED，再加入单 GitLab 与 40/64 位十六进制校验。
4. 同 key、同 type/goal、不同 head SHA 最初错误返回 200；幂等比较加入 Repository Reference
   后返回 409。
5. Workspace 主路径测试删除重复仓库参数后先得到 400；Manager 改为读取 Task 后得到 201，且
   响应仍是 Task 中的仓库和 SHA。旧“同 key 改 head”测试随之改成“同 key 登记另一 Task”。
6. 前端创建测试先因找不到“仓库 ID”标签失败；增加 Task 表单字段和嵌套请求后转绿。
7. Workspace 测试先因找不到只读仓库引用分组失败；删除输入 state/表单并使用 Task 引用后转绿。
8. 全套前端测试第一次运行发现四个旧 mock 缺少 repository，组件访问 undefined；修正测试夹具，
   不在生产代码里用可选链掩盖坏响应。

#### M9 验证结果

- `go test ./...`、`go test -race ./...`、`go vet ./...`：通过。
- 前端 5 个测试文件、19 条测试：全部通过；生产构建通过。
- 真实 Vite → Go 联调依次验证：带仓库引用创建 Task、准入、仅用操作元数据登记 Workspace、查询
  以及登记幂等重放。
- Workspace 返回的 provider/repositoryId/base/head 与 Task 完全相同，重放没有产生第二份记录。

### M10：可信 GitLab Repository Reference 验证

- 状态：完成（2026-09-22）。
- 依据：架构文档 8.1 要求 Workspace Manager 统一拥有 base/head SHA；9.1 要求 Repository 读取
  通过 Connector 平面，以固定仓库和 SHA；服务凭据不能进入 Runtime 或任意 shell。
- Workspace 首次登记前，通过 GitLab HTTPS API 依次确认项目、base commit 与 head commit。
- Workspace HTTP 请求契约不变，仍只含 requestId、idempotencyKey、tenantId；验证对象只来自
  Task 的 Repository Reference。
- 仓库不存在、base 不存在、head 不存在分别返回三个 422 业务错误；GitLab 未配置、认证失败、
  网络异常、非 2xx/404 响应或重定向统一返回 503。
- `AGENT_PLATFORM_GITLAB_BASE_URL` 与 `AGENT_PLATFORM_GITLAB_TOKEN` 必须成对配置。两者都缺失时
  服务仍启动，但 Workspace 登记失败关闭；只有一个存在时服务拒绝启动。
- GitLab Base URL 必须是 HTTPS，且不能内嵌凭据、query 或 fragment；adapter 不跟随重定向，避免
  `PRIVATE-TOKEN` 被带到其他主机。
- 本步不做：clone/worktree、base 是否为 head 祖先、Merge Request 归属验证、缓存、重试、限流、
  熔断、Credential Broker、token 轮换与其他 Git provider。

#### M10 后端代码拆解

1. `repository.ReferenceVerifier` 只有一个 `Verify(ctx, reference)` 方法，是 Workspace 需要的最小
   interface。它类似 Java 应用层 port；GitLab adapter 通过方法集合隐式实现，不写 `implements`。
2. `connector/gitlab.Verifier` 隐藏项目 ID URL 编码、三个 GET、token header、响应关闭和错误分类。
   调用方不需要知道 `/api/v4` 路径，删除这个 module 会迫使这些细节散落回 Workspace Manager。
3. 构造器要求 HTTPS、非空 token 和显式 `http.Client`。它复制 client 后禁用重定向，不修改调用方
   对象；main 注入的 client 有 5 秒超时。
4. Workspace Manager 先在锁内处理已有幂等结果，再释放锁调用 verifier，成功后重新加锁检查并
   创建。Java 中相当于不拿着全局 `synchronized` 锁等待 RestClient，同时用第二次检查关闭竞态窗口。
5. 已成功登记的幂等重放直接返回本地结果，即使 GitLab 随后不可用也不重新验证；成功事实不会被
   短暂下游故障推翻。
6. HTTP Handler 将 provider 无关的验证错误映射为稳定错误码。`cmd/api` 只负责读取进程配置、
   构造 adapter 并注入，不把 token 写日志或下发给 Workspace/Runtime。

#### M10 前端代码拆解

1. Workspace 接口和登记按钮不变，前端继续展示 Task 中的只读仓库引用。
2. 已有联合状态可承载 422/503，无需增加新的 boolean；服务端 message 直接显示给开发阶段用户。
3. 失败文案由“Workspace 加载失败”改为“Workspace 操作失败”，同时覆盖查询和登记阶段。

#### M10 开发过程记录

1. 第一条 HTTP 测试要求未知仓库返回 422；最初因 `internal/repository` 不存在而编译失败。增加最小
   verifier interface、错误和 Manager 注入后转绿。
2. base/head 不存在的测试分别先因错误值不存在而编译失败，再逐条加入错误分类与 HTTP 映射。
3. 默认未配置 verifier 的测试最初得到 500；加入 `repository_verification_unavailable` 映射后得到 503。
4. GitLab 成功测试最初因 `NewVerifier` 不存在而失败；加入标准库 HTTPS adapter 后，项目、base、
   head 三次读取及 `%2F` 项目 ID 编码通过。
5. 恶意重定向测试最初错误返回成功，证明 client 跟随了 302；复制 client 并禁用重定向后，token
   不再到达目标服务器，结果归类为验证不可用。
6. GitLab 404 分类、5xx 分类和不安全/不完整配置测试直接通过，记录为特征确认，没有伪造 RED。
7. 幂等重放测试把 verifier 改为“只成功一次”；第二次登记仍返回原 Workspace，证明不会重复依赖 GitLab。
8. 前端 503 测试先看到“加载失败”，改成“操作失败”后转绿。

#### M10 验证结果

- `go test ./...`、`go test -race ./...`、`go vet ./...`：通过。
- 前端 5 个测试文件、20 条测试：全部通过；生产构建通过。
- GitLab adapter 通过本地 TLS 假服务验证三个真实 HTTPS 请求、token header、URL 编码、404/5xx
  分类和重定向防泄漏。
- 真实 Go HTTP 进程在未配置 GitLab 时依次得到 Task 创建 201、准入 200、Workspace 登记 503、
  查询 404，确认失败关闭且没有留下半成品。
- 未提供企业 GitLab 地址与令牌，因此本步未对真实企业 GitLab 发请求；真实凭据联调仍需在受控环境完成。

### M11：平台准备真实 Git Workspace

- 状态：完成（2026-09-22）。
- 依据：架构文档 5.3 把 `prepareWorkspace` 放在 Runtime 之前；8.1 明确 Workspace Manager 是
  clone、worktree 和 base/head SHA 的唯一 owner，Codex 不得再自行创建 worktree。
- 新增 `POST /api/v1/tasks/{id}/workspace/prepare`。请求只含操作元数据、tenant 与
  `expectedVersion`；仓库和 SHA 继续只从已登记 Workspace 读取。
- Workspace 状态最小扩展为 `REGISTERED → PREPARING → READY`，成功时 version 从 1 依次变为
  2、3，并在 READY 响应中出现实际 `path`。
- 平台创建 `workspace-{id}/repository.git` bare clone 和 `workspace-{id}/worktree` detached
  worktree；base/head 必须都能按 commit 对象读取，worktree HEAD 必须等于固定的 head SHA。
- 准备失败时删除本次创建的整个 `workspace-{id}` 半成品目录，状态从 PREPARING 回到
  REGISTERED，version 仍继续增加到 3。这样读过 PREPARING/v2 的客户端不会把旧数据当成新数据。
- 本步不做：共享 clone cache、清理接口、磁盘配额、Runtime/容器、挂载、Codex、异步队列、
  crash recovery、数据库持久化与企业 GitLab 真凭据联调。

#### M11 状态与 Manager 代码拆解

1. `StateRegistered`、`StatePreparing`、`StateReady` 是有类型的字符串常量。JSON 仍是人能读懂的
   大写字符串，但 Go 编译器会阻止把任意普通字符串误当成 Workspace 状态。Java 中可类比 enum，
   只是 Go 不会自动生成 `values()` 等方法。
2. `Workspace.Path` 使用 `omitempty`。REGISTERED/PREPARING 时不返回假路径；只有准备全部成功后
   才赋值。这里的关键不是节省一个 JSON 字段，而是防止调用方把正在写入的目录当成可用目录。
3. `Preparer` 只有 `Prepare(ctx, repositoryReference, destination)` 一个方法。Manager 只知道“把这份
   不可变代码输入准备到这个平台路径”，不知道 GitLab API、token、askpass 或 Git CLI 参数。它类似
   Java Application Service 依赖的 port；生产 adapter 和测试 adapter 让这个 seam 成为真实变化点。
4. `NewManagerWithPreparer` 要求绝对根目录、在符号链接解析前后都拒绝文件系统根目录、用 `0700`
   创建目录，并通过 `EvalSymlinks` 保存规范路径。macOS 的 `/var` 常映射到 `/private/var`；规范化可
   避免同一目录有两种字符串身份，也防止“看似普通目录、实际指向 `/`”的配置绕过安全检查。
5. `Prepare` 先在锁内检查租户、幂等记录、version 和 REGISTERED 状态，再写入 PREPARING/v2，随后
   释放锁才调用 Preparer。Java 类比：先在短事务内更新实体，再在事务外做慢 `ProcessBuilder` I/O；
   不能拿着 JVM 全局 `synchronized` 锁等网络和磁盘。
6. Git 成功后再次加锁写 READY、path 和 v3；失败后再次加锁恢复 REGISTERED/v3。PREPARING 因此可被
   并发 GET 观察到，但半成品 path 永远不可见。
7. 准备幂等记录保存 task、原 expectedVersion 与成功结果。同一输入重放直接返回 READY，不再 clone；
   同 key 换 Task 或版本则冲突。失败不记录成功结果，调用方需要 GET 新 version 后重试。
8. 登记幂等记录现在只保存 taskID，重放时从 `byTask` 读取当前 Workspace。原因是 Workspace 进入
   READY 后再重放登记，应该返回当前 READY/v3，而不是 M8 时代缓存的 REGISTERED/v1 快照。

#### M11 Git 与 GitLab adapter 代码拆解

1. `gitworkspace.Preparer` 是一个深模块：调用者只给 clone URL、两个 SHA、目标 path 和凭据；内部
   负责目录边界、askpass、四类 Git 命令、输出上限、失败清理和 detached HEAD。删除这个模块会让
   这些易错细节散落到 Manager 或 Handler。
2. 目标必须形如绝对路径 `.../workspace-{id}/worktree`。模块只用 `os.Mkdir` 创建一个原本不存在的
   `workspace-{id}`，因此失败时的 `RemoveAll` 只会删除本次亲手创建、且名称已验证的目录，不会递归
   删除调用方已有目录。
3. 当前 Git 顺序为：`clone --bare --no-local`、用空凭据环境执行 `cat-file -e SHA` 探测两个对象、
   仅在缺失时 `fetch origin <缺失 SHA>`、删除 askpass、用 `cat-file -e SHA^{commit}` 严格验证、
   `worktree add --detach path head`。bare repository 保存对象，worktree 给后续 Runtime 使用；detached
   HEAD 避免把“当前分支”误当成不可变输入。Java 中可把它理解成一个受控的 `ProcessBuilder` 流水线。
4. SHA 在 HTTP 层和 Git 深模块各校验一次 40/64 位十六进制。前者给调用方清楚的 400，后者保护
   非 HTTP 调用路径，避免不可信文本变成 Git 参数。
5. token 不进入 URL 或 argv。clone/fetch 通过一个不含 secret 的临时 `git-askpass.sh` 读取子进程
   环境中的用户名/密码。这个文件不是源码资源：`Prepare` 在运行时根据文件末尾的 `askPassScript`
   常量，把它写到 `workspace-{id}/git-askpass.sh`；最后一次可能联网的命令完成后立即删除。
   clone 后的对象探测和后续本地命令都使用空凭据环境。
6. 子进程不再继承控制平面的整个环境，而使用 PATH、TMPDIR、locale、proxy、CA 等明确白名单，再
   添加本次 Git 所需变量。这阻止 `AGENT_PLATFORM_GITLAB_TOKEN` 以及其他无关服务 secret 被顺带
   交给 Git。命令输出最多保留 64 KiB，HTTP 只返回稳定错误，不回显这些内部文本。
7. `gitLabProjectsAPIPath = "/api/v4/projects/"` 是 GitLab REST API v4 的协议常量，`projectAPIPath`
   负责把可变的 repositoryID 做 URL path 编码后拼到它后面；它不是企业项目地址的硬编码。
8. `connector/gitlab.WorkspacePreparer` 再读一次项目 API 的 `http_url_to_repo`，要求 HTTPS、无内嵌
   凭据/query/fragment，且 host 与配置的 GitLab Base URL 完全相同。即使 GitLab 响应被异常改写，
   token 也不会被送往另一台主机。
9. M10 的 `Verifier.get` 被提取成包内共享方法：它仍负责 PRIVATE-TOKEN、禁重定向和错误分类；
   verifier 丢弃响应体，WorkspacePreparer 则只解析受限大小的项目 JSON。这是复用实现，不扩大对外
   interface。

#### M11 HTTP、进程装配与前端代码拆解

1. prepare 请求必须有 requestId、idempotencyKey、tenantId、expectedVersion。Handler 像 Java
   Controller，只做 JSON/空值校验、调用 Manager、把领域错误翻译成 404/409/503；它不执行 Git。
2. Git 内部失败统一映射为 `503 workspace_preparation_failed`，不会向浏览器返回 URL、文件路径之外
   的 Git 输出或 token。version 过期和状态不允许分别是 `version_conflict` 与
   `invalid_workspace_state`。
3. `cmd/api` 是 composition root，类似 Spring `@Configuration`：读取三个环境变量，构造 HTTP
   verifier、Git Preparer、GitLab WorkspacePreparer、Manager，最后交给 Handler。Base URL、token、
   Workspace root 必须全有或全无，避免“验证能用、准备悄悄不可用”的半配置。
4. 前端 `Workspace` DTO 增加 PREPARING/READY 与可选 path。可选是因为 path 只属于 READY，而不是
   TypeScript 忘记判空。
5. React 本地联合状态增加 `preparing`，原先名为 `ready` 的“HTTP 已加载”状态改名为 `loaded`，避免
   它和领域 READY 混为一谈。这相当于 Java 中区分 `LoadState.LOADED` 与
   `WorkspaceState.READY` 两个不同 enum。
6. REGISTERED 才显示“准备 Workspace”按钮，请求期间显示 clone/worktree 进度并禁用刷新按钮；
   READY 才展示真实工作目录；服务端返回 PREPARING 时只提示稍后刷新，不允许再次发起准备。

#### M11 开发过程记录

1. Manager 第一条测试先引用不存在的 PREPARING、READY、PrepareInput 和构造器，得到编译 RED；补入
   最小状态迁移后，测试在准备期间读到 PREPARING/v2，释放阻塞 adapter 后读到 READY/v3 和规范 path。
2. macOS 测试第一次因 `/var/...` 与 `/private/var/...` 不相等失败；没有硬改测试字符串，而是在
   Manager 构造时规范化根目录，并让测试按真实路径比较。
3. 幂等重放测试第一次得到 `version_conflict`；增加成功准备记录后，同一输入只调用一次 Preparer。
4. 登记重放测试第一次拿到过期 REGISTERED/v1；把登记记录从“保存 Workspace 快照”改成“保存 taskID
   并读取当前资源”后得到 READY/v3。
5. Git 贯穿测试最初因 `NewPreparer` 与 `Input` 不存在而编译失败；最小实现随后真的创建两次 commit、
   bare clone 和 detached worktree，并验证 HEAD 与 base 对象。
6. GitLab 准备测试最初因 `NewWorkspacePreparer` 不存在而失败；实现后验证可信 clone URL、固定 SHA、
   destination 和环境凭据正确传递。另一条测试确认跨 host URL 在 Git 启动前失败关闭。
7. HTTP 测试最初找不到 prepare 构造器与路由；接入后得到 READY/v3/path。失败测试确认只返回稳定
   503，随后 GET 得到 REGISTERED/v3，内部 Git 文本没有出现在响应字段中。
8. 前端测试最初找不到“准备 Workspace”按钮；增加请求与渲染后，断言 READY、path、expectedVersion
   和幂等键全部通过。
9. 自审发现父进程环境仍会把原始 GitLab token 继承给 Git。先扩展假 Git 可执行文件测试，再把环境
   改成白名单；最终专用密码只在 clone/fetch 环境中可见，原始控制平面变量不可见。

#### M11 验证结果

- `go test ./...`、`go test -race ./...`、`go vet ./...`：通过。
- 本地 Git 集成测试使用真实 Git 2.52.0 创建两个 commit，验证 bare clone、base/head commit、
  detached HEAD、askpass 删除和失败目录清理。
- 前端 5 个测试文件、21 条测试全部通过；Vite 生产构建通过。
- 真实 `cmd/api` 在显式清空三项配置后正常启动，`GET /healthz` 返回 200；日志明确提示 Workspace
  操作失败关闭，验证未配置模式没有破坏基础服务。只提供 GitLab Base URL 时进程按预期拒绝启动，
  验证三项配置不会进入半配置状态。
- 未提供企业 GitLab 地址与服务令牌，因此没有对企业 GitLab 执行真实 clone；该联调仍需在受控环境完成。

### M12：读取固定 base/head 的真实 Git Diff

- 状态：完成（2026-09-22）。
- 依据：架构文档 5.3 的 PR Review 节点序列在 `prepareWorkspace` 后执行 `git.getDiff`，再把受控输入
  交给只读 Agent。本步先完成确定性的 diff 读取，不创建尚不能运行的 AgentSession 空壳。
- 新增 `GET /api/v1/tasks/{id}/workspace/diff?tenantId=...`。调用方只能给 Task 和 tenant，不能提交
  path、base SHA 或 head SHA；这些坐标全部来自 READY Workspace。
- 响应包含 `text/x-diff` patch、字节数和 SHA-256，便于下一步 Codex 输入与将来的 Artifact 校验。
- 本步不做：Artifact 持久化、对象存储、AgentSession、Codex exec、Findings schema、成本与评论发布。

#### M12 后端代码拆解

1. `repository.DiffReader` 是应用层 port，可类比 Java interface；`DiffInput` 用具名字段承载 path、
   base 与 head，避免连续三个 `String` 参数传错。`gitworkspace.DiffReader` 是 Git CLI adapter；Go
   不写 `implements`，只要方法签名一致就自动满足接口。
2. `review.Service.Get` 先按 task/tenant 查询 Workspace，并要求状态为 READY 且 path 非空；随后才把
   Workspace 内保存的 path/base/head 交给 adapter。HTTP 请求因此没有机会替换 revision。
3. Git 参数固定为 `-C worktree diff --no-ext-diff --no-textconv --binary base head --`。它直接通过
   `exec.CommandContext` 传参数数组，不经过 shell；`--` 明确结束选项。Java 可类比
   `new ProcessBuilder(List.of(...))`，而不是拼接一条交给 `/bin/sh -c` 的字符串。
4. `--no-ext-diff` 和 `--no-textconv` 禁止仓库配置触发外部 diff/textconv 程序。diff 是纯本地操作，
   使用白名单环境和空凭据，不再接触 GitLab token。
5. `limitedBuffer` 最多保留 1 MiB patch。达到上限后仍继续消费 stdout，并向 Git 报告本次写入成功，
   避免管道塞满使子进程卡死；进程结束后返回 `repository.ErrDiffTooLarge`。
6. `review.Service` 对完整 patch 计算 SHA-256，并连同 task、workspace、base/head、media type 和大小返回。
   当前它还是即时读取结果，不冒充已经持久化且有保留策略的 Artifact。
7. Handler 只解析 tenant、调用 Service 并翻译错误：非 READY 返回 409，超限返回 413，Git 不可用
   返回 503。内部 Git 文本不会进入稳定 HTTP 错误。
8. `cmd/api` 同时创建 Git Workspace Preparer 和 Git Diff Reader。两者使用同一个受控 Git 可执行文件，
   但职责不同：前者负责 clone/worktree，后者只读已经准备好的本地目录。

#### M12 前端代码拆解

1. `WorkspaceDiff` 是 TypeScript DTO，`mediaType` 使用字面量类型 `'text/x-diff'`。它类似 Java record，
   但 TypeScript 类型在运行时会被擦除，当前仍信任平台自己的 JSON 响应。
2. `WorkspaceDetails` 保留原 `WorkspaceState`，另加独立的 `DiffState` 联合类型。两个状态机分开后，
   “正在准备 Workspace”和“正在加载 diff”不会被一个含义模糊的 boolean 混在一起。
3. 只有 READY 才显示“查看固定版本差异”。页面展示媒体类型、字节数、SHA-256 和可滚动 patch；
   REGISTERED/PREPARING 不会发起读取请求。

#### M12 开发过程记录

1. 真实 Git 测试先引用不存在的 `NewDiffReader` 和 `DiffInput`，编译 RED；最小实现后从两次真实 commit
   中读到 `-base` 与 `+head`，完成第一轮 GREEN。
2. 第二条测试让假 Git 输出超过 1 MiB，先因缺少超限错误而 RED；加入有上限但持续排空的 buffer 后
   转为 GREEN。
3. 应用服务测试使用真实 Task Store 和 Workspace Manager，把 Workspace 推到 READY，再确认 adapter
   收到的是 Manager 保存的 path/base/head，并验证响应摘要。
4. HTTP 测试先因 `NewHandlerWithWorkspaceServices` 不存在而 RED；接入 Service 和路由后返回真实 DTO。
   另一条测试确认带内部文本的超限错误只映射为稳定 413。
5. React 测试先找不到“查看固定版本差异”按钮；加入独立 DiffState、请求和 patch 展示后转为 GREEN。

#### M12 验证结果

- `go test ./...`、`go test -race ./...`、`go vet ./...`：通过。
- Git adapter 测试使用真实 Git 2.52.0 验证固定 commit patch；假 Git 验证 1 MiB 上限。
- 前端 5 个测试文件、22 条测试全部通过；Vite 生产构建通过。

### M13：把固定 Git Diff 归档为不可变 Artifact

- 状态：完成（2026-09-22）。
- 依据：架构文档 4 章把 Artifact 定义为带 checksum、task/session/turn/operation 引用、分类与保留
  信息的不可变结果；17.0 要求 Phase 0 跑通 Artifact。本步先稳定最小领域/API 契约，不接对象存储。
- 新增 `POST /api/v1/tasks/{id}/artifacts/diff`、`GET /api/v1/artifacts/{id}` 和
  `GET /api/v1/artifacts/{id}/content`。
- Artifact 元数据包含 task/workspace 归属、`REPOSITORY_DIFF`、`text/x-diff`、SHA-256、字节数与时间。
- 本步不做：数据库、S3/MinIO、保留期、分类策略、加密、Artifact 列表、删除、Session/Turn 关联。

#### M13 Artifact Store 代码拆解

1. `artifact.Store` 继续采用与当前 Task Store 一致的单进程内存实现。它用 `sync.RWMutex` 保护 ID、
   内容和幂等索引；Java 可类比一个用 `ReadWriteLock` 包住的内存 Repository。
2. “不可变”不等于“struct 字段没有 setter”。Go `[]byte` 是共享底层数组的 slice，类似 Java
   `byte[]`：调用方修改原数组会影响 Store。因此 Create 写入时复制一次，GetContent 读取时再复制一次。
3. Store 对完整内容计算 SHA-256，客户端不能自报摘要。Artifact ID 是当前进程内的递增值；这和 Task
   ID 一样只是教学阶段实现，不适合多实例。
4. 幂等键按 tenant 分区。相同 key、归属、类型、媒体类型和内容重放返回原 Artifact；任何一项不同
   返回 `ErrIdempotencyConflict`。比较内容使用 `bytes.Equal`，不只依赖摘要碰撞假设。
5. 元数据查询使用独立 `Get`，不会为了展示卡片而复制最多 1 MiB 的正文；只有 GetContent 才复制内容。

#### M13 Service 与 HTTP 代码拆解

1. `review.Service.Archive` 要求 Workspace 为 READY 且 version 与
   `expectedWorkspaceVersion` 一致。版本过期时在运行 Git 前返回冲突，类似 JPA `@Version` 门禁。
2. 浏览器不上传 patch。Service 再次从 Workspace 读取平台保存的 path/base/head，通过 DiffReader
   生成内容，再交给 Artifact Store；这防止用户把任意文本伪装成平台归档证据。
3. Handler 的 Artifact Store 和 review.Service 共享同一个实例，可类比 Spring composition root
   注入同一个 Repository bean。POST 首次创建返回 201，幂等重放返回 200，并设置 Location。
4. 元数据与正文分开：元数据返回 JSON；正文返回 `text/x-diff`、Content-Length、SHA-256 ETag 和
   `nosniff`。未来换对象存储时可以保留这层公共 API，而不把内存实现泄露给页面。
5. Artifact 读取同时检查 ID 和 tenant。其他 tenant 与不存在都返回相同 404，避免用状态差异枚举资源。

#### M13 前端代码拆解

1. `Artifact` 是新的 TypeScript DTO，type/mediaType 使用字面量类型。它像 Java record，但运行时仍需
   依赖服务端契约；当前没有引入额外 schema 库。
2. `ArtifactState` 与 WorkspaceState、DiffState 分开，包含 idle/archiving/loaded/failed。归档失败
   不会抹掉已经加载成功的 Workspace 和 diff。
3. idempotencyKey 由 Artifact 合同版本、task ID、workspace ID 和 workspace version 确定性派生；
   重试或刷新页面时 requestId 可以变化，但逻辑操作 key 保持不变。Java 中可类比用业务唯一键而不是
   每次请求重新 `UUID.randomUUID()`。
4. 归档成功后页面展示 Artifact ID、类型、摘要和租户范围的内容链接，不把正文再复制进 Artifact DTO。

#### M13 开发过程记录

1. 第一条 Store 测试先因 `NewStore/CreateInput` 不存在而编译 RED；实现两次 defensive copy 后，原
   slice 和读取 slice 的修改都不能改变已归档内容。
2. 幂等测试第一次得到 artifact-1 和 artifact-2；增加 tenant+key 索引后，重放返回 artifact-1/200，
   同 key 换内容则冲突。
3. Service 测试先找不到 `Archive`；实现后确认 Git adapter 只收到 READY Workspace 的可信坐标，
   旧 version 在 Git 调用前被拒绝。
4. HTTP 测试依次经历 POST 404、Location GET 404、content GET 404 三次 RED，再逐个接入路由；最终
   验证 201/200、元数据、正文 headers、内容和稳定归属。
5. React 测试先找不到“归档为 Artifact”按钮；实现后又用第二次点击发现 key 每次变化，最终改为
   从业务身份确定性派生，确认 requestId 变化而 idempotencyKey 复用，并且页面刷新后仍可重放。

#### M13 验证结果

- `go test ./...`、`go test -race ./...`、`go vet ./...`：通过。
- Artifact 测试覆盖写入/读取 defensive copy、tenant 隐藏、幂等重放与冲突。
- HTTP 贯穿测试覆盖 READY diff 归档、Location 元数据、原始内容和幂等 200。
- 前端 5 个测试文件、23 条测试全部通过；Vite 生产构建通过。

### M13.1（第 1 步）：Task 读取按租户隔离

- 状态：完成（2026-09-23）；先处理代码审查报告中的第 1 项，M14 继续暂停。
- 问题：原来的 Task 列表和详情直接读取整个内存 Store；即使其他接口检查 tenant，调用方仍可通过
  Task 的 `goal` 与仓库引用看到别的租户的数据。

#### 代码拆解与 Java 对照

1. `task.Store.Get(id, tenantID)` 在读锁内同时检查 ID 与归属；不存在和归属不符都返回 `false`。
   Java 中可类比 `findByIdAndTenantId`，把租户条件放在 Repository 查询里，而不是查询后由
   Controller 临时过滤。这样今后新增调用者也必须提供 tenant。
2. `task.Store.List(tenantID)` 继续按创建时间倒序遍历，只把匹配的 Task 放入新 slice。
   `make([]Task, 0, len(order))` 保证空结果编码为 `[]`，而不是 JSON `null`。
3. HTTP 列表和详情统一要求查询参数 `tenantId`：缺失返回 400，跨租户详情返回与不存在相同的
   404。创建 Task 的 `Location` 带上 URL 编码后的 tenantId，因此可以直接跟随读取。
4. 前端列表调用复用已有的 `LOCAL_TENANT_ID`，用 `URLSearchParams` 组装查询参数。
   此阶段 tenant 仍由调用方自报；它只是内存数据隔离规则，不等于 Spring Security 中已认证的身份。

#### 测试先行记录

1. 先写 A/B 两租户列表用例，观察到 A 的请求返回了 B 的 Task（RED）；给 Store.List 加 tenant
   过滤后只剩 A（GREEN）。
2. 再写 B 查询 A 的 Task 详情用例，观察到原接口返回 200 和完整目标（RED）；给 Store.Get
   加归属条件后返回 404，A 查询仍为 200（GREEN）。
3. 补缺少 tenantId 的 400 契约。前端测试先指出列表 URL 未带 tenant（RED），更新请求后转绿。
   最后用创建接口测试推动 `Location` 包含 tenantId，并同步更新 README 示例。

#### 验证结果

- `go test ./...`、`go test -race ./...`、`go vet ./...`：通过。
- 前端 5 个测试文件、23 条测试通过；TypeScript 检查与 Vite 生产构建通过。
- 复核变更时确认 `task.Store.Get/List` 的所有调用方均传入 tenant；原有 `docs/code-review-2026-09-23.md`
  作为用户提供的审查报告保留原样。

### M13.1（第 2 步）：前端重试复用幂等键

- 状态：完成（2026-09-23）；处理代码审查报告中的第 2 项。
- 问题：原来四个写按钮每次点击都调用 `crypto.randomUUID()` 生成新幂等键。请求若已在服务端成功、
  但响应在网络中丢失，用户再点一次会被当作新的业务操作；创建 Task 尤其可能产生第二份记录。

#### 代码拆解与 Java 对照

1. `requestId` 每次请求重新生成，用于追踪这一次 HTTP 往返；`idempotencyKey` 标识业务操作，
   重试时必须稳定。Java 中可以把前者类比日志 trace/request ID，后者类比数据库唯一业务键。
2. 创建 Task 尚无 Task ID。`TaskCreator` 用 `useRef` 暂存规范化后表单内容的 JSON 字符串和随机 key：
   相同内容在失败后重试沿用 key；内容变化则换 key；成功后清空。`useRef` 类似 Java 对象的一个
   实例字段，React 重渲染不会清掉，但浏览器刷新或组件卸载会清掉。
3. 准入键是 `task-queue:v1:{taskId}:v{taskVersion}`，登记键是
   `workspace-register:v1:{taskId}`。同一 Task 的同一版本和唯一 Workspace 登记天然有稳定身份。
4. 准备键是 `workspace-prepare:v1:{taskId}:v{workspaceVersion}`。同一次操作携带原版本重放，
   即使服务端已成功也能命中幂等记录；真正准备失败时后端会把 Workspace 恢复为 REGISTERED
   并递增版本，新版本允许发起新的准备。
   这相当于 Java 乐观锁 `@Version` 与业务唯一键共同约束一次操作。
5. 幂等键没有在字符串中重复写 tenant，因为后端的幂等索引已经按 tenant 分区。所有写请求仍提交
   `tenantId`；这不代替身份认证。

#### 测试先行记录与验证

1. Task 创建测试先模拟“第一次请求响应丢失”；旧代码生成两个 key（RED），改为保存同内容的
   待确认操作后，两次 requestId 不同、key 相同（GREEN）。后续测试确认改目标或成功后再次提交
   都会换 key。
2. Task 准入、Workspace 登记与准备都模拟失败后再次点击；原随机 key 测试失败，改为业务坐标后
   转绿。准备测试还确认版本从 1 变为 3 后，新请求使用新的 key。
3. 前端 5 个测试文件共 27 条测试通过；TypeScript 检查与 Vite 构建通过。Go 服务端的原有幂等
   测试与全量测试继续通过。

当前创建 Task 的待确认 key 只保存在组件内存里；刷新页面后无法恢复这次未确认的提交。将来有持久化
草稿或客户端操作记录时，再扩展跨刷新的重试能力；本步先保证页面内重试正确。

### M13.1（第 3 步）：clone 后先探测，缺失才 fetch

- 状态：完成（2026-09-23）；处理代码审查报告中的第 3 项。
- 问题：原实现无论 bare clone 是否已经带回 base/head，都会向远端按裸 SHA fetch。
  有些 Git 服务器不接受这种请求，于是本来已经具备所需 commit 的 Workspace 也可能准备失败。

#### 代码拆解与 Java 对照

1. `Prepare` 仍先 clone。接着创建 `localEnvironment`，用它运行两次 `hasObject`。这个环境里的
   用户名、密码和 `GIT_ASKPASS` 都是空的；虽然临时脚本此时尚未删除，本地探测子进程拿不到 token。
   这类似 Java 为不同 `ProcessBuilder` 分别调用 `environment().put(...)`，而不是让所有子进程继承
   同一份含 secret 的环境。
2. `hasObject` 调用 `git cat-file -e SHA`。退出码 0 表示对象存在，1 表示不存在；其他错误仍然是
   准备失败。Go 的 `errors.As` 从包装后的错误链中找到 `*exec.ExitError`，类似 Java 沿着
   `Throwable.getCause()` 找具体失败原因。`run` 因而用两个 `%w` 同时保留领域错误和进程退出错误。
3. 用原始 SHA 探测，而不是 `SHA^{commit}`：本地 Git 2.52.0 对不存在的原始 SHA 返回 1，
   对不存在的 `SHA^{commit}` 返回 128。把所有 128 都当“缺失”可能掩盖其他 Git 错误。对象存在
   只说明仓库有这串 ID；删除 helper 后仍用 `SHA^{commit}` 验证 base/head 的类型。
4. `missingSHAs` 只装 clone 没带回的 ID；两者相同且缺失时只装一次。列表为空便完全跳过 fetch。
   列表非空时才使用 `credentialEnvironment` 执行 fetch。成功或无需 fetch 后立即删除 helper，
   再严格验证两个 commit 并创建 detached worktree。失败仍由原有 defer 清理本次新建目录。

#### 测试先行记录与验证

1. 先加真实 Git 测试，让包装脚本拒绝任何 fetch。旧代码虽然 clone 已含两个 commit，仍触发
   fetch 并得到退出码 91（RED）；改为先探测后，能完成 detached checkout（GREEN）。
2. 另一个测试让 head SHA 缺失，包装脚本检查本地 `cat-file` 看不到凭据、fetch 能看到凭据和
   临时 helper，并记录 fetch 参数；断言只请求缺失的 head，故意让 fetch 失败后确认目录清理。
   它也发现 `SHA^{commit}` 缺失时的 128 退出码，促使探测改用原始 SHA。
3. `go test ./internal/gitworkspace`、`go test ./...`、`go test -race ./...` 与 `go vet ./...`
   全部通过。全量测试首次在受限沙箱中因 `httptest` 无法绑定本机回环端口而失败；在允许本机监听的
   环境中重跑后通过，这不是代码断言失败。`git diff --check` 也通过。

本步没有把“远端允许按裸 SHA fetch”当作前提。若目标对象不在 clone 结果中且服务器拒绝按 SHA
fetch，Workspace 仍会失败并恢复为 REGISTERED；如何按 GitLab 具体引用取回不可达 commit，
需要结合企业 GitLab 的真实配置单独设计。

### M13.1（第 4 步）：给 HTTP 服务加资源边界

- 状态：完成（2026-09-23）；处理代码审查报告中的第 4 项。
- 问题：原来写接口直接解码不限长的 body，部分字段可无限进入内存索引；服务器只限制读 header，
  收到 SIGTERM 会直接退出，不给同步 Prepare 清理半成品目录的机会。

#### 代码拆解与 Java 对照

1. 五条 POST/PATCH 路由都改用 `decodeRequest`。它先用 `http.MaxBytesReader` 包住 `r.Body`，
   最多实际读取 64 KiB，不能只相信客户端提供的 `Content-Length`。这类似 Java Servlet 的
   request-size limit，但边界放在项目的 JSON 入口，方便一起返回统一错误格式。
2. 第一次 `Decode` 读业务对象；第二次必须读到 `io.EOF`。这样既拒绝一份 body 中塞两份 JSON，
   也会继续消耗尾随空白：否则客户端可在合法 JSON 后追加大量内容，绕过“只读取第一份对象”的限制。
   `*http.MaxBytesError` 转成稳定的 `400 validation_error`，其他 JSON 错误保持原有 `invalid_json`。
3. `goal` 最多 4 KiB，标识类字段最多 256 字节，按 Go 字符串的 UTF-8 字节长度 `len` 判断。
   Java 可类比在 Controller 入参上做 `@Size` 校验，不过这里明确按字节而非字符计数。
   五条写接口都限制 requestId、idempotencyKey、tenantId；创建 Task 还限制 type、repositoryId、goal。
4. `newServer` 集中设置读 header 5 秒、整份请求读取 15 秒、响应写入 5 分钟、空闲连接 60 秒。
   写超时故意比普通 API 长，因为当前 Prepare 仍同步 clone；它只是过渡值，并不能替代将来的
   异步 Activity。Java 中可类比 Tomcat/Jetty 的连接和请求超时配置。
5. 主进程用 `signal.NotifyContext` 接收 SIGINT/SIGTERM，`serveUntilShutdown` 调用
   `Server.Shutdown`：先停接新连接，再最多等 60 秒让在途请求完成。必须等待 Shutdown 返回后
   main 才退出；而且 Shutdown 使用新的 `context.Background()` 派生超时，不能直接传已经因信号
   取消的 context。若宽限耗尽，执行 `Close` 并报错；同步 clone 的彻底恢复仍待后续异步化。

#### 测试先行记录与验证

1. 超过 64 KiB 的 Task 创建请求最初返回 201（RED）；加入 `MaxBytesReader` 后返回 400（GREEN）。
   额外测试覆盖五条写路由的巨大尾随空白，确认第二次 Decode 确实触发上限。
2. `goal` 超过 4 KiB 最初返回 201（RED）；字段校验后返回 400（GREEN），刚好 4 KiB 仍可创建。
   表驱动测试覆盖创建 Task 的五类标识字段，以及其余写接口的超长幂等键。
3. server 配置测试最初找不到 `newServer`（RED）；实现后四种超时均为有限值。
   生命周期测试最初找不到 `serveUntilShutdown`（RED）；实现后模拟信号，证明进行中的请求完成前
   服务不会提前返回。该测试需要本机回环端口，沙箱内无法绑定，允许本机监听后通过。
4. `go test ./...`、`go test -race ./...`、`go vet ./...`、`gofmt -l` 和
   `git diff --check` 全部通过；覆盖率总计 70.2%，高于审查时的 68.3%。

审查报告建议的 `DisallowUnknownFields` 是可选项，本步暂不改变未知字段的兼容行为。
60 秒宽限是有限等待，不保证异常慢的 clone 一定完成并清理；长期方案仍是持久化异步执行与恢复。

### M13.1（第 5 步）：让 5xx 有根因日志，并遮盖 GitLab token

- 状态：完成（2026-09-23）；处理代码审查报告中的第 5 项。
- 问题：原来 Handler 把内部错误翻译成稳定 503/500 后就丢弃，浏览器和服务端日志都找不到
  GitLab、Git 或 diff 失败的具体原因。

#### 代码拆解与 Java 对照

1. `cmd/api` 把默认 `slog` 配成 stderr JSON；`httpapi.logServerError` 在每个 503/500 分支写
   `requestId`、`tenantId`、`taskId`、`errorCode` 和 `cause`。它类似 Java 的 SLF4J 结构化参数加
   MDC：运维按 requestId 找到那一次请求，再读服务端原因，而不是从浏览器的稳定文案猜原因。
2. 写接口从已校验的 body 取 `requestId`；GET diff 没有 body，可以从 `X-Request-ID` 请求头取，
   没传时日志字段为空。日志坐标超过 256 字节时写 `[overlong]`，避免异常长 header/query 撑大日志。
   创建 Task 失败时尚无 Task ID，故 `taskId` 为空。没有在本步新增全链路追踪或成功请求日志。
3. 错误文本用 `%v` 保留已有包装中的上下文，HTTP 响应仍走原有 `writeError`，不回显内部原因。
   代码不主动记录 body、幂等键或 clone URL；但 Git 错误文本本身可能包含无凭据的仓库地址。
   GitLab token 仍只在 API header、受控 Git 环境
   与 askpass 中，不进入 URL/argv。审查报告把 Git stderr 视为天然安全，但远端文本理论上仍可能
   回显敏感值；所以日志入口额外用当前 `AGENT_PLATFORM_GITLAB_TOKEN` 对所有记录字段做直接替换。
   这是兜底而非任意秘密扫描，日志仍应只对运维开放。
4. 日志和响应是两条输出通道：日志提供诊断线索，响应保持稳定协议。Java 可类比 Controller
   捕获 Service 异常后，一边 `logger.error` 记录 cause 和 MDC，一边返回固定的错误 DTO。

#### 测试先行记录与验证

1. 首先让 Prepare adapter 返回“clone 失败 → 远端拒绝 commit”。旧代码日志为空（RED）；
   加入 `slog` 后日志含关联字段与根因，响应仍是原来的 `503 workspace_preparation_failed`（GREEN）。
2. 再模拟外部错误文本意外回显 GitLab token；旧日志泄露了测试 token（RED），加入直接遮盖后，
   日志保留 `remote echoed [REDACTED]`，HTTP 响应和日志都不含 token（GREEN）。
3. 补充测试覆盖 GitLab 验证失败的 503/意外 500，以及 GET diff 的 503 与请求头 requestId。
   `go test ./...`、`go test -race ./...`、`go vet ./...` 与格式检查均通过；总覆盖率 70.4%，
   高于审查报告的 68.3% 基线。

当前只记录 503/500，不是完整的请求审计或分布式追踪；若将来凭据改由 Broker 提供，日志遮盖也必须
同步改为使用当次凭据，而不能继续只读进程环境变量。

### M13.1（第 6 步）：校验 GitLab `repositoryId` 的格式

- 状态：完成（2026-09-23）；处理代码审查报告中的第 6 项，M14 仍暂停。
- 问题：过去只要求 `repositoryId` 非空；单段 `project-7`、`..` 或带 `?` 的字符串都能创建 Task，
  到登记 Workspace 时才可能作为畸形 GitLab API 路径失败。
- 依据：[GitLab Projects API](https://docs.gitlab.com/api/projects/) 接受数字 project ID，或经过 URL
  编码的项目完整路径。平台调用方填写原始的 `namespace/project`，后续 connector 负责编码。

#### 代码拆解与 Java 对照

1. `createTask` 先去除输入首尾空白，再沿用已有的 256 字节上限、必填和 provider 校验，随后调用
   `isValidGitLabRepositoryID`。返回 `false` 就立刻返回统一的 `400 validation_error`，不会把畸形值存进
   Task。Java 中相当于在 Controller 入参上先做格式校验，再调用 Service；不要等远端 GitLab 的
   404 来充当本地输入校验。
2. 辅助函数先拒绝空值、超长值和包含 `..` 的值；接着逐字节判断是否全为 ASCII 数字。若全是数字，
   直接作为 project ID 接受；不是数字，则必须包含 `/`，并逐段检查 `namespace/project`。
   `strings.Split` 会保留开头、结尾和相邻 `/` 造成的空段，所以这些情况都能明确拒绝。Java 的
   `String.split("/", -1)` 才有相同的“保留末尾空段”效果；默认 `split("/")` 会丢掉末尾空段。
3. 每段只放行 ASCII 字母、数字、`.`、`_`、`-`，并拒绝单独的 `.` 段。按字节比较让中文、`%2F`、
   `?` 等自动落入拒绝分支；`len(id)` 也是 UTF-8 字节数，不是 Java `String.length()` 的字符单位。
   这是刻意收紧的输入规则，不表示所有匹配白名单的项目一定存在。
4. 校验与编码、验证分工不同：HTTP 入口检查“形状”；`connector/gitlab` 继续用 `url.PathEscape`
   编码路径，避免原始 `/` 改变 API 路由；GitLab 项目及 commit API 再检查仓库和 SHA 是否真实可读。
   Java 可类比 `@Pattern` 校验、URI builder 编码、Repository/远端服务查询三个独立步骤。

#### 测试先行记录与验证

1. 先写 HTTP 表驱动测试，旧实现把单段名称、`..`、空路径段、预编码 `%2F` 等当成合法输入并返回
   201（RED）；加白名单后它们返回稳定的 400，数字 ID、普通/多级 namespace 路径和恰好
   256 字节的路径仍返回 201（GREEN）。原有超长字段测试继续覆盖 257 字节的拒绝边界。
2. 原来的共享测试仓库 `project-7` 不再符合规则，已改为 `platform/project-7`；前端创建请求的测试
   样例和 README 创建示例也同步调整。幂等键里保留的 `project-7` 只是普通业务字符串，不参与
   `repositoryId` 校验。
3. `go test ./...`、`go test -race ./...`、`go vet ./...`、前端 27 个测试、前端生产构建和格式检查
   均通过；Go 总语句覆盖率 71.0%，高于审查时的 68.3%。全量 Go 测试需要允许本机回环端口，
   沙箱内的监听限制并非测试失败。

这是创建接口的输入约束变更：之前传单段 `project-7` 的客户端须改传数字 ID 或完整
`namespace/project`。平台目前仍由调用方自报租户，格式校验不提供认证、授权或项目访问控制。

### M13.1（第 7 步）：让 Git 路径真正受 root 约束，防止空 Workspace 回写

- 状态：完成（2026-09-23）；处理审查清单第 7 项中的 7.1、7.2，M14 仍暂停。
- 边界：本步不处理审查报告中其他“低”项，也不新增 Workspace 删除 API。

#### 代码拆解与 Java 对照

1. `cmd/api` 将同一个 `AGENT_PLATFORM_WORKSPACE_ROOT` 传给 Workspace Manager、Git Preparer 和
   DiffReader。两个 Git 适配器的构造函数现在都要求绝对且非文件系统根目录的 root，并保存它。
   原先只有 Manager 知道 root，底层适配器只能猜“目录名字像不像 Workspace”；Java 可类比
   Spring 配置把同一 `workspaceRoot` 注入写服务和读服务，而不是每个服务自己猜目录范围。
2. `validateDestination(root, destination)` 先检查绝对路径、末段 `worktree` 和上一段
   `workspace-...`，再对配置 root 与 workspace 目录的父目录调用 `filepath.EvalSymlinks`。
   两者必须是同一个真实目录，才允许继续执行 Git。这里比普通字符串 `HasPrefix` 更严格：
   `/data/workspaces-extra` 虽然以 `/data/workspaces` 开头，却不是它的子目录；而 Manager
   实际只生成 root 的直接子目录。Java 可类比先 `Path.toRealPath()`，再比较 `parent.equals(root)`。
   Prepare 在 `os.Mkdir` 和 clone 之前检查，因此坏路径不会启动 Git。
3. DiffReader 还会解析完整 worktree 路径，要求解析结果仍是该 root 下预期的
   `workspace-.../worktree`。只看父目录不够：准备成功后，如果目录被符号链接替换，
   `git -C` 会跟随链接读到 root 外。Java 的 `Path.toRealPath()` 同样用于辨别“看起来在里面”
   与“实际指向哪里”。这属于路径边界检查，不替代操作系统权限隔离。
4. `Manager.Prepare` 在外部准备器返回后，会重新加锁并从 `byTask` 读取当前 Workspace。
   Go 的 `value, ok := map[key]` 里的 `ok` 表示键是否存在；只写 `value := map[key]` 时，
   缺键会得到结构体零值。失败和成功两条分支现在都先检查 `ok`，缺键就返回
   `ErrWorkspaceNotFound`，不会把空 Workspace 当成真实记录写回。Java 可类比
   `Map.get(key)` 返回 `null` 后，必须先判断存在，不能继续修改并 `put` 回去。

#### 测试先行记录与验证

1. 先写 Preparer 的越界目录测试（RED：构造函数还不接收 root）；注入 root 并在运行 Git 前
   比对真实父目录后转绿。测试特意使用名字相似的兄弟目录 `workspaces-extra`，证明不能靠
   字符串前缀判断。
2. 再写 DiffReader 的越界读取测试（RED：读适配器还不接收 root），随后加相同边界校验转绿。
   接着用符号链接把 `root/workspace-1` 指向外部，旧读取器真的启动了 Git（RED）；增加完整
   worktree 的真实路径核对后，不再启动 Git（GREEN）。
3. 最后用准备器回调模拟 Workspace 在外部操作期间被移除。旧失败分支把零值写回，旧成功分支
   甚至返回成功（两次 RED）；两处 map 读取加 `ok` 后，均返回 `ErrWorkspaceNotFound` 且不重建记录。
4. `go test ./...`、`go test -race ./...`、`go vet ./...`、前端 27 个测试、前端构建与格式检查
   均通过；Go 总语句覆盖率 71.4%，高于审查时的 68.3%。

目前没有 Workspace 删除 API，上述 map 缺键测试是在同包测试中模拟未来删除时的交错执行；
它守住“不写回零值”的局部不变量，不代表已经设计好删除后的目录清理与幂等记录清理。
路径校验和执行 Git 之间也不是原子操作，部署时仍须限制谁能修改 Workspace root。

### M13.1（第 8 步）：补齐 Task 时间线中的 Workspace 与 Artifact 事实

- 状态：完成（2026-09-23）；处理审查清单第 8 项，M14 仍暂停。
- 问题：旧时间线只显示 Task 创建和准入。Workspace 已登记、正在准备、准备成功/失败，
  以及 diff 已归档为 Artifact，调用方都看不到。
- 范围：只扩展当前单进程内存事件流和 React 展示；不接入数据库、MQ、Outbox、持久化审计或自动轮询。

#### 先确定事件契约

事件的外壳仍保留 `eventId`、`eventType`、`tenantId`、`taskId`、`sequence`、
`correlationId`、`causationId` 和 `occurredAt`。`sequence` 只在一个 Task 内递增，
`causationId` 是触发这次事实的 requestId；同一次 Prepare 的“开始”和“完成/失败”使用同一个 requestId。
payload 改成由 `eventType` 决定的分支：

| 事件类型 | payload 分支 | 记录什么 |
| --- | --- | --- |
| `task.created`、`task.queued` | `task` | Task 的 `status`、`version` |
| `workspace.registered`、`workspace.preparing`、`workspace.ready`、`workspace.preparation_failed` | `workspace` | `workspaceId`、变化后的 `state`、`version` |
| `artifact.created` | `artifact` | `artifactId`、`workspaceId`、类型、媒体类型、SHA-256、字节数；不含正文 |

原来的 `payload.status/version` 是扁平结构；现在 Task 版本放在 `payload.task.version`，
Workspace 版本放在 `payload.workspace.version`，避免同名字段被误解。因为 JSON 结构发生不兼容变化，
`schemaVersion` 从 `1.0` 升为 `2.0`，README 与 React 类型也同步迁移。这个 Phase 0 选择用一次
显式破坏性升级换取清晰契约；旧 1.0 客户端不能直接读取 2.0 payload。

#### 代码拆解与 Java 对照

1. `task.EventPayload` 的 `Task`、`Workspace`、`Artifact` 三个指针是三种可选分支；当前事件
   只设置其中一个。Java 可类比 `sealed interface EventPayload` 下的三个 record，但 Go 在这里用
   `eventType` + 可选 JSON 字段表达。`task.Store.AppendEvent` 为同一 Task 分配下一条 sequence，
   并生成统一的事件外壳；创建/准入原来的事件也改用 2.0 分支。
2. 指针让 payload 能省略无关分支，但 Go 的 slice 拷贝不会自动复制指针指向的对象。因此
   `appendEventLocked` 写入时复制 payload，`ListEvents` 读出时再复制，防止调用方修改一份返回值
   就悄悄改掉已记录事实。Java 可类比返回不可变 record，或在 Repository 边界做 defensive copy。
3. `workspace.Manager` 在登记成功、进入 `PREPARING`、进入 `READY`、准备失败后恢复为
   `REGISTERED` 时，分别追加事件。失败事件记录恢复后的 version 3，而不是失败前的 version 2；
   它只含稳定状态，不含 Git stderr、token 或内部异常文本。准备请求的两条事件共享 causationId。
4. `review.Service.Archive` 只有在 `artifact.Store.Create` 返回 `Created: true` 时才追加
   `artifact.created`，幂等重放不再造一条“新建”事实。它写 Artifact 的摘要和归属，不写最大
   1 MiB 的 diff 正文。Java 可类比 Application Service 在归档成功后发布领域事实；
   此处只是内存同步调用，并非真正的事务 Outbox。
5. React 的 `TaskEvent` 是按 `eventType` 区分的 TypeScript 联合类型，时间线用 `switch`
   显示每种 payload。Java 可类比对 sealed hierarchy 做 `switch` 模式匹配；编译器会帮助发现
   新事件类型尚未处理的 UI 分支。

#### 测试先行记录与验证

1. 先把创建事件测试改为期待 `2.0` 与 `payload.task`，旧实现返回 1.0/扁平 payload（RED）；
   调整 Task Event 后变绿。原有准入事件测试也同步迁移。
2. 逐条增加 HTTP 行为测试：登记后事件数仍为 2、Prepare 成功后仍为 3、Prepare 失败后仍为 4、
   Artifact 归档后仍为 5（每条先 RED）。实现后分别得到第 3、4/5、5、6 条正确事件。
   测试还确认失败事件不包含 Git 内部诊断，归档事件不包含 patch 正文。
3. 登记、准备成功、归档的幂等重放均保持原事件数；Store 测试确认修改写入用的 payload
   指针或读出的事件副本，不会改掉已保存记录。
4. 前端先用 2.0 事件样例得到空的状态/版本（RED），再按事件类别渲染后变绿。
   `go test ./...`、`go test -race ./...`、`go vet ./...`、前端 27 个测试与构建均通过；
   Go 总语句覆盖率 77.0%，高于审查时的 68.3%。

当前 Workspace、Artifact 状态和 Task 事件保存在不同的内存 Store 中，虽然调用是同步的，
却没有跨 Store 的原子事务。进程重启后状态与事件都会丢失；未来落数据库时，要让业务状态与
Outbox 记录同事务提交，再异步发布到 Event Bus，才能成为可靠、可恢复的审计链。

### M13.1（第 9 步）：由平台固定 master 快照并计算 Review Base

- 状态：完成（2026-09-23）；M14 仍暂停。
- 问题：最初只把“调用方提供的 `baseSha` 必须是 merge-base”写成契约。它解释了两点 diff 的
  前置条件，却不能阻止调用方误传目标分支 tip。用户提出更准确的规则：Phase 0 固定以
  `master` 为目标，由服务端自己计算共同祖先。
- 决定：创建 Task 时先读取 `master` 当前提交 `T`，再以固定的 `T` 和调用方提供的 head `H`
  向 GitLab 查询 `B = merge-base(T, H)`。Task 保存 `targetBranch=master`、`targetSha=T`、
  `baseSha=B`、`headSha=H`，Workspace 登记时复制它们；后续仍以 `git diff B H` 生成补丁。

#### 为什么先固定 T，再计算 B

假设 `A` 分叉成 `A→B`（master）和 `A→C→D→E`（待评审分支），则
`merge-base(B, E)=A`，`git diff A E` 只显示待评审分支到 E 的最终净变化，不混入 B。
这与该时刻的三点差异思路相同，但不能每次读取时重新解析会移动的 `master`：同一个 Task 的
预览和归档可能得到不同补丁。因此读取分支后，后续 merge-base 请求使用固定的 SHA `T`，
而不是再传分支名 `master`。GitLab 的[分支接口](https://docs.gitlab.com/api/branches/)提供
`commit.id`，[merge-base 接口](https://docs.gitlab.com/api/repositories/)接收两个 refs 并返回
共同祖先的 `id`。

#### 代码拆解与 Java 对照

1. `repository.ReferenceResolver` 是应用层端口，类似 Java interface；GitLab `Verifier.Resolve`
   是 adapter，读取分支、确认 head、调用 merge-base API，并校验返回的 SHA 是完整 Git 对象 ID。API token
   仍只进请求 header，不随重定向外传，响应体也有大小上限。
2. HTTP 创建接口只要求调用方给仓库与 `headSha`，先检查幂等记录，再访问 GitLab。Java 可类比
   Application Service 先查操作记录、再调用外部服务；同一 key 的重试即使碰上 master 前进，
   也返回第一次固定的 T/B/H。并发首创最终由 `task.Store.Create` 在锁内选定赢家的快照。
3. `task.RepositoryReference` 与 `workspace.Workspace` 显式保存 target/base/head，避免只存一个
   会移动的分支名。React 创建表单不再让用户填写 Base SHA，只说明平台将以 master 计算它；
   Workspace 详情同时展示固定的目标分支和目标提交，方便核对这次任务的基线来源。
   旧客户端暂时仍可传格式正确的 `baseSha`，但服务端忽略该值并返回自己计算的结果。
4. `gitworkspace.DiffReader` 仍执行两点 diff；这里的 B 已由平台计算，不再是人工前置条件。
   类比 Java 中先生成不可变的 `ReviewInput(T, B, H)`，再把它交给只读 DiffService。

#### 测试先行与边界

- GitLab 解析测试先因 `Resolve` 不存在而编译 RED；实现后验证先读 master、确认 head，
  再以固定 T/H 求 merge-base。异常测试覆盖缺少 master/head、无共同祖先、GitLab 返回畸形 SHA，以及错误不回显
  响应正文。
- HTTP 测试先因缺少 `baseSha` 返回 400（RED），实现后确认响应固定 T/B/H；另测幂等回放
  不再访问解析器。Store 测试模拟两个首创请求分别看到不同 target，确认只保存首个快照；
  无 GitLab 配置时创建返回 503 并失败关闭。
- Workspace 测试先看不到 target 快照（RED），实现后确认登记结果继承 T；前端先要求不再出现
  Base SHA 输入并说明平台计算（RED），删去输入后变绿。详情页展示 T 的断言也先失败，补齐
  Task/Workspace 两处展示后变绿。
- 真实 Git 分叉测试继续证明用 merge-base 做两点 diff 不会混入 target 独有改动。当前只固定
  `master`，尚未通过 MR 身份信息确认某个 MR 的实际目标分支；非 master 目标的 MR 不在此契约内。
- 收尾验证：`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、
  `node --run test`（28 个前端测试）和 `node --run build` 全部通过。

### M13.1（第 10 步）：隔离前端旧请求并收紧 diff 预览、日志遮盖

- 状态：完成（2026-09-24）；M14 仍暂停。
- 问题：切换 Task 后，旧 Task 的 Workspace diff 或事件请求可能晚于新 Task 返回，覆盖当前页面；
  前端会把最多 1 MiB 的补丁全文放进 `<pre>`；日志遮盖每次从进程环境读取 token，可能与实际
  配置给 GitLab verifier 的 token 不一致。

#### 代码拆解与 Java 对照

1. `WorkspaceDetails` 和 `TaskEventTimeline` 为当前 Task 持有 `AbortController`。Task ID 或租户
   变化、组件卸载时调用 `abort()`，并把 `signal` 交给 `fetch`。每次 `await` 后还检查
   `signal.aborted`，因为测试替身或某些异步阶段可能仍交付一个迟到的结果。Java 可类比给一次
   页面操作绑定 `CancellationToken`：取消不仅通知 I/O，也在写 UI 状态前确认“这还是当前操作”。
2. Task 切换时清空旧 Workspace、diff、Artifact 和事件状态。新 Task 必须主动加载自己的数据；
   旧请求不能借新 Task 的标题显示旧内容。POST 请求也携带 signal；取消客户端等待不保证服务端
   撤销已提交的操作，所以登记、准备、归档仍依赖原有幂等键处理重试。
3. diff 的 JSON 响应仍保留完整的受限补丁和完整大小，但 `<pre>` 最多渲染前 65,536 个字符，
   超限时明确提示“仅预览”；归档继续由服务端读取完整 diff。Java 可类比列表页只展示摘要，
   不把整份大对象展开成 DOM 节点。
4. API 构造函数现在显式接收 GitLab token，并保存在 handler 内专供 `logServerError` 遮盖；
   `main.go` 把同一个 token 传给 verifier 与 handler。Java 可类比构造器注入同一份配置，避免
   日志工具自己去读静态环境变量而与实际依赖脱节。

#### 测试先行记录与验证

- 两个“旧请求晚到”的前端测试先失败：事件页停在旧 Task 的加载态，diff 页被旧补丁覆盖。
  加入取消、状态重置和迟到结果检查后变绿；测试用会无视取消的 `fetch` 替身，验证额外检查确实有效。
- 大 diff 测试先看到 70,004 个字符全部进入 `<pre>`，随后验证预览上限、截断提示和完整大小。
- Go 日志测试把环境变量设为一份过期 token，却把另一份真实 token 传给 handler；先因构造器
  参数缺失而失败，改为显式注入后确认响应和日志都没有真实 token。
- `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、前端 31 个测试和构建通过。

### M13.1（第 11–12 步）：新机恢复 CI 与 Go lint

- 状态：按交接说明恢复并验证（2026-10-04）；M14 继续暂停。
- 迁移核对：交接称原机已有 `88bbaa8`（CI）与 `bfc41b5`（Go lint），但本机 HEAD 和实时查询的
  远端 `main` 都是 `607dbd5`，本地也没有这两个提交对象。用户确认暂时无法从原机推送，授权按
  交接恢复配置。因此此处记录本次恢复结果，不冒充原提交或原机开发记录。
- 先使用 Go 1.27.1、Node 22.23.3 复跑基线：Go 普通/race 测试、vet、前端 31 条测试和构建通过。
- 恢复 `.github/workflows/ci.yml`，在 main push 与 PR 分别执行后端测试/vet、Go lint 和前端检查。
  Go 版本取自 go.mod；当前无第三方 Go 模块和 go.sum，setup-go 暂不开模块缓存。
- 恢复 `backend/.golangci.yml`，固定配置版本 2、`standard` 规则和 5 分钟时限，CI 使用 v2.13.2。

#### Go lint 的 RED → GREEN

首次检查显示 9 处告警；处理后又显示 2 处同文本的 Close 告警，总计处理 11 处。最后用
`--max-same-issues=0 --max-issues-per-linter=0` 核对完整结果为 `0 issues`，避免默认输出限额隐藏问题。

1. `errcheck` 要求调用方明确对待返回的 error。7 处只读请求/响应关闭或测试 listener 清理，改为
   `defer func() { _ = ...Close() }()`，表明有意忽略清理错误，并继续执行关闭。这里没有文件写入
   或 flush，关闭错误不替代读取/解析结果；listener 已由 Shutdown 关闭时允许兜底重复关闭。
   Java 可类比 finally 中清理资源，同时保留主要操作的结果；不能把此约定推广到文件写入。
2. `staticcheck` 的 3 处错误文案改为小写开头；公开 HTTP 错误码仍由 Handler 映射。
3. SHA 字符校验拆为 `isDigit/isLowerHex/isUpperHex`，然后检查三者都为 false；与原判断等价，
   类似 Java 中给复杂布尔表达式起局部变量名，让读者直接看懂条件。

### M13.1（第 13 步）：前端 ESLint 与 CI 门禁

- 状态：完成（2026-10-04）；接续交接中的 ESLint 小切片，M14 仍暂停。
- 新增 `frontend/eslint.config.js`、`lint` 和独立 `typecheck` 脚本；CI 前端 job 依次运行
  `npm ci`、lint、typecheck、行为测试和构建。
- 新依赖均固定精确版本并写入 lockfile：ESLint 10.12.0、@eslint/js 10.0.1、typescript-eslint
  8.71.0、react-hooks 7.1.1、react-refresh 0.5.7、globals 17.13.0。现有依赖版本未改变。
- 选型依据：[ESLint 的 Node 要求](https://eslint.org/docs/latest/use/getting-started)、
  [typescript-eslint 兼容范围](https://typescript-eslint.io/users/dependency-versions/)、
  [官方 Hooks 插件](https://github.com/facebook/react/tree/main/packages/eslint-plugin-react-hooks)、
  [Fast Refresh 的 Vite 配置](https://github.com/ArnaudBarre/eslint-plugin-react-refresh)。

#### 配置拆解与 Java 对照

1. flat config 是按文件匹配叠加的配置数组：所有 JS/TS/TSX 使用 JS 与 TS 推荐规则；src 声明浏览器
   globals；配置文件声明 Node globals。类似按模块分别配置 Checkstyle，避免混淆执行环境。
2. 核心 Hooks 规则检查“Hook 只能按固定顺序调用”和“Effect 依赖必须完整”；两者都是 error。
   本切片没有启用整套 React Compiler 规则。官方插件把核心 Hooks 与 Compiler 规则分组，本项目
   尚未配置 Compiler，其规则采纳另行评估。本步也没有引入格式化或需要类型信息的 lint 规则。
3. TSX 使用插件的 `reactRefresh.configs.vite()`，检查组件导出是否符合热更新要求。
4. 测试 fetch 替身需要保留签名，后续才能对 `mock.calls` 的请求参数做类型检查。仅测试文件允许
   未使用的形参以 `_` 开头，并设置 `args: 'all'`；未使用局部变量、普通形参和源码里的参数仍报错。
   Java 可类比测试 callback 必须保留接口签名，即使某次用例只检查第二个参数。
5. 只忽略 dist、coverage、.vitest 生成物，node_modules 由 ESLint 默认忽略；源码、测试、Vite 配置
   和 ESLint 配置都参与检查。`--max-warnings 0` 类似构建中的 warnings-as-errors。

#### RED → GREEN 与验证

- 初次 lint 得到 8 条测试替身的未使用形参错误（RED）；限定测试 `_` 参数约定后，另检出一个
  未标记的 `input`，将其改名为 `_input` 后 lint 转绿。没有发现需要修改产品行为的缺陷，因此
  本步使用检查器的真实失败输出，复跑已有行为测试，没有新增重复实现的测试。
- 用 ESLint API 对 10 个临时代码样例验证：条件调用 Hook、Effect 遗漏依赖、显式 any、混合导出、
  未使用参数和局部变量均触发对应规则；合法浏览器/Node 代码与测试占位参数通过。
  另外确认四类生成物路径被忽略；这些样例没有写入仓库源码。
- 从更新后的 lockfile 执行 `npm ci` 后，lint、typecheck、31 条前端测试和生产构建通过。
- 恢复后的 Go config verify、lint（0 issues）、普通/race 测试、vet、gofmt 和 diff 检查通过。
- CI 工作流通过本地 actionlint 检查；未提交、未推送，GitHub 托管 runner 尚未验证。

#### 新机工具与下一小步

本机全局环境最初没有 Go，Node 为 23.10.0。迁移恢复时从官方发行包下载并校验 SHA-256，工具先解压在
`/private/tmp/agent-platform-tools`。随后按用户要求，通过 Homebrew 正式安装 Go 1.27.1（2026-10-04），
命令位于 `/opt/homebrew/bin/go`；新开的 zsh 终端可直接使用，后端全部测试通过。
Node 22 和 golangci-lint 仍在临时目录，当前终端可这样继续验证（临时目录清理后需重装这两项）：

```bash
export PATH="/private/tmp/agent-platform-tools/node-v22.23.3-darwin-arm64/bin:/private/tmp/agent-platform-tools/golangci-lint-2.13.2-darwin-arm64:$PATH"
export GOCACHE=/private/tmp/agent-platform-go-cache
export GOPATH=/private/tmp/agent-platform-gopath
export GOLANGCI_LINT_CACHE=/private/tmp/agent-platform-golangci-cache
```

当时的下一小步是按交接审查清单 #11 检视 `handler_test.go` 的职责划分及边界/并发覆盖，见下方
第 14 步。历史审查报告未出现在本机仓库，具体缺口以当前测试和代码为准；不自动恢复 M14。

### M13.1（第 14 步）：拆分 HTTP 测试并补齐 Task 并发、字节边界覆盖

- 状态：完成（2026-10-04）；接续审查清单 #11 的一个测试小切片，M14 继续暂停。
- 本步整理测试和补充已有契约的回归约束，产品代码无需修改。
- 原 `handler_test.go` 有 2,753 行。按健康检查、Task 创建/读取/迁移/事件、请求校验、Workspace
  登记/准备、diff、Artifact 和日志分到对应测试文件，通用 fixture 移到 `test_helpers_test.go`。
  拆分后最大文件为 `workspace_registration_test.go`，511 行。

#### 为什么这样拆

Go 会把同一目录、同一 package 的 `*_test.go` 一起编译。移动测试不要求新增接口或修改测试名称；
可以按 HTTP 职责查找文件，同时保留共享 fixture。Java 可类比将一个巨大 ControllerTest 拆成
TaskCreateTest、TaskTransitionTest 等测试类，把公共准备函数集中到测试辅助类。

拆分时直接按 Go AST 提取原声明，重新生成必要 imports。先确认原有 66 个 HTTP 顶层测试名称
没有增减，再运行 HTTP 测试，全部通过；没有借移动文件改写旧断言。

#### 逐条补充行为约束

1. **并发创建仍只固定一个 Task 快照**：两个同租户、同幂等键、相同内容的 POST 都先进入外部仓库
   解析；测试通过 channel 放行，让两次解析返回不同的 master/base 快照。响应必须恰为一份 201
   和一份 200，并返回完全相同的 Task；列表只有一个 Task，只有一条创建事件，causationId 属于
   首创请求。这里约束公开结果，不要求外部解析只能调用一次。
2. **并发准入只递增一次版本、追加一次事件**：16 个 PATCH 都携带 `expectedVersion=1`。
   同幂等键时全部返回 200/v2；不同键时只有一个 200，另外 15 个返回 `409 version_conflict`。
   最终 GET 是 QUEUED/v2，时间线只有 created 与 queued 两条事实。Java 可类比多个并发请求
   更新同一条带 `@Version` 的记录，业务重试通过幂等键识别，竞争操作通过版本号拒绝。
3. **中文目标按 UTF-8 字节限长**：1,365 个“中”加一个 ASCII 字符正好 4,096 字节，创建成功且原文
   完整保存；再加一字节返回 400/validation_error，列表仍为空。该约束避免把 Java/JavaScript
   的字符数与 Go 的 UTF-8 字节数混用，也确认拒绝请求不会留下 Task。

新增用例逐条运行，首次就通过：现有实现已满足这些并发与长度约束，因此没有声称发现缺陷，也
没有人为制造 RED。两个并发测试使用 channel 确定起跑/交错点，以 5 秒 context deadline 防止
挂起；没有用 sleep 猜执行顺序，goroutine 只交付结果，断言在测试主 goroutine 中执行。

#### 验证与远端状态

- 原 `handler_test.go` 范围内的顶层测试从 66 个增至 69 个，原有名称全部保留；新增并发测试在
  race 下连续 20 次通过。其他已有 HTTP 测试文件保留原样。
- Go 全套普通测试、race 测试、vet、golangci-lint（0 issues）、gofmt 和 `git diff --check` 通过。
- 用户已审阅的上一步变更已提交为 `79ee208` 并推送到 `origin/main`。
  [首次 CI 运行](https://github.com/chenhaitao888/agent-platform/actions/runs/37206467792) 的三个 job
  都未开始执行，GitHub check annotations 明确报告账户因账单问题被锁定。因此远端 CI 尚未完成
  代码验证，需要解除账户限制后重跑；本地绿色不能替代该结果。
- 本步测试和手册变更经用户审阅后，已提交为 `e06366c` 并推送到 `origin/main`。

下一小步可继续审查 Workspace Prepare 的并发请求与失败重试边界，一次选一条公共 HTTP 行为。
当前回归只覆盖单进程内存实现，不证明跨进程幂等、数据库 CAS 或事务 Outbox 的正确性。

### M13.1（第 15 步）：在 Workspace 准备开始前绑定幂等键

- 状态：完成（2026-10-04）；继续审查清单 #11 的 Workspace Prepare 并发与失败重试边界。
- 本步修复一个输入绑定缺口，保持当前同步准备方式；M14 继续暂停。

#### 从公共 HTTP 行为复现缺陷

原实现仅在准备成功后保存 `preparations` 记录。第一个请求进入 PREPARING 并释放锁执行 Git 时，
相同租户的第二个 Task 仍能用同一准备 key 进入准备，两个成功结果还可能覆盖同一幂等记录。
失败路径也没有保留绑定，因此同一 key 可以改用新 `expectedVersion` 开始另一轮操作。
这不符合 README 已有的“复用 key 改变 Task 或版本返回 idempotency_conflict”约定。

先新增两条 HTTP 回归，再修改产品代码，实际得到以下 RED：

- 首请求被 channel 阻塞在准备器中；同租户另一 Task 复用 key，预期 `409 idempotency_conflict`，
  实际返回 `200 READY`。另一个租户的用例还发现：首请求复用 key 改版本，返回了状态错误而非
  幂等输入冲突。
- 准备失败后 Workspace 已恢复为 REGISTERED/v3；复用原 key 改成版本 3，预期幂等输入冲突，
  实际再次准备成功，返回 `200 READY/v5`。

#### 最小修复及 Java 类比

Manager 在同一个互斥锁内完成版本/状态校验、变为 PREPARING，以及按 `(tenantId, key)` 绑定
`(taskId, expectedVersion)`，随后才释放锁调用准备器。失败保留该绑定；成功额外保存 Workspace
快照。`preparationRecord.result` 改为 `*Workspace`：nil 表示已绑定但没有成功快照，只有非 nil
才能重放成功响应，避免把“已有记录”误当作“已有成功结果”。返回时复制快照值。

Java 可类比先在 `synchronized` 临界区里占用请求键并记录输入，再到锁外执行耗时的
`ProcessBuilder`。失败不允许把原请求键改绑定到新输入；获取新版本后，生成新键开始新一轮操作。
这里的锁和记录仍只在单个 Go 进程内生效。

| 场景 | HTTP 结果 | 准备器与时间线 |
| --- | --- | --- |
| 同租户复用已绑定 key，改变 Task 或版本 | 409 idempotency_conflict | 不启动准备、不追加事件 |
| 准备中或失败后，用原 key 和原版本重放 | 409 version_conflict | 不启动准备、不追加事件 |
| PREPARING 时用新 key 和当前版本尝试准备 | 409 invalid_workspace_state | 不启动准备、不追加事件 |
| 不同租户使用相同 key | 各自准备成功 | 各自维护状态和事件 |
| 失败后用新 key 和最新版本重试 | 200 READY/v5 | 追加 PREPARING/v4、READY/v5 |
| 成功后用成功请求的 key 和输入重放 | 200，返回原成功快照 | 不重复准备、不追加事件 |

版本或状态校验未通过的请求不会占用新 key。已有前端键包含 Workspace version，因此失败后
刷新为 v3 会生成新键，本步无需修改前端。README 已补全失败后的重试步骤。

#### 验证与审阅位置

- 新增并发和失败重试测试均经过 RED → GREEN；它们通过公开 HTTP 创建、准入、登记、准备、
  读取和查看事件，不访问 Handler 或 Manager 的私有状态。
- 并发测试用 channel 固定交错点，GET 能读到 PREPARING/v2；同租户冲突保持另一个 Workspace
  在 REGISTERED/v1，事件只有登记前后的三条事实。两个租户的 key 互不占用。
- 失败重试测试确认完整的 v1 → v2 → v3 → v4 → v5 过程、七条事件及 causationId；成功重放
  返回相同响应，准备器总共只执行两次。
- 新用例在 race 下连续 20 次通过；Go 全套普通测试、race、vet、golangci-lint（0 issues）、
  gofmt 与 `git diff --check` 通过。
- 本步文件：`workspace.go`、`workspace_preparation_concurrency_test.go`、
  `workspace_preparation_test.go`、`test_helpers_test.go`、README 和本手册。
- 上一步 `e06366c` 已推送；[该次 CI](https://github.com/chenhaitao888/agent-platform/actions/runs/37208481658)
  的三个 job 仍未启动，分别报告 GitHub 账户因账单问题被锁定。远端验证需解除该限制后重跑。
- 本步经用户审阅后，已提交为 `228e49d` 并推送到 `origin/main`；继续第 16 步的登记并发回归。

### M13.1（第 16 步）：补齐 Workspace 登记的并发幂等回归

- 状态：完成（2026-10-04）；继续审查清单 #11，M14 继续暂停。
- 本步只补充已有契约的测试并整理公共 fixture；现有登记实现已满足这些约束，产品代码无需修改。

#### 同时登记时，调用方应观察到什么

新增 `workspace_registration_concurrency_test.go`，通过一个表驱动的 HTTP 测试覆盖四种组合：

| 并发请求组合 | 两份响应 | 最终 Workspace 与事件 |
| --- | --- | --- |
| 同一 Task、同一 key | 一个 201、一个 200，相同响应内容 | 一个 REGISTERED/v1，只追加一次登记事件 |
| 同一 Task、不同 key | 一个 201、一个 409 workspace_already_exists | 一个 REGISTERED/v1，只追加一次登记事件 |
| 同租户、不同 Task、同一 key | 一个 201、一个 409 idempotency_conflict | 只有赢家 Task 有 Workspace，败方没有登记事件 |
| 不同租户、不同 Task、同一 key | 两个 201，Workspace ID 不同 | 各自 REGISTERED/v1，各自追加一次登记事件 |

测试通过 channel 等待两个请求都进入外部仓库校验，再统一放行，确定覆盖校验结束后的竞争。
校验阻塞期间 GET 返回 404，Task 时间线仍只有 created 与 queued；放行后检查响应的 request ID、
Location、Task/租户归属、状态和版本，并用 GET 确认实际保存的是成功请求的结果。

登记事件的 sequence 必须是 3，causationId 必须属于返回 201 的请求，payload 必须对应该
Workspace。失败请求和重放不会追加事件；不同 Task 的成功登记不能共用 Workspace ID。
全部请求结束后，使用成功请求的 key 再重放，必须返回 200，并保持两次初始校验的调用总数，
确认并发请求没有覆盖赢家的幂等记录。

#### 为什么现有实现能直接通过

`Manager.Register` 在外部校验前检查一次，校验成功后重新取得锁，再检查一次并提交登记。
Java 可类比先检查请求键，释放 `synchronized` 锁执行只读 I/O，回来后再在锁内确认另一请求
是否已经写入。第二次检查同时保护 Task 的唯一 Workspace 和租户内的 key 绑定。

登记记录只保存 Task 归属，重放读取它的当前 Workspace；本步未启动准备操作，因此响应保持
REGISTERED/v1。与上一步 Prepare 保存成功快照的方式不同，这是各自已有的重放契约。

新增四个子用例首次运行就全部通过，因此本步记录为补齐回归，没有人为制造 RED。
公共 fixture 提取 `createQueuedTaskForTest`，通过创建和准入 HTTP 响应返回 QUEUED Task；已有
准备测试继续复用它。测试使用 5 秒 context deadline，结束时放行并等待所有请求 goroutine 退出。

#### 验证与审阅位置

- 新增并发回归在 race 下连续 20 次通过。
- Go 全套普通测试、race、vet、golangci-lint（0 issues）、gofmt 与 `git diff --check` 通过。
- 本步文件：`workspace_registration_concurrency_test.go`、`test_helpers_test.go` 和本手册。
- 上一步 `228e49d` 已推送；本步经用户审阅后，已提交为 `65cc552` 并推送到 `origin/main`。
- 下一小步可继续检查 Artifact 归档的并发幂等行为；当前回归仍只覆盖单进程内存实现。

### M13.1（第 17 步）：补齐 Artifact 并发归档与租户隔离回归

- 状态：完成（2026-10-05）；继续审查清单 #11，M14 继续暂停。
- 本步补充已有归档契约的公共 HTTP 测试；现有实现直接通过，产品代码无需修改。

#### 两个归档请求重叠时的结果

新增 `artifact_concurrency_test.go`，用一个表驱动测试覆盖五种组合：

| 并发请求组合 | 两份响应 | 可见的 Artifact 与创建事件 |
| --- | --- | --- |
| 同一 Workspace、同一 key、相同内容 | 一个 201、一个 200，元数据相同 | 相同 Artifact ID，只追加一次创建事件 |
| 同一 Workspace、同一 key、不同内容 | 一个 201、一个 409 idempotency_conflict | 保存赢家的内容与 checksum，只追加一次创建事件 |
| 同一 Workspace、不同 key、相同内容 | 两个 201 | 两个不同 ID、相同 checksum，追加两次创建事件 |
| 同租户、不同 Task/Workspace、同一 key | 一个 201、一个 409 idempotency_conflict | 只有赢家 Task 追加创建事件 |
| 不同租户、不同 Task/Workspace、同一 key | 两个 201 | 各自的 Artifact 和创建事件，两个 ID 不同 |

不同 key 是两次独立归档，不按内容自动合并 Artifact。为检验同键内容冲突，测试只在外部
DiffReader 接缝模拟两个请求返回不同 patch；没有改变真实仓库的 SHA 或上传浏览器内容。
哪个请求先成功由实际执行顺序决定，测试从 201 响应识别赢家，再用它的 SHA 核对保存的内容。

#### 并发与持久结果如何验证

两个 HTTP 请求都进入外部 diff 读取后，由 channel 统一放行；读取阻塞期间，各 Task 仍只有
创建、准入、Workspace 登记、准备中和准备完成这五条事件。请求完成后验证：

- 每个响应的 request ID 对应自己的请求；成功响应的 Location、Task/租户/Workspace 归属正确。
- Artifact GET 返回成功请求的完整元数据；content GET 的内容、字节数、Content-Type、ETag
  与元数据一致，并包含 nosniff。冲突请求没有覆盖已保存的内容。
- 用另一租户读取 metadata 或 content 均返回 404，包含两个租户各自已归档的场景。
- 成功请求在完成后重放仍返回 200 和相同元数据，时间线不追加重放或冲突事件。
- READY Workspace 的完整公开结果保持不变，包括版本、路径及固定仓库引用。
- 每个 `artifact.created` 事件都与一个返回 201 的请求对应，causationId、发生时间、归属及
  checksum 等元数据一致。事件 sequence 在原五条事实后连续追加：一份归档是六条事件，
  同 Task 的两次独立归档是七条事件；败方 Task 仍是五条。

两个独立归档的事件顺序按实际追加顺序校验，每条事件通过 Artifact ID 找到对应请求，
没有要求事件顺序与 Artifact ID 的分配顺序相同。

#### 现有实现与 Java 类比

`artifact.Store.Create` 在互斥锁内按 `(tenantId, key)` 查找记录，比较 Task/Workspace 归属、
类型、media type 和完整内容，再返回原元数据或创建新 Artifact。Java 可类比在
`synchronized` 中完成请求键索引、归属及 `byte[]` 内容校验，并复制内容后保存。
`review.Service` 仅在 `Created=true` 时追加创建事件，因此并发重放不会重复记录创建事实。

五个子用例首次运行全部通过，本步如实记录为回归补充，没有人为制造 RED。公共 fixture 通过
HTTP 把 Workspace 准备到 READY/v3；5 秒 context deadline 和清理时的放行、等待，保证请求
goroutine 能结束。断言在测试主 goroutine 中执行。

#### 验证与审阅位置

- 新增并发回归在 race 下连续 20 次通过。
- Go 全套普通测试、race、vet、golangci-lint（0 issues）、gofmt 与 `git diff --check` 通过。
- 本步文件：`artifact_concurrency_test.go` 和本手册。上一步 `65cc552` 已推送；本步经用户授权，
  已提交为 `2dcd813` 并推送到 `origin/main`。
- 下一小步可检查归档失败和版本过期时的 HTTP 边界，确认拒绝请求不留下归档事件。
- 这些断言检查并发请求完成后的结果，仍是单进程内存回归，不证明跨 Store 的原子事务或 Outbox。

### M13.1（第 18 步）：补齐 Artifact 归档拒绝、读取失败和重试边界

- 状态：完成（2026-10-05）；继续审查清单 #11，M14 继续暂停。
- 本步新增三个公共 HTTP 测试、共九个子用例；现有业务实现满足这些约束，产品代码无需修改。

#### 请求应在哪里被拒绝

新增 `artifact_boundary_test.go`，验证以下错误及副作用边界：

| 场景 | HTTP 结果 | diff 读取与业务事实 |
| --- | --- | --- |
| expectedWorkspaceVersion 为 0 | 400 validation_error | 不读取 diff、不归档、不追加事件 |
| READY/v3 请求携带版本 2 或 4 | 409 version_conflict | 不读取 diff、不归档、不追加事件 |
| Task 尚未登记 Workspace | 404 not_found | 不读取 diff，也不自动登记 Workspace |
| Workspace 仍为 REGISTERED/v1 | 409 workspace_not_ready | 不读取 diff，保持原状态和时间线 |
| 请求中的 tenant 不属于该 Workspace | 404 not_found | 不读取 diff，保持所有者的状态和时间线 |
| 读取器返回包装后的 ErrDiffTooLarge | 413 diff_too_large | 不保存读取器交付的部分内容 |
| 读取器返回包装后的 ErrDiffUnavailable | 503 diff_unavailable | 不保存部分内容，不追加创建事件 |
| 读取器返回其他错误 | 500 internal_error | 返回稳定文案，不暴露内部原因或部分内容 |

版本、状态和租户用例在外部 DiffReader 接缝记录调用数，确认拒绝发生在 I/O 之前。读取失败用例
故意让接缝同时返回部分字节和错误，确认服务遵守错误结果，业务写入尚未开始。
所有错误响应都校验错误码与稳定文案，且没有成功结果的 Location。

#### 拒绝后如何验证没有残留，以及可以重试

每个用例通过 GET 确认首次预期 Artifact ID `artifact-1` 的 metadata 和 content 均返回 404，
并比较失败前后的完整 Workspace 结果与 Task 事件数组。缺少 Workspace 的用例确认它仍不存在；
其他用例确认原对象没有被改写。检查只使用公开 HTTP 接口。

随后纠正前置条件或让外部读取器恢复，再使用同一租户、同一 key 和新的 requestId 发起请求：

- 版本错误改用当前 v3；缺少 Workspace 时先登记并准备，REGISTERED 时先完成准备。
- 另一租户改用自己的 READY Workspace；该租户成功归档后，原所有者的 Task 与 Workspace
  仍保持不变。
- 读取恢复后保存完整 patch，首次返回 201/artifact-1，checksum 和字节数与完整内容一致，
  content GET 返回完整内容及相同 ETag。
- 成功请求重放返回 200 和相同元数据；最终只新增一条创建事件，之前的事件保持原样，
  causationId 属于纠正后的成功请求，payload 对应此次 Artifact。

这验证失败请求没有提前占用归档键或分配 Artifact。测试通过明确的第二次 HTTP 请求检查重试，
没有增加服务端自动重试策略。

#### 测试记录与 Java 类比

首次运行八个子用例通过；零版本的用例因测试额外要求 400 字段校验响应回显 requestId 而失败。
现有接口在字段校验通过后才采用该标识，因此移除了零版本用例的额外请求头约束；该用例仍检查
稳定错误、I/O 调用数、状态、时间线和重试。调整测试预期后九个子用例全部通过。
这次校准没有修改产品代码，也不记作功能缺陷的 RED → GREEN。

Java 可类比 Application Service 先校验归属、状态和乐观版本，再调用基础设施；读取抛出异常时
直接返回，后续 Repository 写入和业务事件尚未执行。Go 的 `errors.Is` 沿包装链识别领域错误，
使适配器的错误即使被 Service 包装，HTTP 层仍能区分 413、503 与其他 500。

#### 验证与审阅位置

- 三个新增测试、九个子用例全部通过；Go 全套普通测试、race、vet、golangci-lint（0 issues）、
  gofmt 与 `git diff --check` 通过。
- 本步文件：`artifact_boundary_test.go` 和本手册。上一步 `2dcd813` 已推送；本步经用户授权，
  已提交为 `d6c4ec2` 并推送到 `origin/main`。
- 下一小步可复核审查清单 #11 的剩余覆盖缺口，并整理已验证范围；M14 继续暂停。
- 当前校验仍基于单进程内存数据和受控外部接缝，不证明多实例幂等或跨 Store 的原子事务。

### M13.1（第 19 步）：复核 HTTP 测试覆盖范围，接续前端审查

- 状态：完成（2026-10-05）。以交接中已知的 #11 要求“拆分超长 handler_test.go 并补边界/并发
  用例”为范围，第 14～18 步已经完成对应工作，可以转入 #12 的前端细节检查。
- 历史 `docs/code-review-2026-09-23.md` 在本机仍不存在；这个结论只针对已知交接要求，不表示
  已逐条核销无法读取的历史报告，也不自动恢复 M14。

#### 公开行为与测试的对应关系

`handler.go` 当前登记 13 个路由。测试目录有 19 个 `*_test.go` 文件，其中一个是公共 helper，
其余文件共 76 个顶层 `Test...` 函数；表驱动子用例不计入这个数量。原 2,753 行的集中测试文件
已移除，目前最大文件是 `workspace_registration_test.go`，512 行。

| 公开接口职责 | 已验证的关键行为 | 主要测试文件（backend/internal/httpapi 下） |
| --- | --- | --- |
| healthz | 健康结果、非 GET 请求拒绝 | health_test.go |
| 创建 Task | 幂等、租户键隔离、固定 master/base/head、解析失败、并发只保存一个快照和事件 | task_create_test.go、task_review_base_test.go、task_concurrency_test.go |
| Task 查询、列表、事件与 PATCH 准入 | 租户过滤、404、排序、版本冲突、状态迁移、并发同键重放与异键竞争、事件只追加一次 | task_read_test.go、task_transition_test.go、task_events_test.go、task_concurrency_test.go |
| Workspace 登记与查询 | 仓库/commit 验证、QUEUED 前置状态、唯一归属、租户过滤、四种并发登记竞争 | workspace_registration_test.go、workspace_registration_concurrency_test.go |
| Workspace Prepare | 准备中可见、I/O 前绑定键、同租户冲突与跨租户独立、失败恢复、新版本新键重试、成功重放不重复事件 | workspace_preparation_test.go、workspace_preparation_concurrency_test.go |
| 固定 diff 读取 | 固定 Workspace 路径和 base/head、READY 前置状态、超限错误、失败日志关联 | review_diff_test.go、handler_logging_test.go |
| diff 归档、Artifact metadata/content 读取 | 五种并发归档、版本/归属/状态拒绝、部分读取失败不保存、恢复后重试、内容/hash/字节数一致、租户过滤与事件对应 | artifact_http_test.go、artifact_concurrency_test.go、artifact_boundary_test.go、event_lifecycle_test.go |
| 写请求通用校验与日志 | 64 KiB body（含尾随空白）、字段长度、4 KiB UTF-8 字节边界、稳定错误和内部日志/token 遮盖 | request_validation_test.go、handler_logging_test.go |

#### 证据与范围边界

本次额外运行 HTTP 包的覆盖率测试，全部通过，语句覆盖率为 **89.7%**；报告仅保存在
`/private/tmp/agent-platform-httpapi-coverage-2026-10-05.out`。这个数值只统计 HTTP 包，
不表示整个后端的覆盖率，也不能证明并发交错或分布式事务正确。表中的行为依据具体断言复核；
第 14～18 步记录了各次 race、全套 Go 测试和静态检查结果。

本轮没有穷尽每条路由的所有字段组合。例如 Workspace/Artifact 部分 GET 接口的空 tenant 校验
仍缺少独立 HTTP 用例；它们不影响本次已验证行为的结论。跨 Store 的原子事务、多实例幂等和
持久化恢复仍属于既有架构限制。后续出现对应实现或缺陷时，再补能约束行为的测试。

复核 #12 的当前前端时发现一个可复现的准入状态问题：`TaskWorkspace` 用单个 Task ID 表示
在途请求，同时准入两条 Task 会相互覆盖按钮状态。下一步只处理这个用户可见行为，见第 20 步。

### M13.1（第 20 步）：修复多条 Task 同时准入时的按钮状态

- 状态：完成（2026-10-05）；接续交接中的 #12，一个前端行为修复切片，M14 继续暂停。
- 场景：Task A 的 PATCH 尚未返回，用户又准入 Task B。原实现把在途 ID 从 A 改为 B，导致
  A 的按钮提前恢复；任一请求结束后又清空 ID，导致另一条尚未完成的请求也可被重复点击。

#### RED → GREEN

1. 基线：现有 31 条前端测试全部通过。
2. 新增 [TaskWorkspace.concurrency.test.tsx](../frontend/src/TaskWorkspace.concurrency.test.tsx)，
   只在浏览器 fetch 接缝控制两条 PATCH 的响应。先点击 A，再点击 B，断言两个按钮均保持禁用。
   原实现明确失败：A 的按钮已经变成可点击，得到真实 RED。
3. 将单个在途 ID 改为 `Set<string>`。请求开始时加入自己的 Task ID，结束时仅删除自己的 ID，
   每次 React 状态更新都复制 Set；按钮按自身 ID 是否在集合内显示“准入中…”及禁用状态。
   首个失败用例转绿，后端接口和准入幂等键无需改变。
4. 将同一行为测试扩展为四个场景：A 或 B 先成功、A 或 B 先收到 409。两条请求都在途时不能
   重复点击；先结束的请求只更新自己，另一条保持 CREATED/v1 且按钮禁用。成功结果为 QUEUED/v2，
   被拒绝的 Task 保持 CREATED/v1 并恢复可点击；最后一条请求成功后也更新到 QUEUED/v2。

测试检查各自的 PATCH 路径、tenant、expectedVersion 和由 Task/版本派生的幂等键，确认两次
独立操作没有串用请求内容。通过可见按钮、状态和版本断言，不读取 React 内部集合。
延迟响应由 Promise 显式放行，没有用 sleep 推测顺序；finally 中完成未结束的响应，避免泄漏。

#### Java 类比与验证

Java 可类比维护一个请求中的 Task ID 集合：开始操作时 add(id)，finally 中 remove(id)。
某个请求完成只能移除自己的条目。浏览器中这些回调按事件循环执行；React 使用函数式状态更新
读取最新集合，并复制为新对象供视图渲染，不需要引入线程锁。

- 四个新增场景全部通过；前端共 **35 条测试**通过，ESLint（零 warning）、TypeScript 检查及
  生产构建通过，`git diff --check` 通过。
- 本轮没有修改后端代码；第 19 步的 HTTP 覆盖率测试已通过，未重复执行无改动的全套 Go 检查。
- 本步文件：`frontend/src/TaskWorkspace.tsx`、新增并发测试和本手册。第 18 步已推送；
  第 19～20 步经用户授权，已提交为 `e539ef9` 并推送到 `origin/main`。
- 下一小步检查准入错误的归属：当前多个 Task 仍共享一个错误提示，另一条操作可能清除它；
  先用用户可见行为测试确认，再决定如何按 Task 展示。此次修复只处理在途按钮状态。

### M13.1（第 21 步）：让准入错误归属于对应 Task

- 状态：完成（2026-10-05）；继续交接中的 #12，M14 继续暂停。
- 场景：Task A 准入失败后，操作 Task B 会清除 A 的提示；两条 Task 都失败时，后返回的错误
  又会覆盖先返回的错误。原提示放在列表末尾，用户也无法从位置确定失败的是哪条 Task。
- 结果：错误显示在各自的 Task 卡片中，多个失败可以同时显示。开始重试只清除当前 Task 的
  旧错误；其他 Task 的失败仍保留。成功准入仍只更新本条 Task 的状态与版本。

#### RED → GREEN 与行为回归

1. 基线：现有 35 条前端测试通过，第 19～20 步已按用户要求推送。
2. 新增 [TaskWorkspace.errors.test.tsx](../frontend/src/TaskWorkspace.errors.test.tsx)。先让 A 的
   PATCH 返回 409/version_conflict，再点击 B，要求 A 的提示保持可见。原实现明确失败：点击 B
   立即清空全局错误，页面不再有 alert，得到真实 RED。
3. 用 `Map<string, string>` 按 Task ID 保存准入错误。请求开始时复制 Map 并只删除自己的条目；
   HTTP 拒绝或网络异常时，在函数式更新中复制最新 Map 并写入自己的错误。
   将 `role="alert"` 移到对应 Task 卡片，保留原错误文案和样式；首个用例转绿。
4. 完整跑通 A 的 HTTP 拒绝、B 的网络异常、分别重试和成功：B 开始时 A 的错误仍可见；
   A 重试及成功时 B 的错误仍可见；B 自己重试才清除 B 的提示。最终两条 Task 均为 QUEUED/v2，
   没有残留错误，每条 Task 各发送两次 PATCH。
5. 补充两条并发请求的失败到达顺序：HTTP 拒绝先到或网络异常先到。先失败的卡片恢复按钮并显示
   自己的错误，另一条仍在等待；全部结束后两张卡片各有正确的提示，均保持 CREATED/v1，
   两个按钮都可再次点击。补充用例首次即绿，记录为行为回归。

测试只控制浏览器 fetch 的延迟响应或 Promise rejection，使用卡片内的可见状态、版本、按钮和
alert 检查归属，不读取 React 内部 Map。finally 放行未完成的请求，避免测试失败时留下等待。
既有的单条 Task 冲突、响应丢失重试和多条 Task 按钮状态测试继续通过。

#### Java 类比、验证与审阅

Java 可类比从一个全局 `String error` 改为 `Map<TaskId, String>`：发生错误时 put 自己的 ID，
重试时 remove 自己的 ID，视图按卡片 ID 取提示。React 每次使用新的 Map 对象，函数式更新读取
最新状态，使先后到达的独立请求结果可以保留各自条目。

- 三个新增回归通过，前端共 **38 条测试**通过；ESLint（零 warning）、TypeScript 检查、
  生产构建和 `git diff --check` 通过。
- 没有修改后端接口、Task 版本规则或准入幂等键；本轮没有后端改动，未重复 Go 检查。
- 本步文件：`frontend/src/TaskWorkspace.tsx`、新增错误回归和本手册。上一提交 `e539ef9`
  已推送；本步经用户授权，已提交为 `cd05bda` 并推送到 `origin/main`。
- 下一小步检查页面健康请求的取消边界：当前 App 只识别 AbortError，没有在结果写入前确认
  signal 是否已经取消。先以入口已启用的 StrictMode 和延迟结果验证旧请求会不会影响新一轮状态，
  再决定是否修复；本步没有扩大到页面初始化或其他组件。

### M13.1（第 22 步）：阻止已取消的健康请求改写页面状态

- 状态：完成（2026-10-05）；继续交接中的 #12，M14 继续暂停。
- 场景：一轮健康请求因 Effect 清理被取消，下一轮请求已经更新页面，旧请求却仍交付成功或失败
  结果。原 App 只忽略 AbortError，其他晚到结果仍会调用 setPageState，覆盖当前状态。
- 结果：App 在 fetch 完成、JSON 读取完成和错误处理时检查本轮 signal 是否取消。已经取消的
  请求不再更新页面；仍有效的请求正常展示健康结果或失败原因。

#### RED → GREEN

1. 基线：现有 38 条前端测试通过，第 21 步已按用户要求推送。
2. 新增 [App.cancellation.test.tsx](../frontend/src/App.cancellation.test.tsx)，将 App 放在与
   页面入口一致的 StrictMode 中。通过浏览器 fetch 接缝确认发起两轮健康请求，第一轮的 signal
   已取消，第二轮仍有效；先让第二轮展示当前服务名，再放行旧请求交付不同的服务名。
   原实现明确失败：页面从当前服务名变为旧服务名，得到真实 RED。
3. 在每次异步读取之后检查 `controller.signal.aborted`，取消后直接结束此轮处理。
   catch 同样先识别已取消作用域，并保留原来的 AbortError 处理；首个失败用例转绿。
4. 将测试扩展为五个回归场景，补充用例首次即绿：

| 当前请求结果 | 已取消请求的晚到结果 | 最终页面 |
| --- | --- | --- |
| 成功 | 成功，包含旧服务名 | 保留当前服务名和“运行正常” |
| 成功 | HTTP 503 | 保留健康结果，不显示旧失败 |
| 成功 | 普通网络异常 | 保留健康结果，不显示旧异常 |
| 成功 | AbortError | 保留健康结果 |
| HTTP 503 | 成功 | 保留“暂时不可用”和当前 HTTP 503 原因 |

测试故意让 fetch 替身在收到取消信号后仍可交付旧结果，用于验证回调的有效性边界；它不表示
浏览器取消请求后一定会继续返回响应。断言检查可见状态、服务名和错误文案，不读取 React 内部
状态。响应由 Promise 显式放行，finally 结束未完成的请求，没有依靠 sleep 推测顺序。

#### Java 类比、验证与审阅

Java 可类比异步回调持有一个请求作用域的有效标志：作用域关闭后，回调在读取结果及更新视图前
先确认标志仍有效。这里的 AbortSignal 属于单次 Effect；即使后续异步结果继续到达，也不能写入
新一轮页面状态。JSON 读取后的检查与现有 Workspace/事件组件的处理方式保持一致。

- 五个新增场景通过，前端共 **43 条测试**通过；ESLint（零 warning）、TypeScript 检查、
  生产构建和 `git diff --check` 通过。
- 本步只修改 `frontend/src/App.tsx`、新增取消回归和本手册；没有后端改动，未重复 Go 检查。
- 上一提交 `cd05bda` 已推送；本步保留为未提交改动，供用户审阅。
- 下一小步检查 Task 列表首次加载的取消边界：当前 TaskWorkspace 同样只识别 AbortError。
  先验证已取消请求是否会影响新一轮列表状态，再决定是否修复；本步没有改变 Task 列表行为。

### M13.1（第 23 步）：阻止已取消的 Task 列表请求影响当前加载

- 状态：完成（2026-10-05）；继续交接中的 #12，M14 继续暂停。
- 本步按用户要求继续开发，没有提交或推送。第 22 步的 App 健康取消修复仍保留在工作区，
  与本步一起供用户审阅；远端最新提交仍为 `cd05bda`。
- 场景：Task 列表会合并服务器响应与本地数据。旧请求取消后返回的数据仍会进入合并，可能把
  已取消响应里的 Task 加回当前空列表，或覆盖当前加载失败状态。旧请求的普通网络异常还会在
  新请求尚未完成时把“正在加载”改为失败。
- 结果：TaskWorkspace 在 fetch 完成、JSON 读取完成和错误处理时检查本轮 signal 是否取消；
  已取消作用域的成功和失败结果都停止处理。仍有效的列表请求继续使用原版本合并规则。

#### 两次 RED → GREEN

1. 基线：包含第 22 步未提交修复的 43 条前端测试通过。
2. 新增 [TaskWorkspace.cancellation.test.tsx](../frontend/src/TaskWorkspace.cancellation.test.tsx)，
   通过 StrictMode 发起两轮列表加载，确认第一轮的 signal 已取消、第二轮仍有效。
   先让第二轮返回空列表，再放行旧请求返回一条 Task。原实现明确失败：页面出现了已取消请求的
   Task，得到第一次 RED。补充异步读取后的取消检查，此用例转绿。
3. 再新增一个独立用例：第二轮仍在等待时，让已取消请求返回普通网络异常，要求页面继续加载且
   不显示错误。读取后的检查无法处理 Promise rejection，原 catch 仍显示旧网络异常，得到
   第二次 RED。catch 增加取消状态判断，保留原 AbortError 处理，两个用例均转绿。
4. 将成功结果和错误结果的回归各扩展到三个场景，共六个；补充用例首次即绿：

| 当前请求状态 | 已取消请求的结果 | 用户可见结果 |
| --- | --- | --- |
| 已返回空列表 | 成功，包含旧 Task | 保留“暂无 Task”，不显示旧 Task |
| 已返回有数据列表 | 成功，包含旧 Task | 保留当前唯一 Task、版本和列表，不合并旧 Task |
| 已返回 HTTP 503 | 成功，包含旧 Task | 保留当前错误，不改为成功列表 |
| 仍在等待 | HTTP 503 | 继续加载，不显示旧错误；当前响应随后正常展示 |
| 仍在等待 | 普通网络异常 | 继续加载，不显示旧异常；当前响应随后正常展示 |
| 仍在等待 | AbortError | 继续加载；当前响应随后正常展示 |

测试只模拟浏览器 fetch，使用可见 Task、列表、版本、加载文案和 alert 断言，不读取 React 内部
状态。Promise 显式控制响应顺序，finally 放行未完成请求。替身故意在收到取消信号后仍交付结果，
用于检验旧回调失效，不代表浏览器一定继续返回已取消的响应。

#### Java 类比、验证与审阅

Java 可类比异步回调在写入 ViewModel 前检查请求作用域是否仍有效。每一轮 Effect 都持有自己的
AbortSignal；作用域关闭后，成功回调与异常回调都应结束。有效请求的版本合并仍负责处理列表读取
与本地 Task 创建、准入操作之间的交错，这部分业务规则没有改动。

- 六个新增场景通过，前端共 **49 条测试**通过；ESLint（零 warning）、TypeScript 检查、
  生产构建和 `git diff --check` 通过。既有的迟到列表保留新建 Task、保留较新 QUEUED 版本，
  以及多条 Task 准入的按钮和错误归属回归均继续通过。
- 本步只修改 `frontend/src/TaskWorkspace.tsx`、新增列表取消回归和本手册；没有后端改动，
  未重复 Go 检查。
- 第 22～23 步全部保留为未提交改动，供用户审阅。
- 下一小步核对页面连接说明：当前简介固定写“Portal 已连接 Go API”，在检查中或失败状态下
  也显示成功连接。需要让文案与健康状态一致；本步没有修改页面文案或 Task 操作入口。

### M13.1（第 24 步）：让页面连接说明与健康状态一致

- 状态：完成（2026-10-05）；继续交接中的 #12，M14 继续暂停。
- 场景：App 的简介固定声称“Portal 已连接 Go API”，健康检查尚未完成或已经失败时也显示，
  与下方的“检查中”或“暂时不可用”状态相互矛盾。
- 结果：简介直接使用现有 `pageState.kind` 选择说明，与 API 状态共用同一份状态来源：

| 健康状态 | 页面说明 |
| --- | --- |
| loading | 正在检查 API 连接… |
| healthy | API 连接正常，可以在下方创建 Task 并查看最近记录。 |
| unavailable | API 健康检查未通过，请查看下方错误信息。 |

Java 可类比视图层根据已有的状态 enum 选择说明文字。此次调整沿用现有的加载、健康和失败状态，
Task 表单与列表仍按自己的请求结果工作，健康检查失败不会禁用 Task 操作入口。

- 沿用已有健康状态及取消回归验证，本步属于既有状态下的文案纠正，测试数量保持 **49 条**，
  不记作新的功能 RED → GREEN。
- 前端 49 条测试、ESLint（零 warning）、TypeScript 检查、生产构建和 `git diff --check`
  全部通过；没有后端改动，未重复 Go 检查。
- 本步修改 `frontend/src/App.tsx` 的简介及本手册。按用户“继续”的要求，第 22～24 步
  全部保留为未提交改动，供用户一起审阅；最近已推送提交为 `cd05bda`。
- 下一小步复核本轮前端审查的已修复行为、对应回归和剩余限制，整理 M13.1 的阶段交接。
  历史审查报告仍未出现在本机，复核以已知交接要求和当前代码为范围；M14 不自动恢复。

### M13.1（第 25 步）：复核前端行为并整理阶段交接

- 状态：完成（2026-10-05）；本步复核现有代码与测试，只更新本手册。
- 交接中的 #10 已补齐 ESLint 与 CI 配置；#11 的 HTTP 测试拆分、边界与并发回归已在第 19 步
  复核；#12 的本轮前端检查在第 20～24 步修复了下表五类用户可见问题。
- 历史 `docs/code-review-2026-09-23.md` 仍未出现在本机。此次交接确认已知要求和已发现问题的
  处理结果，不将其记为对历史报告所有条目的逐项验收；M14 继续暂停。

#### 本轮前端修复与证据

| 行为 | 修复后的结果 | 证据与提交状态 |
| --- | --- | --- |
| 两条 Task 同时准入 | 每条按钮仅随自己的请求禁用、恢复 | 第 20 步四个到达顺序回归；已随 `e539ef9` 推送 |
| 不同 Task 准入失败、重试 | 各卡片保留自己的错误，重试只清除本条提示 | 第 21 步三个错误归属回归；已随 `cd05bda` 推送 |
| 已取消健康请求晚到 | 旧成功与失败结果均不能改写当前页面 | 第 22 步五个取消回归；未提交 |
| 已取消列表请求晚到 | 旧数据不进入合并，旧异常不结束当前加载 | 第 23 步六个取消回归；未提交 |
| 页面连接说明 | 检查中、健康、失败各自使用现有状态对应的说明 | 第 24 步沿用状态回归；未提交 |

第 20～23 步均先观察到真实失败，再补最小修复；后续扩展场景首次通过的部分按回归记录。
第 24 步只是已有状态下的文案纠正，没有新增测试或宣称新的 RED → GREEN。

#### 当前前端行为测试清单

本次核对了全部九个测试文件。下表按实际运行时展开的参数化用例计数，共 **49 条**；相对
第 19 步的 31 条基线新增 18 条，分别为按钮并发 4 条、错误归属 3 条、健康取消 5 条、列表取消
6 条。文件均位于 `frontend/src/`。

| 测试文件 | 用例数 | 已验证的主要行为 |
| --- | --- | --- |
| App.test.tsx | 2 | 健康检查成功显示服务名，网络失败显示不可用 |
| App.cancellation.test.tsx | 5 | StrictMode 清理后旧响应或异常不覆盖当前健康结果 |
| TaskCreator.test.tsx | 8 | 创建结果、提交禁用、错误恢复、同内容重试复用键、改内容或成功后使用新键 |
| TaskWorkspace.test.tsx | 9 | 列表、创建后展示、迟到列表保留本地新 Task 与较新版本、准入幂等重试与版本冲突 |
| TaskWorkspace.concurrency.test.tsx | 4 | 两条准入请求以不同顺序完成，各自控制按钮与状态 |
| TaskWorkspace.errors.test.tsx | 3 | 独立错误、分别重试、不同失败顺序不相互清除提示 |
| TaskWorkspace.cancellation.test.tsx | 6 | 取消后的旧列表与错误不影响当前列表或加载状态 |
| WorkspaceDetails.test.tsx | 9 | 查看与登记、准备重试键、失败后新版本新键、固定 diff、有限预览、归档重放、旧 Task diff 隔离 |
| TaskEventTimeline.test.tsx | 3 | Task/Workspace/Artifact 事件展示、加载失败恢复按钮、旧 Task 事件隔离 |

最近一次完整前端验证在第 24 步：49 条测试、ESLint（零 warning）、TypeScript 检查和生产
构建全部通过。本步没有修改源码或测试，未重复运行这些检查；文档修改另用 `git diff --check`
验证。后端最近验证与 HTTP 覆盖率证据沿用第 18～19 步，不把前端结果当作后端验证。

#### 验证范围与后续边界

- 组件测试通过 jsdom 和浏览器 fetch 接缝控制响应顺序，验证可见状态、按钮、错误与请求契约。
  取消回归的替身故意仍交付已取消结果，用于验证回调失效；它们不是浏览器端到端测试，也不是
  真实企业 GitLab 凭据联调。
- 已有回归覆盖指定的请求交错，不表示每个操作、每个 JSON 读取阶段或租户切换组合都已穷尽。
  Workspace 的跨 Task 延迟测试直接验证 diff；归档测试验证成功后的同键重放，不宣称已验证
  归档网络失败后重试。事件错误测试验证按钮恢复，不宣称已经跑通第二次成功加载。
- 准入版本冲突仍保留错误与本地版本，没有自动读取服务器新版本。Workspace 和事件仍由用户
  主动查看；后台轮询、跨标签页同步与冲突自动恢复尚未引入。
- 单进程内存、鉴权、持久化和工作流执行等架构限制继续见第 8 节。本次审查不改变这些能力边界。

#### 待审阅文件与接续约定

| 工作区文件 | 待审阅内容 |
| --- | --- |
| frontend/src/App.tsx | 健康请求取消检查与三种连接说明 |
| frontend/src/TaskWorkspace.tsx | 首次列表请求取消检查 |
| frontend/src/App.cancellation.test.tsx | 新增 5 个健康请求取消回归；文件尚未加入 Git 索引 |
| frontend/src/TaskWorkspace.cancellation.test.tsx | 新增 6 个列表请求取消回归；文件尚未加入 Git 索引 |
| docs/development-handbook.md | 第 22～25 步记录、当前能力与目录职责更新 |

以上五个文件保留为未提交改动，最近已推送提交仍为 `cd05bda`。后续用户明确要求 push 时，
一并提交已审阅的代码、新增测试与文档；仅要求“继续”时不自动提交或推送。
已知交接范围的阶段复核到此完成；后续收到历史审查报告或具体问题时，按对应行为继续小切片。
当时 M14 按用户约定暂停；用户后来明确允许持续准备只读 Codex exec 与 Findings，见第 26～28 步。

### M14 准备（第 26 步）：贯通真实 Git、HTTP 与 Artifact 输入

- 状态：完成（2026-10-05）。用户要求持续推进到具备执行 M14 的条件，并确认 M14 准备范围为
  架构 17.0 的只读 Codex exec 与结构化 Findings。
- 起点：前端现有 49 条测试、后端现有测试通过。已知 M13.1 交接要求沿用第 25 步结论；
  缺失的历史报告不阻塞当前代码的输入与执行准备。
- 新增 [review_input_integration_test.go](../backend/internal/httpapi/review_input_integration_test.go)，
  使用真实 Git Preparer、DiffReader、HTTP server/client 和内存 Store；只在 GitLab 接缝用本机仓库
  替代远端项目和服务凭据。测试首次通过，属于贯穿回归，没有宣称发现新的产品 RED。

测试创建共同祖先、master 独有提交与 feature 独有提交，经过公开接口创建 Task 并固定引用。
随后推进 master，再重放创建、准入、登记、准备 Workspace、读取 diff、归档并读取 Artifact：

1. 创建重放仍返回原 Task；master 移动不改变 target/base/head。
2. Workspace 为 READY/v3，真实 worktree HEAD 等于固定 head；master 独有文件不进入 worktree。
3. diff 只包含 feature 的改动，保留 Task/Workspace/base/head 归属，完整内容与 SHA-256、字节数一致。
4. Artifact 正文、ETag 与 Content-Length 对应同一份 diff；其他 tenant 读取返回 404。
5. 归档重放返回同一 Artifact；时间线只有创建、准入、登记、准备中、就绪与归档六个有序事件。

Java 可类比把真实 Git 进程、HTTP 客户端与内存 Repository 组合成一个集成测试，只替换外部 GitLab
边界。这证明现有组件能共同产生 M14 的固定输入，仍不等同于企业 GitLab 凭据联调或 Runtime 执行。

### M14 准备（第 27 步）：定义并校验 Findings 输出契约

- 状态：完成（2026-10-05）。新增 [Findings schema](../backend/internal/review/pr-review-findings-v1.json)、
  [解析与业务校验](../backend/internal/review/findings.go)、行为测试，以及空结果/单条结果两份
  [共享 fixture](../backend/internal/review/testdata/findings-valid.json)。
- 架构 5.3 要求 Agent 输出结构化 Findings，再由平台校验和去重。这里先准备该确定性边界，
  没有启动模型、创建 Session 空壳或增加 HTTP 路由。

| 字段或边界 | 初始合同 |
| --- | --- |
| schemaVersion | `1.0`；只版本化 Findings 输出，不冻结全部平台领域契约 |
| baseSha、headSha | 完整小写 40/64 位十六进制 SHA，并与平台传入的固定引用逐字一致 |
| findings | 必填数组，可为空，去重前最多 50 条 |
| title、description | 必填非空白文本；上限分别为 256、4,096 个 Unicode 字符 |
| severity、confidence | LOW/MEDIUM/HIGH/CRITICAL；必填置信度，范围 0～1，零分合法 |
| path | 最多 1,024 个 Unicode 字符的规范相对路径；拒绝绝对路径、越界段、反斜杠、冒号和控制字符 |
| startLine、endLine | 正整数，start ≤ end ≤ 2,147,483,647 |
| JSON 载荷 | 最多 256 KiB；拒绝非法 UTF-8、未知/重复/大小写不符字段、缺失或 null 字段、尾随 JSON |
| 失败与去重 | 任一条无效则整份拒绝且无可用结果；完全相同的七字段记录去重，保留首次出现顺序 |

#### RED → GREEN 与验证

1. 空结果测试先因缺少 `ParseFindings` 编译失败，补最小实现后通过。
2. `../private.txt` 被原实现接受，新增路径拒绝得到真实 RED，补路径边界后转绿。
3. 缺少 confidence 被普通 JSON 解码当作合法的零分，得到真实 RED；明确字段存在性后转绿。
4. startLine 大于 endLine 的报告仍返回前面的有效结果，得到真实 RED；整份验证失败时返回空结果。
5. 扩展场景观察到未知/重复字段、空白文本、非法置信度、超限和非法 UTF-8 被接受；逐类补齐边界。
   既有版本/引用拒绝与其他首次通过的场景按回归记录。
6. 三条输出中的一个精确重复未去除，得到真实 RED；以完整 Finding 值去重后，保留两个不同问题及顺序。

行为回归还覆盖所有字段的缺失/null、四种 severity 与 0/1 置信度边界、50 条不同 findings、
256 KiB 载荷边界、中文标题字符计数、64 位 SHA 和共享 fixture。schema 及两份 fixture 另用
前端现有依赖中的 Ajv 6.15.0 做了 Draft-07 校验；本步没有增加依赖。

Java 可类比先用 JSON Schema 定义返回 DTO，再做 Bean Validation 和业务校验；DTO 能反序列化
不表示字段齐全、引用正确或结果适合发布。Go 的 token 检查只处理本合同的三层结构，避免默认
解码器把重复字段覆盖、把 null 数字变成零，再用具体 Finding 做领域验证。

当前只验证结构、引用、路径形式与行号范围，没有证明该文件存在、该行位于改动中，或缺陷判断正确。
精确去重也不是语义去重；发布的置信度阈值、定位校验和评测策略由后续场景切片处理。

### M14 准备（第 28 步）：准备固定 CLI 与可重复的隔离预检

本节保留 2026-10-05 的验证记录。版本策略已在第 29 步调整：0.154.0 是当时的验证基线，
当前预检已取消指定版本与 darwin/arm64 默认摘要限制；所需能力仍须通过检查。

- 状态：完成（2026-10-05）。本机全局 Codex 是 0.160.0；另从官方 npm 发布包准备 0.154.0，
  存放于 `/private/tmp/agent-platform-tools/codex-0.154.0-darwin-arm64/`，全局 CLI 未调整。
- darwin/arm64 二进制 SHA-256 为
  `4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc`，与架构 v0.4.10 的固定身份一致。
  该摘要不适用于 Linux 镜像；其他平台必须为对应二进制提供另一个经验证的摘要。
- 新增 [codex.Probe](../backend/internal/codex/probe.go) 与 [runtimecheck](../backend/cmd/runtimecheck/main.go)。
  先比对二进制摘要，再检查固定版本、exec 选项和 read-only；没有发起模型请求。

#### CLI 预检行为与命令

Probe 只执行 `--version` 和 `exec --help`，使用临时 CODEX_HOME、独立工作目录和最小环境；
不读取用户 config/auth，不继承 GitLab token 或模型 key。检查过程有 10 秒超时并接受调用方取消。
成功返回二进制身份和所需选项，失败返回空 Profile，不启动 review。

首个外部可执行文件测试先因缺少 Probe 编译 RED；实现后转绿。真实 0.154.0 的帮助中
`read-only,` 带逗号，使初始选项识别误判；补真实帮助格式的失败回归，再处理列表标点后转绿。
测试还覆盖版本不符、七种缺失选项、沙箱不符、摘要无效或不匹配、取消、配置与凭据隔离。
实际固定二进制已通过以下预检：

```bash
cd backend
go run ./cmd/runtimecheck \
  -codex /private/tmp/agent-platform-tools/codex-0.154.0-darwin-arm64/package/vendor/aarch64-apple-darwin/bin/codex
```

所需选项为 `--sandbox`、`--ephemeral`、`--output-schema`、`--output-last-message`、`--json`、
`--ignore-user-config` 和 `--ignore-rules`。官方用法说明见
[OpenAI 非交互模式](https://learn.chatgpt.com/docs/non-interactive-mode)；具体选项以本次固定二进制实测为准。

#### 本机容器边界验证

已启动本机安装的 Docker Desktop，Server 为 28.3.0。新增
[隔离预检脚本](../scripts/check-runtime-isolation.py)，只创建自己的临时目录与容器，使用无凭据的
公开 Alpine 镜像验证运行边界；镜像以摘要指定，不依赖运行时可变 tag：

```bash
python3 scripts/check-runtime-isolation.py \
  alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
```

实际通过：非 root、全部 capabilities 移除、no-new-privileges、根文件系统及 Workspace 的 ro
挂载标志、写入拒绝、无启用的非 loopback 接口与外部默认路由、临时 CODEX_HOME 可写、无模型或
GitLab 凭据、宿主输入未改变。容器还设置 PID/内存/CPU 限制；这些上限是探针资源配置，未做性能评测。
临时容器退出后删除，临时目录清理；下载的工具与镜像保留作开发缓存。

首版探针错误地要求 sysfs 中只有 lo。本机内核还创建关闭的隧道接口和 `bonding_masters` 普通文件；
实际检查后改为过滤真实接口、确认非 loopback 的 UP 标志未设置，并检查默认路由与进程权限。
该修正不是绕过网络约束。容器运行参数依据见 [Docker 运行说明](https://docs.docker.com/engine/containers/run/)。

这份探针证明本机能执行上述隔离边界，不是 Codex Linux 镜像，也没有验证真实模型网络、企业服务
身份、Linux Codex 的 sandbox 兼容性或真实输出质量。CLI 帮助探测同样不能替代这些执行验证。

#### M14 的具体起点与验收

| 开始开发前的条件 | 证据 |
| --- | --- |
| 现有功能基线稳定 | 本轮前端 49 条测试、lint/typecheck/build；Go 普通/race/vet/lint 全部通过 |
| 固定输入可贯穿现有组件 | 第 26 步真实 Git + HTTP + Artifact 回归通过 |
| 输出合同和确定性校验可用 | 第 27 步 Findings 行为回归、schema 与共享 fixture 校验通过 |
| CLI 身份可记录、所需能力可预检 | 第 28 步验证基线通过；第 29 步本机 0.160.0 预检通过，不再限定 0.154.0 |
| 本机具备隔离开发环境 | Docker 已启动，容器探针实际通过 |

**结论：具备开始 M14 第一个开发切片的条件。** 第一步建立只读 Review Runner 的应用层 port 和
进程 adapter，以外部假可执行文件测试固定 argv、stdin、隔离环境、最终输出文件、超时/取消和
失败分类，再调用现成 ParseFindings 返回经过校验的结果。Java 可类比先定义业务接口，再用
ProcessBuilder adapter 实现它，接口测试不依赖真实模型费用与企业凭据。

第一切片验收：合法 Findings 保留固定 base/head；非零退出、缺失/超限/非法输出、版本引用不符、
超时和取消均无成功结果；请求正文不能指定 CLI 路径、凭据或安全选项。后续再接 Task/Workspace
前置状态、幂等执行与 Findings Artifact/事件，以及固定 Linux Codex 镜像和受控模型连接的实际验证。
首次真实模型调用必须使用场景指定的服务身份和模型配置，不能复用本机个人登录；网络只开放模型
连接所需路径。它们属于 M14 的执行实现与验收，不是本轮已经完成的能力。

本轮新增 18 个顶层 Go 行为测试（HTTP 贯穿 1、Findings 11、CLI Probe 6），包含表驱动子场景。
Go 全套普通/race 测试、vet、golangci-lint（0 issues）、gofmt 通过；前端 49 条测试及静态检查、
生产构建通过。未修改后端现有公开接口，也没有实际模型调用或评论发布。
第 22～28 步所有改动继续保留未提交，供用户审阅；没有提交或推送。

### M14 准备（第 29 步）：按能力预检 CLI，并明确 Harness 分工

- 状态：完成（2026-10-06）。用户要求 Codex 升级时不要强依赖 0.154.0，并询问 M14 是否复用 Codex Harness。
- [Probe](../backend/internal/codex/probe.go) 已移除固定版本和平台摘要常量，读取实际 `codex-cli` 版本，
  计算并返回二进制 SHA-256。兼容性预检继续要求七个选项及 read-only 沙箱能力。
- [runtimecheck](../backend/cmd/runtimecheck/main.go) 默认检查 PATH 中的 `codex`；摘要校验改为可选。
  显式提供摘要时，格式错误或不匹配仍在执行二进制前拒绝；不会因为版本放宽而跳过能力、配置或凭据边界。

#### 版本策略与验证

先扩展外部假可执行文件的公共行为测试：0.154.0 通过，0.160.0 因固定版本限制 RED；
改为记录实际版本后两者 GREEN。再增加不指定摘要的行为测试，因必填摘要 RED；
将摘要改为可选后 GREEN。原先的“版本不同就拒绝”回归改为检查错误 CLI 身份、缺少版本、
异常版本输出；缺失能力、摘要不匹配、取消、配置和控制平面凭据隔离回归继续通过。

本机真实 CLI 已用默认命令完成预检：

```bash
cd backend
go run ./cmd/runtimecheck
```

实际版本为 `0.160.0`，路径 `/opt/homebrew/bin/codex`，SHA-256 为
`112fae7a5a1223e673c8a1791d32338f37df8b527ff1159bb8adac6c4dbf1b4b`。
`-codex` 可选择部署二进制，`-sha256` 可指定对应 OS/架构的已验证摘要；没有默认平台摘要。
版本与摘要用于追溯具体部署，不能仅凭版本号或帮助信息断言执行兼容。
升级时先做能力预检，再运行 M14 adapter、输出契约与隔离执行回归，通过后更新部署制品记录；
当前 adapter 尚待第一个 M14 切片实现。本步骤没有自动安装或升级 Codex。

#### M14 如何复用 Codex Harness

M14 通过 `codex exec` 启动 OpenAI 开源 Codex Harness，运行一次有边界的只读 PR Review。
官方将 exec 定位为非交互任务入口，将 app-server 定位为持久会话、流式事件与审批入口，见
[OpenAI Harness 集成说明](https://developers.openai.com/blog/codex-as-a-platform)。
当前阶段采用 exec，后续需要会话恢复与交互审批时再接 app-server。

| 组件 | 承担的职责 |
| --- | --- |
| 平台的 Task / Workspace | 固定 base/head、准备只读代码与 diff、检查任务和工作区状态 |
| 平台的 Runtime adapter / Worker | 构造受控 argv/stdin、隔离运行环境与配置、限制凭据、处理超时/取消/退出错误 |
| Codex Harness | 模型调用循环、上下文管理、工具调用，以及其配置下的沙箱与审批策略 |
| 平台的 Findings / Artifact / 事件 | 校验输出契约与固定引用、去重、幂等归档、记录执行结果 |

`--output-schema` 请求结构化 Findings，平台仍独立解析和校验结果，见
[OpenAI 非交互模式](https://learn.chatgpt.com/docs/non-interactive-mode)。Harness 的开源执行层与模型访问
分开，实际模型执行仍需场景服务身份与模型配置。Java 可类比业务 Service 调用一个 Runtime 接口，
ProcessBuilder adapter 启动第三方执行引擎；升级主要影响 adapter 与集成回归，不把上游内部代码耦合到业务层。

本轮 Go 全套普通/race 测试、vet、golangci-lint（0 issues）通过；CLI Probe 现有 7 个顶层行为测试，
含版本与能力的表驱动子场景。前端与容器探针未改动，沿用第 28 步验证记录；实际模型调用与评论发布仍未接入。
第 22～29 步改动保留未提交，供用户审阅。

### M14 准备（第 30 步）：修正帮助文本误判，明确检查总预算

- 状态：完成（2026-10-06）。用户指出帮助文本分词、read-only 检查和共享 context 三个审阅点。
- 修改 [Probe](../backend/internal/codex/probe.go) 与 [公共行为测试](../backend/internal/codex/probe_test.go)，
  保留第 29 步按能力预检、记录实际版本/摘要的策略。

#### 三个审阅点的结论

| 审阅点 | 原行为与影响 | 当前处理 |
| --- | --- | --- |
| 对整个 help 使用 `strings.Fields` | 描述或示例提到 `--json` 也会被当作支持；标点或换行变化还可能误拒绝 | 只识别 Options 中的选项声明及所属描述块，支持短/长选项别名，排除正文、缩进示例与其他章节 |
| 全文出现 read-only 就通过 | 即使 `--sandbox` 不支持该值，其他选项或描述出现同名文本也会误通过 | 只检查 `--sandbox` 块中的 possible values，按逗号拆分并精确匹配枚举值，支持紧邻逗号和换行 |
| 两条命令共享 context | 这是总预算语义，本身不是 bug；若理解为每条命令各 10 秒，就会误判剩余时间 | 保留总预算，命名为 inspectionContext，并补充接口注释、文档和行为回归 |

两条子进程检查命令 `--version` 与 `exec --help` **合计共用 10 秒执行期限**。
版本检查若用了约 8 秒，help 仅剩约 2 秒；调用方更短的截止时间或取消仍优先生效。
这不是整个 Probe 从读取二进制到清理目录的耗时上限；命令停止与清理也有额外开销。
Java 可类比两个步骤共用一个请求级 deadline，后一个步骤使用剩余预算。

#### 行为回归与验证

先用外部假 CLI 复现“描述里提到 `--json` 却通过”的 RED，限定选项声明后 GREEN；
再复现“只有其他选项的可选值含 read-only 却通过”的 RED，绑定 sandbox 枚举后 GREEN。
原来的单行帮助片段已换为与真实 CLI 一致的 Options、别名、描述与枚举布局。
扩展回归覆盖正文/示例/其他章节中的 flag、沙箱描述或其他选项中的 read-only、枚举近似值、
未闭合枚举，以及紧邻逗号和换行的合法枚举。

执行中取消测试等待 help 实际启动后取消调用方 context，检查返回 `context.Canceled` 且无成功 Profile。
总预算测试让版本命令持续 4 秒、help 持续 7 秒：两者各自小于 10 秒，但合计超限，
应返回 `context.DeadlineExceeded` 且无成功 Profile，调用方的 20 秒期限尚未耗尽。
等待阶段使用 `exec sleep` 替换测试 shell，取消时直接终止该进程。
这两条测试确认已有预算与取消语义，未把共享 context 描述为新修复的 bug。

Go 全套普通/race 测试、vet、golangci-lint（0 issues）和 gofmt 检查通过；Probe 现有 12 个顶层行为测试，
包含表驱动子场景。本机真实 0.160.0 和缓存中的历史 0.154.0 均通过帮助预检，实际版本/摘要记录正确。
前端与容器探针未修改，沿用已有验证记录；第 22～30 步改动继续保留未提交。

help 仍是面向人的文本，不是稳定的机器协议。当前解析针对已验证的完整帮助布局；
无法识别的布局会拒绝预检，升级时需要更新适配并回归。帮助中的能力声明不能替代真实只读执行、
隔离边界和 Findings 输出契约的集成验收；这些仍由后续 M14 执行切片完成。

### M14 实现（第 31 步）：只读 Runner port 与 Codex 进程 adapter

- 状态：完成（2026-10-06）。用户审阅并要求 push 后，已推送第 22～30 步为 `04d9b15`，再开始本切片。
- 新增 [review.Runner](../backend/internal/review/runner.go)、[codex.ExecRunner](../backend/internal/codex/runner.go)
  和 [外部 CLI 行为测试](../backend/internal/codex/runner_test.go)。接口只接收工作区路径、固定 base/head 和 patch，
  返回已校验 Findings；二进制、模型、超时与权限选项来自部署配置。Java 可类比业务接口及 ProcessBuilder adapter。

#### 启动与每次执行的边界

部署必须配置存在的绝对 Workspace 根目录、模型和正数执行时限。构造 Runner 时先执行现有 Probe，
再检查根帮助中的 `--no-daemon`、`--ask-for-approval` 和 `--model`；三次元数据命令共用 10 秒启动预算。
Runner 的 Profile 返回实际版本、摘要与所需选项的副本；每次执行前再比对启动时记录的二进制摘要。
部署二进制改变后须重新预检、创建 Runner，以使升级与追溯记录一致；没有按指定版本号判断兼容性。

M14 的直接进程 profile 增加上述根选项要求。本机 0.160.0 已通过真实构造/启动预检；历史 0.154.0
不声明 `--no-daemon`，因此通用 exec Probe 仍可通过，但不满足本切片的直接进程 profile。
该控制项依据本机已安装 CLI 的根帮助实测；升级时仍须重新做能力与执行契约验收。

每次 Run 先检查完整小写 Git SHA、UTF-8 patch（最多 1 MiB）和实际 worktree 位置。
路径解析后必须是配置根目录直接下属 `workspace-*/worktree`，外部目录、相似前缀和指向外部的链接被拒绝。
Runtime 参数由 adapter 固定，业务文本通过 JSON stdin 输入；patch 中的 CLI 开关不会变成命令参数。

执行采用 `--no-daemon`、审批 never、显式模型、read-only、ephemeral、忽略用户配置/rules、
平台 Findings schema、最终输出路径及 JSONL 模式。每次生成自己的临时 HOME、CODEX_HOME 和 TMPDIR，
仅设置基础 PATH，不继承控制平面 GitLab token、个人模型 key 或个人登录文件。
输出与配置目录位于临时执行目录，成功和失败都清理；重复执行不会复用旧输出。
选项用途参考 [OpenAI 非交互模式](https://learn.chatgpt.com/docs/non-interactive-mode)，实际 profile 以预检结果为准。

CLI 进程运行期间使用部署时限及调用方 context。每次执行创建独立 POSIX 进程组，取消、超时和主进程
退出后终止该组的剩余进程。JSONL 与 stderr 当前只排空，不作为 Findings 或公开错误正文。
只有进程退出成功，才读取指定的最终输出文件，再调用现有 ParseFindings 绑定 base/head、校验和去重。

最终输出使用原子 no-follow、non-blocking 打开并检查描述符，只接受单链接普通文件；
拒绝符号链接、硬链接、目录和 FIFO。先检查文件大小，再以 256 KiB + 1 的读取上限防止文件增长越界；
平台解析仍执行完整 Findings 契约。非零退出即使写了合法 JSON，也不会返回成功结果。

| 失败类型 | 调用方可判断的结果 |
| --- | --- |
| 输入、引用或工作区路径不合法 | ErrInvalidRunInput；不启动评审 |
| 二进制与启动记录不符 | ErrIncompatibleRuntime；不启动评审 |
| 非零退出或进程无法启动 | ErrExecutionFailed |
| 缺失、链接或非普通输出文件 | ErrOutputUnavailable |
| 输出超出 256 KiB | ErrOutputTooLarge |
| JSON/字段/固定引用不合法 | ErrInvalidFindings |
| 调用方取消或执行超时 | context.Canceled / context.DeadlineExceeded |

所有失败都返回空 FindingsReport，没有部分成功数据。

#### 回归与真实预检

首个公共 port 测试因类型/实现缺失编译 RED，最小受控进程链路实现后 GREEN。
随后逐条复现并修正：输出超限未分类、链接读取到外部有效 JSON、非法引用启动后才拒绝、
二进制被替换后仍执行，以及取消 CLI 后子进程继续写文件。每个问题均经过真实失败回归再转绿；
硬链接也单独复现了返回成功的 RED，再增加描述符链接计数检查。

新增 13 个顶层 Runner 行为测试，含表驱动子场景。测试使用标准库 Python 3 的外部假 CLI，
直接观察参数、stdin、工作目录、schema、环境和最终结果，不 mock 自己的组件。
覆盖非零退出/缺失/非法/引用不符输出、FIFO、链接、两端大小边界、超时、启动后取消、进程组效果、
输入路径/编码边界、部署配置和能力缺失、二进制变化及重复执行隔离。
Go 全套普通/race 测试、vet、golangci-lint（0 issues）、gofmt、Linux/amd64 编译通过。
本机真实 0.160.0 的 NewExecRunner 启动预检通过，读取元数据后退出；本轮没有真实模型调用。
前端和容器探针未改动，沿用已有验证记录。用户于 2026-10-08 审阅本切片并批准提交推送。

当前 adapter 面向 Linux/macOS 的隔离 Worker 内部使用，尚未装配到 HTTP 服务。
CLI 只读参数、路径检查和进程组并不替代只读挂载、网络/凭据隔离及容器生命周期控制；
脱离进程组的程序与文件系统检查之间的竞态仍需 Worker 边界收口。
输入引用与 patch 的事实来源目前由调用方负责，本切片未验证 Git HEAD、changed-line 定位或缺陷正确性。
服务模型身份/网关、真实 Linux Codex 镜像、真实模型执行及输出质量仍待后续切片验证。

下一步将 Runner 接入 review 应用服务，从同租户的 Task 与 READY Workspace 获取固定输入，
在执行 I/O 前声明幂等请求，校验后创建 Findings Artifact 并记录结果事件；业务请求仍不能指定
二进制路径、模型凭据、patch 或安全选项。再实现隔离 Worker 和受控服务身份的真实模型联调。

### M14 实现（第 32 步）：Task/Workspace 审阅执行与 Findings 归档

- 状态：完成（2026-10-08）。用户批准 push 后，已推送第 31 步为 `50025c2`，再实现本切片。
- 新增 [执行应用服务](../backend/internal/review/execution.go)、[HTTP 入口](../backend/internal/httpapi/review_execution.go)
  和 [跨 Git/CLI/HTTP 行为回归](../backend/internal/httpapi/review_execution_test.go)；扩展 Artifact、Task 事件
  payload 与前端时间线。Java 可类比 Application Service 在调用外部执行器前登记操作表，再保存结果和领域事件。

#### 公共接口与状态

`POST /api/v1/tasks/{id}/review` 只接收 requestId、tenantId、idempotencyKey、expectedWorkspaceVersion。
未知 JSON 字段被拒绝，不能指定 binary、model、credentials、patch、SHA、sandbox 或本地路径；
沿用 64 KiB 请求体、256 字节标识符限制与结构化错误。GET 同一路径以 tenantId 查询最近一次执行。

前置检查确认同租户 Task 存在、类型 PR_REVIEW、准入状态 QUEUED，以及存在 READY、有 path 的 Workspace。
Workspace version 必须匹配，仓库 provider/id 和 base/head 必须与 Task 的固定引用一致。
路径、引用和 patch 全部由服务端选取，不能从浏览器提交的内容构造 RunInput。
输入校验与 Runner 返回后的平台 Findings 校验继续执行，成功内容先去重，再以规范 JSON 归档。

审阅执行单独使用 RUNNING / SUCCEEDED / FAILED / CANCELED 投影，包含 executionId、tenant/Task/Workspace、
Workspace version、base/head、开始/结束时间及成功 Artifact 或稳定失败码。
当前不扩展 Task 的 CREATED → QUEUED 准入状态机，也未引入持久队列或工作流引擎。
首次成功 POST 返回 201，成功重放返回 200；Location 指向该 Task 最近执行的 GET，并包含 tenantId。
最近执行 GET 与 Artifact metadata/content 读取都在 Store 范围内检查 tenant；不存在和其他 tenant 均为 404。

#### 执行前幂等与并发边界

幂等键作用域为 tenant + key，绑定 Task 和 expectedWorkspaceVersion，requestId 不参与操作身份。
首次调用在 Git diff 与 Runner I/O **之前**，于 Service 锁内登记 RUNNING 并追加 review.started。
相同键会先查询登记记录，成功返回第一次快照，失败返回第一次安全分类后的错误，
不重新读取 Git、启动模型、创建 Artifact 或追加事件；改变 Task/version 返回 409 idempotency_conflict。

同一键进行中返回 409 review_in_progress。同一 Task 使用不同键也只能有一个 RUNNING；
正在执行时被拒绝的新键不会登记，可在终态后再次使用。
不同 Task（包括不同 tenant 使用相同 key）可以同时执行，Git/Runner I/O 不持有 Service 锁。
同一 Task 的终态后允许使用新 key 重新审阅，生成新的执行和结果；旧 key 仍重放原结果。
因此需要区分“重试同一个请求”和“明确开启新的尝试”，新 key 也可能产生新的模型消耗。

#### 结果、失败与事件

成功保存 `PR_REVIEW_FINDINGS / application/json` Artifact，已有 checksum、不可变副本和租户读取规则继续生效。
其幂等 namespace 由应用层固定为 review-execution，与用户发起的 diff 归档分开；
避免用户选用类似 `review:review-1` 的归档键阻塞内部 Findings 创建，namespace 不暴露为 HTTP 参数。

成功事件顺序为 review.started → artifact.created → review.succeeded；失败为开始 → review.failed，
调用方取消为开始 → review.canceled。Review payload 只包含执行/Workspace 坐标、状态、Artifact ID 或失败码。
同一次尝试的事件使用首个执行请求的 requestId 作为 causationId；重放仅回显当前请求的 X-Request-ID。
事件与执行返回值中的可变指针都复制，不能通过读取结果修改已有快照。
前端时间线支持四种 review 事件，显示状态、执行 ID、Workspace ID、成功 Artifact ID 或失败码。

Git 读取阶段和 Runner 阶段都检查调用方 context；取消、超时、非零退出、输出不合法或引用不符均无部分 Findings。
失败登记保留至进程结束，使用相同 key 不会自动重跑；新 key 可发起新尝试。
Git/模型内部诊断不进入公开失败正文、执行投影或新接口日志，只记录稳定错误码和业务坐标。
Runtime 的子进程时限仍由部署配置控制，整个应用操作还没有独立的统一 wall-clock deadline。

#### RED → GREEN 与验证

先写一条贯穿真实 Git、外部 CLI 和真实 HTTP 的成功回归，因缺少公共装配入口编译 RED；
补齐最小 Task/Workspace → Runner → Artifact/事件链路后 GREEN。
随后逐条复现并修正：成功重放重新读取已经移动的 worktree、不同 key 并发启动同一 Task、
用户 diff 归档键阻塞内部 Findings、Git 输入阶段取消被误分类为 diff_unavailable，以及未知运行配置字段被静默接受。
前端回归先复现 review 事件没有摘要，再补齐类型与展示转为 GREEN。

新增 14 个顶层 HTTP 行为测试（含表驱动子场景），使用真实本地 Git/clone/diff、外部 Python 假 Codex CLI，
以及一次真实 httptest HTTP 服务。只替代 GitLab 和模型 CLI 等系统外部接缝，不替代平台 Store、Manager、Service 或 Runner。
覆盖固定输入、Findings 去重与空报告、内容/checksum/tenant 读取、失败与失败重放、新尝试、
模型取消/超时、Git 输入取消、准入/JSON 边界、默认不装配 Runner、跨 Task/key/tenant 幂等、
独立 Task 并发及 12 个同时到达的同键请求只执行一次。前端新增一条可见行为回归。
Go 全套普通/race 测试、vet、golangci-lint（0 issues）、gofmt 与 Linux/amd64 编译通过。
前端 lint、类型检查、9 个文件中的 50 条行为测试及生产构建通过；真实 HTTP/Git/外部 CLI 链路回归通过。
本轮没有调用真实模型，也没有启用个人登录态或 Skills；容器预检脚本未改动，沿用已有验证记录。

#### 装配边界与下一步

`NewHandlerWithReviewServices` 是可注入部署 Runner 的集成入口。已有构造函数和 cmd/api 默认传入空 Runner，
满足 Task/Workspace 前置条件后返回 503 review_unavailable，并在 Git/模型 I/O 和执行登记前退出。
真实模型认证、Linux Codex Worker 镜像、只读挂载、网络/凭据边界与 Worker 生命周期尚未装配到这条链路。

本切片已由用户审阅并批准本次提交推送。下一步接入隔离 Worker，补齐场景服务模型身份/受控配置及实际运行记录，
再做真实模型只读执行与 Findings 质量验收。Skills 继续复用 Codex 原生机制，但当前没有向临时 HOME
交付场景 Skill，也不会自动继承本机个人 Skill、MCP 配置或登录文件。审阅触发与 Findings 内容展示的 UI 尚待后续切片。

## 7. 常用验证命令

具体启动命令和 curl 示例见项目根目录 README。开发完成前至少运行：

```bash
cd backend
go test ./...
go test -race ./...
go vet ./...
golangci-lint config verify
golangci-lint run ./...

cd ../frontend
node --run lint
node --run typecheck
node --run test
node --run build
```

## 8. 当前限制

- CI 配置已推送，ESLint/Go lint 已本地验证；已知的账户账单锁定问题按当前协作约定忽略。
- Task ID 是单进程递增值，不适合多实例部署。
- Task 数据重启即丢失。
- 当前 tenant 仍来自请求，Task 读取已按租户过滤，但还没有身份认证或租户授权。
- 幂等索引只存在于单个 Go 进程，重启或多实例部署后不能提供全局唯一保证。
- request ID 已用于 503/500 结构化日志，但尚未覆盖所有请求，也没有审计存储或全链路追踪。
- Task/Workspace/Artifact 事件与 ID 只存在于单进程内存；跨 Store 更新不是原子事务，
  它们还不是事务 Outbox，也没有发布到 Event Bus。
- Review 执行及幂等记录也只在单进程内存中；GET 只返回每个 Task 最近执行，暂无按 executionId 查询的历史接口。
  Review、Artifact 与事件提交不是持久事务，尚无崩溃恢复、持久调度、完整 Task 执行状态机或统一执行 deadline。
  默认启动未装配 Runner；只读 Worker、服务模型身份/网关、实际模型输出质量及场景 Skills 交付未接入。
- M7 的 sequence 只表示单个 Task 内的时间线顺序，不提供跨 Task 或分布式全局顺序。
- Workspace 元数据和状态仍只在内存；真实目录已创建，但没有启动恢复、共享 clone cache、磁盘配额、
  清理 API、runtimeId、挂载或孤儿目录回收。
- Workspace prepare 当前在一个 HTTP 请求内同步执行；大仓库尚未进入持久化异步 Activity，也没有
  独立的 wall-clock timeout、进度事件或取消后的 reconciliation。
- GitLab 验证只确认项目和两个 commit 可读取，尚未确认 base/head 祖先关系、Merge Request 归属或
  diff 规模；也没有缓存、重试、限流、熔断与持久化验证证据。
- GitLab token 暂由控制平面环境变量提供，并只进入受控 clone/fetch 子进程；尚未接入 Credential
  Broker、短时凭据与自动轮换。
- 固定版本 diff 仍随 HTTP JSON 即时返回最多 1 MiB；页面只渲染前 65,536 个字符，但目前还没有
  分片、大文件引用、内容分类与保留策略。
- Artifact 当前是单进程内存 Store，已经具备不可变副本、checksum、幂等和 tenant 范围读取，但进程
  重启即丢失，也没有对象存储、数据库索引、加密、保留期、分页列表或垃圾回收。
- Repository Connector 目前只支持 GitLab；真实企业 GitLab 凭据联调尚未执行。
- Task 目前只支持 `CREATED → QUEUED`；`QUEUED` 只是状态投影，还没有真实队列、调度器或
  工作流执行。
- 列表一次返回进程内的全部 Task，尚无分页协议。
