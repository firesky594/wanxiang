# M09-M10 本机发布编排与端到端验收 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 完成本机声明式流水线、有限重试、确认式发布/回滚，并以端到端安全测试验收 M01-M10。

**Architecture:** 新增 `pipelines` 领域负责定义快照、运行状态机、受控命令、Worker 和确认；HTTP/Web 只暴露管理员控制。M10 在 `e2e` 包复用真实领域服务和临时 Git/SQLite 夹具验证整链路。

**Tech Stack:** Go 1.24、SQLite、chi、Vue 3、TypeScript、Vitest、PM2。

## Global Constraints

- 所有行为受 `wanxiangAgent.md` 约束。
- 不经过 shell；不接入远程发布；不读取或回显密钥。
- 部署、迁移、删除、回滚必须针对具体运行由管理员单独确认。
- 测试先行，中文提交，保留用户未提交文档改动。

---

### Task 1: 流水线持久层与声明协议

**Files:** Create `server/internal/pipelines/types.go`, `metadata.go`, `service.go`, tests; Modify `server/internal/db/migrations.go`.

- [ ] 写失败测试覆盖表、严格定义、命令允许列表和路径安全。
- [ ] 实现定义快照、运行、步骤尝试、确认和回滚记录。
- [ ] 运行 `go test ./internal/db ./internal/pipelines` 并提交。

### Task 2: 受控执行、分类、重试与恢复 Worker

**Files:** Create `server/internal/pipelines/runner.go`, `worker.go` and tests; Modify `server/internal/app/app.go`.

- [ ] 写失败测试覆盖成功、四类失败、有限重试、不可逆不重试、重启幂等。
- [ ] 实现无 shell Runner、输出脱敏、步骤条件 claim、退避和阻塞 Issue。
- [ ] 接入 App 生命周期，定向测试通过后提交。

### Task 3: 发布确认与回滚入口

**Files:** Create `server/internal/pipelines/release.go` and tests.

- [ ] 写失败测试覆盖未确认拒绝、提交/哈希快照、失败生成 rollback、重复确认幂等。
- [ ] 实现管理员确认和本机安全版本记录；不可逆动作保持阻塞。
- [ ] 定向测试通过后提交。

### Task 4: 管理员 API 和 Web 控制台

**Files:** Create `handlers_pipelines.go`, tests, `web/src/views/Pipelines.vue`, tests; Modify router/client/navigation/app wiring.

- [ ] 写 API 401/403/409、列表详情、启动和确认测试。
- [ ] 写 Web 状态、重试、确认警告、回滚入口和可访问性测试。
- [ ] 实现并运行 Go/Web 定向测试后提交。

### Task 5: M10 端到端与安全矩阵

**Files:** Create `server/internal/e2e/workflow_test.go`, `security_test.go`.

- [ ] 建立临时根目录、SQLite、Git 和假 Provider 夹具。
- [ ] 覆盖单/多 Agent、缺配置恢复、中断恢复/接管、阻塞 Issue、流水线确认与回滚。
- [ ] 覆盖租约、scope、身份、路径、符号链接、命令注入和密钥扫描。
- [ ] 运行端到端和全量回归后提交。

### Task 6: 审查、Mission 证据、合并与本机部署

**Files:** Modify `wanxiangAgentWorkMission.md`, `README.md` as needed.

- [ ] 代码审查并修复 Critical/Important。
- [ ] 运行 Go/Web 全量测试、构建、diff/密钥扫描。
- [ ] 更新 M09、M10 证据并合并 main；保留用户文档改动。
- [ ] 经用户确认后备份并替换后端、生成前端 dist、PM2 重启和健康检查。
