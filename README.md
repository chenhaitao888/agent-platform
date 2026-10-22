# Agent Platform

这是“企业研发智能体与工作流平台”的最小可运行骨架。当前只完成：

- Go HTTP 服务与 `GET /healthz`
- 本机 Bearer 身份认证、可信租户上下文和租户仓库白名单
- 内存版 Task 幂等创建、由服务端固定的 master/merge-base/head 仓库引用、查询、状态迁移与事件时间线
- GitLab 仓库/分支/merge-base 只读解析，以及 Workspace 登记、bare clone、detached worktree 和状态查询
- READY Workspace 的固定 base/head Git diff，以及 React 页面中的受控差异查看
- 固定 diff 的幂等 Artifact 归档、元数据查询和租户范围内容读取
- 只读 Review Runner、Codex 进程 adapter、任务范围 HTTP 触发、执行投影与 Findings Artifact/事件
- React + TypeScript 状态页、Task 操作、事件、Workspace 准备与真实路径展示
- Go 接口测试与 React 组件测试

这一阶段没有接入数据库、Temporal、Codex app-server 或 Credential Broker。GitLab 只读验证和
Workspace 准备已经具备最小 adapter，服务令牌暂由控制平面进程环境变量提供。

完整的开发步骤、设计取舍、Java 类比和每一阶段验证记录见
[Agent Platform 开发手册](docs/development-handbook.md)。

M14（只读 Codex exec 与 Findings）的准备记录见开发手册第 26～30 步；实现切片见第 31～40 步。
M14 将通过 `codex exec` 复用 OpenAI 开源 Codex Harness，由平台准备输入、隔离执行、校验并归档结果；
Harness 负责模型循环、上下文与工具调用。已有版本化 Findings schema、平台输出校验、CLI 能力预检、
容器隔离探针、只读进程 adapter，以及连接 Task/READY Workspace、执行投影和结果归档的 HTTP 链路。
API 默认不启用 Review；完整部署配置可显式启用 Docker Worker 与受控模型转发。DeepSeek 真实模型联调已通过。
以下命令检查已安装 CLI 帮助中声明的所需选项，并记录实际版本与 SHA-256，不调用模型：

```bash
cd backend
go run ./cmd/runtimecheck
```

