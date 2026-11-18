# 公司 Mac：自行配置 MCP，走一次真实需求交付

2026-11-17。代码已补充通用 MCP 连接、工具发现、模型辅助绑定、人工确认、配置 Reader/Writer、公司工单持久追踪和发布核验。
本文验收的是**真实需求与公司测试环境**。受控 MCP/自动测试仅验证实现，不代表已经连接公司平台。

流程：**需求与开发分支 → Review → 配置计划 → 批准保存/提单 → 公司工单审批 → 公司自动发布 → 核验测试环境版本和值**。
平台不代替审批人通过工单、不主动调用发布。审批通过和发布成功分别记录；应用是否加载新版本仍需应用侧验证。

## 1. 准备电脑和测试需求

准备 Go 1.27.1、Node 22.13～22.x、Python 3.9+、Git、Docker Desktop，版本以仓库 go.mod/package.json 为准。
拉取**包含 D6 MCP 改动的版本**；未推送的本机改动不会出现在另一台电脑。

```bash
git clone https://github.com/chenhaitao888/agent-platform.git
cd agent-platform
git pull --ff-only
cd frontend
npm ci
cd ..
umask 077
export AP_COMPANY_DIR="$HOME/.config/agent-platform/company-pilot"
mkdir -p "$AP_COMPANY_DIR/bin" "$AP_COMPANY_DIR/workspaces"
chmod 700 "$AP_COMPANY_DIR" "$AP_COMPANY_DIR/workspaces"
cd backend
go build -o "$AP_COMPANY_DIR/bin/mcpctl" ./cmd/mcpctl
cd ..
```

选择一个真实服务、开发完成且已推到公司 GitLab 的分支、一个可修改的测试配置空间，先用一个非敏感 key。
记录 GitLab 项目 ID、目标分支以及 app/namespace/环境含义。当前业务仓库适配器是 GitLab；本项目源码托管在 GitHub 不等于业务仓库已支持 GitHub。

## 2. 配置 MCP，发现工具

将 [连接示例](examples/company-mcp.json) 复制为私有目录中的 `mcp.json`，修改 URL、targets 和所有绝对路径。
`bindingFile` 指向稍后生成的 `binding-v1.json`，首次发现时可以不存在。
所有配置、凭据、绑定、公司说明放到仓库与 Review 工作区之外，文件 0600、目录 0700。

读/写凭据文件分别指定，HTTP 格式如下；也可以明确引用同一文件：

```json
{"headers":{"Authorization":"Bearer 在公司电脑填写实际令牌"}}
```

公司使用其他鉴权头时按实际头名填写。自定义 CA 使用 `connection.caFile` 的私有 PEM 路径。
远端要求 HTTPS；仅明确 loopback IP 允许 HTTP。URL 不含凭据/query/fragment。
无鉴权本地 MCP 可省略凭据引用。没有交互式 OAuth 登录；通过公司既有方式取得短期令牌并更新私有文件。

| transport | 连接字段 |
| --- | --- |
| streamable-http | url：公司给出的 MCP endpoint |
| sse | url：兼容旧 HTTP+SSE endpoint |
| stdio | command：绝对程序路径；args：参数数组；directory：绝对工作目录；不填 URL/CA |

stdio 凭据文件使用 `{"env":{"COMPANY_TOKEN":"…","PATH":"所需路径"}}`。
不经过 shell、不继承全部宿主环境；需要 HOME/PATH 等应明确填写。
每次调用建立会话、启动并关闭所配置进程，适合连接持久公司服务的本地 MCP 桥接程序。
该程序由部署者安装，以当前用户运行，属于可信控制平面。

```bash
chmod 600 "$AP_COMPANY_DIR"/mcp*.json
"$AP_COMPANY_DIR/bin/mcpctl" discover \
  --config "$AP_COMPANY_DIR/mcp.json" --workspace-root "$AP_COMPANY_DIR/workspaces" \
  --out "$AP_COMPANY_DIR/tools-v1.json"
```

预期：保存真实工具名称、描述、输入/输出 schema 与 hash。分别发现读/写身份可见的工具，**不调用业务工具**。
输出不覆盖已有文件，重新发现使用新文件名。

