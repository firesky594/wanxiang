# Mission 04 项目元数据与隔离工作区实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 M03 assignment 转换为数据库与 Git 元数据互相印证的独立分支和 worktree，并提供安全的校验、修复和清理入口。

**Architecture:** SQLite 保存运行状态和绝对路径，项目 `.wanxiang/` 保存规范化审计快照；workspace provisioner 以可重试状态机协调数据库、Git 提交、分支和 worktree。reconciler 双向比较数据库、快照与 Git 现场，发现漂移时只标记和报告，由管理员明确选择修复方向。

**Tech Stack:** Go 1.26、SQLite、Git worktree、Chi、Vue 3、Pinia、Vitest。

## Global Constraints

- `wanxiangAgent.md` 是行为规范，`wanxiangAgentWorkMission.md` 是实施状态与交接；本计划只作补充，冲突时以前两者为准。
- 数据库与 Git 元数据必须互相校验，不一致时不得静默覆盖。
- Agent 分支格式为 `agent/<agent-name>/<task-id>-<work-item-key>`，不同工作包不得共享分支或 worktree。
- 客户端不得提交任意项目绝对路径；所有路径必须通过根目录和符号链接校验。
- 不使用强推、`git reset --hard` 或自动删除未知分支及目录。
- 平台生成的 Git 提交说明使用中文。
- 所有功能先写失败测试，再写最小实现，并按任务创建中文 checkpoint 提交。

---

### Task 1: 项目复用和 workspace 数据状态

**Files:**
- Modify: `server/internal/db/migrations.go`
- Modify: `server/internal/db/db_test.go`
- Modify: `server/internal/tasks/service.go`
- Modify: `server/internal/tasks/types.go`
- Modify: `server/internal/httpapi/handlers_tasks.go`
- Test: `server/internal/tasks/service_test.go`
- Test: `server/internal/httpapi/auth_test.go`

**Interfaces:**
- Produces: `tasks.CreateTaskInput{Title, Description string; ProjectID *int64}`
- Produces: `tasks.Service.CreateTaskWithInput(context.Context, CreateTaskInput, ...string) (Task, error)`
- Produces: `project_workspaces` table with unique `step_id`、`branch_name`、`worktree_path`，并分别记录代码 `base_commit` 与实际分支起点 `provision_commit`。

- [x] 写失败测试：未传 `project_id` 创建新项目；传入已登记项目则复用；任意路径、脏仓库、非 `main` 和不存在项目均被拒绝且不产生任务。
- [x] 写数据库失败测试，要求 `project_workspaces` 包含 assignment、分支、worktree、base commit、scope、hash、状态和错误字段及唯一约束。
- [x] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/tasks ./internal/db ./internal/httpapi`，确认新断言失败。
- [x] 实现 `CreateTaskWithInput`；保留 `CreateTask` 兼容包装；复用项目时只接受数据库 ID，并用 `files.UnderRoot`、真实路径和 Git 状态校验目录。
- [x] 修改管理员创建任务请求解析 `project_id`，错误映射为 400、404 或 409。
- [x] 把新项目初始化提交改为中文，运行定向测试确认通过。
- [ ] 提交：`功能：支持安全复用已登记项目`。

### Task 2: 规范化项目和 assignment 快照

**Files:**
- Create: `server/internal/workspaces/metadata.go`
- Test: `server/internal/workspaces/metadata_test.go`

**Interfaces:**
- Produces: `ProjectMetadata`, `AssignmentMetadata`.
- Produces: `EncodeProject(ProjectMetadata) ([]byte, error)`.
- Produces: `EncodeAssignment(AssignmentMetadata) ([]byte, string, error)`，第二个返回值为 SHA-256。
- Produces: `DecodeAssignment([]byte) (AssignmentMetadata, error)`.

- [x] 写失败测试，固定字段顺序、LF、稳定哈希、空负责人、多个 Agent 和默认 `write_scope: ["."]` 的 golden 输出。
- [x] 写失败测试，拒绝绝对 scope、`..`、无效 Agent、无效分支和未知 YAML 字段。
- [x] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/workspaces -run Metadata`，确认失败。
- [x] 实现无密钥、确定性 YAML 编解码和路径/名称校验，不把绝对 worktree 路径写入 Git。
- [x] 重跑测试确认通过。
- [ ] 提交：`功能：生成可校验的项目分配元数据`。

### Task 3: 幂等 provision 和独立 worktree

**Files:**
- Create: `server/internal/workspaces/service.go`
- Create: `server/internal/workspaces/git.go`
- Test: `server/internal/workspaces/service_test.go`
- Modify: `server/internal/tasks/states.go`（若状态定义仍位于 `service.go`，只修改实际文件）

**Interfaces:**
- Consumes: M03 `task_assignments`、`team_decisions` 和 Task 2 metadata 编码器。
- Produces: `workspaces.Service.ProvisionTask(context.Context, int64) (TaskWorkspace, error)`.
- Produces: `workspaces.Service.GetTask(context.Context, int64) (TaskWorkspace, error)`.
- Produces: task status `workspace_ready`。