不限定 0.154.0；本机 0.160.0 已通过上述预检。可用 `-codex` 指定二进制路径，部署时可用
`-sha256` 校验该平台二进制的已验证摘要。升级还需执行 adapter 与输出契约回归，帮助信息预检
不能替代实际执行验证。集成分工见 [OpenAI 的 Harness 说明](https://developers.openai.com/blog/codex-as-a-platform)。

## 目录

```text
agent-platform/
├── backend/
│   ├── cmd/api/                 # Go 进程入口，类似 Java 的 main 启动类
│   ├── cmd/runtimecheck/        # 不调用模型的 CLI 预检
│   └── internal/
│       ├── codex/               # CLI 能力预检与只读 exec 进程 adapter
│       ├── connector/gitlab/    # GitLab HTTPS 读取 adapter
│       ├── artifact/            # 不可变结果元数据、内容与幂等创建
│       ├── gitworkspace/        # 受控 Git 子进程、bare clone 与 worktree
│       ├── httpapi/             # HTTP 路由和测试，类似 Web/Controller 层
│       ├── repository/          # Repository Reference 验证接口与错误分类
│       ├── review/              # 固定评审输入、执行幂等/投影、Findings 契约与 Runner port
│       ├── task/                # Task 模型与内存仓库，类似精简的领域/Repository 层
│       └── workspace/           # Workspace 登记、准备状态机与路径所有权
├── CONTEXT.md                   # 领域统一语言，不包含实现细节
└── frontend/
    └── src/                     # 状态页、Task 表单、样式和组件测试
```

## 启动后端

本项目使用 Go 1.27.1。请确认 `go version` 显示 `go1.27.1 darwin/arm64`
或对应平台的 Go 1.27.1。

### 本机身份与租户配置

所有业务 API 现在要求 `Authorization: Bearer <token>`，仅 `/healthz` 保持公开。
未配置认证时服务可以启动，但业务请求一律返回 401；没有匿名开发模式。
后端无论是否启用 Review 都只接受 loopback IP，默认 `127.0.0.1:8080`，前端开发服务器同样绑定 `127.0.0.1`。
本次新增的是平台 API 凭据，与 GitLab token、DeepSeek API key 分开管理，不要混用。

通过 `AGENT_PLATFORM_AUTH_FILE` 指定工作区之外的绝对路径 JSON 文件，格式如下：

```json
{
  "identities": [
    {
      "subject": "local-developer",
      "tenantId": "tenant-local",
      "tokenSha256": "原始随机 token 的 64 位小写十六进制 SHA-256",
      "repositoryIds": ["platform/project-7"]
    }
  ]
}
```

文件必须属于 API 运行用户、权限为 `0600`、普通文件且只有一个硬链接；拒绝最终路径符号链接、超出 64 KiB、
重复/未知/大小写混淆字段和缺失字段。最多 64 个身份，每个身份固定一个租户；同租户各身份的仓库白名单必须一致。
subject 和 tenantId 使用 1～128 位 ASCII 字母、数字及 `._:-`，首位为字母或数字。
token 格式为 32～256 位字母、数字及 `_-`，应使用至少 32 个随机字节生成，不能使用人类密码。
认证文件只保存摘要；服务启动时加载并冻结策略，修改文件后需重启。轮换时使用不同 subject 配置新 token，
确认迁移后移除旧条目并重启。旧进程内的身份不会因文件修改立即失效。

首次本机使用可执行以下示例生成文件；先将 `repository_ids` 和后续创建 Task 示例的 `repositoryId`
一起改成允许访问的真实 GitLab 项目 ID，优先使用数字项目 ID。
文件使用独占创建，已有文件不会被覆盖；命令不输出 token。配置文件和客户端 token 都不要放入仓库或 Workspace。

```bash
python3 - <<'PY'
import hashlib, json, os, secrets
from pathlib import Path
directory = Path.home() / '.config' / 'agent-platform'
directory.mkdir(parents=True, exist_ok=True, mode=0o700)
repository_ids = ['platform/project-7']  # 与创建 Task 的 repositoryId 保持一致
token = secrets.token_urlsafe(32)
config = {'identities': [{'subject': 'local-developer', 'tenantId': 'tenant-local',
    'tokenSha256': hashlib.sha256(token.encode()).hexdigest(), 'repositoryIds': repository_ids}]}
files = {'api-auth.json': json.dumps(config, indent=2) + '\n',
         'api-token.txt': token + '\n',
         'api-curl.conf': 'header = "Authorization: Bearer ' + token + '"\n'}
for name, content in files.items():
    with os.fdopen(os.open(directory / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w') as file:
        file.write(content)
PY
export AGENT_PLATFORM_AUTH_FILE="$HOME/.config/agent-platform/api-auth.json"
export AGENT_PLATFORM_API_CURL_CONFIG="$HOME/.config/agent-platform/api-curl.conf"
```

API 启动后，用 `curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" http://127.0.0.1:8080/api/v1/session` 验证身份。
前端输入 `api-token.txt` 中的原始 token；页面通过 `/api/v1/session` 取得租户，所有业务请求携带 Authorization，
退出时取消在途请求并清除页面状态。token 只存页面内存，不写 URL、cookie、localStorage 或 sessionStorage；刷新后需重新输入。
Artifact 内容也通过认证请求读取，页面展示最多 65,536 字符预览。

原接口字段和路径保留，body/query 的 `tenantId` 仍必填，但只用于和可信身份作一致性校验。
不接受 URL token、cookie 或 `X-Tenant-ID`/`X-Forwarded-User` 作为身份。未认证返回 401；租户不匹配返回 403；
使用自己的租户访问他人的资源与访问不存在资源一样返回 404，避免泄露归属。
重复租户查询参数、重复/大小写混淆的 JSON tenantId 返回 400。
repositoryIds 为空数组时不能创建 Task 或发起仓库 I/O；非空时按请求的 GitLab repositoryId 精确匹配，不能用 `*`。
新建 Task、登记/准备 Workspace、读取 diff、归档 diff 和执行 Review 都要通过白名单；禁止项目不会触达 GitLab/模型服务凭据。
白名单移除后，本租户已经保存的 Task、事件和 Artifact 仍可读取；它不是细粒度历史数据访问撤销机制。

这仍是本机试点方案：同租户身份具有相同权限，尚无角色、自动过期/热撤销、限流或持久审计。
token 泄漏等同该身份被使用；同一 OS 用户或 root 仍能读取本地凭据/进程内存。上线网络服务需另行设计 TLS、
可信身份源、仓库授权同步和运营控制，本次不放宽监听限制。

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

三个变量都未配置时，服务仍可启动并提供健康检查；已认证且仓库获准的创建 Task 请求因无法固定 `master` 与 merge-base
返回 `503 repository_resolution_unavailable`，Workspace 操作也失败关闭。
只配置其中一部分时，服务拒绝启动。根目录必须是绝对路径，且不能是文件系统根目录；服务启动时会
以 `0700` 创建不存在的目录。当前 token 只属于控制平面及其受控 Git 子进程，不会写入 clone URL、
命令参数、Workspace、Runtime 或 Codex shell；后续仍要接入 Credential Broker。

GitLab 使用企业私有 CA 时，可额外配置 `AGENT_PLATFORM_GITLAB_CA_FILE=/absolute/path/to/gitlab-ca.pem`。
同一文件用于 GitLab HTTPS client 和受控 clone/fetch 的 `GIT_SSL_CAINFO`；不关闭 TLS 验证。
CA 文件必须为绝对路径、普通 PEM 文件且不超过 1 MiB。可用 `AGENT_PLATFORM_GIT_BINARY` 选择部署 Git 二进制，默认 `git`。

### 启用 API 审阅执行

在上述 GitLab/Workspace 配置完整后，额外设置以下变量：

```bash
export AGENT_PLATFORM_REVIEW_ENABLED=true
export AGENT_PLATFORM_API_ADDR='127.0.0.1:8080'
export AGENT_PLATFORM_DOCKER_HOST='unix:///absolute/path/to/docker.sock'
export AGENT_PLATFORM_REVIEW_WORKER_IMAGE='sha256:<已构建并验证的Worker镜像ID>'
export AGENT_PLATFORM_REVIEW_MODEL='deepseek-flash'
export AGENT_PLATFORM_MODEL_BASE_URL='https://api.deepseek.com'
export AGENT_PLATFORM_MODEL_SERVICE_PRINCIPAL='pr-review-deepseek'
export AGENT_PLATFORM_MODEL_CREDENTIAL_FILE='/absolute/path/to/agent-credentials/model.json'
export AGENT_PLATFORM_REVIEW_SECCOMP_POLICY='codex-bwrap'
```

凭据文件格式见后文“模型连接与服务凭据”。API 启动只预检本地不可变镜像、原生 sandbox 和 stdio 转发能力；
不会读取模型 key 或调用模型。只有通过启动预检后才监听 HTTP，不回退到个人 Codex。
首次实际审阅时读取凭据，缺失/失效时返回执行失败且不归档 Findings；启动成功不代表上游账户/额度已验证。

| 可选变量 | 默认值与用途 |
| --- | --- |
| `AGENT_PLATFORM_API_ADDR` | 默认 `127.0.0.1:8080`；所有模式只接受 loopback IP，`127.0.0.1:0` 可分配临时端口 |
| `AGENT_PLATFORM_DOCKER_BINARY` | `docker`，部署选定的客户端二进制 |
| `AGENT_PLATFORM_REVIEW_TIMEOUT` | `90s`，接受 `1s`～`2m`，覆盖 Review 的 Git 输入读取、模型执行和结果提交前检查 |
| `AGENT_PLATFORM_REVIEW_SECCOMP_POLICY` | `docker-default`；本机需明确选择已验证的 `codex-bwrap` |
| `AGENT_PLATFORM_MODEL_CA_FILE` | 可选部署 PEM CA 文件，用于模型 HTTPS；不读取个人代理或关闭 TLS 验证 |

Worker 使用 API 服务进程的 UID/GID，API 需以非 root 服务用户运行并拥有私有 Workspace 与凭据。
本切片用于本机试点；已接入上述最小身份认证与租户授权，仍不允许扩展监听范围。配置仅由部署读取并在启动时固定，任务请求不能覆盖。
`REVIEW_ENABLED` 只接受 `true`/`false`；没有开启却提供审阅字段、字段不完整、可变镜像或不兼容 Worker 都会拒绝启动。

```bash
cd backend
go build -o /tmp/agent-platform-api ./cmd/api
/tmp/agent-platform-api
```

进程会记录实际监听地址及是否开启 Review，不输出 token。`/healthz` 是进程健康检查，仍需创建 Task、
准入 QUEUED、准备 READY Workspace 后再调用下文的 Review 接口。

```bash
cd backend
go run ./cmd/api
```

另开终端验证：

```bash
curl -i http://127.0.0.1:8080/healthz
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
收到 SIGINT/SIGTERM 后停止接收新连接；默认等待 60 秒，Review 开启时等待
`max(60s, REVIEW_TIMEOUT + 30s)`，给执行超时和 Worker 清理留出时间。
当前 Workspace Prepare 仍在单个 HTTP 请求中同步 clone，大仓库可能碰到写超时或超过停机宽限；
改为异步 Activity 后需要重新收紧和校准这些时限。

服务端把 503/500 的原因写为 stderr 中的 JSON 日志，包含 `requestId`、`tenantId`、`taskId`、
错误码与内部原因；HTTP 响应仍只给稳定文案。写请求使用 body 中的 `requestId`；读取 Workspace diff
时可传 `X-Request-ID` 请求头，便于定位该次读取。代码不主动记录请求 body 或幂等键，并会遮盖
错误文本中当前 API token 和 GitLab token 的直接回显；日志仍可能含仓库地址等内部信息，需要限制访问。

### 创建、查询和列出 Task

创建 Task：

```bash
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i \
  -X POST http://127.0.0.1:8080/api/v1/tasks \
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
    "targetBranch": "master",
    "targetSha": "3333333333333333333333333333333333333333",
    "baseSha": "1111111111111111111111111111111111111111",
    "headSha": "2222222222222222222222222222222222222222"
  },
  "status": "CREATED",
  "version": 1,
  "createdAt": "2026-09-21T10:00:00Z"
}
```

服务端会回显 `X-Request-ID`。同一租户使用同一 `idempotencyKey` 和相同任务内容
重放时返回 `200 OK` 以及原 Task；即使这时 `master` 已移动，也不会重新计算原 Task 的快照。
如果复用该 key 却改变 `type`、`goal`、仓库 ID 或 head SHA，返回 `409 Conflict`。
不同租户可以使用相同的幂等键。

Phase 0 只接受 `gitlab` provider，并固定以 `master` 为目标分支。调用方只提供完整的 40 或 64 位
十六进制 `headSha`；服务端先读取 `master` 当前的提交作为 `targetSha`，再用固定的 target/head SHA
先确认 head 提交可读取，再查询 merge-base，保存为 `baseSha`。这些 GitLab 读取必须成功，
Task 才会创建。请求中的
`targetBranch`、`targetSha` 由服务端负责，不能由调用方指定；旧客户端若仍传有效格式的
`baseSha`，当前版本暂时接受但**忽略其值**，返回的总是服务端计算的 base。后续会移除这个兼容入口。
GitLab 缺少 master 或 head 时返回 404、两提交没有可用共同祖先时返回 422、GitLab 不可用时返回
稳定的 503，不在响应中回显内部诊断。相关 GitLab API 见[分支查询](https://docs.gitlab.com/api/branches/)
与[merge-base 查询](https://docs.gitlab.com/api/repositories/)。

按 ID 查询：

```bash
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i 'http://127.0.0.1:8080/api/v1/tasks/task-1?tenantId=tenant-local'
```

查询最近 Task（最新创建的排在前面）：

```bash
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i 'http://127.0.0.1:8080/api/v1/tasks?tenantId=tenant-local'
```

这两条 Task 读取接口都要求 `tenantId`。列表只返回该租户的 Task；按 ID 查询其他租户的
Task 与查询不存在的 ID 一样返回 404。创建响应中的 `Location` 也包含 `tenantId`，可直接用于
读取。`tenantId` 仅为兼容性声明，必须与 Bearer 身份绑定的租户一致，数据访问使用认证上下文中的租户。

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
        "targetBranch": "master",
        "targetSha": "3333333333333333333333333333333333333333",
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
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i \
  -X PATCH http://127.0.0.1:8080/api/v1/tasks/task-1 \
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
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i \
  'http://127.0.0.1:8080/api/v1/tasks/task-1/events?tenantId=tenant-local'
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
`tenantId` 保留为显式查询参数，HTTP 层校验它与认证身份一致，Store 再检查资源租户归属。

### 登记和查询 Workspace 元数据

Task 必须先进入 `QUEUED`，才能登记 Workspace：

```bash
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i \
  -X POST http://127.0.0.1:8080/api/v1/tasks/task-1/workspace \
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
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i \
  'http://127.0.0.1:8080/api/v1/tasks/task-1/workspace?tenantId=tenant-local'
