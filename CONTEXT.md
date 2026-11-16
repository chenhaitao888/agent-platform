# Agent Platform

Agent Platform 是企业研发智能体的控制平面。它管理任务、执行所需的代码工作区及可审计事实，
但不重新实现智能体的模型循环。

2026-11-13 的后续设计把用户入口扩展为需求级交付任务，关联多个现有单仓库 Task，
组织 Review、配置与 AB 变更准备。D1～D3 已实现服务目录、需求快照、多服务 Review 与配置/实验只读计划；后续阶段见
[交付智能体设计](docs/post-coding-delivery-design.md)。以下词汇描述当前代码。

## Language

**ChangeRequest**:
用户提交的一项需求交付任务，持有租户、说明、目标环境和多个服务选择；快照完成、审阅/计划生成开始和结束均推进版本。
每个快照的逐服务 Review 复用已有单仓库 Task，并保持输入和结果归属。
_Avoid_: 多仓库 Task、已完成交付

**Service Catalog**:
由可信部署配置加载的租户服务目录，映射服务、仓库、默认/允许目标分支及环境；目录存在不等于拥有权限。
实际使用还需满足当前身份的服务/环境/操作授权及原仓库白名单。
_Avoid_: 用户自定义连接、权限白名单的替代品

**Change Snapshot**:
一次明确的多服务分支解析记录，保存目录版本、完整源/目标 SHA、merge-base 和逐服务解析时间。
只有全部服务成功的 READY 快照才能成为当前可用输入；FAILED 尝试保留逐服务错误。
同键重放不重新读取分支，新快照不会改写历史记录；跨仓库不保证全局瞬时原子性。
_Avoid_: 实时分支视图、已审阅版本

**Change Review**:
针对某个 READY Change Snapshot 的一次多服务审阅尝试，记录逐服务阶段、状态以及确切的 Task、Workspace、执行和 Artifact 引用。
重试仅推进该快照中尚未成功的服务，已成功结论保留；新快照建立独立子任务，历史结果不会变成新版本的结论。
全部成功只表示审阅执行完成，问题仍由人判断处置。
_Avoid_: 跨服务兼容性证明、交付完成、持久工作流

**Change Candidate**:
与固定服务/SHA 绑定的配置或实验变更候选，来自代码证据或显式业务要求。候选需要平台现状、类型与业务值校验；
没有候选不等于明确不涉及，分析失败也不能当成检查完成。
_Avoid_: 写入指令、已批准动作、完整语义分析

**Change Plan**:
针对一个 Change Snapshot 的版本化只读变更计划，保存审阅引用、候选证据、资源/连接版本、当前/期望值、前置条件和摘要。
共享实际资源的要求合并或报冲突；新生成不覆盖旧记录。READY_FOR_REVIEW 表示可供人工审阅，不包含执行许可。
_Avoid_: 执行记录、批准、测试变更已生效

**Inactive Experiment Draft**:
平台明确保证不参与分流的实验定义草稿；D3 只生成创建/更新该草稿的提案。
流量设置为零不能替代草稿生命周期保证，已有激活实验不能直接作为草稿更新。
_Avoid_: 已激活实验、零流量即草稿

**Task**:
调用方希望平台完成的一项业务目标，也是状态、幂等和执行关联的聚合视图；创建时持有一份
Repository Reference，后续 Workspace 从中派生代码输入。需求子任务还保存所属需求、快照、服务与环境，
由需求入口统一发起操作；独立单仓库任务保留原有入口。
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
解析时目标分支所指向的不可变提交；旧 Task 入口默认 `master`，需求快照取目录授权的目标分支。
即使目标分支后来移动，本次输入仍以该提交确定
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
