# 2026-10-27：常驻前端真实试用

页面 `http://127.0.0.1:5174` 通过 Vite 代理连接独立 API `127.0.0.1:8081`，
使用本机 GitLab HTTPS 项目 `2`、真实 Git clone/worktree、Docker Worker 和 `deepseek-flash`。
操作方法、登录凭据位置及启动/停止命令见[试用说明](../../local-pilot.md)。

Chrome 155 从空 Task 列表完成登录、创建/准入 `task-1`、登记/准备 Workspace、读取固定 diff，
显式发起唯一一次 `review-1`。执行于 10:23:58–10:24:19 UTC（本地 18:23:58–18:24:19），
耗时约 21.53 秒，生成一条 `pricing.py:2` HIGH Finding：移除空列表保护导致除零。
随后从事件读取原执行/Findings，发现并修复窄屏历史 SHA 和事件时间线溢出。
修复后的浏览器复验仅读取既有记录，没有新推理，截图是该次复验保存的。

| 文件 | 含义 |
| --- | --- |
| `task.json` / `workspace.json` | 固定仓库引用与 READY Workspace 的 API 快照 |
| `execution.json` | 成功执行、Artifact 摘要、实际 Worker/Codex/模型身份 |
| `findings.json` | API 返回的原始 Artifact 字节，552 B，与执行中 SHA-256 一致 |
| `events.json` | 8 条事件，只有一次审阅开始和成功 |
| `browser-verification.json` | 布局修复后的浏览器读回；`reviewPosts=0`，不是首次审阅的请求追踪 |
| `deployment-verification.json` | 旧 GitLab 凭据 401、新凭据 200、原始内容摘要和事件数核验 |
| `desktop-review.png` / `mobile-review.png` | 1440px/390px 视口下的审阅区域截图，已由助手查看 |
| `manifest.json` | 基线提交、改动源码及证据摘要；不含真实凭据 |

浏览器验证包括：未认证访问 401、认证租户不一致的 Task/Artifact 访问 403、
最新/历史 Findings 一致、390px 视口无横向溢出、退出清空结果、刷新后重新登录，
localStorage/sessionStorage 均为空，无页面 JavaScript 异常。
正常的 Workspace/Review 尚不存在查询返回 404；不将该状态误记为业务失败。

GitLab 项目 Reporter 凭据替换后保持 `read_api/read_repository`，新的到期日为 2026-11-26。
旧凭据撤销前已确认原 `8080` API 未使用它；未停止原 `8080/5173` 服务。
试点继续运行，用户可以查看 `task-1` 的结果；API 重启会丢失内存记录，文件证据仍可读取。

这次真实浏览器验证未使用 GitLab/Worker/模型替身；离线测试仍使用受控进程/服务。
浏览器自动化工具 Playwright 只安装在临时目录，没有引入业务依赖。首次页面导航遇到 Vite 配置热重启，
重新导航后完成登录；未因此新增模型调用。该试点没有测量独立上游请求数、token 用量或费用。
合成案例与助手检查不能替代用户人工复核、代表性企业 PR 数据集或 M14 正式质量判定。
