# Mission 05 租约、Checkpoint 与恢复实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按 `wanxiangAgent.md` 第 14 节实现持久租约、Git checkpoint、上下文摘要、中断恢复和安全接管链路。

**Architecture:** `task_steps` 保存当前写租约的原子校验字段，租约、checkpoint 和接管表保存完整历史；Go 服务用可控时钟管理领取、心跳、过期和恢复。Git checkpoint 由 Agent 创建，服务校验其 branch、HEAD、祖先关系和工作区状态；接管从最近干净 checkpoint 创建新分支和独立 worktree，保留原现场。

**Tech Stack:** Go 1.26、SQLite、Git worktree、Chi、Vue 3、Pinia、Vitest。

## Global Constraints

- `wanxiangAgent.md` 是唯一主规范，`wanxiangAgentWorkMission.md` 只负责实施状态和交接。
- 同一工作包只能有一个有效写租约；所有任务写入校验 `agent + task + step + lease_id + lease_version + scope`。
- 默认租约有效期 60 秒，任务心跳建议每 15 秒，原 Agent 恢复窗口 5 分钟；测试使用可控时钟，不使用真实 sleep。
- Git checkpoint 由 Agent 创建，平台验证并登记；平台不得自动提交未知文件或执行 `reset`、`clean`。
- 接替 Agent 使用新分支和新 worktree；原 worktree 和未提交修改不得删除、覆盖或共享。
- checkpoint 和摘要不得包含密钥、令牌、完整模型对话或用户隐私。
- 平台仓库 Git 提交说明使用中文；项目 checkpoint 提交遵循 `checkpoint(<step-id>): <中文摘要>`。

---

### Task 1：租约、Checkpoint 和接管数据结构

**Files:**
- Modify: `server/internal/db/migrations.go`
- Modify: `server/internal/db/db_test.go`
- Create: `server/internal/leases/types.go`
- Create: `server/internal/leases/clock.go`
- Test: `server/internal/leases/types_test.go`

**Interfaces:**
- Produces: `task_steps` 的 `lease_id`、`lease_version`、`lease_expires_at`、`last_heartbeat_at`、`checkpoint_id`、`attempt`、`interrupted_at`、`resume_deadline` 字段。
- Produces: `task_step_leases`、`task_checkpoints`、`step_reassignments` 表。
- Produces: `Clock` 接口和 `LeaseRef{TaskID, StepID int64; AgentName, LeaseID string; LeaseVersion int64}`。

- [x] 写失败数据库测试，检查新表、唯一约束、索引和现有数据库补列；迁移重复执行两次仍成功。
- [x] 写失败类型测试，验证 lease 状态、60 秒 TTL、5 分钟恢复窗口和公开 JSON 不返回其他 Agent 的敏感租约数据。
- [x] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/db ./internal/leases -run 'Migrate|LeaseTypes'`，确认失败。
- [x] 实现幂等补列迁移、历史表、必要索引、系统时钟和 fake clock。
- [x] 重跑定向测试确认通过。
- [x] 提交：`数据库：增加任务租约与检查点结构`（`91c8c49`）。

### Task 2：领取、心跳和统一 Lease Guard

**Files:**
- Create: `server/internal/leases/service.go`
- Create: `server/internal/leases/guard.go`
- Test: `server/internal/leases/service_test.go`

**Interfaces:**
- Produces: `Acquire(context.Context, taskID, stepID int64, agent string) (Lease, error)`。
- Produces: `Heartbeat(context.Context, LeaseRef) (Lease, error)`。
- Produces: `Authorize(context.Context, LeaseRef, relativePath string) error`，内部复用 `workspaces.Service.AuthorizeAgent`。
- Produces: `ErrConflict`，HTTP 层映射为 409。

- [ ] 写失败测试：仅 `workspace_ready`、ready workspace 和 assignment owner 可领取；重复领取返回同一 lease。
- [ ] 写并发失败测试：多个 goroutine 同时领取同一步骤，数据库只有一个 active lease。
- [ ] 写失败测试：心跳续期 60 秒；错误 Agent、ID、version、step、过期或 frozen lease 返回 `ErrConflict`。
- [ ] 写失败测试：Lease Guard 拒绝绝对路径、`..`、scope 外路径和非 assignment Agent。
- [ ] 实现随机 lease ID、事务化领取、条件心跳和统一 Guard。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/leases -run 'Acquire|Heartbeat|Authorize'`。
- [ ] 提交：`功能：实现任务租约领取与心跳校验`。

### Task 3：Git Checkpoint 和上下文摘要

**Files:**
- Create: `server/internal/leases/checkpoint.go`
- Create: `server/internal/leases/summary.go`
- Test: `server/internal/leases/checkpoint_test.go`

**Interfaces:**
- Produces: `CreateCheckpoint(context.Context, LeaseRef, CheckpointInput) (Checkpoint, error)`。
- Produces: `GetCheckpoint(context.Context, checkpointID int64) (Checkpoint, error)`。
- Produces: `CheckpointInput`，包含幂等键、Git 提交、clean、文件、测试、completed、next_action、decisions、blockers、risks 和高风险标记。

