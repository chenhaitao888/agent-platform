# Agent Platform

Agent Platform 是企业研发智能体的控制平面。它管理任务、执行所需的代码工作区及可审计事实，
但不重新实现智能体的模型循环。

2026-11-13 的后续设计把用户入口扩展为需求级交付任务，关联多个现有单仓库 Task，
组织 Review、配置与 AB 变更准备。D1～D3 首版已有目录、快照、Review 与只读计划，D4.1/D4.2 增加持久交付包、模拟批准与编排；后续阶段见
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
与固定服务/SHA 绑定的配置或实验变更候选，来自规则/模型的代码证据或显式业务要求。候选需要平台现状、类型与业务值校验；
模型候选带有理由和输入/输出摘要，位置校验不证明语义正确；待核对问题阻断计划。
没有候选不等于明确不涉及，分析失败也不能当成检查完成。
_Avoid_: 写入指令、已批准动作、完整语义分析

**Change Plan**:
针对一个 Change Snapshot 的版本化只读变更计划，保存审阅引用、候选证据、资源/连接版本、当前/期望值、前置条件和摘要。
共享实际资源的要求合并或报冲突；新生成不覆盖旧记录。READY_FOR_REVIEW 表示可供人工审阅，不包含执行许可。
_Avoid_: 执行记录、批准、测试变更已生效

**Delivery Package**:
显式保存某份不可变计划及其需求说明、代码快照、审阅执行和 Findings 内容的持久记录。
它具有独立 ID、摘要和租户/服务授权，API 重启后仍可读取；历史证据不依赖可被复用的临时 Task/Artifact ID。
保存不代表批准或执行，也不会把原内存工作台恢复成可继续运行的状态。
_Avoid_: 批准、发布包已执行、完整 Workflow 恢复

**Simulation Approval**:
针对不可变交付包、计划与策略摘要的一次有限期决定，记录主体和理由，仅授权模拟。
批准可撤回，消费与逐动作记录在事务中完成；执行前仍需重验当前权限和输入。
_Avoid_: 真实写入批准、永久权限、模型自行授权

**Simulation Run / Operation Ledger**:
通过事务 Outbox 进入 Temporal 的模拟运行；逐动作记录状态、稳定 ID、租约代次和合成回执。
丢响应先核对回执，保留成功事实，失败或撤回停止后续效果。历史只传引用，业务值留在应用数据库。
_Avoid_: 公司平台已生效、仅靠 Temporal 保证远端 exactly-once

**Inactive Experiment Draft**:
平台明确保证不参与分流的实验定义草稿；D3 只生成创建/更新该草稿的提案。
流量设置为零不能替代草稿生命周期保证，已有激活实验不能直接作为草稿更新。
_Avoid_: 已激活实验、零流量即草稿

**Write Approval / Execution**:
与模拟分离的有限期批准及持久执行记录，绑定包/计划/动作、目标、Writer 和策略摘要。消费批准、创建操作及 Outbox 在同一事务完成。
逐项保存完整请求后才发送一次；重启只回查，不凭缺失回执重发。未知/漂移会停止剩余动作，已成功事实保留。
已有显式部署、业务 API 与页面入口，默认关闭；当前完成参考平台验收，公司平台尚未接入。
_Avoid_: 模拟批准升级为写批准、自动恢复剩余写入、公司平台已验收

**Compensation Plan**:
对原写入执行的不可变处置记录，绑定源包/执行摘要、已知回执及准备时的资源条件，区分配置恢复、人工处置、无需恢复和待核对项。
可执行的配置恢复进入独立交付包，以新权限、新批准和新操作执行；原记录不变。补偿动作完成不代表人工事项已完成。
_Avoid_: 自动回滚、原批准复用、覆盖外部后续修改、恢复代码或激活实验

**Write Intent**:
从不可变计划与本次已核验成功前缀派生的外部请求，绑定 operationId、读写契约、目标、条件版本、期望值、期限和摘要。
同资源的后续请求只能沿本次回执推进资源版本，不能直接吸收平台最新版本；控制平面仍须在调用前批准并持久保存。
_Avoid_: 批准凭据、已消费授权、可由浏览器直接执行的命令

**External Write Receipt / Verification**:
参考 Writer 查询的不可变 APPLIED/REJECTED 回执，以及另行 GET 回读得到的 VERIFIED/DRIFTED 记录。
回执证明远端决定，核验描述当前资源；后续漂移不抹掉原事实。超时或查不到回执仍为未知，不能据此重写。
_Avoid_: 应用已加载、跨平台原子事务、自动补偿、模拟回执可替代外部回执

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