## 3. 模型建议绑定，人工确认

将 [流程说明模板](examples/company-process.md) 复制为 `company-process.md`，补上环境映射、状态枚举、版本/幂等规则。
它是供模型理解的公司说明/Skill 内容，不作为 shell 或动态代码执行；可使用已有公司 Skill 的相关说明。
工具 schema 解释不了的业务含义在此补齐，不需要把公司工具清单发回个人电脑来改 Go。

准备私有 `binding-model.json`，使用公司允许的、支持 Responses API JSON 输出的模型或网关：

```json
{
  "url":"https://模型网关/v1/responses",
  "model":"实际模型名",
  "credentialFile":"/absolute/private/company-pilot/binding-model-token.json"
}
```

token 文件格式为 `{"token":"实际凭据"}`；可选 caFile 指定网关 CA。
这是绑定建议命令的配置，Review/代码候选仍用原模型配置。
`suggest` 会调用模型，发送工具 schema/描述和公司说明；不发送 MCP 凭据、业务代码或配置现值，不给模型执行工具的能力。

```bash
chmod 600 "$AP_COMPANY_DIR/company-process.md" "$AP_COMPANY_DIR/binding-model.json" "$AP_COMPANY_DIR/binding-model-token.json"
"$AP_COMPANY_DIR/bin/mcpctl" suggest \
  --config "$AP_COMPANY_DIR/mcp.json" --workspace-root "$AP_COMPANY_DIR/workspaces" \
  --model-file "$AP_COMPANY_DIR/binding-model.json" --out "$AP_COMPANY_DIR/proposal-v1.json"
```

打开 proposal，检查 binding 和 questions。有问题时补公司说明或修正映射，生成新草案；确认命令拒绝未解决的问题。
也可手工编辑草案。完整模板语法、输入/输出契约见 [绑定指南](../backend/internal/connector/mcpbridge/binding-guide.md)。
支持 JSON Pointer、精确枚举转换、数组映射、标量转换、按操作分支，不运行脚本或猜审批结果。

重点复核：草稿/已发布查询是否区分；保存是否检查版本/不存在条件；稳定请求 ID 是否用于幂等；工单是否绑定确切变更版本；实际发布版本如何读取。
支持合并“保存并提单”和两个独立工具。首版按**每个配置动作一个工单**执行；只支持多配置合并工单的平台还需扩展编排。
核验要求实际版本与对应发布记录一致；若共享空间被后续发布推进到另一版本，会报告漂移，不会仅凭值相同认可旧发布。

```bash
"$AP_COMPANY_DIR/bin/mcpctl" check \
  --config "$AP_COMPANY_DIR/mcp.json" --workspace-root "$AP_COMPANY_DIR/workspaces" \
  --draft "$AP_COMPANY_DIR/proposal-v1.json"

# 用 check 输出的完整 Binding digest 替换占位符。
"$AP_COMPANY_DIR/bin/mcpctl" confirm \
  --config "$AP_COMPANY_DIR/mcp.json" --workspace-root "$AP_COMPANY_DIR/workspaces" \
  --draft "$AP_COMPANY_DIR/proposal-v1.json" \
  --expected-digest '刚复核的完整摘要' --by '你的公司身份' \
  --out "$AP_COMPANY_DIR/binding-v1.json"

"$AP_COMPANY_DIR/bin/mcpctl" probe \
  --config "$AP_COMPANY_DIR/mcp.json" --workspace-root "$AP_COMPANY_DIR/workspaces"
```

check/confirm 只发现工具、检查并保存绑定。probe 调用已确认的配置查询，报告版本和条目数，省略配置值；不能代替保存/工单/发布的真实验收。
绑定与 Skill 内容、工具 schema/描述进入版本摘要；变更后重新确认、重启并生成新计划，旧批准不能使用新语义。

## 4. 服务映射与权限

填写并复制三个示例到私有目录：

- [service-catalog.json](examples/company-service-catalog.json)：真实 GitLab 项目 ID、分支、服务名、配置 resourceRef。
- [planning.json](examples/company-planning.json)：company-config 连接 ID，指向 mcp.json。
- [writes.json](examples/company-writes.json)：同一 mcp.json，明确写入 targets 和独立 Temporal 队列。