- [x] 写 Git 集成失败测试：两个 Agent 共享代码 `base_commit`，从同一 `provision_commit` 创建不同分支和不同 worktree，数据库记录为 `ready`。
- [x] 写幂等与恢复失败测试：重复调用不重复提交或创建；`provisioning` 中断后校验现存资源并继续。
- [x] 写安全失败测试：同名未知分支、未知非空目录、脏 `main`、分支格式错误只记录 `failed`，不删除现场。
- [x] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/workspaces -run Provision`，确认失败。
- [x] 实现项目级进程内锁、`provisioning -> ready/failed` 状态机、中文 metadata 提交、`git worktree add -b` 和安全错误摘要。
- [x] 更新合法任务状态转换 `assigned -> workspace_ready`，重跑 workspaces 和 tasks 测试。
- [ ] 提交：`功能：创建独立 Agent 分支与工作区`。

### Task 4: 双向漂移校验、修复和安全清理

**Files:**
- Create: `server/internal/workspaces/reconcile.go`
- Test: `server/internal/workspaces/reconcile_test.go`
- Modify: `server/internal/workspaces/service.go`

**Interfaces:**
- Produces: `ReconcileTask(context.Context, int64) (TaskWorkspace, error)`.
- Produces: `RepairTask(context.Context, int64, RepairDirection, string) (TaskWorkspace, error)`，方向只允许 `database` 或 `git_snapshot`。
- Produces: `RequestCleanup` 和 `ConfirmCleanup`；非终态必须管理员显式确认。

- [x] 写失败测试：修改快照、数据库 Agent、分支、HEAD 或 worktree 路径后进入 `drifted`，产生事件且不覆盖任一侧。
- [x] 写失败测试：按 `database` 重建快照；按 `git_snapshot` 恢复数据库前重新校验项目、step、Agent、branch 和 scope。
- [x] 写失败测试：终态可清理受管 worktree；非终态无确认拒绝；未知目录或分支永不删除。
- [x] 实现双向 reconciler、显式修复、审计事件和两阶段清理。
- [x] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/workspaces`，确认通过。
- [ ] 提交：`功能：检测并修复工作区元数据漂移`。

### Task 5: 自动 Worker、管理员 API 和 Agent ownership

**Files:**
- Create: `server/internal/workspaces/worker.go`
- Test: `server/internal/workspaces/worker_test.go`
- Create: `server/internal/httpapi/handlers_workspaces.go`
- Modify: `server/internal/httpapi/router.go`
- Modify: `server/internal/app/app.go`
- Test: `server/internal/httpapi/handlers_workspaces_test.go`
- Test: `server/internal/app/app_test.go`

**Interfaces:**
- Produces: assigned 任务自动 provision；ready workspace 周期校验。
- Produces: `GET /api/admin/tasks/{id}/workspace`、`POST .../reconcile`、`POST .../repair`、`POST .../cleanup`。
- Produces: `Service.AuthorizeAgent(context.Context, agent, taskID, stepID, relativePath) error` 供 M05、M06 复用。

- [x] 写失败测试，Worker 消费 `assigned` 并周期校验 `workspace_ready`，重启不重复创建资源。
- [x] 写失败 HTTP 测试，覆盖查询、校验、修复方向、清理确认和管理员审计。
- [x] 写 ownership 失败测试，拒绝非 assignment Agent、跨 task/step、绝对路径、`..` 和 scope 外路径。
- [x] 实现 Worker、App 生命周期和管理员路由；Agent ownership 保持为服务接口，不开放任意写文件 API。
- [x] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/workspaces ./internal/httpapi ./internal/app`。
- [ ] 提交：`功能：接通工作区自动编排与管理接口`。

### Task 6: 管理台项目选择和 workspace 轨迹

**Files:**
- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/Dashboard.vue`
- Modify: `web/src/views/TaskDetail.vue`
- Test: `web/src/api/client.test.ts`
- Test: `web/src/stores/tasks.test.ts`

**Interfaces:**
- Consumes: Task 1 项目复用请求和 Task 5 workspace API。
- Produces: 创建任务时选择新项目或已有项目；任务详情展示 base commit、分支、worktree 状态、汇报关系和 drift 原因。

- [x] 写失败前端测试，验证 `project_id` 请求、workspace 查询以及 reconcile/repair/cleanup 请求体。
- [x] 修改创建任务表单，默认新建项目，已有项目选项只发送 ID。
- [x] 增加 workspace 轨迹区，`drifted` 状态显示修复方向和确认文案，清理操作区分普通申请与强制确认。
- [x] 运行 `npm test` 和 `npm run build`，确认测试及 TypeScript 构建通过。
- [x] 提交：`前端：展示并管理隔离工作区`。

### Task 7: 全量验证、交接和合并

**Files:**
- Modify: `wanxiangAgentWorkMission.md`
- Modify: `docs/superpowers/plans/2026-07-14-mission-04-project-worktrees.md`

- [x] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./...`，需要本地监听端口时使用已获准的测试权限。
- [x] 运行 `GOCACHE=/tmp/wanxiang-go-cache go build -buildvcs=false -o /tmp/wanxiang-m04-bin ./cmd/wanxiang`。
- [x] 运行 `npm test && npm run build`，生成并核验 `web/dist`。
- [x] 在 `wanxiangAgentWorkMission.md` 更新链路核对、M04 状态、checkpoint、测试证据、风险、前端构建/部署和后端构建/重启判断。
- [x] 使用中文合并提交合并到 `main`，在主分支复核后推送 `origin/main`。
