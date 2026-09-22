# Agent Platform

这是“企业研发智能体与工作流平台”的最小可运行骨架。当前只完成：

- Go HTTP 服务与 `GET /healthz`
- 内存版 Task 幂等创建、查询、最近列表、状态迁移与事件时间线
- React + TypeScript 状态页、Task 创建表单、最近列表、准入操作与事件展示
- Go 接口测试与 React 组件测试

这一阶段没有接入数据库、Temporal、Codex app-server、Connector 或企业凭据。

完整的开发步骤、设计取舍、Java 类比和每一阶段验证记录见
[Agent Platform 开发手册](docs/development-handbook.md)。

## 目录

```text
agent-platform/
├── backend/
│   ├── cmd/api/                 # Go 进程入口，类似 Java 的 main 启动类
│   └── internal/
│       ├── httpapi/             # HTTP 路由和测试，类似 Web/Controller 层
│       └── task/                # Task 模型与内存仓库，类似精简的领域/Repository 层
└── frontend/
    └── src/                     # 状态页、Task 表单、样式和组件测试
```

## 启动后端

本项目使用 Go 1.27.1。请确认 `go version` 显示 `go1.27.1 darwin/arm64`
或对应平台的 Go 1.27.1。

```bash
cd backend
go run ./cmd/api
```

另开终端验证：

```bash
curl -i http://localhost:8080/healthz
```

预期响应：

```json
{"status":"ok","service":"agent-platform-api"}
```

### 创建、查询和列出 Task

创建 Task：

```bash
curl -i \
  -X POST http://localhost:8080/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "requestId":"req-01",
    "idempotencyKey":"review:project-7:mr-42:head-a13f",
    "tenantId":"tenant-local",
    "type":"PR_REVIEW",
    "goal":"Review pull request 42"
  }'
```

成功时返回 `201 Created`、`Location: /api/v1/tasks/{id}`，以及类似下面的 JSON：

```json
{
  "id": "task-1",
  "tenantId": "tenant-local",
  "idempotencyKey": "review:project-7:mr-42:head-a13f",
  "type": "PR_REVIEW",
  "goal": "Review pull request 42",
  "status": "CREATED",
  "version": 1,
  "createdAt": "2026-09-21T10:00:00Z"
}
```

服务端会回显 `X-Request-ID`。同一租户使用同一 `idempotencyKey` 和相同任务内容
重放时返回 `200 OK` 以及原 Task；如果复用该 key 却改变 `type` 或 `goal`，返回
`409 Conflict`。不同租户可以使用相同的幂等键。

按 ID 查询：

```bash
curl -i http://localhost:8080/api/v1/tasks/task-1
```

查询最近 Task（最新创建的排在前面）：

```bash
curl -i http://localhost:8080/api/v1/tasks
```

响应使用 `items` 包装数组，为以后增加分页信息保留空间：

```json
{
  "items": [
    {
      "id": "task-1",
      "tenantId": "tenant-local",
      "idempotencyKey": "review:project-7:mr-42:head-a13f",
      "type": "PR_REVIEW",
      "goal": "Review pull request 42",
      "status": "CREATED",
      "version": 1,
      "createdAt": "2026-09-21T10:00:00Z"
    }
  ]
}
```

将一个 `CREATED` Task 准入为 `QUEUED`：

```bash
curl -i \
  -X PATCH http://localhost:8080/api/v1/tasks/task-1 \
  -H 'Content-Type: application/json' \
  -d '{
    "requestId":"req-02",
    "idempotencyKey":"queue:tenant-local:task-1:v1",
    "tenantId":"tenant-local",
    "expectedVersion":1,
    "status":"QUEUED"
  }'
```

成功时返回 `200 OK`，状态变为 `QUEUED`，`version` 从 1 增加到 2。完全相同的迁移使用
同一 `idempotencyKey` 重放，会返回同一个 version 2 结果；新的请求如果仍携带旧的
`expectedVersion: 1`，返回 `409 version_conflict`。版本正确但状态迁移不合法时，返回
`409 invalid_transition`。