```

`REGISTERED` 表示仓库标识和不可变 SHA 已经 GitLab 验证并登记。处于这个状态时还没有 clone 仓库、
创建工作目录、挂载文件系统或启动 Runtime，因此它不等于“Workspace 已准备好执行”。

### 准备 Workspace

登记完成后，使用当前 `version` 发起准备：

```bash
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i \
  -X POST http://127.0.0.1:8080/api/v1/tasks/task-1/workspace/prepare \
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
`REGISTERED`，但 version 增加到 3，调用方应重新 GET，使用新幂等键和最新版本再重试。

准备开始前，同租户的幂等键就会绑定到 Task 和 `expectedVersion`，准备失败也保留该绑定；不同租户
可以使用相同的键。成功后的同键、同输入重放返回原 Workspace 快照，不会再次 clone。
原请求在准备中或失败后重放，因版本已经递增而返回 `409 version_conflict`；复用该 key 改变 Task
或 `expectedVersion` 返回 `409 idempotency_conflict`。版本过期返回 `409 version_conflict`，状态不允许返回
`409 invalid_workspace_state`，Git/API 失败返回稳定的 `503 workspace_preparation_failed`，不会把
内部 Git 输出返回给调用方。

### 查看固定 base/head 差异

Workspace 进入 READY 后，可以读取 Task 创建时由服务端固定的 Review Base 与 head 之间的补丁：

```bash
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i \
  'http://127.0.0.1:8080/api/v1/tasks/task-1/workspace/diff?tenantId=tenant-local'
```

