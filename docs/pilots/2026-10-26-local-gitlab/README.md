# 本机 GitLab 与真实模型联调

2026-10-26 完成执行，2026-10-27 中断恢复后整理证据。使用生产代码提交 `97e0f5a`，
由新增的 `TestLiveGitLabPilotThroughAPI` 启动独立临时 API，完成以下真实链路：

GitLab HTTPS → 固定 master/head 与 merge-base → Task 准入 → bare clone / detached Workspace →
固定 diff → Docker / Codex 原生只读沙箱 → DeepSeek Flash → Findings Artifact → 同键重放及历史读取。

全程没有使用 GitLab、Docker、Codex 或模型替身。输入仍为专用私有仓库中的合成案例，不能据此认定企业项目质量达标。

## 结果与证据

- GitLab 项目 `2`：`root/agent-platform-pilot-20261023`；审阅分支为 `review/simplify-average`。
- base：`6762d4b6bb277debaac18a12d8b3df0b4057c9aa`。
- head：`9ce4c65d38c6c9c713902744e168887b3dee0624`。
- 只创建一次新的 Review 执行，执行耗时约 18.33 秒；整个测试耗时 21.50 秒。
- 返回一条 HIGH Finding，定位 `pricing.py:2`：移除空列表保护后 `average_price([])` 出现除零异常。
  该描述与准备阶段已验证的回归相符；严重度和模型置信度仍为模型输出，未当作正式人工标注。
- 同键重放及按执行 ID 读取与首次结果逐字节一致；事件始终为 8 条，工作树前后均干净且 HEAD 固定。
- 未认证请求为 401，冒用租户创建为 403，跨租户 Task / Workspace / Review / 历史执行 / Findings 读取为 404；
  跨租户评审返回 404，拒绝后事件保持原样。
- 临时 API 正常退出，检查时没有遗留平台 Worker 容器；本次临时 Task、身份与 Workspace 不会出现在常驻开发服务中。

七个 JSON 文件保持采集字节不变。`manifest.json` 记录它们的摘要、测试源码摘要、生产源提交、
TLS 验证和边界；`verification.json` 记录本次 API 可执行文件摘要与检查结果。
`execution.json` 保存实际 Worker 镜像、Codex 版本/二进制摘要、沙箱策略和模型身份。
`findings.json` 的 SHA-256、字节数与执行记录中的 Artifact 元数据一致。
路径字段指向已清理的临时工作目录，不是可复用的部署路径。

发布前已检查这些证据不包含实际 GitLab token 或模型 key。没有归档凭据、私钥或完整运行日志。
本次 API 未保存独立的上游请求计数、token 用量或账单；一次 Review 不能等同于一次模型 HTTP 请求。
摘要仅用于本地内容一致性检查，不提供第三方时间证明。

## 当前 HTTPS 部署