查询 Task 事件时间线：

```bash
curl -i \
  'http://localhost:8080/api/v1/tasks/task-1/events?tenantId=tenant-local'
```

响应按 `sequence` 从小到大返回该 Task 已经发生的事实：

```json
{
  "items": [
    {
      "schemaVersion": "1.0",
      "eventId": "evt-1",
      "eventType": "task.created",
      "occurredAt": "2026-09-22T01:00:00Z",
      "tenantId": "tenant-local",
      "taskId": "task-1",
      "sequence": 1,
      "correlationId": "task-1",
      "causationId": "req-01",
      "payload": { "status": "CREATED", "version": 1 }
    }
  ]
}
```

Task 准入后会追加 `task.queued`。创建或准入请求被幂等重放时，不会重复追加事件。
`tenantId` 当前是显式查询参数；它只能提供最小租户范围校验，不能替代后续的身份认证与授权。

这里的 `QUEUED` 目前只是 Task 的状态投影，表示“已准入”；还没有真实消息队列或执行器。
Task 只保存在 Go 进程内存中，重启服务后数据会丢失。本阶段也还没有数据库、完整状态机
或工作流执行。

运行后端测试：

```bash
cd backend
go test ./...
```

## 启动前端

本机需要 Node.js 22.13–22.x。首次运行先安装依赖：

```bash
cd frontend
npm install
node --run dev
```

浏览器打开 Vite 输出的本地地址。开发服务器会把健康检查
`/api/healthz` 转发到后端 `/healthz`，并把 `/api/v1/*` 原样转发到 Go
业务接口，所以需要同时启动 Go 后端。

页面会先调用 `GET /api/v1/tasks` 加载最近 Task。“创建 Task”表单调用
`POST /api/v1/tasks`；提交成功后会显示后端生成的 Task ID、状态和目标，并立即
更新最近列表。前端在 Phase 0 固定使用 `tenant-local`，并为每次提交生成 request ID
和 idempotency key。`CREATED` Task 会显示“加入队列”按钮，成功后用后端返回的
`QUEUED / version 2` 替换旧对象。请求期间按钮会禁用，后端校验错误或网络错误会直接
显示在对应区域。列表请求晚到时，前端按 Task ID 和 version 合并，不让旧快照覆盖新状态。
每个 Task 卡片的“查看事件”按钮会按需加载事件时间线，不会在列表加载时为每个 Task 自动
发起额外请求。

运行前端测试与构建：

```bash
cd frontend
node --run test
node --run build
```

`node --run` 是 Node.js 22 自带的 package script 运行方式。在当前这台使用
universal Node 的 macOS 上，它能稳定保持 arm64 架构；脚本内容与常见的
`npm run dev/test/build` 完全相同。

## Java 开发者速记

- `go.mod` 类似 Maven/Gradle 的工程与依赖声明，但没有 Maven 生命周期。
- `cmd/api/main.go` 类似含 `public static void main` 的启动类。
- `http.Handler` 类似 Servlet 请求处理契约，但 Go 的接口是隐式实现的。
- React 组件不是一个长期存活、可随意改字段的 Java UI 对象；它是根据输入和状态重新计算 UI 的函数。
- `useState` 保存组件局部状态，必须通过 setter 更新；不要像普通 Java bean 一样直接改字段。
- `Promise` 和 `async/await` 可类比 `CompletableFuture`，写法更像同步代码，但底层仍是异步流程。
- Task 的 `version` 类似 JPA 的 `@Version`：更新方声明自己看到的版本，版本过期就返回冲突，
  防止旧对象覆盖新对象。
- Task 事件类似只追加的 Domain Event/Audit Log；当前和 Task 共用一把内存锁，未来落数据库时
  应由业务更新与 Outbox 在同一事务中提交。
