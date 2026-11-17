# D3：只读配置与实验计划契约

2026-11-17。本文描述已实现的参考契约，不代表已接入某家公司的配置/AB 系统。
核心接口是 `planning.ConfigReader`、`ExperimentReader` 和版本化 `Analyzer`；
`connector/reference` 只提供 GET 读取，没有 Apply、批准、发布或实验激活方法。

## 1. 可信部署

原服务目录条目可增加 `planning` 数组。仓库字段、`environments` 和原有授权格式保持不变：

```json
{
  "planning": [
    {
      "environment": "test",
      "config": {"connectorRef": "config-reference", "resourceRef": "shared/settings"},
      "experiment": {"connectorRef": "experiment-reference", "resourceRef": "checkout-app"}
    }
  ]
}
```

每项环境必须在该服务的 `environments` 中；每环境只能登记一次，可只登记配置或实验。
有候选但没有映射时计划被阻断。共享 namespace/app 使用相同的连接实例、环境与 resourceRef，
核心按真实资源和 key 合并相同提案，或保留不同要求并报冲突，不能用 serviceId 绕过冲突检查。

给适用的 subject/tenant/service/environment 显式增加 `plan` 操作（仍要求 `read`），
它只允许分析和读平台现状，不授予未来写入权限。原示例目录没有自动增加权限。
生成前一次核验全部服务/环境/仓库，读取计划历史则要求相应服务 `read`。

API 设置 `AGENT_PLATFORM_PLANNING_CONNECTORS_FILE`，指向 [连接文件示例](examples/planning-connectors.json)。
启动器可选字段为 `planningConnectorsFile`，只传给 API，不传给浏览器。
连接文件必须是工作区外的绝对路径、当前用户拥有且无组/其他用户权限的普通文件，推荐 `0600`，最多 1 MiB；
凭据文件最多 8 KiB，使用相同所有权/路径检查。两者均拒绝符号链接、硬链接、重复/未知/大小写别名 JSON 字段及 null。
启动器额外限制它读取的 JSON 文件为 `0600` 和 64 KiB。

连接字段为 `id/version/kind/baseUrl`、可选 `caFile/credentialFile`；kind 只能是 `config/experiment`。
只接受没有 userinfo、query、fragment 的 HTTPS URL，CA 沿用受控 PEM 加载，不关闭证书验证。
可选凭据文件为 `{"token":"..."}`，不把 token 放到目录、计划、前端环境或模型输入里。
只有明确不需要认证的参考端点才省略 credentialFile。

启动验证全部映射的连接类型及实例存在，不读取平台资源或调用模型。部署配置改动需重启，
内存中的需求、审阅和计划随之丢失。现有本机试点未被本轮重启或修改。

## 2. 参考 HTTP 协议

两个 Reader 分别调用：

```text
GET {baseUrl}/v1/config?tenantId=...&environment=...&resourceRef=...
GET {baseUrl}/v1/experiment?tenantId=...&environment=...&resourceRef=...
Authorization: Bearer <configured token>
```

查询字段全部来自可信身份与目录绑定。请求不重定向，单次最多 5 秒，响应最多 512 KiB，
必须返回 200 和严格 JSON；失败只返回稳定类别，不转发上游正文、URL 或凭据。
当前参考平台通过受控本机 HTTPS 测试服务验证，未安装常驻模拟平台。

配置响应示例：

```json
{
  "schemaVersion": "config-read.v1",
  "state": {
    "target": {"tenantId": "tenant-local", "environment": "test", "connectorRef": "config-reference", "resourceRef": "shared/settings"},
    "version": "namespace-v7",
    "saveMode": "publish_required",
    "conditionalUpdates": true,
    "items": [
      {"key": "feature.timeout", "version": "key-v3", "value": {"type": "integer", "text": "100"}, "constraints": {"min": 1, "max": 500}}
    ],
    "secretReferences": [{"ref": "vault/service/key", "versions": ["v1", "v2"]}]
  }
}
```

`target` 必须与请求的 tenant/environment/connector/resource 完全一致。
`version` 是整个资源的观察版本，更新操作同时记录项级版本；不存在的项形成必须不存在的新增提案。
`saveMode` 为 `draft/publish_required/immediate/unknown`，unknown 阻断计划；
更新依赖 `conditionalUpdates`，已有值相同则标记 `config.no_change`。
普通值支持 string/boolean/integer/number，以 `text` 保存有界字面值；integer 使用规范十进制，number 必须有限。
范围和 allowed 列表由确定性代码校验；整数范围元数据限精确可表达的整数边界。

敏感项必须标记 `sensitive: true` 并返回 `type: secretRef`，仅含 `secretRef/secretVersion`，禁止 text。
计划里的期望密钥引用及版本还必须出现在该资源的 `secretReferences` 中；平台不返回真实密钥。
已有 secretRef 项的明文替代提案会被清除并阻断。平台漏标的普通字符串不能被当前实现可靠识别为秘密；
实际 adapter 必须正确分类，用户不能在普通值或理由里填写凭据。D4/D5 执行阶段仍需独立的凭据交付边界。

实验响应示例：

```json
{
  "schemaVersion": "experiment-read.v1",
  "state": {
    "target": {"tenantId": "tenant-local", "environment": "test", "connectorRef": "experiment-reference", "resourceRef": "checkout-app"},
    "version": "app-v4",
    "draftNoTraffic": true,
    "conditionalUpdates": true,
    "supportsExclusion": true,
    "allocationModes": ["weighted"],
    "items": []
  }
}
```

