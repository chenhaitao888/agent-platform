# Agent Platform

这是“企业研发智能体与工作流平台”的最小可运行骨架。当前只完成：

- Go HTTP 服务与 `GET /healthz`
- 内存版 Task 幂等创建、不可变仓库引用、查询、最近列表、状态迁移与事件时间线
- GitLab 仓库/commit 可信验证，以及 Workspace 登记、bare clone、detached worktree 和状态查询
- READY Workspace 的固定 base/head Git diff，以及 React 页面中的受控差异查看
- 固定 diff 的幂等 Artifact 归档、元数据查询和租户范围内容读取
- React + TypeScript 状态页、Task 操作、事件、Workspace 准备与真实路径展示
- Go 接口测试与 React 组件测试

这一阶段没有接入数据库、Temporal、Codex app-server 或 Credential Broker。GitLab 只读验证和
Workspace 准备已经具备最小 adapter，服务令牌暂由控制平面进程环境变量提供。

完整的开发步骤、设计取舍、Java 类比和每一阶段验证记录见
[Agent Platform 开发手册](docs/development-handbook.md)。

## 目录

```text
agent-platform/
├── backend/
│   ├── cmd/api/                 # Go 进程入口，类似 Java 的 main 启动类
│   └── internal/
│       ├── connector/gitlab/    # GitLab HTTPS 读取 adapter
│       ├── artifact/            # 不可变结果元数据、内容与幂等创建
│       ├── gitworkspace/        # 受控 Git 子进程、bare clone 与 worktree
│       ├── httpapi/             # HTTP 路由和测试，类似 Web/Controller 层
│       ├── repository/          # Repository Reference 验证接口与错误分类
│       ├── review/              # 从 READY Workspace 读取不可变评审输入
│       ├── task/                # Task 模型与内存仓库，类似精简的领域/Repository 层
│       └── workspace/           # Workspace 登记、准备状态机与路径所有权
├── CONTEXT.md                   # 领域统一语言，不包含实现细节
└── frontend/
    └── src/                     # 状态页、Task 表单、样式和组件测试
```

## 启动后端

本项目使用 Go 1.27.1。请确认 `go version` 显示 `go1.27.1 darwin/arm64`
或对应平台的 Go 1.27.1。

Workspace 登记和准备需要同时配置 GitLab 地址、只读服务令牌和一个绝对路径的 Workspace 根目录。
Base URL 必须使用 HTTPS，不能包含用户名、密码、query 或 fragment，也不包含 `/api/v4`；平台会
自行拼接 API 路径。不要把 token 写进代码、README 或命令行参数：

```bash
export AGENT_PLATFORM_GITLAB_BASE_URL='https://gitlab.example.com'
read -s -p 'GitLab token: ' AGENT_PLATFORM_GITLAB_TOKEN
printf '\n'
export AGENT_PLATFORM_GITLAB_TOKEN
export AGENT_PLATFORM_WORKSPACE_ROOT='/var/lib/agent-platform/workspaces'
```

三个变量都未配置时，服务仍可启动，健康检查和 Task 接口仍可使用，但 Workspace 操作会失败关闭。
只配置其中一部分时，服务拒绝启动。根目录必须是绝对路径，且不能是文件系统根目录；服务启动时会
以 `0700` 创建不存在的目录。当前 token 只属于控制平面及其受控 Git 子进程，不会写入 clone URL、
命令参数、Workspace、Runtime 或 Codex shell；后续仍要接入 Credential Broker。

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

所有写接口的 JSON body 上限是 64 KiB；`goal` 上限 4 KiB，`requestId`、`tenantId`、
`idempotencyKey`、`type`、`repositoryId` 上限各 256 字节。`repositoryId` 还必须是纯数字的
GitLab project ID（如 `42`），或 `namespace/project` 路径（如 `platform/project-7`）；
路径只接受 ASCII 字母、数字、`.`、`_`、`-` 和分隔用的 `/`，不接受空路径段、`.` 段或连续的
`..`。填写原始路径，不要预先 URL 编码。格式或长度不合规返回 `400 validation_error`。
服务设置读请求 15 秒、写超时 5 分钟、空闲连接 60 秒；
收到 SIGINT/SIGTERM 后停止接收新连接，最多等待 60 秒让在途请求完成。
当前 Workspace Prepare 仍在单个 HTTP 请求中同步 clone，大仓库可能碰到写超时或超过停机宽限；
改为异步 Activity 后需要重新收紧和校准这些时限。

