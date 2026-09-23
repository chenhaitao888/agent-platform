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

**Review Base**:
PR Review 中目标分支快照与待评审 head 的最近公共祖先（merge-base）提交；平台在创建 Task 时确定并固定
其 SHA，而不是由调用方指定。
_Avoid_: 目标分支最新提交、target tip

**Review Target Snapshot**:
创建 Task 时目标分支 `master` 所指向的不可变提交；即使 `master` 后来移动，本次评审仍以该提交确定
Review Base。
_Avoid_: 当前 master、动态目标分支

**Immutable Review Diff**:
平台只使用 Ready Workspace 已记录的 Review Base SHA 与 head SHA 生成的只读补丁；调用方不能临时替换
revision 或本地路径。当前补丁用于 Phase 0 PR Review 的受控输入，不等同于可长期保存的 Artifact。
_Avoid_: 最新差异、工作区当前改动

**Artifact**:
平台归档的不可变结果或证据，记录任务/工作区归属、类型、媒体类型、内容大小与校验和；读取内容不能
改变已经保存的事实。当前内存实现用于先稳定领域契约，不代表已经具备对象存储的持久性与保留策略。
_Avoid_: Workspace 临时文件、可修改附件

**Repository Reference**:
由代码托管平台、仓库标识、Review Target Snapshot、Review Base 和 head SHA 共同确定的可复现代码输入；
Task 创建时固定，Workspace 登记前再次验证所需提交。
_Avoid_: 分支名、最新代码

**Task Event**:
以 Task 为范围、按发生顺序只追加的事实记录，包含 Task 及其 Workspace、Artifact 的生命周期变化；
幂等重放不产生第二份相同事实。
_Avoid_: 请求日志、可修改状态记录