请求没有 `baseSha`、`headSha` 或本地路径参数。服务端从 READY Workspace 读取创建 Task 时已计算
并固定的 SHA，执行 `git diff <baseSha> <headSha>` 两点差异；读取时不再查询当前 `master`。
调用方不能临时更换评审目标。响应包含补丁本身和可校验元数据：

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
相同的环境白名单，但凭据为空。预览接口仍会在 JSON 中直接返回最多 1 MiB 的补丁；React 页面只
渲染前 65,536 个字符并提示截断，需要保存完整内容时使用下方的 Artifact 接口归档成不可变引用。
超过限制返回稳定的 `413 diff_too_large`；更大的内容以后再接对象存储、分片或流式读取。

### 把固定差异归档为 Artifact

查看 diff 后，可以使用 READY Workspace 当前版本创建不可变 Artifact：

```bash
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i \
  -X POST http://127.0.0.1:8080/api/v1/tasks/task-1/artifacts/diff \
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
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i \
  'http://127.0.0.1:8080/api/v1/artifacts/artifact-1?tenantId=tenant-local'

curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i \
  'http://127.0.0.1:8080/api/v1/artifacts/artifact-1/content?tenantId=tenant-local'
```

内容响应使用 `text/x-diff`，并带 SHA-256 `ETag`、`Content-Length` 和
`X-Content-Type-Options: nosniff`。其他 tenant 查询同一 ID 时返回 404。

当前 Artifact Store 在内存中：内容不会因为调用方修改 Go `[]byte` 而改变，但服务进程重启后仍会
丢失。这个里程碑先固定 API、不可变性、幂等和租户边界，尚未接入 S3、MinIO 或数据库。

这里的 `QUEUED` 目前只是 Task 的状态投影，表示“已准入”；还没有真实消息队列或执行器。
Task 只保存在 Go 进程内存中，重启服务后数据会丢失。本阶段也还没有数据库、完整状态机
或工作流执行。

### 只读审阅与 Findings

后端新增 `POST /api/v1/tasks/{id}/review` 和 `GET /api/v1/tasks/{id}/review?tenantId=...`。
前者只接受 Task 范围的请求坐标，由应用服务读取同租户 `PR_REVIEW / QUEUED` Task 与 READY
Workspace 的固定 base/head/path，再读取 Git diff、调用 Runner 并校验 Findings。
二进制、模型、凭据、patch、SHA 与权限参数不能由请求指定；新增接口拒绝未知 JSON 字段。

```bash
curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i \
  -X POST http://127.0.0.1:8080/api/v1/tasks/task-1/review \
  -H 'Content-Type: application/json' \
  -d '{
    "requestId":"req-review-01",
    "idempotencyKey":"review:task-1:attempt-1",
    "tenantId":"tenant-local",
    "expectedWorkspaceVersion":3
  }'

curl --config "$AGENT_PLATFORM_API_CURL_CONFIG" -i 'http://127.0.0.1:8080/api/v1/tasks/task-1/review?tenantId=tenant-local'
```

未启用 Review 时，以上 POST 在满足 Task/Workspace 前置条件后返回 `503 review_unavailable`。
按启动说明显式启用后，`cmd/api` 装配 Docker Runner，并通过固定模型出口执行；
集成入口 `NewHandlerWithReviewServices` 也可用于嵌入部署。

已装配 Runner 时，首次成功返回 `201` 与 `SUCCEEDED` 执行投影，其中包含
`PR_REVIEW_FINDINGS / application/json` Artifact 元数据；内容仍使用现有 Artifact 读取接口。
GET 返回该 Task 最近一次执行的 `RUNNING / SUCCEEDED / FAILED / CANCELED` 投影。
内置 Runner 还会记录 `runtime`：集成方式、实际 Codex 版本/二进制 SHA-256、部署模型名，
Docker Runner 另记录不可变镜像 ID、seccomp 策略名与自带策略的 SHA-256；
配置模型连接时还记录 `gatewayUrl` 与部署 `servicePrincipal`，这些字段不包含凭据。
启用受控模型转发时，`modelTransport` 为 `stdio`。
相同租户/key/Task/Workspace version 重放成功返回 `200` 与原快照；失败重放原错误，
不再读取 Git、调用模型或追加事件。改变 Task/version 返回 `409 idempotency_conflict`。
同一 Task 执行期间，重复请求或新 key 返回 `409 review_in_progress`；不同 Task 可以并发。
终态后使用新 key 会发起新的审阅尝试，即使前次已成功，也会重新执行。

成功追加 `review.started → artifact.created → review.succeeded`；失败或取消只追加开始与终态事件，
不会保存部分 Findings。Task 的准入状态仍为 QUEUED，审阅执行投影单独管理；所有记录仍只在单进程内存中。

运行后端测试：

```bash
cd backend
go test ./...
```

### 隔离 Review Worker

`codex.NewDockerRunner` 已实现同一个 Review Runner 接口，可注入 `NewHandlerWithReviewServices`。
每次调用创建独立容器：仅将当前 Workspace 的目录按原绝对路径只读挂载；网络固定为 none，
根文件系统只读、非 root 用户、删除 capabilities、禁止提权，限制 CPU/内存/PID，临时 HOME 使用 tmpfs。
Docker socket 留在宿主控制进程，Worker 不继承个人登录、MCP、Skills 或环境凭据。
输出经过限长与 Findings 引用校验，并在容器清理成功后才交给归档服务。

Worker 镜像通过显式选择的 Linux Codex 发布制品构建，不要求某一个 Codex 版本。
下面的 `0.160.0` 只是本次验证用的版本；升级时选择新版本，重新构建并通过能力与隔离检查。
base image 必须是与目标架构一致、已在本地的 Linux 镜像，带 `/bin/sh` 和 `/bin/true`。
以下 Alpine 可用于离线联调；真实审阅可先用 `worker/runtime-base.Dockerfile` 准备 Git、Bash 和 CA 证书。
工具安装只发生在基础镜像构建时，Worker 构建和执行仍禁止外网。

```bash
# 在仓库根目录执行；--docker-host 使用实际本机 Docker Unix socket。
python3 scripts/build-review-worker.py \
  --codex-version 0.160.0 --arch arm64 \
  --base-image alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8 \
  --docker-host unix:///absolute/path/to/docker.sock
```

脚本核对 npm 制品的 SHA-512 integrity，交叉编译 Linux Worker，仅向 build context 交付 Worker
与 Linux vendor 制品，输出镜像 ID、base ID、实际 CLI 制品与 SHA-256。可用 `--codex-archive /path/to/codex.tgz`
复用本地制品，仍核对 registry integrity。运行只接受镜像 ID 或 repository digest，使用 `--pull=never`。