服务端把 503/500 的原因写为 stderr 中的 JSON 日志，包含 `requestId`、`tenantId`、`taskId`、
错误码与内部原因；HTTP 响应仍只给稳定文案。写请求使用 body 中的 `requestId`；读取 Workspace diff
时可传 `X-Request-ID` 请求头，便于定位该次读取。代码不主动记录请求 body 或幂等键，并会遮盖
错误文本中当前 GitLab token 的直接回显；日志仍可能含仓库地址等内部信息，需要限制访问。

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
    "goal":"Review pull request 42",
    "repository":{
      "provider":"gitlab",
      "repositoryId":"platform/project-7",
      "baseSha":"1111111111111111111111111111111111111111",
      "headSha":"2222222222222222222222222222222222222222"
    }
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
  "repository": {
    "provider": "gitlab",
    "repositoryId": "platform/project-7",
    "baseSha": "1111111111111111111111111111111111111111",
    "headSha": "2222222222222222222222222222222222222222"
  },
  "status": "CREATED",
  "version": 1,
  "createdAt": "2026-09-21T10:00:00Z"
}
```

服务端会回显 `X-Request-ID`。同一租户使用同一 `idempotencyKey` 和相同任务内容
重放时返回 `200 OK` 以及原 Task；如果复用该 key 却改变 `type`、`goal` 或仓库引用，返回
`409 Conflict`。不同租户可以使用相同的幂等键。

Phase 0 只接受 `gitlab` provider。`baseSha` 和 `headSha` 必须是 40 或 64 位十六进制对象 ID；
这里仅校验格式，尚未连接 Git 平台验证仓库和 commit 是否真实存在。

按 ID 查询：

```bash
curl -i 'http://localhost:8080/api/v1/tasks/task-1?tenantId=tenant-local'
```

查询最近 Task（最新创建的排在前面）：

```bash
curl -i 'http://localhost:8080/api/v1/tasks?tenantId=tenant-local'
```

这两条 Task 读取接口都要求 `tenantId`。列表只返回该租户的 Task；按 ID 查询其他租户的
Task 与查询不存在的 ID 一样返回 404。创建响应中的 `Location` 也包含 `tenantId`，可直接用于
读取。当前 `tenantId` 仍由调用方提供，尚未接入认证和授权，不能把它当作真实身份凭证。

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
      "repository": {
        "provider": "gitlab",
        "repositoryId": "platform/project-7",
        "baseSha": "1111111111111111111111111111111111111111",
        "headSha": "2222222222222222222222222222222222222222"
      },
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
      "schemaVersion": "2.0",
      "eventId": "evt-1",
      "eventType": "task.created",
      "occurredAt": "2026-09-22T01:00:00Z",
      "tenantId": "tenant-local",
      "taskId": "task-1",
      "sequence": 1,
      "correlationId": "task-1",
      "causationId": "req-01",
      "payload": { "task": { "status": "CREATED", "version": 1 } }
    }
  ]
}
```

Task 准入后会追加 `task.queued`；Workspace 登记、开始准备、准备成功或失败，分别追加
`workspace.registered`、`workspace.preparing`、`workspace.ready` 或
`workspace.preparation_failed`；归档固定 diff 后追加 `artifact.created`。事件的
`schemaVersion` 已从 `1.0` 升为 `2.0`：Task payload 现在是 `{"task":{"status":"QUEUED","version":2}}`，
Workspace payload 是 `{"workspace":{"workspaceId":"workspace-1","state":"READY","version":3}}`，
Artifact payload 包含 `artifactId`、`workspaceId`、`type`、`mediaType`、`sha256`、`sizeBytes`，
不包含 diff 正文。`workspace.preparation_failed` 记录恢复后的 `REGISTERED` 状态与新版本，
不暴露内部 Git 错误。客户端若依赖旧的扁平 `payload.status/version`，须同步迁移。
创建、准入、登记、准备成功和归档请求被幂等重放时，不会重复追加事件。
`tenantId` 当前是显式查询参数；它只能提供最小租户范围校验，不能替代后续的身份认证与授权。

### 登记和查询 Workspace 元数据

Task 必须先进入 `QUEUED`，才能登记 Workspace：

```bash
curl -i \
  -X POST http://localhost:8080/api/v1/tasks/task-1/workspace \
  -H 'Content-Type: application/json' \
  -d '{
    "requestId":"req-03",
    "idempotencyKey":"workspace:tenant-local:task-1",
    "tenantId":"tenant-local"
  }'
```

Workspace Manager 从 Task 读取仓库与不可变 SHA，调用方不再重复提交。首次成功返回
`201 Created` 和 `REGISTERED / version 1`。同一幂等键、同一 Task 重放返回
`200 OK` 以及原 Workspace；同一 Task 使用新 key 再登记返回
`409 workspace_already_exists`。同一租户使用同一 key 登记另一个 Task 时返回
`409 idempotency_conflict`。

