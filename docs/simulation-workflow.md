# D4.2：持久审批与 Temporal 模拟执行

2026-11-16。本切片在 [持久交付包](durable-delivery.md) 上加入模拟批准、事务 Outbox、逐动作操作记录、
自托管 Temporal 调度和回执核对。所有批准均为 **SIMULATION**，所有效果均为独立数据库中的合成回执。
没有配置/AB 写接口，不激活实验；模拟批准不能用于未来的真实写入。

## 用户流程

1. 在“交付包”选择已经保存且 READY_FOR_REVIEW 的计划，核对 Findings、业务值与变更要求。
2. 填写理由、选择 15 分钟/1 小时/24 小时有效期，勾选“仅批准模拟”，创建一份持久批准。
3. 选择未使用的有效批准和演练场景，点击“发起模拟”。请求落库后即返回，Worker 可以稍后上线。
4. 页面只读轮询状态，显示逐项结果、错误类别、尝试次数、租约代次与回执。
5. 可填写理由撤回批准；撤回停止新增模拟效果，不抹掉已提交的回执或已完成的结果。

场景包括正常完成、响应丢失后核对、中途失败并停止后续。响应丢失场景在回执提交后制造失败，
下次 Activity 先核对原回执，不再重复产生效果。中途失败场景在第二项前失败，不足两项时在第一项前失败。
无动作计划只做前置核验后完成。READY_FOR_REVIEW 和“已核对 Findings”仍是当前参考流程的门槛，
不代表正式质量门禁、生产审批或全部问题已经处置。

## 授权与不可变输入

- 目录增加 `approve_simulation` 和 `simulate`，分别控制批准/撤回与发起模拟；必须同时具备各服务 read 和原仓库权限。
  原 read/plan/archive 权限不自动扩权，读取模拟记录仍要求当前 read 权限。
- 批准绑定交付包摘要、plan hash、批准者、理由、策略摘要和有效期。计划摘要已经覆盖逐动作 hash、资源与前置版本。
  同一个批准只能绑定一次模拟；新演练需要新的明确批准。
- `allowSelfApproval` 默认 false；同一人创建/保存并批准，仅在可信模拟配置显式设为 true 时允许。
  本机示例为 true，记录真实主体，不宣称职责分离。
- Worker 在开始和每次新增动作前重新加载可信身份/目录/规划连接文件，重验批准者和发起者的权限、
  服务仓库绑定、目录版本和真实资源映射。配置策略摘要变化会阻断旧批准，不悄悄接受新连接。
- 重新读取源/目标分支，比较全部冻结 repository reference；重新读取配置/实验现状，比较版本、描述契约、
  当前值、类型和草稿/分配/互斥等能力。漂移要求生成并保存新计划。
- 临时读取故障交由 Temporal Activity 重试，不推定验证成功。明确的过期、撤回、权限/策略变化或版本漂移进入 BLOCKED。
  主动撤回可使等待中的工作在下一次检查时停止。

API Bearer 验证仍采用启动配置，凭据轮换需要重启 API；这与 Worker 每步重新检查主体授权范围是不同边界。
配置文件应原子替换并保留安全权限，不能原地暴露半份 JSON；不完整或不可验证的策略会失败关闭。

## 持久协调与恢复

```mermaid
flowchart LR
  A[持久交付包] --> B[模拟批准]
  B --> C[事务保存 Run 与 Outbox]
  C --> D[稳定 Workflow ID 投递 Temporal]
  D --> E[重验权限与固定输入]
  E --> F[事务消费批准并创建操作记录]
  F --> G[租约和代次检查]
  G --> H[单独事务提交模拟回执]
  H --> I[读取回执并记录结果]
```

PostgreSQL schema v2 增加 approvals、revocations、simulations、outbox、operations 和 simulation_receipts。
schema v1 的包、摘要及幂等记录保持不变，迁移按顺序验证 checksum；不提供自动降级。

批准创建与撤回分别保存幂等绑定；撤回保留主体、理由和时间。
启动事务一次写入 Run 和 Outbox，API 不需要 Temporal 在线，也不在请求线程运行模拟。
Dispatcher 通过租约与 `SKIP LOCKED` 领取消息；稳定 Workflow ID 使用拒绝重复/复用正在运行的策略。
发布成功但 ACK 丢失可以再次投递；应用账本的终态与操作幂等仍是最终防线，不只依赖 Temporal 历史保留期。

执行前的校验通过后，消费批准与创建逐动作记录在同一数据库事务中完成。
操作持有有界租约和递增 fencing token，过期写者不能创建回执或覆盖新结果。
模拟器单独提交 `simulation_receipts`，再更新操作记录，刻意保留可验证的“效果完成但回复/本地状态未完成”窗口。
回执的 operation ID/action hash 必须一致；UNKNOWN 恢复先核对回执。即使批准随后过期或撤回，已提交的事实仍会记录，
只有后续新效果被禁止。业务失败停止后续动作，成功项保持不变。

Temporal Workflow/Activity 只携带 tenant/run/operation ID，返回操作 ID、布尔值或状态。
计划正文、业务值、代码和凭据留在应用存储，不能进入 history。Workflow 名为 `delivery.simulation.v1`。
不要在有未完成运行时随意更换 namespace/taskQueue 或修改旧 Workflow 定义；本切片没有提供工作流代码版本迁移工具。

## 本机部署