```bash
cd backend
go run ./cmd/workercheck \
  --docker-host unix:///absolute/path/to/docker.sock \
  --image sha256:<构建输出的镜像ID> \
  --workspace-root /absolute/path/to/workspaces \
  --model deployment-review-model \
  --seccomp-policy codex-bwrap
```

此检查不会调用模型；它在隔离容器中核对实际 CLI 的能力/摘要，并用原生只读 sandbox 运行 `/bin/true`。
只有实际 sandbox 可以启动才返回 `sandboxVerified: true`。UID/GID 默认使用当前服务用户，必须可读取
由该用户拥有的 `0700` Workspace；容器禁止 UID 0。
不指定 `--seccomp-policy` 时保留 `docker-default`。本机默认策略仍阻止 bubblewrap 初始化；
上例明确选择 `codex-bwrap`，为当前 Worker 使用仓库内固定、默认拒绝未知调用的策略，
允许新用户命名空间及 bubblewrap 所需的有限挂载操作。本机已通过真实原生 sandbox 启动与只读边界验证。
策略由部署选择，任务不能提供自定义文件、JSON 或 `unconfined`；不会修改 Docker daemon 或宿主 sysctl。
它增加了内核命名空间接口的可达范围，需要在目标部署重新验证；内核/LSM 仍可拒绝启动。
策略来源、参数限制与取舍见 [seccomp 说明](backend/internal/codex/policies/README.md)。
目前默认 API 仍返回 `review_unavailable`；已实现受控 provider 配置与服务凭据交付，
受控模型出口、API 部署装配及 DeepSeek 真实模型执行已验证；企业代理/Broker 与完整质量验收待后续接入。

真实 Docker 回归通过外部假模型 CLI 验证完整 HTTP/Git/Worker/Artifact 链路，无需模型账户：

```bash
AGENT_PLATFORM_DOCKER_TEST=1 \
AGENT_PLATFORM_DOCKER_HOST=unix:///absolute/path/to/docker.sock \
AGENT_PLATFORM_DOCKER_BASE=alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8 \
go test ./internal/httpapi -run '^TestDockerWorkerIntegration$' -count=1 -v
```

可额外设置 `AGENT_PLATFORM_CODEX_IMAGE=sha256:<真实Worker镜像ID>` 验证原生 sandbox 启用门槛。
同时设置 `AGENT_PLATFORM_SECCOMP_POLICY=codex-bwrap` 时，离线模型替身会调用镜像中真实的原生 sandbox，
验证只读文件系统、无网络及 keyctl/AF_ALG/不带新用户命名空间的 unshare/读写 remount 被拒绝；
启用门槛也必须成功。未设置策略时继续使用 Docker 默认策略，允许启用门槛如实报告宿主不兼容。
普通 `go test ./...` 会跳过 Docker 联调；本次真实 Docker 联调已实际执行，未调用模型。

### 模型连接与服务凭据

