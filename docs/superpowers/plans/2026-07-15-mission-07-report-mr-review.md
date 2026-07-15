# Mission 07 完成报告、MR 与项目负责人审核实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让执行 Agent 原子提交完成报告和 MR，由项目负责人审核并合入项目 `main`，随后可靠通知 manager。

**Architecture:** 在现有 `mr` 包内拆分报告仓储、权限校验、审核状态机和 Git 合并器；所有写请求从认证上下文读取服务端身份，并用任务分配、租约、checkpoint、负责人和工作区记录交叉验证。报告、MR、审核、运行事件和 manager 通知使用 SQLite 事务保存，Vue 管理台只调用管理员查询接口。

**Tech Stack:** Go 1.26、SQLite、Chi、现有 leases/workspaces/events/gitx 服务、Vue 3、Element Plus、Vitest。

## Global Constraints

- `wanxiangAgent.md` 是权限和流程的唯一规范来源，`wanxiangAgentWorkMission.md` 只记录实现状态。
- 客户端只提交 Issue、补充需求、确认高风险操作和查看状态，不创建报告、MR、审核或合并。
- Agent 写请求必须交叉校验 Token、`agent_name`、`role`、task、step、lease、项目成员、项目负责人和分支归属。
- 单 Agent 项目由该 Agent 以负责人身份自审自合并；多 Agent 项目只有登记的项目负责人能审核和合并。
- 执行 Agent 不能修改项目 `main`、其他 Agent 分支或 worktree、平台源码。
- 合并前必须校验 checkpoint、HEAD、依赖、阻塞 Issue、分支归属和干净工作区；使用 `--no-ff`，冲突后 abort。
- Provider 密钥、Token 和 env 内容不得进入数据库、日志、事件、报告、Git 或 API 响应。
- Git commit 使用中文；每次交付判断前端 `dist` 构建和后端 PM2 重启。

## 文件结构

- `server/internal/db/migrations.go`：报告、MR 扩展、通知表和唯一索引。
- `server/internal/events/transaction.go`：事务内写持久事件，提交后唤醒 SSE。
- `server/internal/httpapi/middleware.go`：保存服务端确认的 Agent 名称和角色。
- `server/internal/mr/types.go`：报告、审核、通知、请求和错误类型。
- `server/internal/mr/authorization.go`：任务、租约、负责人、workspace、checkpoint 和依赖校验。
- `server/internal/mr/report.go`：报告与 MR 原子创建及版本替换。
- `server/internal/mr/review.go`：审核状态机和负责人权限。
- `server/internal/mr/merge.go`：Git 合并、冲突恢复、数据库对账和 manager 通知。
- `server/internal/mr/query.go`：Agent 详情和管理员只读列表。
- `server/internal/httpapi/handlers_mr.go`：Agent 写接口和管理员查询接口。
- `web/src/views/MergeRequests.vue`：只读 MR、报告和审核视图。

---

### Task 1：完成报告、MR 和 manager 通知数据结构

**Files:**
- Modify: `server/internal/db/migrations.go`
- Modify: `server/internal/db/db_test.go`
- Modify: `server/internal/mr/types.go`
- Create: `server/internal/mr/types_test.go`

**Interfaces:**
- Produces: `completion_reports`、扩展后的 `merge_requests`、`manager_notifications` 和相关唯一索引。
- Produces: `CompletionReportInput`、`CompletionReport`、`ReviewInput`、`MRDetail`、`ManagerNotification`。

- [ ] **Step 1: 写迁移失败测试**

在 `db_test.go` 断言新表、列和唯一索引存在，并执行两次 `Migrate` 验证幂等。至少检查：

```go
required := map[string][]string{
    "completion_reports": {"task_id", "step_id", "lease_id", "agent_name", "agent_role", "version", "checkpoint_commit", "head_commit", "completed_json", "tests_json", "risks_json"},
    "merge_requests": {"report_id", "step_id", "lease_id", "report_version", "source_commit", "project_lead", "reviewed_at", "approved_at", "merged_by", "merge_commit"},
    "manager_notifications": {"project_id", "task_id", "mr_id", "report_id", "project_lead", "main_commit", "payload_json", "status"},
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/db ./internal/mr -run 'Mission07Migration|CompletionReportValidation'`

