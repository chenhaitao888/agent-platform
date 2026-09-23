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

当前代码只覆盖健康检查、持有不可变仓库引用的 Task 幂等创建、查询、第一条状态迁移、Task
事件时间线、GitLab 仓库/commit 只读验证，以及 Workspace 登记、bare clone、detached worktree、
真实 path 与对应前端闭环。数据库、工作流执行、Codex app-server、Credential Broker、鉴权与其他
企业 Connector 仍未接入。

## 3. 当前目录与职责

```text
agent-platform/
├── backend/
│   ├── cmd/api/                 # Go 进程入口，类似 Java main 启动类
│   └── internal/
│       ├── connector/gitlab/    # GitLab 验证与可信 clone URL adapter
│       ├── gitworkspace/        # 受控 Git 子进程、bare clone 与 worktree
│       ├── httpapi/             # HTTP 路由和 JSON 适配，类似 Controller 层
│       ├── repository/          # 验证接口与 provider 无关的错误分类
│       ├── task/                # Task 模型与内存 Store
│       └── workspace/           # Workspace 登记、准备状态机与 Manager
├── frontend/src/                # React 页面、Task UI 和组件测试
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
- 当前 tenant 仍来自请求，Task 读取已按租户过滤，但还没有身份认证或租户授权。
- 幂等索引只存在于单个 Go 进程，重启或多实例部署后不能提供全局唯一保证。
- request ID 已用于 503/500 结构化日志，但尚未覆盖所有请求，也没有审计存储或全链路追踪。
- Task 事件与 ID 只存在于单进程内存；它们还不是事务 Outbox，也没有发布到 Event Bus。
- M7 的 sequence 只表示单个 Task 内的时间线顺序，不提供跨 Task 或分布式全局顺序。
- Workspace 元数据和状态仍只在内存；真实目录已创建，但没有启动恢复、共享 clone cache、磁盘配额、
  清理 API、runtimeId、挂载或孤儿目录回收。
- Workspace prepare 当前在一个 HTTP 请求内同步执行；大仓库尚未进入持久化异步 Activity，也没有
  独立的 wall-clock timeout、进度事件或取消后的 reconciliation。
- GitLab 验证只确认项目和两个 commit 可读取，尚未确认 base/head 祖先关系、Merge Request 归属或
  diff 规模；也没有缓存、重试、限流、熔断与持久化验证证据。
- GitLab token 暂由控制平面环境变量提供，并只进入受控 clone/fetch 子进程；尚未接入 Credential
  Broker、短时凭据与自动轮换。
- 固定版本 diff 的预览仍随 HTTP JSON 即时返回，归档 Artifact 也受 1 MiB 上限约束；目前还没有分片、
  大文件引用、内容分类与保留策略。
- Artifact 当前是单进程内存 Store，已经具备不可变副本、checksum、幂等和 tenant 范围读取，但进程
  重启即丢失，也没有对象存储、数据库索引、加密、保留期、分页列表或垃圾回收。
- Repository Connector 目前只支持 GitLab；真实企业 GitLab 凭据联调尚未执行。
- Task 目前只支持 `CREATED → QUEUED`；`QUEUED` 只是状态投影，还没有真实队列、调度器或
  工作流执行。
- 列表一次返回进程内的全部 Task，尚无分页协议。
