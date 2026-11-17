# D5.3：配置/实验写入部署与业务入口

2026-11-17。已装配独立写连接、当前权限策略、业务 API、专用 Worker 和交付包页面。
默认关闭，只有可信部署明确配置 `AGENT_PLATFORM_WRITES_FILE` 才能使用。**本轮没有更改或重启常驻试点，也没有接入公司平台。**
控制层和下游协议分别见 [写入执行](write-execution.md)、[参考 Writer](reference-writes.md)。

## 部署配置

前置条件是已有认证、服务目录、GitLab、Planning Readers 和 PostgreSQL 交付存储。
写配置独立于 `PLANNING_CONNECTORS_FILE` 和 `SIMULATION_FILE`，样例见 [writes.json](examples/writes.json)。
示例的 `.invalid` URL 和绝对路径是占位符；仓库没有可直接启动的公司配置/AB 服务。

每个写连接必须明确给出：

- 连接 ID、kind、声明版本、HTTPS 地址、CA 与独立指定的 credentialFile。
- 完整 tenantId/environment/connectorRef/resourceRef 目标列表，不能使用通配符，最多 64 个目标。
- 目标必须在当前服务目录的 Planning 绑定中登记；只读绑定本身不会授予写入权。

首版参考 HTTP adapter 要求 Reader/Writer 的 ID、kind、声明版本、baseUrl 和 CA 路径一致，确保针对同一平台资源。
写凭据没有默认值，不会自动继承 Reader 的凭据。凭据文件格式仍为 `{"token":"..."}`，凭据值不写入本仓库。
Writer 的 HTTPS/响应上限、禁止代理/重定向/Cookie 和 POST 不重放规则不变。

写配置、凭据和相关 CA 必须为绝对路径、工作区外、当前用户拥有的私有普通文件；不允许链接或其他用户权限。
推荐文件 0600、目录 0700。API 身份文件仍严格要求 0600；目录文件沿用既有部署验证规则。
API 和专用写 Worker 都属于可信控制平面，会读取写连接凭据；浏览器、Review/候选分析的模型 Worker 不接收这些凭据。
Credential Broker、公司 IAM/MCP 身份交付仍属于后续部署适配。

Temporal 地址目前只允许明确的 loopback IP 和端口。写任务队列必须与同一 Temporal namespace/server 下的模拟队列不同。
修改 Temporal address/namespace/taskQueue 需要重启 API 和写 Worker；运行中的策略会拒绝与启动时路由不一致的新写入。
其他连接/凭据/CA、目录、身份或 self-approval 设置变化会在当前授权时重新读取；旧批准的策略摘要不再匹配时停止新动作。
连接路由/实现语义变化也应更新声明的 connection version，不能仅复用旧 ID 冒充原平台。

目录 grant 增加三个明确操作（仍需 `read`）：

```json
{
  "subject": "delivery-approver",
  "tenantId": "tenant-local",
  "serviceId": "S1",
  "environment": "test",
  "operations": ["read", "approve_write", "revoke_write"]
}
```

执行者另授 `execute_write`；一份多服务计划要求具备全部服务的相应权限。
`allowSelfApproval` 默认 false，计划创建者和封存者不能自行批准。本机受控测试可以显式设为 true，并进入策略摘要。
模拟批准与真实写批准没有转换入口，模拟角色也不会自动获得这些新权限。

## 启动与身份更新

API 设置 `AGENT_PLATFORM_WRITES_FILE=/absolute/private/writes.json`，启动时只校验配置，不修改外部平台。
本机 launcher 可在私有 pilot JSON 中添加 `writesFile`；它仅把此路径传给 API，不传给前端。
API 接收执行请求后只持久保存并排队，不调用外部写入。

专用 Worker 单独启动：

```bash
python3 scripts/run-write-worker.py --config /absolute/private/pilot.json --check
python3 scripts/run-write-worker.py --config /absolute/private/pilot.json
```

`--check` 只检查本地文件，不证明平台/数据库/Temporal 可达；真正启动 `cmd/writeworker` 后才连接 PostgreSQL/Temporal。
launcher 过滤模型、Review、浏览器与父进程额外凭据环境；它复用 pilot 的文件/路径检查，不读取模型密钥内容。
原 `run-delivery-worker.py` / `cmd/deliveryworker` 仍只启动 SIMULATION Worker，不能替代写 Worker。
没有 Worker 时请求停在 QUEUED，启动 Worker 可能执行此前已批准且仍有效的排队请求。

