# D5.1：参考 Writer、条件写入与回查核验

2026-11-17。D5 第一段已实现配置/AB Writer 的副作用契约，用受控 HTTPS 平台验证实际状态修改、
幂等回执和回读核验。D5.2/D5.3 已补充独立批准、持久执行和显式业务入口；默认关闭；D5.4 已补充独立配置补偿，见 [处置与补偿](compensation.md)。
见 [写入执行](write-execution.md)、[写入部署](write-deployment.md)。
现有 Planning Readers 保持 GET-only，D4 的 SIMULATION 批准与回执没有任何调用 Writer 的路径。

## 代码入口与边界

- [resourcewrite](../backend/internal/resourcewrite/intent.go)：Intent、Receipt、Verification、Writer 端口及逐项请求派生。
- [reference Writers](../backend/internal/connector/reference/writers.go)：NewConfigWriter / NewExperimentWriter，独立于 Planning Reader 装配。
- [回读核验](../backend/internal/resourcewrite/verify.go)：按回执中的确切资源/条目版本核对值和生命周期。
- [参考平台 fixture](../backend/internal/connector/reference/write_platform_test.go)：仅在测试中存在的 HTTPS 平台，分别使用内存和 PostgreSQL。

Writer 构造需要可信部署给定 HTTPS、连接 ID/版本、非空服务凭据和精确的 tenant/environment/resourceRef 目标列表。
没有通配授权，不接受用户或模型上传 URL、目标绑定、凭据或 Writer 版本。
传输最多 5 秒、64 KiB 回执；禁止重定向、Cookie jar、环境代理和不校验证书的 TLS，沿用受控根证书。
POST 关闭传输层重放，不因网络错误自动发送第二次请求。

D5.3 已增加私有 WRITES_FILE 开关；未加载公司写凭据，也没有安装常驻参考平台。
`PLANNING_CONNECTORS_FILE` 仍只创建 Reader。下述接口是 Connector 的下游协议，不是 Agent Platform 对浏览器开放的业务接口。

## 写入请求不是批准

`resourcewrite.Prepare` 从有摘要的 READY_FOR_REVIEW 计划派生 `resource-write.v1` Intent：

| 字段 | 作用 |
| --- | --- |
| operationId | 稳定外部幂等键，由未来持久账本生成；重复调用不得更换 |
| planHash / actionId / actionHash | 绑定原始计划和动作 |
| kind / target / key / operation / effect | 明确资源、操作及保存后的影响 |
| reader / writer | 固定读写协议、实现和连接版本 |
| originalResourceVersion / resourceVersion | 原始观察版本及本次请求需要匹配的资源版本 |
| mustNotExist / expectedVersion | 创建必须不存在；更新必须匹配条目版本 |
| value / experiment | 计划中各服务已一致的类型化期望值；敏感配置仅使用 secretRef/secretVersion |
| expiresAt / hash | 不可变请求期限及完整请求摘要 |

只支持 config.create/update/no_change 与 experiment.create_draft/update_draft。
没有删除、发布、实验激活或调整线上流量操作。prepare 拒绝摘要失配、共享值冲突、未补值、未知协议/操作和目标租户/环境失配。

Intent **没有授权含义**。其 hash 只是完整性摘要，不是签名或权限证明。
D5.2 控制层要求独立写批准、当前主体与目标权限核验、原代码输入重验、批准消费与 Operation Ledger 事务，
并持久保存本次 Intent，之后才能调用 Writer。不能从浏览器接受 Completed/Intent 作为可信执行证据。

## 同资源的连续动作

每个写入同时要求资源版本和条目版本/不存在条件成立，条件检查、资源修改与回执必须在远端同一事务完成。
这样平台约束、保存语义或其他配置发生变化时，旧意图也不能静默执行。

例如同一 namespace 的两项计划都观察到 v1：第一项写入后得到 v2，第二项不能继续用 v1，也不能重新读取“最新版本”直接写。
Prepare 要求从第一项开始的完整成功前缀，每项都包含匹配原计划的 Intent、APPLIED 回执与 VERIFIED 回读记录。
它只沿本次成功回执将同一资源的预期版本推进为 v2；不同资源仍使用各自原版本，条目前置版本不变。
原观察版本仍保留，所有关联摘要必须匹配，失败/未知/未核验项不能作为推进依据。
若别人随后修改为 v3，第二项请求仍要求 v2，远端返回条件失败，禁止自动吸收 v3。