Expected: FAIL，缺少表、列或类型校验。

- [ ] **Step 3: 增加迁移和严格输入类型**

`CompletionReportInput` 固定字段，不接受 `created_by` 或 `project_lead`；请求中的名称和角色只用于交叉校验：

```go
type CompletionReportInput struct {
    AgentName       string     `json:"agent_name"`
    Role            string     `json:"role"`
    ProjectID       int64      `json:"project_id"`
    TaskID           int64      `json:"task_id"`
    StepID           int64      `json:"step_id"`
    LeaseID          string     `json:"lease_id"`
    LeaseVersion     int64      `json:"lease_version"`
    SourceBranch     string     `json:"source_branch"`
    CheckpointCommit string     `json:"checkpoint_commit"`
    HeadCommit       string     `json:"head_commit"`
    Completed        []string   `json:"completed"`
    Incomplete       []string   `json:"incomplete"`
    KeyFiles         []string   `json:"key_files"`
    Tests            []TestEvidence `json:"tests"`
    Risks            []string   `json:"risks"`
    Dependencies     []int64    `json:"dependencies"`
    MergeOrder       []int64    `json:"merge_order"`
    UserDecision     string     `json:"user_decision"`
}
```

给每个 JSON 列表设置最多 100 项、每项最多 2 KiB，报告总 JSON 最多 256 KiB；状态常量只允许 `pending_review`、`changes_requested`、`approved`、`merged`、`closed`。