- [ ] 写失败测试：clean checkpoint 必须存在于当前 branch，等于 worktree HEAD，并是 provision/base commit 的后代。
- [ ] 写失败测试：Git branch、HEAD、祖先关系、工作区 clean 声明不一致时拒绝登记。
- [ ] 写摘要失败测试：要求单项非空 `next_action`，拒绝密钥字段、控制字符、越界路径和超长内容。
- [ ] 写幂等失败测试：同一 lease 与幂等键只生成一个 checkpoint、一个摘要文件和一个事件。
- [ ] 写脏现场失败测试：允许 commit 为空、`clean=false` 的上下文型 checkpoint，保留未提交文件且不能作为接管基线。
- [ ] 实现规范化 YAML 镜像、SHA-256、Git 校验、幂等事务和 `task.step.checkpointed` 事件。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/leases -run Checkpoint`。
- [ ] 提交：`功能：登记 Git 检查点与恢复摘要`。

### Task 4：过期扫描和原 Agent 恢复

**Files:**
- Create: `server/internal/leases/recovery.go`
- Create: `server/internal/leases/worker.go`
- Test: `server/internal/leases/recovery_test.go`
- Test: `server/internal/leases/worker_test.go`

**Interfaces:**
- Produces: `InterruptExpired(context.Context) (int, error)`。
- Produces: `Resume(context.Context, LeaseRef) (Lease, error)`。
- Produces: 周期扫描 Worker，App 重启后立即扫描一次。

- [ ] 写失败测试：fake clock 推进到 60 秒后步骤原子进入 `interrupted`，设置 5 分钟 deadline；重复扫描不重复事件。
- [ ] 写失败测试：服务重建后从同一 SQLite 读取 lease、checkpoint 和 deadline，不立即重复分配。
- [ ] 写恢复失败测试：原 Agent 在期限内且 branch、HEAD、worktree、checkpoint 一致时恢复同一 ID/version。
- [ ] 写恢复失败测试：超过 deadline、出现新 version、Git 漂移或 worktree 现场不一致时保持 interrupted。
- [ ] 实现扫描、幂等事件、Git 现场校验和原租约恢复。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/leases -run 'Interrupt|Resume|Worker'`。
- [ ] 提交：`功能：扫描中断任务并恢复原租约`。

### Task 5：冻结、延期和安全接管

**Files:**
- Create: `server/internal/leases/admin.go`
- Create: `server/internal/leases/handoff.go`
- Test: `server/internal/leases/admin_test.go`
- Test: `server/internal/leases/handoff_test.go`

**Interfaces:**
- Produces: `ExtendResumeDeadline`、`FreezeStep`、`UnfreezeStep`。
- Produces: `Reassign(context.Context, ReassignInput, actor string) (Lease, error)`。
- Produces: 接力分支 `agent/<new-agent>/<work-item>-resume-<attempt>` 和独立 recovery worktree。

- [ ] 写失败测试：冻结立即撤销写权限；解冻递增 version 并生成新 lease，不复活旧 ID。
- [ ] 写延期失败测试：只允许 interrupted lease 延期，并写审计日志。
- [ ] 写接管失败测试：未过 deadline 且未明确 immediate 时拒绝；新 Agent 必须 online、满足 assignment ownership 或经 manager 改派。
- [ ] 写 Git 集成失败测试：从最近 clean checkpoint 创建新分支/worktree，原 worktree 和脏文件保持不变。
- [ ] 写阻塞失败测试：没有 clean checkpoint、分支冲突、基线无效时进入 `blocked: recovery_review`，不复制现场。
- [ ] 实现撤销旧 lease、递增 version、更新 assignment、记录 `step_reassignments` 和 `task.step.reassigned`。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/leases -run 'Freeze|Deadline|Reassign'`。
- [ ] 提交：`功能：实现工作包冻结与安全接管`。

### Task 6：Agent/Admin API、App Worker 和管理台

**Files:**
- Create: `server/internal/httpapi/handlers_leases.go`
- Modify: `server/internal/httpapi/router.go`
- Modify: `server/internal/app/app.go`
- Test: `server/internal/httpapi/handlers_leases_test.go`
- Test: `server/internal/app/app_test.go`
- Modify: `web/src/api/client.ts`
- Modify: `web/src/views/TaskDetail.vue`
- Test: `web/src/api/client.test.ts`

**Interfaces:**
- Produces Agent API：acquire、heartbeat、checkpoint、resume 和自身租约查询。
- Produces Admin API：timeline、extend、freeze、unfreeze、reassign 和历史 checkpoint 查询。
- Produces 管理台租约剩余时间、心跳、checkpoint、next action、attempt 和高风险确认操作。

- [ ] 写失败 HTTP 测试：Agent 身份覆盖请求体 agent_name，旧 lease 冲突返回 409，越权资源不泄露详情。
- [ ] 写失败 Admin 测试：延期、冻结、解冻、立即接管和指定 checkpoint 均写 audit log。
- [ ] 接入恢复 Worker 生命周期，App Close 必须等待 Worker 退出。
- [ ] 写失败前端 API 测试，验证 lease timeline、extend、freeze、unfreeze 和 reassign 请求体。
- [ ] 在任务详情 M04 workspace 下增加恢复时间线和二次确认操作，移动端保持单列可用。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/httpapi ./internal/app` 和 `npm test && npm run build`。
- [ ] 提交：`功能：接通租约恢复接口与管理台`。

### Task 7：全量验证、交接和合并

**Files:**
- Modify: `wanxiangAgentWorkMission.md`
- Modify: `docs/superpowers/plans/2026-07-15-mission-05-lease-checkpoint-recovery.md`

- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test -count=1 -timeout=60s ./...`。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go build -buildvcs=false -o /tmp/wanxiang-m05-bin ./cmd/wanxiang`。
- [ ] 运行 `npm test && npm run build`，生成并核验 `web/dist`。
- [ ] 更新 M05 状态、checkpoint、测试证据、风险、前端构建/部署和后端构建/重启判断。
- [ ] 使用中文合并提交合并到 `main`，在主分支复核并推送可信 `origin/main`。