首次登记前，GitLab adapter 会通过 HTTPS 依次确认项目、base commit 和 head commit。项目不存在或
不可访问时返回 `422 repository_not_found`；两个 commit 不存在时分别返回
`422 base_commit_not_found` 或 `422 head_commit_not_found`；认证失败、网络错误、GitLab 5xx 或
重定向返回 `503 repository_verification_unavailable`。adapter 不跟随重定向，避免把
`PRIVATE-TOKEN` 转发到其他地址。

查询 Workspace：

```bash
curl -i \
  'http://localhost:8080/api/v1/tasks/task-1/workspace?tenantId=tenant-local'
```

`REGISTERED` 表示仓库标识和不可变 SHA 已经 GitLab 验证并登记。处于这个状态时还没有 clone 仓库、
创建工作目录、挂载文件系统或启动 Runtime，因此它不等于“Workspace 已准备好执行”。

### 准备 Workspace

登记完成后，使用当前 `version` 发起准备：

```bash
curl -i \
  -X POST http://localhost:8080/api/v1/tasks/task-1/workspace/prepare \
  -H 'Content-Type: application/json' \
  -d '{
    "requestId":"req-04",
    "idempotencyKey":"workspace-prepare:tenant-local:task-1:v1",
    "tenantId":"tenant-local",
    "expectedVersion":1
  }'
```

准备过程依次经历 `REGISTERED / version 1 → PREPARING / version 2 → READY / version 3`。平台在
`AGENT_PLATFORM_WORKSPACE_ROOT/workspace-1/` 下创建 `repository.git` bare clone，再创建
`worktree/`，并把它以 detached HEAD 固定到 Task 的 `headSha`。成功响应包含真实 `path`：

```json
{
  "id": "workspace-1",
  "state": "READY",
  "version": 3,
  "path": "/var/lib/agent-platform/workspaces/workspace-1/worktree"
}
```

Git 准备器和差异读取器也使用同一个 Workspace root：只接受该 root 直属的
`workspace-{id}/worktree`，差异读取前还会检查实际路径，拒绝跳到 root 外的符号链接。

GitLab 返回的 clone URL 必须是 HTTPS、不能含内嵌凭据，并且必须与已配置的 GitLab Base URL
同源。Git token 通过临时 `GIT_ASKPASS` 和白名单环境传给 clone；clone 后先用空凭据环境
检查 base/head 对象，只在缺失时才向远端 fetch 缺失的 SHA，然后立即删除 helper；
这个 helper 由 Go 临时写到 `workspace-{id}/git-askpass.sh`，脚本模板位于
`backend/internal/gitworkspace/preparer.go`，并不是需要手工准备的部署文件。`cat-file` 和
`worktree add` 不再携带 token。失败时平台删除本次新建的半成品目录，并把 Workspace 恢复为
`REGISTERED`，但 version 增加到 3，调用方应重新 GET 后再重试。

同一准备幂等键和相同输入重放不会再次 clone；复用该 key 改变 Task 或 `expectedVersion` 返回
`409 idempotency_conflict`。版本过期返回 `409 version_conflict`，状态不允许返回
`409 invalid_workspace_state`，Git/API 失败返回稳定的 `503 workspace_preparation_failed`，不会把
内部 Git 输出返回给调用方。

### 查看固定 base/head 差异

Workspace 进入 READY 后，可以读取 Task 创建时已经固定的两个 commit 之间的补丁：

```bash
curl -i \
  'http://localhost:8080/api/v1/tasks/task-1/workspace/diff?tenantId=tenant-local'
```

请求没有 `baseSha`、`headSha` 或本地路径参数。服务端从 READY Workspace 读取这些值，再执行受控的
本地 Git 命令；调用方不能把评审目标临时换成其他 revision。响应包含补丁本身和可校验元数据：

```json
{
  "taskId": "task-1",
  "workspaceId": "workspace-1",
  "baseSha": "1111111111111111111111111111111111111111",
  "headSha": "2222222222222222222222222222222222222222",
  "mediaType": "text/x-diff",
  "sha256": "64位十六进制摘要",
  "sizeBytes": 123,
  "patch": "diff --git ..."
}
```

Git 使用参数数组运行，不经过 shell，并关闭 external diff 与 textconv。子进程使用与 Workspace 准备
相同的环境白名单，但凭据为空。预览接口仍会在 JSON 中直接返回最多 1 MiB 的补丁；需要保存时，使用
下方的 Artifact 接口归档成不可变引用。超过限制返回稳定的 `413 diff_too_large`；更大的内容以后再接
对象存储、分片或流式读取。

### 把固定差异归档为 Artifact

查看 diff 后，可以使用 READY Workspace 当前版本创建不可变 Artifact：

```bash
curl -i \
  -X POST http://localhost:8080/api/v1/tasks/task-1/artifacts/diff \
  -H 'Content-Type: application/json' \
  -d '{
    "requestId":"req-05",
    "idempotencyKey":"artifact-diff:v1:task-1:workspace-1:v3",
    "tenantId":"tenant-local",
    "expectedWorkspaceVersion":3
  }'
```