启用写入后，`/api/v1/session` 和全部 `/api/v1/deliveries...` 请求先重新加载 Bearer 身份文件。
凭据被替换后旧 token 立即无法用于这些新请求；交付包列表、详情、Findings 证据和写入结果也逐次检查当前服务/仓库读取权限。
回查只要求当前读取权限，可在原批准撤回后补记旧请求事实；不会因此授予新写入权。
原 Task/Change 工作台其他路由仍使用启动时认证/目录，需要重启才能完整应用其配置变化。

## 业务 API

所有路径均在 `/api/v1/deliveries/{deliveryId}` 下，使用当前 Bearer 身份；跨租户对象不可见。
严格 JSON 拒绝主体、模式、策略摘要、Writer、URL、Intent、Completed、未知/重复/null 字段。

| 路径 | 用途 |
| --- | --- |
| GET `/write-access` | 当前主体可批准/执行/撤回的能力，不授予许可 |
| GET/POST `/write-approvals` | 列表或批准；新建 201、幂等重放 200 |
| POST `/write-approvals/{approvalId}/revoke` | 按当前版本撤回 |
| GET/POST `/write-executions` | 列表或排队；新建 202、幂等重放 200 |
| GET `/write-executions/{executionId}` | 读取持久状态、回执与核验 |
| POST `/write-executions/{executionId}/reconcile` | 对 UNKNOWN 执行只读核对；body 为 `{}` |

批准请求包含 idempotencyKey、expectedDigest、ttlSeconds（1～3600）、reason、findingsReviewed。
执行请求只含 idempotencyKey、expectedDigest、approvalId；撤回请求只含 idempotencyKey、expectedVersion、reason。
可以声明 tenantId，但必须与认证身份一致。查询与结果核对也检查执行记录确实属于 URL 中的交付包。
核对接口不接受 operationId 或重发参数；重复核对已结束的记录返回 `write_not_unknown`，通过 GET 读取结果即可。
公开执行记录包含请求摘要和结果证据，省略内部 Intent、业务请求载荷和回查等待时间。

## 页面与结果语义

交付包页面在计划/Findings 旁展示“配置与实验写入”：目标环境、保存生效方式、批准理由/期限、明确执行确认、撤回和状态。
浏览器只提交引用和人工决定，不决定资源地址、凭据、操作 ID 或 Writer 参数。
页面载入/轮询不会自动批准或执行；执行状态只做 GET 轮询。

API 回复丢失时保留原请求体与幂等键，允许重试本次申请；这不是重发外部 Writer 请求。
UNKNOWN 单独提供“只读核对结果”，不会解除剩余动作的阻断。身份或交付包变化会中止等待并丢弃迟到结果。
过期批准不可选，权限检查失败时清空旧记录并禁用操作；服务端始终再次授权。

publish_required 仅表示保存后仍需发布，实验 DRAFT 不激活、不参与分流。部分成功、漂移与 UNKNOWN 的处理仍遵守 D5.2。
D5.4 已增加明确的配置补偿计划与独立批准，见 [处置与补偿](compensation.md)。没有自动回滚、配置发布、删除配置、激活实验或变更线上流量入口。

## 验证与下一步

已通过受控 HTTPS 配置/AB 平台、真实 PostgreSQL/Temporal、独立 API/写 Worker 进程的联调。
覆盖配置更新/新增与未激活实验草稿、当前撤权/自批限制/代码漂移/连接和队列变化、严格输入、跨租户/跨包引用、
凭据轮换和证据读取撤权、API 重启幂等、写 Worker 在远端提交后 SIGKILL 再启动、UNKNOWN 撤回后纯回查。
页面通过真实 HTTP 完成批准/执行及 API 丢响应重放，也覆盖未知结果下撤回和只读核对。
受控平台在这些 API 测试中由测试进程持有；远端 PostgreSQL 回执和平台重建仍由 D5.1/D5.2 的参考契约测试覆盖。
没有真实模型推理或公司平台操作，常驻试点未被改动。

D5.4 已实现独立处置报告和配置更新原值恢复，新增项/实验项保留为人工事项，见 [处置与补偿](compensation.md)。
下一步 D6 需要公司平台文档、测试空间、资源映射和凭据交付方式；模型候选质量评测仍单独推进。
