# 模型候选分析：只读接入与验证边界

2026-11-17。模型候选通路已实现并通过受控推理 fixture 验证；尚未采集真实模型输出或完成企业代码质量评测。
它补充 D3 规则分析，沿用 D4 交付包保存，不改变模拟批准的用途。

## 启用方式

API 部署显式设置 `AGENT_PLATFORM_PLANNING_ANALYZER=model`；省略或设为 `literal` 保持原规则行为。
本机启动器对应可选字段 `"planningAnalyzer":"model"`。该设置只传 API，浏览器和交付模拟 Worker 不接收。

模型模式要求已有认证、服务目录、Planning Readers 和完整 Review Docker/模型部署。
它复用 Review 的模型、网关、凭据文件和单次超时，不支持请求选择模型、URL、提示词、Skill 或 MCP。
服务主体仍需所有服务的 `read/plan` 及仓库权限。启用意味着明确生成新计划时可能产生模型费用。

必须用当前源码重新构建 Review Worker，并把不可变镜像 ID 写入私有部署配置，构建入口见
[构建脚本](../scripts/build-review-worker.py) 和 [Worker 镜像](../worker/Dockerfile)。
启动预检检查 `candidatesSupported`；旧 Worker 在模型模式下被拒绝，规则模式仍支持旧镜像。
预检不读取模型凭据或发起推理。本轮没有修改私有试点配置、重启既有 API 或调用真实收费模型。

## 从输入到计划

1. 明确点击“分析并生成新计划”，沿用 `POST /api/v1/changes/{id}/plans`、幂等键与 expectedVersion。
2. 从已冻结快照的 READY Workspace 读取固定 base/head diff；不重新解析分支，也不自动准备工作区或发起 Review。
3. 从可信目录绑定的 Reader 读取配置/实验现状。同次计划按实际资源缓存读取结果，所有服务和后续规则共用该版本。
4. 只给模型需求标题/描述、环境、固定 diff、服务/SHA/快照、资源 key/类型/状态/版本。
   模型可在相同只读 Workspace 查看文件上下文；平台读取到的值、secretRef/secretVersion、约束值、完整实验定义、
   连接/资源地址和凭据不进入模型上下文。结构化 requirements 的 desired 和 reason 也不发送给模型，它们在平台确定性合并。
5. Codex 使用独立的 `model-candidates.v1` JSON Schema 输出配置/实验 key、理由、代码行证据及待核对问题。
6. Worker、宿主和规划器依次验证输出，再与 `literal-calls.v1` 候选合并；模型不能删除规则候选。
7. 显式要求填充期望值，沿用类型、范围、共享资源冲突、版本条件和未激活实验草稿规则。
   未填值、未知依赖、读取失败或覆盖缺口仍阻断计划。

模型不接收平台写工具或凭据；其输出不能指定目标连接、环境、批准或执行动作。
真实公司 Skill/MCP 的接入仍由后续 Connector 工作完成，不能把此模型通路视为已经接入公司平台。
原始代码和需求文本沿用 Review 的输入边界；本步没有实现通用秘密扫描或数据脱敏，仓库中的硬编码秘密不会自动被移除。

## 输出与证据

Schema 文件：[model-candidates-v1.json](../backend/internal/planning/model-candidates-v1.json)。

- 顶层必须完整匹配 `schemaVersion/inputHash/snapshotHash/serviceId/baseSha/headSha`，包含 candidates 和 questions 数组。
- 单份输出最多 256 KiB、100 个候选、每候选 10 条证据、20 个待核对问题；理由最多 1024 字节。
- 候选只能是 `config/experiment`，key 使用现有受限标识符；禁止重复候选、重复证据、未知/重复/大小写别名字段、null 和尾随 JSON。
- 每条证据必须对应固定 diff 的实际新增行，拒绝上下文行、虚构行号和越界路径。
  这验证证据位置，**不证明模型对该行的语义判断正确**；理由和业务意图仍需要人工审阅。
- 模型响应不接受 desired、connectorRef、任意动作或批准字段。不能猜出的动态 key 使用 questions，产生 `analysis_question` 阻断项。
- 模型超时、无效输出等记为 ANALYSIS_FAILED，不静默降级成规则分析成功；新键生成新计划才能重试。
- 空输出是 NO_CANDIDATES，仍需明确不涉及依据；模型不能自行产生 NOT_INVOLVED。

计划标识 `analyzerVersion=model-and-literal.v1`。每服务保存 `analysis`：独立 schema 版本、输入/模型输出摘要、模型名和运行时摘要。
运行时摘要来自现有完整 RuntimeIdentity（含镜像、CLI、模型与沙箱配置）；inputHash 不包含本地工作区路径。
候选保存 `origin=model/literal/model+literal`、理由和绑定到服务/head 的证据，questions 也包含在计划摘要中。
原始未校验响应不保存，平台元数据投影和 diff 目前仅保留摘要及既有源版本引用，不是可完全回放模型提示的证据存储。
显式归档后，这些已校验信息随交付包保存；API 重启后的读取与归档同键重放不调用模型。
新字段使用可选编码，历史规则计划和交付包的摘要保持兼容，无需数据库迁移。

## 运行与恢复

复用 `network=none`、只读挂载、非 root、能力裁剪、seccomp、原生只读 sandbox、临时 HOME、忽略仓库规则、
宿主受控模型转发和超时清理。只有平台内置的 Review/候选两种协议，不开放任意 schema/prompt 执行接口。
真实 Docker 验收用最新 Worker 和原生 sandbox，仅将推理 CLI 换为受控替身；同时检查工作区不能写、工具不能访问模型中继。

规则计划仍限时 30 秒；模型计划整次上限 10 分钟，逐服务复用 REVIEW_TIMEOUT（默认 90 秒，最多 2 分钟）。
服务按顺序分析，剩余服务遇总超时记录未分析；API 写响应与优雅退出窗口随模型模式延长。
这仍是同步、进程内的计划生成；API 重启不会恢复在途分析，Temporal 目前只恢复归档后的模拟流程。
同键重放、列表、详情、历史切换和页面刷新均不启动推理；只有新计划请求会重新读取并分析。
本步未为候选调用提供独立 token 账本、预算门禁或持久任务队列。

## 验证与后续

覆盖独立输出校验、规则合并、平台元数据投影、缺失值/动态依赖、模型失败、同键重放、前端 HTTP 流程、
归档后 API 进程重启读取，以及真实 Docker/原生 sandbox。全部使用合成数据和本地模型替身。
真实模型能否准确识别公司包装 API、常量传播和复杂动态 key，需要代表性样例、人工标签和显式授权的模型采集验证。

D5.1 已补齐 [参考 Writer 契约](reference-writes.md)：配置 CAS、未激活 AB 草稿、未知结果回查及逐项核验。
D5.2/D5.3 已完成 [写入控制层](write-execution.md) 和 [部署/API/前端](write-deployment.md)，默认关闭；D5.4 已补充 [独立配置补偿](compensation.md)。
在公司平台文档和测试空间到位后，再实现对应 MCP/API adapter；SIMULATION 批准始终不能用于真实写入。