浏览器不上传 patch。服务端重新从 READY Workspace 的固定 base/head 生成 diff，再创建 Artifact。首次
创建返回 `201 Created`，同一幂等键和相同内容重放返回 `200 OK` 与原 Artifact；复用 key 改变归属或
内容返回 `409 idempotency_conflict`。响应示例：

```json
{
  "id": "artifact-1",
  "tenantId": "tenant-local",
  "taskId": "task-1",
  "workspaceId": "workspace-1",
  "type": "REPOSITORY_DIFF",
  "mediaType": "text/x-diff",
  "sha256": "64位十六进制摘要",
  "sizeBytes": 123,
  "createdAt": "2026-09-22T12:00:00Z"
}
```

查询元数据和读取原始内容：

```bash
curl -i \
  'http://localhost:8080/api/v1/artifacts/artifact-1?tenantId=tenant-local'

curl -i \
  'http://localhost:8080/api/v1/artifacts/artifact-1/content?tenantId=tenant-local'
```

内容响应使用 `text/x-diff`，并带 SHA-256 `ETag`、`Content-Length` 和
`X-Content-Type-Options: nosniff`。其他 tenant 查询同一 ID 时返回 404。

当前 Artifact Store 在内存中：内容不会因为调用方修改 Go `[]byte` 而改变，但服务进程重启后仍会
丢失。这个里程碑先固定 API、不可变性、幂等和租户边界，尚未接入 S3、MinIO 或数据库。

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

页面会先调用 `GET /api/v1/tasks?tenantId=tenant-local` 加载最近 Task。“创建 Task”表单调用
`POST /api/v1/tasks`；表单同时采集仓库 ID 与不可变 base/head SHA，提交成功后会显示后端生成的
Task ID、状态、仓库和目标，并立即更新最近列表。前端在 Phase 0 固定使用 `tenant-local` 与
GitLab provider。每次提交都有新的 request ID；同一份未修改的 Task 表单在当前页面内重试会沿用
原幂等键，修改表单或成功后再次提交才生成新键。准入、Workspace 登记和准备则由 Task ID、Workspace
版本等稳定坐标生成幂等键。`CREATED` Task 会显示“加入队列”按钮，成功后用后端返回的
`QUEUED / version 2` 替换旧对象。请求期间按钮会禁用，后端校验错误或网络错误会直接
显示在对应区域。列表请求晚到时，前端按 Task ID 和 version 合并，不让旧快照覆盖新状态。
每个 Task 卡片的“查看事件”按钮会按需加载事件时间线，不会在列表加载时为每个 Task 自动
发起额外请求。`QUEUED` Task 还会显示“查看 Workspace”：已有记录时展示仓库和 SHA；尚未
登记时展示 Task 已固定的仓库引用和一个登记按钮，不会让用户重复输入。REGISTERED Workspace
会显示“准备 Workspace”；成功后页面展示 READY、后端记录的真实工作目录，并允许按需查看固定
base/head 的差异。diff 展示后可归档为 Artifact，页面会显示 Artifact ID、摘要和内容读取链接。

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
- `workspace.Manager` 类似一个小型 Java 领域服务：它读取 Task 状态，并集中执行“一 Task 一
  Workspace”、不可变引用和幂等规则，但当前仍是单进程内存实现。
- `RepositoryReference` 类似不可变 Java record：Task 在创建时持有它，Workspace 只从 Task
  派生自己的仓库元数据，避免两份请求参数产生分歧。
- `repository.ReferenceVerifier` 类似应用层 port，`connector/gitlab.Verifier` 是 HTTPS adapter。
  Go 通过方法集合隐式实现接口，不需要写 `implements`。
- `workspace.Preparer` 类似 Java 应用层定义的基础设施 port；GitLab adapter 负责取得可信 clone URL，
  `gitworkspace.Preparer` 则像封装好的 `ProcessBuilder`，集中控制参数、环境、目录和失败清理。
- `review.Service` 类似 Java Application Service：它只接受 task/tenant，从 Workspace Manager 取得
  可信 path/base/head，再调用 `repository.DiffReader` port；Controller 不允许调用方提交 revision。
- `artifact.Store` 类似带唯一键约束的内存 Repository。Go `[]byte` 与 Java `byte[]` 一样是可变引用，
  因此 Store 在写入和读取时都复制内容，不能只靠 struct/record 宣称“不可变”。
- `REGISTERED → PREPARING → READY` 类似带 `@Version` 的实体状态迁移。慢 Git I/O 发生时不会持有
  Manager 的互斥锁，因此 GET 仍能读取 PREPARING；失败回到 REGISTERED 时也递增 version。
