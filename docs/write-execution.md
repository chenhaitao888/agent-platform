# D5.2：写入批准、持久账本与执行控制层

2026-11-17。D5.1 的参考 Writer 已推送为 `cdf745c`；本步接入独立 WRITE 批准、PostgreSQL 操作账本、
专用 Temporal Workflow，以及只读结果核对，已以 `33f8870` 推送。
D5.3 已补充业务 API、前端和显式部署，见 [写入部署](write-deployment.md)；常驻试点未更改。
本步没有公司连接凭据、模型调用、实验激活、配置发布或自动补偿。

## 入口与部署边界

- [WriteService](../backend/internal/delivery/write_service.go)：Approve、Start、Revoke、Begin、Step、Finish、Reconcile。
- [写入类型与策略端口](../backend/internal/delivery/writes.go)：可信调用方提供 WritePolicy；用户不能提供策略实现或 Intent。
- [数据库事务](../backend/internal/delivery/write_store.go)、[迁移 003](../backend/internal/delivery/migrations/003_writes.sql)：批准、撤回、执行与 Outbox。
- [独立 Workflow](../backend/internal/orchestration/writes.go)：`delivery.write.v1`，活动名 `write.*`。
- [资源重验](../backend/internal/resourcewrite/revalidate.go)：基于已核验前缀检查原计划的资源条件。

WritePolicy 必须由可信部署实现，包含当前身份/服务/仓库/环境/资源授权与版本摘要、固定代码引用重验、
以及按精确目标查找 Writer。D5.3 已提供实际认证/目录/私有写连接加载与 GitLab 引用检查的部署实现。
`RevalidateRemaining` 复用实际 Planning Readers；本控制层测试仍保留策略端口的故障注入。

`RunWrites` 由 D5.3 新增的 `cmd/writeworker` 调用；`cmd/deliveryworker` 仍只启动模拟 Worker。
Planning 的部署文件仍只创建 GET Reader，原 SIMULATION API、批准、账本及 `delivery.simulation.v1` 不变。

## 批准与单次消费

批准绑定交付包摘要、planHash、逐动作 actionHash、目标、Writer 描述、批准主体与策略摘要，要求明确的理由和 Findings 已复核。
期限 1～3600 秒，默认禁止计划创建者/封存者自行批准；可信部署可显式允许，选项也进入批准策略摘要。
`approve_write`、`execute_write`、`revoke_write` 已由 D5.3 加入目录权限，必须显式授予。
调用者传入的 PolicyHash 会被服务端计算结果覆盖。SIMULATION 批准 ID 在 WRITE 表中不可见。

Start 在一个 PostgreSQL 事务中：

1. 锁定批准，核对包摘要、数据库时间与有效状态。
2. 消费批准并绑定唯一执行 ID，生成稳定的逐动作 operationId。
3. 保存完整操作列表和 `write.requested.v1` Outbox 消息。

同租户/幂等键的相同请求返回原执行；换键不能重复消费批准，相同键不同内容冲突。
批准和执行文档采用有摘要的 JSONB；整个逐动作账本在执行行锁下更新，读取前缀时获得一致快照。
撤回按版本和幂等键记录，撤回与首次派发按相同的批准行锁顺序串行化。数据库过期时间不会因重放而延长。
GetWriteApproval 返回持久状态与 expiresAt；展示“过期”时应结合时间计算，不能只看 CONSUMED/APPROVED 字样。

## 先存请求，再发送一次

每个新动作都重查批准人、执行人的当前策略摘要、绑定/Writer 版本，以及代码与资源条件。
资源检查使用原始计划加本次完整的 VERIFIED 前缀：同资源版本仅沿自己的回执推进，其他资源保持原观察版本；
原计划不变，已完成资源也继续检查版本，不能采用外部最新值来绕过 CAS。

派发事务再次核对批准有效性和操作前缀，保存确切 Intent、完整摘要和 DISPATCHED 标记后才调用 Writer.Apply。
只有首次保存标记的调用者可以发送 POST；并发执行者、Activity 重试和重启都不能取得第二次发送资格。
网络调用期间不持有数据库锁。

