# M08 总管汇总、用户验收与返工 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 M07 的 manager 通知汇总为可追溯的不可变交付快照，并提供用户验收、拒绝、调整与版本化返工链路。

**Architecture:** 新增 `deliveries` 领域服务统一管理快照、验收决定、返工轮次和后台消费；数据库保存全部版本及幂等键，HTTP 仅向管理员暴露查询和验收决定。返工规划复用现有 planning 的解析与持久化能力，但通过计划版本隔离历史步骤；Vue 页面只消费管理员 API。

**Tech Stack:** Go 1.24、SQLite、chi、Vue 3、TypeScript、Element Plus、Vitest、PM2。

## Global Constraints

- `wanxiangAgent.md` 是角色、权限、恢复和部署行为的主规范。
- 客户端只提交验收决定与返工意见，不创建、审核或合并 MR，不修改项目代码。
- 验收不能授权部署、删除、生产迁移、权限扩大或密钥操作；高风险事项转成独立阻塞 Issue。
- Provider 只使用 manager 自身 `agents/manager/env`，不得借用其他 Agent 密钥。
- 所有快照、决定和返工轮次追加保存；禁止覆盖旧历史。
- 每项任务测试先行，完成后更新 Mission checkpoint，中文提交并推送 `origin/feat/mission-08`。

---

### Task 1: 计划版本与交付持久层

**Files:**
- Modify: `server/internal/db/migrations.go`
- Create: `server/internal/deliveries/types.go`
- Create: `server/internal/deliveries/service.go`
- Create: `server/internal/deliveries/service_test.go`
- Modify: `server/internal/planning/service.go`
- Modify: `server/internal/planning/service_test.go`

**Interfaces:**
- Produces: `deliveries.NewService(db, bus, issues) *Service`
- Produces: `Service.BuildSnapshot(ctx, notificationID) (Snapshot, error)`
- Produces: `planning.Service.PlanVersion(ctx, taskID, version, source) (Plan, error)`

- [ ] 写迁移测试，断言 `delivery_snapshots`、`acceptance_decisions`、`rework_rounds`、`task_plan_versions` 存在，且 `task_steps`、`workflow_edges` 拥有 `plan_version`。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/db ./internal/deliveries ./internal/planning`，确认因表和接口缺失失败。
- [ ] 增加表、唯一索引、类型和 JSON 证据结构；初始规划写版本 1，返工规划只向新版本追加步骤和依赖。
- [ ] 增加快照聚合，事务校验当前版本所有步骤完成、MR 已合并且没有未解决阻塞 Issue；重复通知返回已有快照。
- [ ] 重跑定向测试至通过并提交 `功能：增加 M08 交付快照与计划版本`。

### Task 2: 通知消费 Worker 与恢复

**Files:**
- Create: `server/internal/deliveries/worker.go`
- Create: `server/internal/deliveries/worker_test.go`
- Modify: `server/internal/app/app.go`
- Modify: `server/internal/app/app_test.go`

**Interfaces:**
- Consumes: `Service.BuildSnapshot`
- Produces: `deliveries.NewWorker(db, service, interval) *Worker`
- Produces: `Worker.Start()` and `Worker.Close()`

- [ ] 写 Worker 失败测试：pending 通知生成一次快照并变为 consumed，条件未满足保持 pending 并写脱密错误与重试时间，重启后可继续。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/deliveries ./internal/app`，确认 Worker 缺失失败。
- [ ] 实现单轮扫描、并发 claim、退避字段和关闭协议，在 app 生命周期装配。
- [ ] 重跑定向测试至通过并提交 `功能：接入交付通知消费与恢复 Worker`。

### Task 3: 验收决定与返工规划

**Files:**
- Create: `server/internal/deliveries/acceptance.go`
- Create: `server/internal/deliveries/acceptance_test.go`
- Create: `server/internal/deliveries/rework.go`
- Create: `server/internal/deliveries/rework_test.go`
- Modify: `server/internal/planning/service.go`
- Modify: `server/internal/tasks/service.go`
- Modify: `server/internal/tasks/service_test.go`

