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

## 2. 当前技术基线

| 范围 | 当前选择 | Java 类比 |
| --- | --- | --- |
| 后端 | Go 1.27.1、标准库 `net/http` | JDK + Servlet/Spring MVC 的最小 HTTP 层 |
| 前端 | React 19、TypeScript、Vite | 浏览器侧表示层；React 组件近似“根据状态生成视图”的函数 |
| 后端测试 | Go `testing`、`httptest` | JUnit + MockMvc 的轻量组合 |
| 前端测试 | Vitest、Testing Library、jsdom | JUnit + 面向用户行为的 UI 测试 |
| 持久化 | 进程内存 | 临时的 InMemoryRepository，重启即丢失 |

当前代码只覆盖健康检查、Task 幂等创建、按 ID 查询、最近列表、第一条状态迁移、Task 事件
时间线和对应前端闭环。数据库、工作流执行、Codex app-server、Connector、鉴权与企业凭据
仍未接入。

## 3. 当前目录与职责

```text
agent-platform/
├── backend/
│   ├── cmd/api/                 # Go 进程入口，类似 Java main 启动类
│   └── internal/
│       ├── httpapi/             # HTTP 路由和 JSON 适配，类似 Controller 层
│       └── task/                # Task 模型与内存 Store
├── frontend/src/                # React 页面、Task UI 和组件测试
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
- 当前产生两类事件：Task 首次创建时的 `task.created`，以及成功准入时的 `task.queued`。
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

## 7. 常用验证命令

具体启动命令和 curl 示例见项目根目录 README。开发完成前至少运行：

```bash
cd backend
go test ./...
go test -race ./...
go vet ./...

cd ../frontend
node --run test
node --run build
```

## 8. 当前限制

- Task ID 是单进程递增值，不适合多实例部署。
- Task 数据重启即丢失。
- 当前 tenant 只是请求与数据字段，还没有身份认证、租户授权或按租户过滤查询。
- 幂等索引只存在于单个 Go 进程，重启或多实例部署后不能提供全局唯一保证。
- request ID 目前只用于响应关联，还没有进入结构化日志和审计存储。
- Task 事件与 ID 只存在于单进程内存；它们还不是事务 Outbox，也没有发布到 Event Bus。
- M7 的 sequence 只表示单个 Task 内的时间线顺序，不提供跨 Task 或分布式全局顺序。
- Task 目前只支持 `CREATED → QUEUED`；`QUEUED` 只是状态投影，还没有真实队列、调度器或
  工作流执行。
- 列表一次返回进程内的全部 Task，尚无分页协议。
