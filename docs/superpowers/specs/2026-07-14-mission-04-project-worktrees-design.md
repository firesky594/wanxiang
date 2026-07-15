# Mission 04 项目元数据与隔离工作区设计

## 目标与边界

M04 把已完成匹配的工作包变成可执行、可审计的 Git 隔离环境。数据库与项目仓库内的 `.wanxiang/` 元数据必须同时保存 assignment；两份记录互相校验，但用途不同：数据库负责运行时查询、并发控制和状态流转，Git 元数据负责人工审阅、历史追踪和灾难恢复。

本 Mission 不启动执行 Agent，不实现租约、心跳、Git checkpoint 或上下文摘要。这些能力由 M05、M06 基于本 Mission 提供的 assignment 和 worktree 实现。

## 已确认方案

采用“数据库事实源 + Git 可审计快照 + 双向漂移检测”。不采用纯数据库方案，因为仓库脱离平台后将失去任务上下文；不采用纯文件方案，因为并发更新、身份校验和快速恢复不可靠。

发生不一致时不得静默选择一方覆盖另一方。系统把工作区标记为 `drifted`，写入运行事件和审计日志，并要求管理员明确选择以数据库重建快照，或在校验通过后从 Git 快照恢复数据库记录。

## 数据模型

新增 `project_workspaces`：

- `task_id`、`step_id`、`assignment_id`：关联任务和 M03 assignment，`step_id` 唯一。
- `project_id`、`agent_name`、`reports_to`：保存项目边界和汇报关系。
- `branch_name`：固定格式 `agent/<agent-name>/<task-id>-<work-item-key>`。
- `worktree_path`：平台数据目录下的绝对路径，不放在项目仓库内部。
- `base_commit`：写入本批平台元数据前的代码基线提交。
- `provision_commit`：包含本批 `.wanxiang` 快照、所有工作分支实际使用的起点提交。拆分两个字段可避免快照引用包含自身的提交哈希。
- `write_scope_json`：项目相对路径数组；M04 默认 `["."]`，禁止绝对路径和 `..`。
- `metadata_hash`：规范化 assignment 快照的 SHA-256。
- `status`：`provisioning`、`ready`、`drifted`、`cleanup_pending`、`cleaned` 或 `failed`。
- `last_error`、`created_at`、`updated_at`、`cleaned_at`：恢复与审计字段。

项目表继续保存项目目录和 `main_commit`。项目复用不复制项目记录；新任务通过 `project_id` 关联已登记项目。

## Git 元数据格式

`.wanxiang/project.yaml` 保存项目级稳定信息：项目标识、manager、动态项目负责人、Agent 汇报关系、分支策略、合并目标和元数据版本。

每个工作包写入 `.wanxiang/assignments/<task-id>-<step-id>.yaml`，至少包含：任务、步骤、工作包 key、Agent、汇报对象、分支、worktree 逻辑标识、起始提交、写入范围和状态。绝对 worktree 路径只保存在数据库，避免把机器路径固化进 Git。

序列化字段顺序固定，换行固定为 LF。系统对规范化 assignment 内容计算 SHA-256，并与 `project_workspaces.metadata_hash` 比较。

系统先记录当前 `main` 为 `base_commit`，再把元数据写入 `main` 并使用中文提交说明提交，所得提交记为 `provision_commit`，所有工作包分支从 `provision_commit` 创建。这样同一批 assignment 共享相同代码基线和实际分支起点，快照也不会循环引用包含自身的提交哈希。

## 项目选择与创建

创建任务接口增加可选 `project_id`：

- 未提供时，沿用安全 slug 规则创建新项目和独立 Git 仓库。
- 提供时，只能复用数据库已登记、目录存在、位于允许项目根目录内、当前分支为 `main` 且工作区干净的项目。
- 不接受客户端直接提交任意文件系统路径。
- 项目不存在、不是 Git 仓库、主分支错误或工作区有未提交修改时，返回明确冲突，不修改数据库和文件。

新项目初始化提交和平台生成的元数据提交均使用中文 Git 提交说明。

## Provision 流程

匹配 Worker 把任务置为 `assigned` 后，workspace Worker 执行以下幂等流程：

1. 加载 task、project、task_steps、task_assignments 和 team_decisions，并验证关系完整。
2. 锁定同一项目的 provision 操作；确认 `main` 工作区干净，读取当前提交。
3. 为缺失记录写入 `project_workspaces(status=provisioning)`；已为 `ready` 的步骤进入校验而不是重复创建。
4. 生成 `project.yaml` 和全部 assignment 快照，提交到 `main`，更新 `projects.main_commit`。
5. 使用 `git worktree add -b <branch> <path> <base_commit>` 创建每个隔离 worktree。分支或目录已存在时必须验证归属，不能直接复用未知现场。
6. 验证分支、worktree、HEAD、快照哈希和数据库一致后，把记录置为 `ready`，任务置为 `workspace_ready`，发布事件。

Git 与 SQLite 无法共享事务，因此采用可恢复的状态机。任一步失败都记录 `failed` 和安全错误摘要；重试从现存状态校验后继续。只清理由本次失败创建且可证明归属的平台资源，不删除未知目录或分支。

## 双向校验与恢复

服务启动和周期 Worker 都会校验 `ready` 工作区：

- 数据库到 Git：项目、任务、步骤、Agent、分支、起始提交、汇报关系和写入范围必须与快照相同。
- Git 到数据库：每个受管 assignment 快照必须能找到唯一数据库记录，分支必须由对应 worktree 使用。
- worktree 到 Git：登记目录必须是相应项目的 worktree，当前分支和 HEAD 必须符合记录。

快照缺失、哈希变化、分支被改名、worktree 被移动或出现未知同名分支时，记录改为 `drifted`。管理员修复接口必须携带 `database` 或 `git_snapshot` 方向；从 Git 恢复时仍需通过路径、Agent、任务和分支格式校验。

## 权限与路径安全

- 所有项目、worktree 和元数据路径经过根目录约束和符号链接检查。
- Agent 写接口必须通过认证身份找到唯一 active assignment，并验证目标 task、step、project 和 write scope；管理员不受 Agent assignment 限制，但所有修复和清理操作写审计日志。
- 两个工作包不能共享分支或 worktree 路径；数据库唯一约束和 Git 校验同时执行。
- 不执行 `git reset --hard`、强推或自动删除未知分支。
- worktree 只有在 assignment 进入终态后才能申请清理；非终态清理必须由管理员明确确认。

## 管理接口与页面

管理员 API 支持：创建任务时选择已有项目、查询任务 workspace、触发重新校验、按明确方向修复漂移、申请或确认清理。任务详情页展示项目、base commit、每个 Agent 的分支、worktree 状态、汇报对象和漂移原因。

Agent 侧先提供 assignment ownership 校验能力，供 M05、M06 的租约和执行接口复用；M04 不提供任意命令执行或任意路径写入 API。

## 测试与验收

- 单元测试覆盖规范化 YAML、哈希、分支名、路径逃逸和状态流转。
- Git 集成测试在临时仓库创建两个 Agent 的独立分支和 worktree，验证不同目录、相同 base commit 和幂等重试。
- 漂移测试分别修改快照、分支、worktree 和数据库，确认进入 `drifted` 且不会静默覆盖。
- HTTP 测试覆盖项目复用、查询、校验、修复、清理权限和 Agent ownership。
- 前端测试覆盖已有项目选择、workspace 展示和漂移修复请求。
- 交付时运行完整 Go 测试与构建、前端测试与 `web/dist` 构建，并记录是否部署静态资源、是否替换后端二进制及是否重启。