已有实验项为 `{key, version, state, spec}`，state 为 DRAFT/ACTIVE/ARCHIVED。
只生成 create_draft/update_draft，更新对象必须为 DRAFT；已有激活实验不能伪装为草稿。
必须显式保证 `draftNoTraffic=true`，不以流量填 0 代替该能力。分配方式需在平台支持列表中，互斥组需要支持能力。
spec 含 variants（名称和整数 weight，总和 100，2～10 组）、audience、metrics、allocation、可选 exclusionGroup。
这些是参考契约的语法/必填/能力检查，不宣称已实现某家公司的分群、指标或分桶语义。

每份观察及动作记录 Reader 的 kind、contractVersion、implementationVersion 和 connectionVersion。
当前实现版本为 `reference-http.v1`；适配另一平台时实现相同窄接口并提供可信语义，不能让请求上传 URL 或脚本。

## 3. 候选与覆盖

生成计划使用指定 READY 快照下现有子任务的 READY Workspace 和固定 base/head diff，不解析最新分支，
不自动创建 Task、准备工作区或启动 Review。尚无可用工作区时记录 NOT_ANALYZED 和 workspace_required；
已有工作区但审阅未成功时可提供候选，计划仍以 review_incomplete 阻断。

默认规则模式 `literal-calls.v1` 在新增 diff 行识别字面调用：

- `config.Get/GetString/GetBool/GetInt/get("key", ...)`
- `experiments.Variant/variant("key", ...)`

输出独立契约 `change-candidates.v1`，绑定 serviceId/snapshotHash/headSha，证据为路径和行号。
它不复制代码片段或默认值，不推断期望业务值，不修改原 Findings schema，也不调用模型。
这是有限的文本候选识别。现已增加可显式启用的模型候选通路，结合只读代码上下文与平台元数据，
输出理由、固定新增行证据及待核对问题，随后与规则候选合并。启用和限制见 [模型候选分析](model-candidates.md)。
模型通路目前用受控推理 fixture 验证，企业私有 API/复杂动态 key 的真实模型识别质量及结构化清单接入仍待验证。
“识别到字符串”仍是候选，不是写入指令。未识别到候选不能自动证明没有配置/实验变化。

每服务/领域分别展示 NOT_ANALYZED、ANALYSIS_FAILED、NO_CANDIDATES、NOT_INVOLVED、CANDIDATES_FOUND。
NO_CANDIDATES 必须由显式业务要求或“不涉及”理由补齐；分析失败不能靠不涉及声明掩盖。
已发现候选与不涉及声明冲突时阻断。用户要求可增加候选和填值，不能删除代码候选来跳过检查。
仓库文本及分析结果不能更换连接、环境、授权或触发外部写入。

## 4. 计划 API 与版本

`POST /api/v1/changes/{id}/plans`：

```json
{
  "idempotencyKey": "change-A-plan-1",
  "expectedVersion": 4,
  "snapshotId": "snapshot-1",
  "requirements": [
    {
      "serviceId": "S1",
      "config": [{"key": "feature.timeout", "desired": {"type": "integer", "text": "200"}, "reason": "验收要求超时 200ms"}],
      "experimentNotInvolved": "已核对该服务实验调用无变化"
    }
  ]
}
```

requirements 可省略以先分析。配置 desired 缺失时保留待补项；实验 requirements 使用 `experiments: [{key, desired: spec, reason}]`。
浏览器表单集中提供这些字段，不要求填写真实 namespace、连接地址或 SHA；初版 UI 支持两个实验分组，API 最多十组。

返回 `{changeId, changeVersion, plan}`；首次 201，同键重放 200。
`plan.state` 为 READY_FOR_REVIEW 或 BLOCKED，进行中的 GET 返回 RUNNING。
READY_FOR_REVIEW 仅表示当前参考规则没有发现待补/阻断项，仍需人工核对 Findings、业务要求与平台语义；
不是批准、写入许可、上线结论或需求交付完成。

生成开始和结束各增加一次需求 version，与快照解析/Review 互斥。规则模式整次请求限时 30 秒，显式模型模式 10 分钟；单服务模型调用沿用 REVIEW_TIMEOUT。
同键同输入等待/返回原记录；相同键不同输入冲突；新键需当前 version，重新读取平台现状并生成新计划版本。
失败/取消的已记录计划仍可重放，不隐式重试。浏览器响应丢失时沿用原 body；刷新丢失键后先读取历史。
共享资源在一次计划内只观察一次，避免各服务分别读取到不一致的基线。

`GET /api/v1/changes/{id}/plans` 列出历史；`GET /api/v1/changes/{id}/plans/{planId}` 读取指定版本，均不调用模型或平台。
计划保存确切审阅证据、源 SHA、目录/连接/契约版本、观察版本、当前/期望值、前置条件、动作 hash 和 plan hash。
修改要求、重新观察或新快照都会产生独立记录；旧计划保持不变。
没有 approvals/executions/activation 路由；生成中的工作台记录仍在内存。D4.1 已可显式保存为 [持久交付包](durable-delivery.md)，模拟批准/Temporal 与操作记录已接入 [D4.2](simulation-workflow.md)，真实写入仍未开放。
