# D4.1：持久交付包

2026-11-16。D4 的第一个可验收切片：显式保存选定计划、代码快照和审阅证据，API 重启后仍可读取和幂等重放。
审批、Operation Ledger、Outbox、Temporal 模拟执行及原工作台全量持久化尚未接入，不能将本切片称为 D4 全部完成。

## 保存范围

在需求计划下点击“保存当前计划为交付包”。服务端取得已有不可变计划，核对其摘要，
再收集对应需求说明、READY 代码快照、审阅执行与 Findings 内容。客户端只能提交幂等键和预期计划摘要，
不能上传替代计划、Artifact 内容、SHA 或数据库地址。

交付包采用独立随机 ID，包含以下内容：

- 原需求 ID、标题、说明、目标环境、保存人和保存时间。
- 固定快照、完整仓库引用和原计划（含观察版本、动作及原 plan hash）。
- 原审阅执行、Artifact 元数据与完整 Findings 内容。
- 整包摘要与版本化格式 `delivery-archive.v1`。

READY_FOR_REVIEW 和 BLOCKED 计划均可保存。失败/未开始的审阅保留其实际状态，不补造成功证据。
保存不代表批准、外部执行或需求交付完成，也不重新访问 GitLab、配置/AB 平台或模型。
一包紧凑 JSON 最大 8 MiB；Findings 继续使用原大小、类型、租户、工作区、SHA 和 schema 校验。

快照保存的是不可变代码版本引用，不包含 Git 源码或 diff 正文。
原 Change/Task/Workspace/Review 草稿仍在内存，未保存就会随 API 重启丢失。
交付包从 PostgreSQL 独立读取，不恢复成可继续运行的旧 Task，也不依赖旧工作区目录。
后续计划变化不覆盖已保存版本；保存时的资源基线也不代表现在仍未漂移。

## 本机部署