mcp.json.targets、服务目录 planning 和 writes.json.targets 必须一致。
示例 tenantId=company-pilot、subject=company-developer 与下面的 API 身份一致。
示例 allowSelfApproval=true 方便单人测试，只允许本人确认平台内保存/提单，**仍不能替代公司工单审批**。
需要平台内职责分离时设为 false，为批准者和执行者配置不同身份/grant。
首个试点只接 Config，不登记 AB；真实需求不涉及 AB 时，在页面明确填写原因。

## 5. 启动数据库、Temporal、API 和 Worker

仓库根目录启动独立基础设施；端口冲突时统一修改 JSON：

```bash
python3 infra/d4/init-local.py --directory "$AP_COMPANY_DIR/database" --port 15439
export D4_PASSWORD_FILE="$AP_COMPANY_DIR/database/password"
export D4_PG_PORT=15439 D4_TEMPORAL_PORT=17239
docker compose -p agent-platform-company -f infra/d4/compose.yaml --profile simulation up -d --wait postgres temporal
docker compose -p agent-platform-company -f infra/d4/compose.yaml --profile simulation run --rm --no-deps temporal-namespace

cd backend
# 将 101 换成真实项目 ID；auth 目录事先不创建。
go run ./cmd/authinit -dir "$AP_COMPANY_DIR/auth" \
  -tenant company-pilot -subject company-developer -repository 101 \
  -workspace-root "$AP_COMPANY_DIR/workspaces"
cd ..
```

Compose profile 名称是 simulation，但这里只启动共享基础设施，后面运行的是**写 Worker**。真实试跑保留数据库目录和数据卷。