**Interfaces:**
- Produces: `Service.Decide(ctx, snapshotID, DecisionInput) (DecisionResult, error)`
- Produces: `Service.ProcessRework(ctx, roundID) (ReworkRound, error)`
- Decision errors: `delivery_not_ready`, `stale_snapshot`, `acceptance_closed`, `decision_comment_required`

- [ ] 写验收测试：accepted 完成任务；rejected/revision_requested 创建下一轮和下一计划版本；幂等键重复返回原结果；并发只成功一次。
- [ ] 写返工测试：旧步骤不变，新步骤带新版本；manager 配置缺失持久化 `blocked: missing_config`；恢复配置后续跑。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/deliveries ./internal/planning ./internal/tasks`，确认新行为失败。
- [ ] 实现条件更新、16 KiB 意见限制、manager Provider 调用、M02 计划校验复用、事件和脱密错误。
- [ ] 重跑定向测试至通过并提交 `功能：实现用户验收与版本化返工`。

### Task 4: 管理员 API 与鉴权

**Files:**
- Create: `server/internal/httpapi/handlers_deliveries.go`
- Create: `server/internal/httpapi/handlers_deliveries_test.go`
- Modify: `server/internal/httpapi/router.go`
- Modify: `server/internal/app/app.go`

**Interfaces:**
- Produces: `GET /api/admin/deliveries`
- Produces: `GET /api/admin/deliveries/{id}`
- Produces: `POST /api/admin/deliveries/{id}/decisions`
- Produces: `GET /api/admin/tasks/{id}/rework-rounds`

- [ ] 写路由测试，覆盖未认证 401、列表详情、三种决定、错误码、幂等键和敏感字段不回显。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/httpapi`，确认路由缺失失败。
- [ ] 实现请求解析、管理员身份注入、领域错误到 HTTP 状态映射和 JSON 响应。
- [ ] 重跑定向测试至通过并提交 `接口：开放 M08 交付验收查询与决定`。

### Task 5: 交付验收 Web 页面

**Files:**
- Modify: `web/src/api/client.ts`
- Modify: `web/src/router.ts`
- Modify: `web/src/views/Dashboard.vue`
- Modify: `web/src/views/MergeRequests.vue`
- Create: `web/src/views/Deliveries.vue`
- Create: `web/src/views/Deliveries.test.ts`

**Interfaces:**
- Consumes: Task 4 管理员 API
- Produces: `/deliveries` 页面及调度台、MR 页入口

- [ ] 写 Vitest 用例，覆盖列表、详情、证据、历史、空状态、验收、拒绝必填意见、调整、重复提交禁用和高风险提示。
- [ ] 运行 `npm test -- --run web/src/views/Deliveries.test.ts`，确认页面缺失失败。
- [ ] 按现有管理台视觉令牌实现响应式证据轨道和返工时间线，补充可访问标签、焦点状态与移动端折叠。
- [ ] 运行 `npm test -- --run` 和 `npm run build`，通过后提交 `界面：增加交付验收与返工轨迹页面`。

### Task 6: 全链路验收、文档与生产交付

**Files:**
- Modify: `wanxiangAgentWorkMission.md`
- Modify as needed: `README.md`

**Interfaces:**
- Consumes: Tasks 1-5 完整功能
- Produces: 可恢复 Mission 证据和生产部署结果

- [ ] 运行 `gofmt`、`git diff --check`、占位符扫描和变更差异密钥扫描。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test -count=1 -timeout=120s ./...` 和 `go build -o /tmp/wanxiang-m08 ./cmd/wanxiang`。
- [ ] 运行 `npm test -- --run` 和 `npm run build`，确认 `web/dist` 为本次产物。
- [ ] 请求代码审查，修复 Critical/Important 问题并重跑相关验证。
- [ ] 更新 Mission 最终状态、提交链、测试证据、构建字段、PM2 和健康检查字段，中文提交并推送功能分支。
- [ ] 合并到 `main`，推送 `origin/main`，备份并替换 `server/wanxiang`，执行 `pm2 restart wanxiang-agent`、`pm2 show wanxiang-agent` 和 `/api/health` 验证。
- [ ] 更新最终部署证据并确认 `HEAD == origin/main`，仅保留用户原有未提交改动。