- [ ] **Step 4: 运行定向测试**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/db ./internal/mr -run 'Mission07Migration|CompletionReportValidation'`

Expected: PASS。

- [ ] **Step 5: 中文提交**

```bash
git add server/internal/db/migrations.go server/internal/db/db_test.go server/internal/mr/types.go server/internal/mr/types_test.go
git commit -m "数据库：增加完成报告与负责人通知结构"
```

### Task 2：事务事件与服务端 Agent 身份上下文

**Files:**
- Create: `server/internal/events/transaction.go`
- Create: `server/internal/events/transaction_test.go`
- Modify: `server/internal/events/bus.go`
- Modify: `server/internal/httpapi/middleware.go`
- Modify: `server/internal/httpapi/auth_test.go`

**Interfaces:**
- Produces: `events.InsertTx(ctx context.Context, tx *sql.Tx, event Event) (Event, error)`。
- Produces: `events.Bus.Notify(event Event)`，只通知订阅者，不重复写数据库。
- Produces: `AgentPrincipal(ctx) (AgentPrincipalValue, bool)`，值包含 `Name` 和数据库确认的 `Role`。

- [ ] **Step 1: 写失败测试**

覆盖事务回滚后 `runtime_events` 不留记录、提交后 `Notify` 只产生一次 SSE 消息，以及请求头伪造名称或角色不能改变 principal：

```go
type AgentPrincipalValue struct {
    Name string
    Role string
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/events ./internal/httpapi -run 'InsertTx|AgentPrincipal'`

Expected: FAIL，接口尚不存在。

- [ ] **Step 3: 实现事务事件和身份解析**

`RequireAgent` 使用 token hash 联查 `agent_tokens` 与 `agent_registry`，只把数据库中的名称和角色放入 context。保留 `AgentIdentity` 作为兼容包装，但 M07 handler 只调用 `AgentPrincipal`。

- [ ] **Step 4: 运行定向测试**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/events ./internal/httpapi -run 'InsertTx|AgentPrincipal'`

Expected: PASS。

- [ ] **Step 5: 中文提交**

```bash
git add server/internal/events server/internal/httpapi/middleware.go server/internal/httpapi/auth_test.go
git commit -m "安全：绑定 Agent Token 名称与角色"
```

### Task 3：报告和 MR 原子创建

**Files:**
- Create: `server/internal/mr/authorization.go`
- Create: `server/internal/mr/authorization_test.go`
- Create: `server/internal/mr/report.go`
- Create: `server/internal/mr/report_test.go`
- Modify: `server/internal/mr/service.go`

**Interfaces:**
- Produces: `Service.SubmitReport(ctx context.Context, principal Principal, input CompletionReportInput) (MRDetail, error)`。
- Produces: `authorizeSubmission(ctx, query, principal, input) (submissionContext, error)`。
- Consumes: active `task_step_leases`、`task_assignments`、`project_workspaces`、最新 `task_checkpoints`、`team_decisions` 和 Git HEAD。

- [ ] **Step 1: 写权限失败测试**

覆盖 token 名称与请求 Agent 不一致、角色不一致、非步骤 owner、过期或错误版本 lease、错误分支、checkpoint/HEAD 漂移。错误使用可比较哨兵值：

```go
var (
    ErrIdentityMismatch   = errors.New("identity_mismatch")
    ErrLeaseInvalid       = errors.New("lease_invalid")
    ErrCheckpointMismatch = errors.New("checkpoint_mismatch")
    ErrBranchOwnership    = errors.New("branch_ownership")
)
```

- [ ] **Step 2: 写原子性失败测试**

测试成功时报告、MR 和两条 `runtime_events` 同时存在；注入第二条事件写入失败时四类记录均为零；相同 lease 和报告版本重复提交返回同一业务结果或 `409`，不能生成重复 MR。

- [ ] **Step 3: 运行测试确认失败**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/mr -run 'AuthorizeSubmission|SubmitReport'`

Expected: FAIL，提交服务尚不存在。

- [ ] **Step 4: 实现最小事务服务**

版本号按 `task_id + step_id` 递增。首次创建 `pending_review` MR；若上一 MR 为 `changes_requested`，同一事务将旧 MR 改为 `closed` 后创建新版本。`created_by`、`agent_role` 和 `project_lead` 全部取自查询结果。

- [ ] **Step 5: 运行定向测试并提交**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/mr -run 'AuthorizeSubmission|SubmitReport'`

Expected: PASS。

```bash
git add server/internal/mr
git commit -m "功能：原子提交完成报告与 MR"
```

### Task 4：项目负责人审核状态机

**Files:**
- Create: `server/internal/mr/review.go`
- Create: `server/internal/mr/review_test.go`
- Create: `server/internal/mr/query.go`
- Create: `server/internal/mr/query_test.go`

**Interfaces:**
- Produces: `Service.Review(ctx context.Context, principal Principal, mrID int64, input ReviewInput) (MRDetail, error)`。
- Produces: `Service.Detail(ctx context.Context, principal Principal, mrID int64) (MRDetail, error)`。
- Produces: `Service.AdminList(ctx context.Context, taskID *int64, limit, offset int) ([]MRDetail, error)`。

- [ ] **Step 1: 写失败测试**

覆盖普通执行 Agent 拒绝、其他项目负责人拒绝、请求角色伪造拒绝、单 Agent 负责人允许自审、多 Agent 负责人允许审核。manager 接管必须同时满足负责人失联、租约撤销或用户授权之一，并提供 1 至 2 KiB 的 `takeover_reason`；服务把原因写入审核记录、事件和 `audit_logs`。状态只允许：

```go
pending_review -> changes_requested
pending_review -> approved
approved       -> changes_requested
```

审核 `merged`、`closed` 或陈旧报告版本返回 `ErrStateConflict`。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/mr -run 'Review|Detail|AdminList'`

Expected: FAIL。

- [ ] **Step 3: 实现审核和查询**

在一个事务内追加 `mr_reviews`、条件更新 MR、写 `mr.reviewed` 持久事件。`changes_requested` 必须有 1 至 8 KiB 意见；`approved` 可使用空意见。查询返回报告、审核历史和当前负责人，不返回 lease 内部 token 或 env。

- [ ] **Step 4: 运行定向测试并提交**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/mr -run 'Review|Detail|AdminList'`

Expected: PASS。

```bash
git add server/internal/mr/review.go server/internal/mr/review_test.go server/internal/mr/query.go server/internal/mr/query_test.go
git commit -m "功能：实现项目负责人审核状态机"
```

### Task 5：负责人合并、冲突恢复与 manager 通知

**Files:**
- Create: `server/internal/mr/merge.go`
- Create: `server/internal/mr/merge_test.go`
- Modify: `server/internal/mr/service.go`
- Modify: `server/internal/issues/service_test.go`

**Interfaces:**
- Produces: `Service.Merge(ctx context.Context, principal Principal, mrID int64) (MergeResult, error)`。
- Produces: `Service.ReconcileMerge(ctx context.Context, mrID int64) (MergeResult, error)`，仅对 Git 已合并而数据库未完成的记录对账。
- Consumes: `workflow_edges`、阻塞 Issue、workspace、checkpoint、Git source commit 和项目 `main`。

- [ ] **Step 1: 写合并守卫失败测试**

覆盖未批准、非负责人、无原因的 manager 接管、过期 lease、HEAD 漂移、依赖步骤未完成、依赖 MR 未合并、阻塞 Issue、脏项目仓库、错误源分支和重复合并。合法 manager 接管必须复用 Task 4 的接管资格并记录同一原因。

- [ ] **Step 2: 写 Git 行为失败测试**

在临时仓库验证 `--no-ff` 产生 merge commit；制造冲突后确认 `MERGE_HEAD` 不存在且 MR 保持 `approved`；成功时 MR 为 `merged`，`merge_commit` 与 `main` HEAD 相同。

- [ ] **Step 3: 写通知和恢复失败测试**

成功合并后断言 `manager_notifications` 与 `mr.merged` 事件同时存在，payload 含测试摘要、风险、未完成事项和用户决策。模拟 Git 成功、数据库写失败，再调用 `ReconcileMerge`，按 `git merge-base --is-ancestor <source_commit> main` 完成一次性对账。

- [ ] **Step 4: 运行测试确认失败**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/mr ./internal/issues -run 'Merge|Reconcile|ManagerNotification|Blocking'`

Expected: FAIL。

- [ ] **Step 5: 实现合并器**

继续复用 `validateSourceBranch` 和 `abortMerge`。Git 成功后读取 `main` HEAD，再用条件事务将 `approved` 更新为 `merged`、更新步骤完成状态、写通知和事件。数据库失败时写脱密恢复日志，后续请求先检查祖先关系，禁止重复执行 merge。

- [ ] **Step 6: 运行定向测试并提交**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/mr ./internal/issues -run 'Merge|Reconcile|ManagerNotification|Blocking'`

Expected: PASS。

```bash
git add server/internal/mr server/internal/issues/service_test.go
git commit -m "功能：完成负责人合并与总管通知"
```

### Task 6：Agent 写接口与管理员只读接口

**Files:**
- Modify: `server/internal/httpapi/handlers_mr.go`
- Create: `server/internal/httpapi/handlers_mr_test.go`
- Modify: `server/internal/httpapi/router.go`
- Modify: `server/internal/httpapi/handlers_queries.go`
- Modify: `server/internal/httpapi/handlers_queries_test.go`

**Interfaces:**
- Produces: `POST /api/agent/completion-reports`。
- Produces: `GET /api/agent/mrs/{id}`、`POST /api/agent/mrs/{id}/reviews`、`POST /api/agent/mrs/{id}/merge`。
- Produces: `GET /api/admin/mrs`、`GET /api/admin/mrs/{id}`、`GET /api/admin/manager-notifications`。
- Removes: `/api/agent/mr/create` 和 manager 专用旧 merge handler。

- [ ] **Step 1: 写 HTTP 失败测试**

测试无 Token 为 `401`；管理员会话调用 Agent 写接口为 `401`；伪造 `agent_name` 或 `role` 为 `403 identity_mismatch`；租约、checkpoint、依赖和 Git 冲突分别映射为稳定的 `409` 错误码；管理员查询为 `200` 且响应不含 `token`、`api_key`、`password` 或 env 值。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/httpapi -run 'CompletionReport|MRReview|MRMerge|ManagerNotifications'`

Expected: FAIL，路由尚不存在。

- [ ] **Step 3: 实现 handler 和错误映射**

handler 从 `AgentPrincipal` 取名称和角色；所有 Agent 写请求必须包含 `agent_name` 和 `role`，并与 principal 完全一致。审核和合并请求另带 `takeover_reason`，普通负责人必须留空，manager 接管必须提供。使用统一错误结构：

```go
writeJSON(w, status, map[string]any{
    "ok": false,
    "error": code,
})
```

业务错误不回传底层 SQL、绝对路径、Git 完整输出或认证信息。

- [ ] **Step 4: 运行 HTTP 与全后端测试**

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test ./internal/httpapi -run 'CompletionReport|MRReview|MRMerge|ManagerNotifications'`

Expected: PASS。

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test -count=1 -timeout=90s ./...`

Expected: PASS，0 failures。

- [ ] **Step 5: 中文提交**

```bash
git add server/internal/httpapi
git commit -m "接口：接入报告审核与只读查询链路"
```

### Task 7：只读管理台、全链路验收与生产部署

**Files:**
- Modify: `web/src/views/MergeRequests.vue`
- Modify: `web/src/api/client.ts`
- Create: `web/src/views/MergeRequests.test.ts`
- Modify: `wanxiangAgentWorkMission.md`
- Modify: `docs/superpowers/plans/2026-07-15-mission-07-report-mr-review.md`

**Interfaces:**
- 管理台只调用 `/api/admin/mrs` 和 `/api/admin/mrs/{id}`，展示报告、审核历史、负责人、状态、测试和风险。
- 页面不包含创建 MR、`created_by` 输入、审核按钮、合并按钮或 `/api/agent/*` 请求。

- [ ] **Step 1: 写前端失败测试**

mock 管理员查询，断言页面显示 MR 状态、报告版本、测试、风险和审核意见；断言没有“创建”“请求合并”按钮，源码请求记录不包含 `/api/agent/`。

- [ ] **Step 2: 运行测试确认失败**

Run: `cd web && npm test -- --run src/views/MergeRequests.test.ts`

Expected: FAIL，页面仍包含写操作。

- [ ] **Step 3: 实现只读页面**

删除表单、`created_by`、`createMR` 和 `mergeMR`，增加管理员列表与详情加载。空列表、加载失败和长风险文本使用现有 Element Plus 样式，不增加新的 UI 依赖。

- [ ] **Step 4: 运行完整验证**

Run: `cd web && npm test -- --run && npm run build`

Expected: Vitest 全部通过，`web/dist` 构建成功。

Run: `cd server && GOCACHE=/tmp/wanxiang-go-cache go test -count=1 -timeout=90s ./... && GOCACHE=/tmp/wanxiang-go-cache go build -o /tmp/wanxiang-m07 ./cmd/wanxiang`

Expected: Go 测试全部通过，生成 `/tmp/wanxiang-m07`。

- [ ] **Step 5: 低量全链路验收**

使用测试项目和测试 Agent 自身 env，分别验证：单 Agent 自审自合并并通知 manager；多 Agent 普通执行者审核返回 `403`；伪造角色、过期租约、阻塞 Issue、依赖未完成和冲突返回预期错误。扫描数据库、事件、日志、API 响应和 Git，确认测试密钥命中数为 0。

- [ ] **Step 6: 更新任务状态并中文提交**

在 `wanxiangAgentWorkMission.md` 记录测试命令、提交、前端构建结果、生产二进制哈希、PM2 重启和健康检查；保持 `wanxiangAgent.md` 只写规范。

```bash
git add web/src web/dist wanxiangAgentWorkMission.md docs/superpowers/plans/2026-07-15-mission-07-report-mr-review.md
git commit -m "交付：完成 M07 报告审核与合并链路"
```

- [ ] **Step 7: 部署并验证 PM2**

备份现有 `server/wanxiang`，用 `/tmp/wanxiang-m07` 原子替换生产二进制，执行 `pm2 restart wanxiang-agent`。验证：

```bash
pm2 status wanxiang-agent
curl --noproxy '*' -fsS http://127.0.0.1:8088/api/health
```

Expected: PM2 状态 `online`，健康接口返回 `{"ok":true}`。随后中文提交部署证据并推送 `origin/main`。