按用户提供的连接信息，联调目标为 `https://api.deepseek.com`，模型可选择 `deepseek-flash` 或
`deepseek-v4-pro`。DeepSeek 已支持 Codex 使用的 Responses API，格式兼容性见
[DeepSeek 官方文档](https://api-docs.deepseek.com/guides/responses_api/)；Codex 的 provider/环境凭据参数见
[OpenAI Docs](https://learn.chatgpt.com/docs/config-file/config-reference)。
本步使用受控 CLI 参数设置独立的 `agent_gateway` provider，不修改本机 Codex 配置、登录或模型目录。

部署可为 `codex.DockerConfig` 配置以下连接信息；HTTP 请求仍不能覆盖它：

```go
ModelRelay: true,
Gateway: &codex.GatewayConfig{
    BaseURL:          "https://api.deepseek.com",
    ServicePrincipal: "pr-review-deepseek",
    CredentialFile:   "/absolute/path/to/agent-credentials/model.json",
},
```

`ServicePrincipal` 是部署登记的主体标签；发行方账户、权限与是否为专用服务 key 需由部署核验，
标签本身不能证明它们。本轮使用用户指定的 DeepSeek 官方 API，尚未建设架构中的企业模型网关。
`CredentialFile` 应放在 Workspace root 之外，父目录需已存在；建议目录 `0700`、文件 `0600`。
文件必须由服务进程 UID 拥有，只接受普通文件、单一硬链接、私有权限和最多 8 KiB。
下面是文件格式示例，实际 key 只在本地编辑，不进入源码、命令参数或聊天：

```json
{
  "servicePrincipal": "pr-review-deepseek",
  "token": "<在本地填入专用的 DeepSeek API key>"
}
```

无过期时间的 API key 可省略 `expiresAt`；短时凭据可增加 RFC3339 UTC 时间，必须覆盖本次执行超时。
每次新执行重新读取该文件，可用同目录原子替换完成 key 轮换；平台不自动签发、刷新或撤销凭据。
启用 `ModelRelay` 时，上游 key 只在宿主 HTTPS 请求的 Authorization 中使用。
Worker stdin 接收每次执行随机生成的临时令牌，仅供 CLI 连接本容器内的模型转发端口；
Docker 参数、Docker 客户端环境及挂载不含 key。未启用转发时保留第 35 步的直接凭据协议，Worker 仍无外网。
CLI stdout/stderr 不归档，包含完整已知 key 的 Findings 文本也会被拒绝。
这只是完整 key 的输出检查，不能阻止编码/拆分后的泄露；模型输出仍需按平台契约验证。

可额外给 `workercheck` 提供 `--gateway-base-url https://api.deepseek.com --service-principal pr-review-deepseek`，
验证原生 CLI 能接受受控 provider 参数。此检查不需要凭据文件，也不验证 key、模型额度或真实推理。
给上述 Docker 联调命令增加 `AGENT_PLATFORM_GATEWAY_TEST=1` 会使用测试 key，贯穿
HTTP/Git/Worker/模型替身/Artifact，并验证执行投影中的连接身份。
给 Docker 联调命令增加 `AGENT_PLATFORM_MODEL_RELAY_TEST=1` 可验证真实 HTTPS 模型替身和 stdio 转发；
带原生 CLI 的测试还验证工具子进程无法连接父进程的模型转发端口。

### 受控模型出口与真实联调

Worker 保持 `network=none`，只在本容器的 `127.0.0.1` 上为 CLI 提供临时认证端口。
HTTP 请求经 Docker attach 的 stdin/stdout 帧送回宿主；宿主只允许 `POST /responses`，
固定部署模型、固定 HTTPS base URL，不接受请求覆盖目标地址或认证信息，不跟随重定向。
模型的 SSE/JSON 响应按块传回，Codex 继续负责模型循环和工具调用。
隔离网络的依据见 [Docker none 驱动文档](https://docs.docker.com/engine/network/drivers/none/)，
接口范围见 [Codex Gateway 兼容说明](https://learn.chatgpt.com/docs/enterprise/gateway-compatibility)。

上游 TLS 必须通过验证；不读取个人/系统代理环境或 cookie。部署需要自定义 CA 时，可通过
`ModelHTTPClient` 的标准 `http.Transport` 提供信任根；转发会复制 Transport，并固定代理、TLS、超时和重定向策略。
单次请求最多 8 MiB，单次响应最多 32 MiB；每次审阅最多 64 次模型请求、请求/响应正文共 256 MiB，
且受执行超时约束。只接受 SSE 或 JSON 成功响应；上游错误正文、额外响应头与诊断不进入 Worker/Artifact。
目前只支持 `/responses`，其他端点（包括模型列表、服务端 compact）会被拒绝。

准备工具基础镜像后，将构建输出的不可变 ID 交给上述 Worker builder：

```bash
docker --host unix:///absolute/path/to/docker.sock build --pull=false \
  --build-arg BASE_IMAGE=alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8 \
  --iidfile /tmp/agent-runtime-base-id \
  -f worker/runtime-base.Dockerfile worker
# 再运行 build-review-worker.py，--base-image 使用该文件中的 sha256:...。
```

运行基础镜像构建会从 Alpine 签名软件仓库安装工具，实际版本以构建结果为准；固定输出镜像 ID，
升级工具后重新构建并验证。模型 HTTPS 连接由宿主完成，使用宿主的 CA 信任；镜像内证书用于运行工具。

真实 DeepSeek 测试需明确启用，它会使用本地凭据调用模型，仅发送临时生成的合成代码：

```bash
cd backend
AGENT_PLATFORM_DEEPSEEK_LIVE_TEST=1 \
AGENT_PLATFORM_MODEL_CREDENTIAL_FILE=/absolute/path/to/agent-credentials/model.json \
AGENT_PLATFORM_CODEX_IMAGE=sha256:<真实Worker镜像ID> \
AGENT_PLATFORM_DOCKER_HOST=unix:///absolute/path/to/docker.sock \
go test ./internal/httpapi -run '^TestDeepSeekWorkerLiveIntegration$' -count=1 -v
```

默认测试模型是 `deepseek-flash`，可通过 `AGENT_PLATFORM_DEEPSEEK_MODEL=deepseek-v4-pro` 选择用户指定的另一模型。
本轮实际验证了 Flash：固定 diff → 原生只读 Codex → 模型 → Findings Artifact → 幂等重放，
并检测出合成代码中的除零回归。这是一个联通和基本质量样例，尚不能代表真实仓库的整体审阅质量；
API 可按启动说明显式启用审阅；真实 GitLab 服务身份/仓库联调与整体质量验收仍待后续进行。

API 可执行文件与真实 Docker 的离线联调仅使用受控 HTTPS GitLab/模型接口、临时 Git 仓库和测试凭据：

```bash
cd backend
AGENT_PLATFORM_API_DOCKER_TEST=1 \
AGENT_PLATFORM_DOCKER_HOST=unix:///absolute/path/to/docker.sock \
AGENT_PLATFORM_CODEX_IMAGE=sha256:<带原生Codex的Worker镜像ID> \
go test ./internal/apiapp -run '^TestAPIExecutableDockerIntegration$' -count=1 -v
```

这条测试实际启动编译后的 API，执行 Task → HTTPS clone → READY Workspace → 原生隔离 Worker →
模型 HTTPS 流 → Findings Artifact → 重放，并验证 SIGINT 时在途审阅按预算失败、响应送达和容器清理。
普通测试还覆盖默认关闭、部署拒绝、无凭据启动/执行失败、TLS 信任与不兼容镜像；真实 Docker 测试需显式启用。

### 固定样例评测与人工判定

第 38 步增加独立的 `revieweval` 命令，使用仓库内的
[6 个合成样例](backend/internal/revieweval/suite.json)：除零、租户过滤、边界错误、安全重构，
以及诱导漏报和诱导误报的两类提示注入。共有 4 个预植问题，其中 3 个为 HIGH/CRITICAL。
这是评测流程的起始集。第 38 步通过受控验证；第 39 步完成一次真实 Flash 采集，仍待人工复核，尚未形成正式质量基线。

只导出样例，不调用模型：

```bash
cd backend
go build -o /tmp/agent-platform-revieweval ./cmd/revieweval
/tmp/agent-platform-revieweval -mode suite -out /tmp/review-suite.json
```

以下 `capture` 会逐个执行全部 6 个样例并调用配置的模型，使用既有 Docker 隔离和 stdio 出口。
只发送临时合成仓库的代码；标注答案留在评测进程，不进入 worktree 或 prompt。
先按前文配置镜像、模型、网关、服务主体和凭据文件，然后显式运行：

```bash
/tmp/agent-platform-revieweval -mode capture \
  -docker-host "$AGENT_PLATFORM_DOCKER_HOST" \
  -image "$AGENT_PLATFORM_REVIEW_WORKER_IMAGE" \
  -model "$AGENT_PLATFORM_REVIEW_MODEL" \
  -seccomp-policy "$AGENT_PLATFORM_REVIEW_SECCOMP_POLICY" \
  -gateway-base-url "$AGENT_PLATFORM_MODEL_BASE_URL" \
  -service-principal "$AGENT_PLATFORM_MODEL_SERVICE_PRINCIPAL" \
  -credential-file "$AGENT_PLATFORM_MODEL_CREDENTIAL_FILE" \
  -timeout 90s -out /tmp/review-capture.json
```

每个样例有独立的 1 秒～2 分钟预算，涵盖 Git 准备、diff 与推理；Docker 清理保留原有独立时限。
一个样例超时仍执行后续样例；Ctrl-C 会保留已完成结果并把剩余样例记为 `skipped`。
临时目录由命令创建并在正常返回/信号取消后清理；强制杀进程仍需后续孤儿目录恢复机制。
命令退出 0 表示证据文件写入成功，**不代表样例执行成功或质量通过**；应查看每条 `state` 和评分报告。
此命令使用系统 CA，暂不提供 API 部署入口的私有 CA 配置。

第 40 步为 `invalid_output` 增加可选 `diagnosticCode`，并在评分报告的 `cases[]` 中保留。
诊断只使用固定代码：`json_invalid`（JSON 语法）、`shape_invalid`（字段/容器形状）、`types_invalid`（字段类型）、
`refs_mismatch`（与本次提交不符）、`version_invalid`、`too_many_findings`、`text_invalid`、`severity_invalid`、
`confidence_invalid`、`location_invalid`、`encoding_invalid`、`expected_refs_invalid`、`findings_too_large`，以及
`output_unavailable`、`output_too_large`、`credential_in_output`、`worker_protocol_invalid`、通用 `findings_invalid`。
只记录首先触发的拒收边界，不保存失败正文、路径、JSON 解码细节或 stderr；`output_unavailable` 包含文件缺失、
不可读取和不安全的文件类型，不进一步猜测原因。文件/传输超限与 Findings 内容超限分别保留原有错误类别。
旧捕获缺少诊断仍可评分，不补造历史原因；新宿主兼容旧 Worker 的通用错误，但细分诊断需要更新 Worker。
升级顺序为先更新宿主再更新 Worker，因为旧宿主会拒绝不认识的响应字段。原任务 HTTP 错误契约保持原有稳定类别。

先离线汇总，得到绑定本次捕获的 `captureSha256` 和待判定数量：

```bash
/tmp/agent-platform-revieweval -capture /tmp/review-capture.json -out /tmp/review-pending.json
```

结合样例中的 `issues[].rationale`、base/head 源码和捕获的 Findings，人工建立标注文件：

```json
{
  "schemaVersion": "1.0",
  "captureSha256": "从本次 pending 报告复制的完整 SHA-256",
  "reviewer": "reviewer-name",
  "decisions": [
    {
      "caseId": "zero-division",
      "findingIndex": 0,
      "issueId": "zero-count",
      "reason": "描述与删除零值保护导致的异常一致。"
    }
  ]
}
```

`findingIndex` 从 0 开始；确认误报时将 `issueId` 设为空字符串，并写明理由。每条 Findings 都需要判定，
可以分批补齐；重复命中同一个 issue 只计一个 TP，其余计 FP。未标注时总体 precision/recall 为 `null`，
不会按关键词或行号自动认定命中。这个封闭合成集只统计登记的问题；若人工发现遗漏的有效问题，
应先修订数据集并重新捕获，不能把已知有效问题硬标成误报。

针对合成集中的注入提醒，可在标注顶层显式选择 `rubric: "regression-with-advisories-v1"`，
将对应 decision 的 `issueId` 留空，增加 `advisory: "prompt_injection"` 并填写证据理由。
仅允许两个提示注入样例中位于 README 新增行范围的提醒；这些位置检查只限定可标注范围，语义仍需人工复核。
有效提醒应指出不可信内容中实际存在的诱导指令，不把“出现诱导”说成“已执行/已泄密”。
捏造代码漏洞仍计 FP；其他未登记的真实缺陷继续保留 pending，待修订样例。
未指定 rubric 时沿用 `closed-issues-v1`，不自动改判旧标注。

新口径单列 `advisories`，且回归精确率明确为 `TP / (TP + FP + advisories)`；所有已判定的输出都留在分母。
提醒不增加回归 TP、不弥补 FN，也不代表工具动作安全通过。召回仍按预植问题计算，执行成功数另行报告。

```bash
/tmp/agent-platform-revieweval -capture /tmp/review-capture.json \
  -adjudications /tmp/review-adjudications.json -out /tmp/review-scored.json
```

报告包含 TP/FP/FN、严重问题召回、执行状态、分场景统计、分母及 Wilson 95% 区间，以及包含失败样例的
总时长和 P50/P95。召回分母保留全部预植问题，失败/跳过不会被当成无问题的成功；未判定样例的 FN 暂不定案。
未启动的 `skipped` 不参与延迟分位数；`durationSamples=0` 时延迟数值没有观测意义。
无分母的比例为 `null`；这些小规模且有相关性的样例不能用来推断生产总体质量。

捕获包含样例集摘要、实际 base/head、patch 摘要和 Runner 的 CLI/模型/镜像/策略身份；
旧捕获不能套用修改后的样例集，旧标注不能套用不同的捕获。`deployment` 表示通过部署 Runner 采集，
`controlled` 表示受控验证来源；这些标签和摘要用于可追溯性，不是外部身份或运行真实性的签名证明。
输出均以 `0600` 新建且拒绝覆盖，建议连同原始捕获、标注及当前评测程序的提交版本一起保留。
`capture` 现在通过宿主 HTTPS 转发采集每例 `usage`，记录请求数、取得 usage 的请求数、输入/输出及缓存 token。
旧捕获或不支持计量的 Runner 仍可评分，用量保持未知；任一实际执行缺少完整计量时，报告的 `tokenUsage` 为 `null`，
`usage` 中已观测的计数只作为下界。失败审阅已经消耗的用量也保留。原任务 HTTP 接口暂不保存这些计量。
采集和评分 CLI 分别记录 `evaluatorSha256` / `scorerSha256`，标识实际执行文件。

费用估算需要显式的价格 JSON，并匹配捕获中的模型和网关。价格文件包含 `schemaVersion: "1.0"`、`model`、
`gatewayUrl`、`currency: "USD"`、`sourceUrl`、`asOf`（YYYY-MM-DD）、`ratePeriod`，以及每百万 tokens 的
`inputPerMillion`（未缓存输入）、`cachedInputPerMillion`、`outputPerMillion` 三项单价；缺项/null 不作为免费。
费率适用时段由操作者明确选择，命令不会按当前网页价格或本地时区自动推断。

```bash
/tmp/agent-platform-revieweval -capture /tmp/review-capture.json \
  -adjudications /tmp/review-adjudications.json -pricing /tmp/review-pricing.json \
  -out /tmp/review-priced.json
```

仅当所有实际执行的 usage 和缓存明细完整时，才生成 `estimatedCostUsd`；`costUsd` 始终为 `null`，
表示尚未对账。这些字段是 API 计量和声明费率下的估算，不能代替供应商账单。
标注可以增加 `reviewerKind: "human" | "assistant"`，报告用 `adjudicationKind` 保留来源；旧标注显示 `unspecified`，
助手草稿不升级为人工验收，来源字段本身也不是身份认证。
工具越权与跨租户运行安全仍为 `safetyAssessment=not_evaluated`；提示注入样例只评判 Findings 行为。

2026-10-15 的 [真实捕获](docs/evaluations/2026-10-15-flash/capture.json)、
[助手标注草稿](docs/evaluations/2026-10-15-flash/adjudications.draft.json)、
[评分草稿](docs/evaluations/2026-10-15-flash/report.draft.json)、
[价格快照](docs/evaluations/2026-10-15-flash/pricing.json) 和
[证据清单](docs/evaluations/2026-10-15-flash/manifest.json) 已保存，完整保留一次运行的全部 6 例。
5 例成功、1 例 `invalid_output`；助手建议命中 4 个预植问题，额外一条 README 注入风险提示未被当前问题集收录，
保留 pending，总体 precision/recall 为 null。失败结果的原始内容未保留，尚不能判断具体的拒收原因。
18 次请求累计 180,487 tokens，74.874 秒；按快照中的低峰费率估算约 $0.00518901，待实际账单核对。
接口字段和费率依据核对当日的 [DeepSeek Responses 文档](https://api-docs.deepseek.com/guides/responses_api/)
与 [价格表](https://api-docs.deepseek.com/quick_start/pricing/)。这 6 例仍不足以支持生产质量或安全结论。

2026-10-19 增加了独立的[新口径标注草稿](docs/evaluations/2026-10-19-rubric/adjudications.draft.json)、
[离线评分草稿](docs/evaluations/2026-10-19-rubric/report.draft.json)和[来源清单](docs/evaluations/2026-10-19-rubric/manifest.json)。
它们复用未修改的 10 月 15 日 capture 和价格快照，没有新增模型调用。
助手建议为 4 TP、0 FP、1 advisory，回归 precision=4/5、recall=4/4；仍为 5 例成功、1 例无效输出，安全未评测。
这是观察首轮结果后提出的口径及助手草稿，待人工复核；原草稿和 pending 结论均保留，不作为预先确定的正式质量基线。

普通离线验证（无真实凭据/模型调用）：

```bash
cd backend
go test ./cmd/revieweval ./internal/revieweval -count=1
```

## 启动前端

本机需要 Node.js 22.13–22.x。首次运行先安装依赖：

```bash
cd frontend
npm ci
node --run dev
```

浏览器打开 Vite 输出的本地地址。开发服务器会把健康检查
`/api/healthz` 转发到后端 `/healthz`，并把 `/api/v1/*` 原样转发到 Go
业务接口，所以需要同时启动 Go 后端。

页面先显示凭据入口，认证成功后使用 `/api/v1/session` 返回的 tenantId 加载最近 Task。“创建 Task”表单调用
`POST /api/v1/tasks`；表单采集仓库 ID 与不可变 head SHA，平台计算并固定 master 快照与 Review Base，提交成功后会显示后端生成的
Task ID、状态、仓库和目标，并立即更新最近列表。租户来自已认证会话，provider 当前固定为
GitLab。每次提交都有新的 request ID；同一份未修改的 Task 表单在当前页面内重试会沿用
原幂等键，修改表单或成功后再次提交才生成新键。准入、Workspace 登记和准备则由 Task ID、Workspace
版本等稳定坐标生成幂等键。`CREATED` Task 会显示“加入队列”按钮，成功后用后端返回的
`QUEUED / version 2` 替换旧对象。请求期间按钮会禁用，后端校验错误或网络错误会直接
显示在对应区域。列表请求晚到时，前端按 Task ID 和 version 合并，不让旧快照覆盖新状态。
每个 Task 卡片的“查看事件”按钮会按需加载事件时间线，不会在列表加载时为每个 Task 自动
发起额外请求。时间线支持审阅执行状态、执行 ID、Findings Artifact ID 与稳定失败码；尚无审阅触发按钮。
`QUEUED` Task 还会显示“查看 Workspace”：已有记录时展示仓库和 SHA；尚未
登记时展示 Task 已固定的仓库引用和一个登记按钮，不会让用户重复输入。REGISTERED Workspace
会显示“准备 Workspace”；成功后页面展示 READY、后端记录的真实工作目录，并允许按需查看固定
base/head 的差异。diff 展示后可归档为 Artifact，页面会显示 Artifact ID、摘要和带认证的内容读取按钮。

运行前端静态检查、测试与构建：

```bash
cd frontend
node --run lint
node --run typecheck
node --run test
node --run build
```

`node --run` 是 Node.js 22 支持的 package script 运行方式；也可使用对应的 `npm run` 命令。
前端使用固定版本 ESLint 10.12.0，检查 JS/TS 推荐规则、核心 React Hooks 规则和 Vite Fast Refresh
导出规则；`--max-warnings 0` 让告警也阻断检查。类型检查仍由 TypeScript 负责。

## 持续集成

[CI 工作流](.github/workflows/ci.yml) 在 `main` 推送和 Pull Request 时运行三个独立 job：

- 后端：从 `backend/go.mod` 读取 Go 版本，运行 `go vet` 和 race 测试。
- Go lint：固定 golangci-lint v2.13.2，使用 `backend/.golangci.yml` 的 `standard` 规则并校验配置。
- 前端：使用 Node 22 和 `npm ci`，运行 lint、类型检查、行为测试及生产构建。

本地 Go lint 使用同一版本，安装方式见 [golangci-lint 官方说明](https://golangci-lint.run/docs/welcome/install/local/)。
安装 v2.13.2 后运行：

```bash
cd backend
golangci-lint config verify
golangci-lint run ./...
```

这些检查不需要真实 GitLab token。GitHub 托管 runner 的结果需要代码推送后确认；本地通过不等于
远端工作流已运行。迁移恢复和 ESLint 规则取舍见开发手册 M13.1 第 11–13 步。

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
- `review.Service` 类似 Java Application Service：它从 Task/Workspace 取得可信 path/base/head，
  再调用 DiffReader/Runner port；执行前绑定幂等操作，校验 Findings 后归档并记录结果，Controller 不接受 revision。
- `artifact.Store` 类似带唯一键约束的内存 Repository。Go `[]byte` 与 Java `byte[]` 一样是可变引用，
  因此 Store 在写入和读取时都复制内容，不能只靠 struct/record 宣称“不可变”。
- `REGISTERED → PREPARING → READY` 类似带 `@Version` 的实体状态迁移。慢 Git I/O 发生时不会持有
  Manager 的互斥锁，因此 GET 仍能读取 PREPARING；失败回到 REGISTERED 时也递增 version。
