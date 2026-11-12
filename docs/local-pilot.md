# 本机真实前端试用

第 50 步把已验证的 GitLab HTTPS、API、Docker Worker 和模型连接到独立的本机页面。
这是开发试点，所有 HTTP 入口仍绑定 `127.0.0.1`。打开页面、登录、查询结果都不会启动审阅；
点击“开始只读审阅”或“开始新一轮审阅”才调用模型并产生费用。

## 这台电脑的入口

- 页面：<http://127.0.0.1:5174>；API：`127.0.0.1:8081`。
- 私有配置目录：`/Users/haitaochen/.config/agent-platform/local-pilot-20261027`。
- API 凭据文件：该目录的 `auth/api-token.txt`。页面输入文件内容，不带 `Bearer`，不是 GitLab/DeepSeek key。
- API 身份：`local-developer`；租户：`tenant-local`；仅允许 GitLab 项目 `2`。
- 原有 `5173/8080` 服务保留；它们与新服务的 Task、身份和 Workspace 不共享。

在终端复制 API 凭据到剪贴板，然后粘贴到页面：

```bash
pbcopy < /Users/haitaochen/.config/agent-platform/local-pilot-20261027/auth/api-token.txt
```

当前服务由本用户的 launchd 管理。关闭终端不会退出；不设置开机登录自动启动或崩溃自动重启。
以下命令均不需要 `sudo`：

```bash
# 状态；state = running 表示监督进程在运行，HTTP 健康另行检查
launchctl print gui/501/com.agent-platform.local-pilot
curl --fail http://127.0.0.1:5174/api/healthz

# 停止；给正在进行的审阅最多 150 秒收尾
launchctl kill SIGTERM gui/501/com.agent-platform.local-pilot

# 已加载的服务重新启动（不要使用 -k 强制重启）
launchctl kickstart gui/501/com.agent-platform.local-pilot
```

注销/重启电脑后需要重新加载，再启动：

```bash
launchctl bootstrap gui/501 /Users/haitaochen/.config/agent-platform/local-pilot-20261027/local-pilot.plist
launchctl kickstart gui/501/com.agent-platform.local-pilot
```

服务已经加载时不要重复 `bootstrap`。私有日志位于同目录的 `service.log`。
启动器每次构建当前后端代码，再启动 API 与 Vite；Vite 使用上述私有目录内的 Node 22.23.4。
没有在系统 PATH 中替换 Node 23。Docker Desktop、本机 GitLab 和已有 Worker 镜像必须可用。

**重启 API 会丢失内存中的 Task、事件、执行和 Artifact。** Workspace 文件会保留，但不能据此恢复完整业务记录。
重启前应先查看或导出所需结果；已归档的验收证据见 [本轮记录](pilots/2026-10-27-frontend/README.md)。

## 页面操作

1. 验证 API 凭据，看到最近 Task。
2. 优先查看已有的 `task-1`。若要新建，仓库 ID 填 `2`，Head SHA 填
   `9ce4c65d38c6c9c713902744e168887b3dee0624`，任务类型保持 `PR_REVIEW`。
3. 新任务点击“加入队列”→“打开审阅工作区”→“登记 Workspace”→“准备 Workspace”。
4. 已有任务点击“打开审阅工作区”，READY 时会自动读取最新审阅和 Findings。宽屏下代码差异与结果并排展示，窄屏优先展示结果；完整 SHA、工作目录和归档摘要可展开详情查看。
5. 需要重新推理时才点击“开始只读审阅”/“开始新一轮审阅”。本例应由人确认空列表除零问题。
6. “查看事件”→成功事件的“查看本次审阅”会自动读取那次执行的原始 Findings。退出身份后页面清除凭据和结果。

2026-11-12 已调整为审阅工作台布局，截图与交互验证见[界面改版记录](pilots/2026-11-12-workbench/README.md)。

目前项目是专门构造的合成样例；功能闭环通过不代表 M14 正式质量验收通过。

## 在其他环境复现启动

仓库入口为 `scripts/run-local-pilot.py`，仅使用 Python 3.9+ 标准库（POSIX/macOS）。
需要 Node 22.13–22.x、Go、Git、Docker，以及已按 README 验证的只读 Worker、GitLab HTTPS/CA 和服务模型凭据。
先用 Node 22 在 `frontend` 执行 `npm ci`。启动器不自动安装依赖、配置 GitLab、创建模型账号或执行模型调用。

复制 [配置示例](local-pilot.example.json) 到工作区之外并填写绝对路径。`stateDir` 必须由本用户持有，权限 `0700`；
配置、GitLab/模型凭据和 `auth/api-auth.json` 必须为本用户持有的普通文件，权限 `0600`。
凭据不能位于 `stateDir/workspaces` 内。GitLab 凭据格式为
`{"servicePrincipal":"项目机器人名称","token":"本地填写","expiresAt":"YYYY-MM-DD","scopes":["read_api","read_repository"]}`，
应使用目标项目的 Reporter 只读 token；模型凭据沿用 README 的服务身份格式。
凭据值不放进配置 JSON、命令行、仓库或浏览器环境变量。

用现有 authinit 为新的状态目录生成身份（目标 auth 目录不能预先存在）：

```bash
cd backend
go run ./cmd/authinit -dir /absolute/private/local-pilot/auth \
  -tenant tenant-local -subject local-developer -repository 2 \
  -workspace-root /absolute/private/local-pilot/workspaces
cd ..
python3 -B scripts/run-local-pilot.py --config /absolute/private/local-pilot/pilot.json --check
python3 -B scripts/run-local-pilot.py --config /absolute/private/local-pilot/pilot.json
```

`--check` 检查配置、权限、声明的 GitLab 到期日、端口和 Node 版本，不连接外部服务，也不保证 Docker/模型就绪。
正式启动保留 API 自身的 HTTPS、身份、仓库、模型出口和 Worker 预检。端口冲突直接失败；
API 或前端退出时停止另一子进程，不接管现有端口。Ctrl-C/SIGTERM 先请求正常关闭，超时后才清理本次启动的子进程组。

前端仅接收基础 PATH/HOME/临时目录和服务端变量 `AGENT_PLATFORM_DEV_API_URL`。
该变量只允许 `http://127.0.0.1[:port]`，没有用户信息、路径、查询或 fragment；未设置时仍代理 `8080`。
API 的 GitLab token 和模型凭据文件路径不会传给 Vite；不继承父进程中的任意 `VITE_*`、代理或密钥环境变量。
本机同一 OS 用户依然能够读取自己的凭据和进程环境，这不是操作系统多用户安全隔离。

GitLab 只读凭据已于 2026-10-27 替换，新的到期日为 **2026-11-26**；替换后需要更新私有文件并重启 API，
无需重新生成 API 登录凭据。专用 GitLab TLS 证书到期日仍为 **2027-01-24**，只通过显式 CA 文件信任。
本轮启动未改变 GitLab 原有端口映射或系统证书信任。

离线启动器回归：

```bash
python3 -B -m unittest discover -s scripts -p 'test_local_pilot.py' -v
```