已验证 PostgreSQL 17.12、pgx v5.11.0、Go 1.27.1。镜像在 [Compose 文件](../infra/d4/compose.yaml) 中同时固定版本和 registry digest，
连接仅绑定 loopback。版本依据见 [PostgreSQL 官方版本策略](https://www.postgresql.org/support/versioning/)、
[官方镜像](https://hub.docker.com/_/postgres) 和 [pgx 发布记录](https://github.com/jackc/pgx/releases/tag/v5.11.0)。

生成一个新的私有配置目录，脚本不会覆盖已有目录、打印密码或启动服务：

```bash
python3 infra/d4/init-local.py --directory /absolute/private/d4 --port 15432
D4_PASSWORD_FILE=/absolute/private/d4/password D4_PG_PORT=15432 \
  docker compose -f infra/d4/compose.yaml up -d --wait
```

父目录必须已经存在，目标目录应在源码和 Workspace 之外。脚本生成 `0700` 目录以及 `0600` 的 password/database.json。
数据库使用独立 Compose 项目/数据卷，不复用 GitLab 内部数据库。默认端口为 127.0.0.1:15432，可显式调整，不能覆盖已占用端口。
官方镜像首次创建数据卷时设置凭据；更改密码文件不会自动轮换已有数据库密码。

API 增加：

```text
AGENT_PLATFORM_ARCHIVE_DATABASE_FILE=/absolute/private/d4/database.json
```

继续保留原认证、服务目录和 GitLab/Workspace 配置。给需要保存的身份/服务/环境显式增加 `archive` 操作，
并保留 read；其他旧权限不会自动获得保存能力。已有交付包的读取仍要求所有涉及服务的 read、原仓库白名单和可信租户。
启动器可选字段为 `archiveDatabaseFile`，只传给 API，不进入前端环境。

数据库文件是 `{"url":"postgres://..."}`，API 检查绝对路径、Workspace 外位置、当前用户所有权、无组/其他用户权限、
非符号链接/硬链接、8 KiB 上限和严格 JSON。D4.1 只接受单一显式 loopback IP、端口、数据库、用户名、
至少 16 字符的密码及 `sslmode=disable`；不支持远程数据库配置。
拒绝连接字符串里的额外选项及环境 PGSERVICE，显式固定连接坐标，避免通过隐式服务配置读取其他连接。
不打印连接串、密码或数据库原始错误。

未配置数据库时旧工作台继续运行，交付包接口返回 503。
配置了数据库但无法连接或 schema 校验失败时 API 启动失败，运行中的存储故障返回 503；不回退为内存保存。
API 正常退出时关闭连接池。

## API 与数据库一致性

| 接口 | 行为 |
| --- | --- |
| `POST /api/v1/changes/{id}/plans/{planId}/archive` | 保存服务端已有计划；首次 201，重复 200 |
| `GET /api/v1/deliveries` | 当前身份可读的交付包列表 |
| `GET /api/v1/deliveries/{deliveryId}` | 读取完整元数据、快照与计划，Findings 内容按需读取 |
| `GET /api/v1/deliveries/{deliveryId}/artifacts/{artifactId}/content` | 读取该交付包内部的原 Findings |

保存请求：

```json
{"idempotencyKey":"save-plan-A-1","expectedPlanHash":"<当前计划的完整 hash>"}
```

请求可省略 tenantId，显式声明时必须与 Bearer 身份一致。
同键异输入返回 409；原始请求重放先查询持久幂等记录，因此 API 重启、原内存计划消失后仍能重放。
浏览器对网络错误及 5xx 保留原键，避免提交可能已成功时丢失重放标识。
重放也重新检查 archive 权限；撤销保存权限后不能靠旧键绕过授权。
同一租户、原需求、计划 ID 和 plan hash 即使用新键保存也只产生一个交付包。

`agent_delivery.archives` 保存不可变 JSONB 文档；`agent_delivery.requests` 保存租户范围的请求绑定，并用复合外键约束归属。
包与请求绑定在一个 PostgreSQL 事务中提交；事务级锁和唯一约束处理并发，不依赖进程内互斥锁。
schema v1 在事务及迁移锁下初始化，记录 migration checksum；未知 schema/摘要不匹配时失败关闭，不执行降级或删除迁移。

读取时检查包摘要、plan hash、服务/SHA/执行关联及 Findings 摘要/schema，错误返回稳定的完整性失败类别。
旧临时 Task/Review/Artifact ID 在新 API 进程中被复用，也不会改变包内证据；前端读取带 deliveryId 的独立路径，
不能把已保存记录链接回新的临时 Artifact。

## 验收与边界

本轮使用独立 PostgreSQL 容器完成了：

- 数据库连接池关闭/重开、同键并发与新键去重、取消事务不留下键绑定。
- 租户/服务/仓库授权及权限撤销、请求不能伪造包正文、损坏记录拒绝读取。
- 真实 API 可执行程序停止/重启，旧工作区移走后仍读取同一包与 Findings；丢失响应重放保持同一 ID/摘要。
- 新进程复用临时 ID 时，包内容仍来自原记录；读取不触发 Review 或平台查询。
- 对本轮测试 PostgreSQL 容器强制终止并重启，18 条测试记录的数量和 ID/摘要集合保持一致。

完整命令（使用独立测试数据库，测试会添加合成记录并对自己创建的随机租户记录注入受控损坏）：

```bash
cd backend
AGENT_PLATFORM_TEST_ARCHIVE_DB_FILE=/absolute/private/d4/database.json go test ./...
AGENT_PLATFORM_TEST_ARCHIVE_DB_FILE=/absolute/private/d4/database.json \
  AGENT_PLATFORM_FRONTEND_INTEGRATION=1 go test -race ./...
```

前端 HTTP 验收需要 Node 22 在 PATH。未设置数据库测试文件时，数据库相关验收显式跳过，不视为持久化验收通过。
未设置部署变量时，默认工作台的日常测试不依赖正在运行的 PostgreSQL。

本轮测试容器、网络、合成数据卷及临时凭据已清理；镜像保留在本机缓存，既有试点未重启。

本切片没有原工作台全量状态恢复、分页/配额、备份恢复工具、生产数据库权限拆分、远程 TLS 配置或持久审批。
保存的结构化业务值和审阅结果需要按实际部署的数据政策管理；目前不提供加密存储/保留期策略。

下一切片 D4.2 在这个不可变输入边界上接审批、Outbox、Operation Ledger 与自托管 Temporal 模拟执行，
验证批准过期、撤权、资源漂移、重复投递与重启恢复，仍不开放真实配置/AB 写入。
Temporal 沿用原架构决定，部署时按 [官方自托管指南](https://docs.temporal.io/self-hosted-guide) 复核；本轮仅调研部署与版本，没有安装 Temporal 或引入 SDK。