D5.2 已先保存派生好的 Intent 再发送，重试读取原 Intent，不能重新调用 Prepare 生成新的期限、版本或 hash。
这也意味着 D4 模拟的“每一步重验整个原始 Plan”不能直接用于真实写入：本次成功写入本身会推进版本。
D5.2 已结合已保存的成功前缀重验后续条件，保留原 Plan 不变，并继续逐步检查代码、绑定和当前权限。

## 参考 HTTP 契约

```text
POST {baseUrl}/v1/config/operations
POST {baseUrl}/v1/experiment/operations
Authorization: Bearer <deployment service credential>
Idempotency-Key: <operationId>
Content-Type: application/json
Body: resource-write.v1 Intent

GET {baseUrl}/v1/{config|experiment}/operations
    ?tenantId=...&environment=...&resourceRef=...&operationId=...&intentHash=...
Authorization: Bearer <deployment service credential>
```

200 返回严格 `resource-write-receipt.v1`。回执绑定 operationId、intentHash、kind、完整 target 和 key，
包含稳定回执 ID、决定时间和以下终态之一：

- APPLIED：写入前/后资源版本、条目版本、值摘要和实际保存语义。改变资源的动作必须产生新资源/条目版本。
  config.no_change 只记录已满足要求，资源版本与条目版本不变。
- REJECTED：持久拒绝决定，code 仅允许 precondition_failed、intent_expired、unsupported、value_rejected；不包含成功版本或值摘要。

远端必须先按租户和 operationId 查重：同一完整请求返回原回执，不执行第二次；不同请求返回 409 和
`{"schemaVersion":"resource-write-error.v1","code":"idempotency_conflict"}`。回执不能因当前资源后来变化而改写。
远端也在持锁的决定点检查 expiresAt；已经发出的请求可能先于撤回完成，不能把取消等待当作取消远端效果。
调用方本地到期后拒绝新的 Apply，但 Lookup/Verify 仍可查明此前发生的事实。

未知字段、重复字段、null、越界响应、目标/摘要/版本失配、重定向和上游错误正文都不能成为成功证据。
时间戳使用严格 JSON 字符串时间格式；普通对象仍拒绝重复、未知和大小写别名字段。

## 未知结果与核验

网络超时、连接关闭、5xx、无效回执等返回 ErrUnknown，不返回原始上游正文或凭据。
Lookup 的 404 也返回 ErrUnknown：原请求可能仍在途，尚未提交回执；不能凭“未找到”认定未执行并重写。
当前 Writer 不包含自动重试器。D5.2 调度器只回查已发送请求；无法确认时保留 UNKNOWN 并停止剩余动作。

找到 APPLIED 回执后，Verify 使用 GET-only Reader 重新读取真实资源：

- 配置：资源版本、条目版本、值/密钥引用摘要和保存语义都必须一致。
- 实验：除上述版本与定义摘要外，还需 DraftNoTraffic 能力及实际 DRAFT 生命周期。
- 匹配返回 VERIFIED；后续修改、缺失或生命周期变化返回 DRIFTED；无法可靠读取返回 ErrUnknown。

DRIFTED 不抹掉先前 APPLIED 事实，也不触发重写。publish_required 仅证明配置已保存，不能证明已发布或应用已加载。
本步没有跨平台原子事务或自动回滚；部分成功保留原回执，失败前缀不能派生后续动作。
补偿必须另建反向计划，使用当前条件和独立授权；不能覆盖别人后来的修改。

## 已验证与尚未完成

内存与 PostgreSQL 两种受控存储运行相同 HTTP 契约。PostgreSQL fixture 用独立随机 schema，
在单行 JSON 文档事务中串行修改资源与回执；它是故障验收替身，不是公司平台的实现或生产服务。
测试关闭并重建 HTTP 服务、关闭并重开数据库连接，只依赖 PostgreSQL 恢复回执。

覆盖配置新增/更新/no_change/密钥引用、未激活实验草稿创建/更新、并发 CAS、同键并发请求、不同内容冲突、
已提交但丢响应、在途请求查询不到回执、到期、重启、共享资源版本续接、部分成功、后续漂移和敏感输出边界。
没有调用真实模型、公司配置/AB 系统，也没有改变常驻试点。

D5.2 已完成独立写入批准、持久 Intent/回执/核验和专用 Temporal 控制层；策略端口与恢复规则见 [写入执行](write-execution.md)。
D5.3 已装配可信部署策略/写连接、独立 Worker、业务 API 与前端；D5.4 已实现独立处置/配置补偿。
真实公司适配留给 D6：其 API/MCP 必须兑现上述原子条件、幂等和可查询结果；不能只翻译字段或凭最终值猜测是否执行。