已验证 Temporal Server **1.32.2**、Go SDK **1.51.0**、PostgreSQL 17.12；镜像固定 tag 与 registry digest。
版本依据为 [Server 发布记录](https://github.com/temporalio/temporal/releases/tag/v1.32.2)、
[SDK 发布记录](https://github.com/temporalio/sdk-go/releases/tag/v1.51.0)。Compose 布局参考
[官方自托管部署说明](https://docs.temporal.io/self-hosted-guide/deployment) 和
[PostgreSQL 示例](https://github.com/temporalio/temporal-server-operations/blob/main/compose/docker-compose-postgres.yml)。

使用 D4.1 生成的同一私有密码文件：

```bash
D4_PASSWORD_FILE=/absolute/private/d4/password D4_PG_PORT=15432 D4_TEMPORAL_PORT=17233 \
  docker compose -f infra/d4/compose.yaml --profile simulation up -d --wait postgres temporal
D4_PASSWORD_FILE=/absolute/private/d4/password D4_PG_PORT=15432 D4_TEMPORAL_PORT=17233 \
  docker compose -f infra/d4/compose.yaml --profile simulation run --rm --no-deps temporal-namespace
```

PostgreSQL 的 agent_platform 数据库保存业务记录，独立 temporal/temporal_visibility 数据库保存 Temporal 数据。
初始化作业只创建缺失数据库并运行官方版本迁移；命名空间是单独的一次性任务，不能让 Compose 的 --wait 把它当作长期服务等待。Temporal 仅发布到 127.0.0.1:17233，未发布 Web UI。
示例是单机开发环境，共用开发数据库主体，部分启动容器以 root 读取私有 secret；不是生产部署方案。
不能把这些数据库或无认证的本机 Temporal 端口暴露到公网。

复制 [模拟配置示例](examples/simulation.json) 到 Workspace 外的私有普通文件，推荐 `0600`，设置：

```text
AGENT_PLATFORM_SIMULATION_FILE=/absolute/private/d4/simulation.json
```

沿用 AUTH_FILE、SERVICE_CATALOG_FILE、PLANNING_CONNECTORS_FILE、ARCHIVE_DATABASE_FILE、GitLab/CA 与 Workspace 配置。
模拟文件只接受显式 loopback IP/端口、namespace、taskQueue、allowSelfApproval 和 leaseSeconds（默认 30，范围 1～300）。
API 只校验和持久排队，不自动启动 Worker。

在私有 pilot.json 增加 `simulationFile` 后，可分别启动 API/前端和独立 Worker：

```bash
python3 scripts/run-local-pilot.py --config /absolute/private/pilot.json
# 另一个终端；会领取已批准且已排队的模拟任务
python3 scripts/run-delivery-worker.py --config /absolute/private/pilot.json
```

两个启动器均支持 `--check` 做本地文件预检，不是数据库/Temporal readiness 检查。
Worker 启动器不检查或占用 API/前端端口，不传模型/Review/浏览器环境，也不继承父进程模型 key。
直接运行 `go run ./cmd/deliveryworker` 时也应只提供只读连接、策略、数据库与模拟配置。
API 与 Worker 必须使用同一组可信配置和同一部署的数据库/namespace/taskQueue；本切片不是不同策略部署之间的共享调度服务。

## API

接口均以 `/api/v1/deliveries/{deliveryId}` 为前缀：

| 路径 | 行为 |
| --- | --- |
| `POST /simulation-approvals` | idempotencyKey、expectedDigest、ttlSeconds（1～86400）、reason、findingsReviewed=true；首次 201、重放 200 |
| `GET /simulation-approvals` | 读取批准记录；过期状态按数据库时间投影 |
| `POST /simulation-approvals/{approvalId}/revoke` | idempotencyKey、expectedVersion、reason；持久撤回，重放不重复更改 |
| `POST /simulations` | idempotencyKey、expectedDigest、approvalId、scenario；首次 202 表示已持久排队，重放 200 |
| `GET /simulations`、`GET /simulations/{simulationId}` | 只读当前运行和逐操作状态 |

scenario 为 success/response_loss/fail_before。body 不能指定主体、策略摘要、Workflow ID、工具、连接、动作正文或真实执行模式。
Run 状态为 QUEUED/RUNNING/SIMULATED/BLOCKED/FAILED；操作另有 NOT_STARTED/UNKNOWN/SUCCEEDED。
重放返回同一对象的当前状态，不会延长批准有效期或重新开始终态运行。
客户端在网络错误/5xx 时保留原 body 和键；刷新丢失键后，先读取批准和运行记录。

## 验证和当前限制

```bash
cd backend
AGENT_PLATFORM_TEST_ARCHIVE_DB_FILE=/absolute/private/d4/database.json \
  AGENT_PLATFORM_TEST_TEMPORAL_ADDRESS=127.0.0.1:17233 \
  AGENT_PLATFORM_FRONTEND_INTEGRATION=1 go test -p 1 -race ./...
```

使用独立测试环境；`-p 1` 避免不同测试包的 Worker 领取彼此的合成任务。
已覆盖 API/Worker 进程重启、回执已提交后的 Worker 强制退出、Outbox ACK 丢失和重复投递、批准过期/撤回、
目录权限热重验、分支/资源漂移、过期租约写者、部分失败、v1 包无损迁移，以及真实 React/HTTP/Temporal 贯穿。
Temporal 容器重启前后，选定已完成 Workflow 的 31 条事件与摘要一致；历史 payload 未含测试业务值或凭据。

本轮独立测试容器、网络、合成数据卷和临时凭据已清理，镜像保留在缓存；既有试点未重启。

本切片只有合成回执，不证明真实公司的 CAS、幂等、核对、补偿或分桶语义。
真实写入必须另行设计独立授权模式、具体 Connector 契约、未知结果处理和问题处置门禁，不能直接复用模拟批准。
原工作台未保存草稿仍在内存，当前没有生产 HA/TLS/IAM、分页配额、完整操作审计查询或真实平台补偿。
模型候选理解仍是完整目标的必要待办，literal-calls.v1 没有因本次编排接入而变成模型分析。