GitLab 入口及它公布的 HTTP clone URL 已改为 `https://localhost:8929`，旧 `http://…:8929` 入口需迁移到 HTTPS。
沿用 Docker 的宿主 `8929 → 容器 80` 映射，由 GitLab 内置 NGINX 在容器 80 端口终止 TLS。
平台 API 仍只监听 loopback，未改变监听策略。配置遵循 GitLab 的
[手动 HTTPS 配置](https://docs.gitlab.com/omnibus/settings/ssl/#configure-https-manually)及
[监听端口配置](https://docs.gitlab.com/omnibus/settings/nginx/#set-the-nginx-listen-port)。
本机 GitLab 为 18.11.1，使用该版本的顶层 `nginx[...]` 配置键。

本步追加的配置：

```ruby
external_url 'https://localhost:8929'
letsencrypt['enable'] = false
nginx['listen_port'] = 80
nginx['listen_https'] = true
nginx['ssl_certificate'] = '/etc/gitlab/ssl/agent-platform-pilot/localhost.crt'
nginx['ssl_certificate_key'] = '/etc/gitlab/ssl/agent-platform-pilot/localhost.key'
nginx['redirect_http_to_https'] = false
nginx['hsts_max_age'] = 0
```

`hsts_max_age=0` 避免本机试点为其他 localhost 开发端口留下长期 HSTS 规则。
新增 CA 仅通过平台 `AGENT_PLATFORM_GITLAB_CA_FILE` 和 Git 的部署信任传递，未加入 macOS 系统证书信任。
带该 CA 的 HTTPS 读取成功；未提供 CA 时证书被拒绝。浏览器尚未信任该 CA 时会显示证书提示。
服务器证书有效期至 `2027-01-24T06:48:05Z`，SHA-256 指纹见 manifest。

本机部署目录为 `$HOME/.config/agent-platform/gitlab-pilot-20261026/`（0700），包含：

| 文件 | 用途 |
| --- | --- |
| `ca.pem` | 平台/Git 的独立 CA 信任文件 |
| `ca.key`、`localhost.key`、`localhost.crt` | 本机证书材料；私钥不得进入仓库 |
| `gitlab-credential.json` | 项目 Reporter 凭据，仅 read_api/read_repository，0600，2026-10-30 到期 |
| `gitlab.rb.before-https` | 应用 HTTPS 前的完整 GitLab 配置备份 |
| `configuration-plan.json`、`reconfigure.log` | 配置前后摘要与本机重载日志 |

GitLab 配置文件仍为 `$HOME/gitlab/config/gitlab.rb`。容器健康检查通过。
发现已有 Runner 容器缺少 `config.toml`，本轮没有修改或验证它；测试项目的 CI 仍禁用。
使用旧 HTTP 地址的其他客户端需要按新 HTTPS 地址及 CA 信任配置迁移。

需要回滚时，先确认没有后续配置变更；有变更则合并移除本步配置，不直接覆盖整份文件。
仅恢复本步之前配置的命令为：

```bash
cp "$HOME/.config/agent-platform/gitlab-pilot-20261026/gitlab.rb.before-https" "$HOME/gitlab/config/gitlab.rb"
docker exec gitlab gitlab-ctl reconfigure
```

回滚将恢复旧 HTTP 入口，平台 HTTPS 联调配置随之失效。本次没有执行回滚。

## 显式复跑

先按根 README 的“启用 API 审阅执行”配置真实 GitLab、CA、Docker、不可变 Worker 镜像和模型服务身份。
GitLab token 从工作区外凭据文件安全载入进程环境，不能写在命令行或提交到仓库。
本机 GitLab URL 应为 `https://localhost:8929`，CA 文件为上述部署目录的 `ca.pem`。
模型凭据仍由后端读取本地文件，Worker 不持有上游 key。

随后显式执行下面命令；它会新建一次可能产生费用的模型审阅，普通测试默认跳过：

```bash
cd backend
AGENT_PLATFORM_GITLAB_LIVE_TEST=1 \
AGENT_PLATFORM_PILOT_PROJECT=2 \
AGENT_PLATFORM_PILOT_BASE=6762d4b6bb277debaac18a12d8b3df0b4057c9aa \
AGENT_PLATFORM_PILOT_HEAD=9ce4c65d38c6c9c713902744e168887b3dee0624 \
AGENT_PLATFORM_PILOT_EVIDENCE_DIR=/absolute/existing-parent/new-pilot-evidence \
go test ./internal/apiapp -run '^TestLiveGitLabPilotThroughAPI$' -count=1 -v
```

证据目录必须是绝对路径、尚未创建且父目录已存在；失败时可能保留部分证据，不覆盖旧结果。
测试自行配置随机双租户 API 身份、临时 Workspace 根目录及 `127.0.0.1:0`，执行完正常关闭 API。
复跑前先确认服务凭据仍有效，固定 master/base/head 尚符合预期；更换样例必须另存证据。

后续优先把前端接到真实试点部署并由用户复核 Findings，再扩展代表性 PR；
此处尚未完成真实浏览器视觉验证、正式质量验收、持久化恢复或长期部署运维。