DISPATCHED 后有 30 秒等待窗口，避免重复 Activity 在原请求仍在途时立即查回执。窗口结束只允许 Lookup，
**不会恢复 POST 资格**。即使进程恰好在存库之后、真正发送之前退出，也保守视为可能已执行。
这种情况会损失自动继续执行的机会，需要人工处理；不会用 404 猜测未执行并重发。

远端成功回执先独立保存，再回读核验。已保存的回执不可被不同内容替换，核验失败仍保留 APPLIED 事实。
恢复核对先于新写授权：撤回/过期后仍可查询已经发出的操作，不能把撤回当成远端事务取消。

## 结果与停止规则

| 状态 | 含义 |
| --- | --- |
| SUCCEEDED | 本次各动作均有匹配回执和逐项 VERIFIED 记录 |
| FAILED | 远端持久 REJECTED，例如 CAS 失败；此前成功记录保留 |
| DRIFTED | 有 APPLIED 回执，但核验时资源版本、值或生命周期不匹配 |
| UNKNOWN | 无法确认远端决定，或已知 APPLIED 但无法完成回读 |
| BLOCKED | 批准/权限/输入失效，或发生未知结果后尚有未开始动作 |

出现拒绝、漂移或未知时，尚未开始的动作持久标记为 BLOCKED，不会自动回滚已成功动作。
未知请求经 `Reconcile` 找到回执并通过核验后，会补全事实；如果仍有被停止的动作，执行维持 BLOCKED。
本版不提供解除阻断或继续剩余动作的授权入口。应重新读取现状、生成新计划并另行批准，不能修改原 Intent 重试。
只有全体动作均已执行并核验时，回查才可能使 UNKNOWN 变为 SUCCEEDED。

Reconcile 对外部平台只使用 GET。D5.3 业务 API 会检查调用者当前的包/服务/证据读取权限；
不能因为允许记录旧执行事实而开放匿名或跨租户回查。

VERIFIED 是该次回读的事实，不是持续观察。`publish_required` 只表示已保存，不能宣称已发布或应用已加载；
AB 只支持平台保证不分流的 DRAFT。补偿仍需当前条件、独立反向计划和批准，不允许自动覆盖外部后续修改。

## Temporal 与故障验收

Outbox 与模拟表完全分离，采用租约代次确认投递；工作流 ID 固定，重复投递连接已有运行，已完成运行不重新创建。
Workflow/Activity 入参只有 tenantId/runId/operationId，活动返回 ID 列表、布尔值或状态；业务值、Intent、回执与凭据不进入历史。
无法确认结果时 Workflow 结束为 UNKNOWN，持久账本等待显式只读核对；不会以无限 Activity 重试反复 POST。
数据库暂时不可用仍允许 Activity 重试，重试只从已保存标记决定下一步。

验收覆盖：

- PostgreSQL 批准/执行/Outbox 原子提交、同键并发、批准单次消费、租户隔离与文档损坏拒绝。
- 当前权限/策略/Writer/代码/资源漂移、批准到期/撤回，以及在途撤回后的事实保留。
- 存请求后发送前的受控中断、丢响应、Lookup 缺失、并发 Step、部分成功、远端 CAS 拒绝、核验漂移。
- 实际参考 HTTPS Writers + PostgreSQL 参考平台完成配置更新、新增和未激活 AB 草稿；丢响应后关闭并重建 HTTP 服务及数据库连接，纯回查恢复。
- 真实 Temporal 服务完成成功/UNKNOWN 流程；重建 Worker、重复 Outbox 投递不重发写入，检查历史只含引用。
- v1 归档、v2 待执行模拟及 Outbox 升级到 v3 后保留，模拟批准不会自动转为 WRITE。

参考平台仅为测试 fixture，未做公司平台接入。发送前中断是测试内故障注入；Temporal Worker 重建是进程内 Worker 实例重建，
不宣称本轮进行了新写 Worker 可执行进程的 SIGKILL 验收。

## 后续接线

D5.3 已完成可信写连接、当前部署策略、独立 Worker、严格业务 API 和前端操作，见 [写入部署](write-deployment.md)。
下一步 D5.4 准备部分成功的明确处置/补偿计划并独立批准；D6 再接公司测试空间。模型候选质量仍需独立评测。