Review 基础配置按 [其他环境启动说明](local-pilot.md#在其他环境复现启动) 准备：

1. 公司 GitLab HTTPS、CA、只读服务 token（read_api/read_repository）、允许的实际项目。
2. 当前源码的 Review Worker 镜像；按 [README 的 Worker 构建步骤](../README.md) 使用包含 Git/bash/CA 的 runtime-base，
   按 Mac 架构选择 arm64/amd64；运行 cmd/workercheck，确认 sandboxVerified=true。不要复制个人电脑的镜像 ID。
3. 公司允许的 Review 模型网关和服务凭据；启用 planningAnalyzer=model 才有模型代码候选，MCP 凭据不注入模型 Worker。

复制 [pilot 基础示例](local-pilot.example.json) 为私有 pilot.json，填写绝对路径、Docker socket、镜像 ID、GitLab/模型配置；
stateDir 使用 AP_COMPANY_DIR 的实际路径。再加入以下字段（JSON 不展开环境变量）：

```json
{
  "serviceCatalogFile":"/absolute/private/company-pilot/service-catalog.json",
  "planningConnectorsFile":"/absolute/private/company-pilot/planning.json",
  "archiveDatabaseFile":"/absolute/private/company-pilot/database/database.json",
  "writesFile":"/absolute/private/company-pilot/writes.json",
  "planningAnalyzer":"model"
}
```

两个终端分别启动（新终端重新设置 AP_COMPANY_DIR）：

```bash
# 终端一；启动不会自动发起 Review 或写配置。
python3 scripts/run-local-pilot.py --config "$AP_COMPANY_DIR/pilot.json" --check
python3 scripts/run-local-pilot.py --config "$AP_COMPANY_DIR/pilot.json"

# 终端二
python3 scripts/run-write-worker.py --config "$AP_COMPANY_DIR/pilot.json"
```

预期页面 http://127.0.0.1:5174、API 127.0.0.1:8081（以 pilot 为准）；写 Worker 出现 delivery write worker ready。
页面登录使用 auth/api-token.txt，**不是 MCP/GitLab/模型 token**；Mac 可用 `pbcopy < "$AP_COMPANY_DIR/auth/api-token.txt"` 复制。

## 6. 在页面走需求 A

| 操作 | 观察结果 |
| --- | --- |
| 登录“需求交付”，填需求 A、服务、开发分支 | 真实授权服务与 test 环境 |
| 固定分支快照 | 源/目标/merge-base SHA 对应公司 GitLab，READY |
| 开始多服务审阅 | 真实代码与配置模型产生 Findings，人工复核 |
| 分析并生成计划 | MCP 读取配置现状，模型提取候选；填写期望值/理由，消除缺值和冲突 |
| 保存交付包 | 计划、快照与证据进入 PostgreSQL，记下包 ID |
| 批准保存与提单 | 平台内短期、具体计划授权，尚未修改公司平台 |
| 保存配置并提交公司工单 | Worker 调用绑定的 MCP，显示真实工单 ID/链接、变更 ID/版本 |
| 去公司工单系统审批 | 平台等待公司审批；通过后继续等待自动发布 |
| 等公司发布 | 发布记录、实际版本和值一致后显示“公司交付完成 · 测试环境已核验” |

人工对照公司平台：环境/namespace 正确、只改计划内 key、工单内容/版本正确、发布记录与实际值一致。
后台每 30 秒查询一次；页面只读取后端状态，不重复提单。关页或重启 Worker 后从同一交付包继续查看。
已归档包/执行可跨进程恢复；未归档草稿、审阅过程仍有既有内存存储限制。

## 7. 异常验收与限制

- 公司审批拒绝/撤回：显示明确终态，不显示成功。
- 等待审批时关页、重启写 Worker：继续同一工单，不重复创建；审批后仍可完成核验。
- 审批通过但发布失败：显示发布失败，不能把通过当成完成。
- 变更版本或实际值不符：显示证据不匹配/发布漂移，不能认可另一个变更。
- 保存/提单结果不确定：按原稳定 ID 回查，不重发。仅在公司有故障演练方法时人为制造丢响应。

撤回本地批准只停止尚未发送的动作，不能撤销公司已收到的工单或阻止公司已授权的自动发布。
提交后查询使用当前读取权限；过期写许可不会变成长效发布许可。
补偿需新计划与独立批准，MCP 配置补偿也经过公司工单/自动发布；新增配置删除仍是人工事项。

通用配置不等于所有 MCP 都满足交付契约：需要原子版本/不存在条件、稳定幂等键与回查、提交时间和有效期、
确切变更版本的工单绑定、发布记录、实际环境读取。只返回自然语言“成功”或缺少这些能力时，草案应提出问题，不能伪造结果。
公司可补充能力或提供契约包装；已有能力的工具名/字段差异通过配置适配，无需改 Go。

| 现象 | 排查 |
| --- | --- |
| discover 失败 | VPN/网络、endpoint/协议、CA、读写凭据；stdio 检查程序路径及显式 env |
| check 提示 schema/Skill 变化 | 重新发现/生成、检查工具 hash、确认新版本 |
| probe 或计划读取失败 | 参数/结果编码、草稿版本/类型、目标映射 |
| QUEUED 不动 | 数据库/Temporal、namespace/taskQueue 与写 Worker |
| 保存前停止 | 权限、自批规则、分支 SHA、平台版本或绑定变化 |
| tracking_unavailable | 公司查询暂时失败，恢复后自动重试读查询 |
| tracking_permission_or_binding_changed | 当前读权限/原绑定变化，恢复后继续，不重新提单 |
| 不确定且查不到 | 不能认定没执行；保留待核对，人工对照稳定请求 ID |

模型建议是 CLI 的独立 Responses 请求，不继承个人 Codex 的 MCP/Skills。协议使用 [官方 MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0)。
URL、程序、凭据和资源 allowlist 来自部署配置；运行时仅调用已确认工具，变更请求不自动重放。
业务参数不进入 Temporal history，新流程为 delivery.company.v1，旧 delivery.write.v1 保持原语义。

## 8. Mac 验收后到 ECS

Ubuntu 22.04 首期使用单台 ECS：API、写 Worker、PostgreSQL、Temporal 使用明确 loopback 地址，以普通服务用户运行。
重新配置凭据/CA/绝对路径/Docker socket，按 CPU 架构重建并验证 Review Worker，确认 ECS 到公司 MCP/GitLab/模型网关可达。
先重复测试空间验收，再接团队流程。数据库备份/迁移及服务托管按部署环境落实；保留真实试跑数据卷，不能照搬 Mac 的路径和镜像 ID。
