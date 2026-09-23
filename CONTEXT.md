# Agent Platform

Agent Platform 是企业研发智能体的控制平面。它管理任务、执行所需的代码工作区及可审计事实，
但不重新实现智能体的模型循环。

## Language

**Task**:
调用方希望平台完成的一项业务目标，也是状态、幂等和执行关联的聚合视图；创建时持有一份
Repository Reference，后续 Workspace 从中派生代码输入。
_Avoid_: Job、工单

**Workspace**:
绑定到一个 Task 的隔离代码工作区，记录仓库及不可变版本引用；同一 Task 至多拥有一份。
_Avoid_: 项目目录、Codex worktree

**Registered Workspace**:
仓库标识和不可变版本引用已经由对应代码托管平台验证并登记，但尚不表示代码已 clone、目录已挂载
或执行环境已就绪。
_Avoid_: Ready Workspace、Prepared Workspace

**Ready Workspace**:
平台已经为 Registered Workspace 完成 bare clone，确认 base/head 都是本地可读 commit，并在隔离目录中
以 detached HEAD 创建指向 head SHA 的 worktree；`path` 此时才可供后续 Runtime 使用。READY 只表示
代码工作区就绪，不表示容器、Codex 或 Workflow 已经启动。
_Avoid_: Running Workspace、Runtime-ready Workspace

**Immutable Review Diff**:
平台只使用 Ready Workspace 已记录的 base SHA 与 head SHA 生成的只读补丁；调用方不能临时替换 revision
或本地路径。当前补丁用于 Phase 0 PR Review 的受控输入，不等同于可长期保存的 Artifact。
_Avoid_: 最新差异、工作区当前改动

**Repository Reference**:
由代码托管平台、仓库标识、base SHA 和 head SHA 共同确定的可复现代码输入；Task 创建时先记录，
Workspace 登记前必须向代码托管平台验证。
_Avoid_: 分支名、最新代码

**Task Event**:
描述 Task 已经发生之事实的只追加记录；重复请求不得重复产生同一事实。
_Avoid_: 请求日志、可修改状态记录
