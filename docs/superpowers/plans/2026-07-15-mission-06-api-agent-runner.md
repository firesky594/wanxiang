# Mission 06 API Agent 多进程执行器实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Go 主服务以独立 Worker 进程监管 Agent，使每个 Agent 使用自身 env 调用远程 Provider API，在 M04 worktree 与 M05 租约边界内消费工作包。

**Architecture:** Supervisor 在主进程中选择可运行步骤、领取租约并启动同一二进制的 `agent-worker` 模式；Worker 读取自身 env、调用 Provider API 并提交版本化动作。文件、测试和 Git 操作由受控工具执行，每次写入复用 Lease Guard；PM2 只管理主服务。

**Tech Stack:** Go 1.26、SQLite、`os/exec`、现有 Provider Adapter、M04 worktree、M05 leases、Chi、Vue 3、Vitest。

## Global Constraints

- 禁止调用本机 Codex、OpenCode 或其他 AI CLI，禁止把模型输出传给任意 shell。
- 每个 Agent 只使用自己的 `agents/<agent>/env`；测试复制 manager env 时目标必须不存在、权限为 `0600`，不得修改源文件。
- Provider 密钥不得进入数据库、日志、事件、checkpoint、Git、命令参数、标准输出或 API 响应。
- 所有文件与命令动作校验 Agent、task、step、lease ID/version、worktree 和 write scope。
- 低量测试默认并发 1、单工作包、最多 3 次 Provider 请求，禁止部署、删库和不可逆命令。
- Git 提交使用中文；每次更新分别判断前端构建、后端构建和 PM2 重启。

---

### Task 1：测试 env 引导与执行数据结构

**Files:**
- Create: `server/internal/executor/config.go`
- Create: `server/internal/executor/config_test.go`
- Modify: `server/internal/db/migrations.go`
- Modify: `server/internal/db/db_test.go`
- Create: `server/internal/executor/types.go`

**Interfaces:**
- Produces: `CopyTestEnv(source, target string) error`，仅复制不存在的目标并固定 `0600`。
- Produces: `executor_runs`、`executor_actions`，以及 `RunStatus`、`ActionRequest`、`WorkerInput`。

- [x] 写失败测试：复制 manager env 到目标 Agent，内容一致、权限 `0600`、已有目标拒绝覆盖、错误信息不包含源内容。
- [x] 写失败迁移测试：两张执行表、索引和幂等迁移存在，字段不保存完整 Provider 请求/响应或密钥。
- [x] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/executor ./internal/db -run 'CopyTestEnv|ExecutorMigration'` 确认失败。
- [x] 实现安全复制、执行类型、迁移和索引。
- [x] 重跑定向测试确认通过。
- [x] 提交：`数据库：增加 Agent 执行记录与测试配置引导`（`c00cac5`）。

### Task 2：受控文件工具与 Lease Guard

**Files:**
- Create: `server/internal/executor/files.go`
- Create: `server/internal/executor/files_test.go`
- Create: `server/internal/executor/redact.go`
- Create: `server/internal/executor/redact_test.go`

**Interfaces:**
- Consumes: `leases.Service.Authorize`。
- Produces: `ReadFile(context.Context, LeaseRef, path string)`、`WriteFile(...)` 和 `Redact(string)`。

- [x] 写失败测试：只允许 scope 内普通文件，拒绝绝对路径、`..`、符号链接、`.git`、env、平台源码和越权 lease。
- [x] 写失败测试：写入使用同目录临时文件和原子替换，失败不破坏原文件。
- [x] 写脱密失败测试：API key、Bearer token、密码字段和 env 行被替换，普通错误保留且总长度受限。
- [x] 实现受控读写、大小限制、符号链接检查和脱密。
- [x] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/executor -run 'ReadFile|WriteFile|Redact'`。
- [x] 提交：`功能：实现租约约束的 Agent 文件工具`（`d0ba281`）。

### Task 3：允许列表测试命令与 Git checkpoint

**Files:**
- Create: `server/internal/executor/checks.go`
- Create: `server/internal/executor/checks_test.go`
- Create: `server/internal/executor/checkpoint.go`
- Create: `server/internal/executor/checkpoint_test.go`

**Interfaces:**
- Produces: `RunCheck(context.Context, LeaseRef, CheckRequest) CheckResult`，参数数组直传 `exec.CommandContext`。
- Produces: `CreateGitCheckpoint(context.Context, LeaseRef, WorkerSummary)`，提交格式 `checkpoint(<step-id>): <中文摘要>` 并登记 M05 checkpoint。

- [x] 写失败测试：仅运行项目/assignment 允许的命令和参数，拒绝 shell、重定向、管道、部署、删除与超时命令。
- [x] 写失败测试：命令 cwd 固定为 Agent worktree，输出脱密并限制长度，租约失效立即拒绝。
- [x] 写 Git 测试：只提交受控变更，工作区未知敏感文件时拒绝，不执行 reset/clean。
- [x] 实现命令允许列表、超时、输出限制和中文 checkpoint。
- [x] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/executor -run 'RunCheck|GitCheckpoint'`。
- [x] 提交：`功能：增加受控测试命令与 Git 检查点`（`f4c0005`）。

### Task 4：Provider JSON 动作循环

**Files:**
- Create: `server/internal/executor/protocol.go`
- Create: `server/internal/executor/protocol_test.go`
- Create: `server/internal/executor/runner.go`
- Create: `server/internal/executor/runner_test.go`

**Interfaces:**
- Consumes: 现有 Provider Adapter、Task/assignment/checkpoint 数据和 Tasks 2-3 工具。
- Produces: `Runner.Run(context.Context, WorkerInput) (WorkerResult, error)`。

- [ ] 写失败协议测试：严格解析版本、状态、summary 和动作；拒绝未知动作、控制字符、超长内容、秘密字段和越界路径。
- [ ] 写失败 Runner 测试：只从目标 Agent env 创建 Provider，最多 3 次请求，不回退 manager env，不保存完整对话。
- [ ] 写动作循环测试：read/write/check/checkpoint 顺序执行，每一步重新校验 lease；401、429、5xx、超时和非法 JSON 得到确定状态。
- [ ] 实现安全 prompt、JSON 协议、请求预算、Token 记账、有限重试和脱密事件。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/executor -run 'Protocol|Runner'`。
- [ ] 提交：`功能：实现 Agent Provider API 动作循环`。

### Task 5：Worker 子进程模式

**Files:**
- Create: `server/internal/executor/worker.go`
- Create: `server/internal/executor/worker_test.go`
- Modify: `server/cmd/wanxiang/main.go`
- Test: `server/cmd/wanxiang/main_test.go`

**Interfaces:**
- Produces: `wanxiang agent-worker --input-fd 3`，输入不含 Provider 密钥。
- Produces: 心跳 goroutine、信号退出 checkpoint 和结构化退出结果。

- [ ] 写失败进程测试：命令参数不含密钥、Agent env 只注入子进程、stdout/stderr 脱密。
- [ ] 写失败测试：禁止启动 `codex`、`opencode`、shell；Worker 只运行当前二进制的内部模式。
- [ ] 写失败测试：15 秒心跳、租约冲突退出、关闭信号触发 checkpoint，异常退出不伪造完成。
- [ ] 实现 fd 输入、Worker 主函数、信号处理和退出协议。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/executor ./cmd/wanxiang -run Worker`。
- [ ] 提交：`功能：增加 API Agent 独立 Worker 进程`。

### Task 6：Supervisor 调度与 App 生命周期

**Files:**
- Create: `server/internal/executor/supervisor.go`
- Create: `server/internal/executor/supervisor_test.go`
- Modify: `server/internal/app/app.go`
- Modify: `server/internal/app/app_test.go`

**Interfaces:**
- Produces: `Supervisor.Start()`、`Supervisor.Close()`、`Scan(context.Context)`。
- Consumes: M03 assignment、M04 ready workspace、M05 acquire/recovery 和 Agent `max_concurrency`。

- [ ] 写失败调度测试：只消费依赖完成且 workspace ready 的步骤；同一步骤不重复启动。
- [ ] 写并发测试：按 Agent `max_concurrency` 和全局限制启动独立进程；低量模式全局并发固定为 1。
- [ ] 写重启测试：从 `executor_runs` 和租约恢复，不为同一有效 lease 重复启动。
- [ ] 写关闭测试：停止领取、通知 Worker、等待退出；超时后终止并由 M05 中断。
- [ ] 实现扫描、PID/退出状态、事件、App Start/Close 集成。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test ./internal/executor ./internal/app -run 'Supervisor|Executor'`。
- [ ] 提交：`功能：监管 Agent 并行任务消费`。

### Task 7：管理 API、低量真实 API 验收与交接

**Files:**
- Create: `server/internal/httpapi/handlers_executor.go`
- Create: `server/internal/httpapi/handlers_executor_test.go`
- Modify: `server/internal/httpapi/router.go`
- Modify: `wanxiangAgentWorkMission.md`
- Modify: `docs/superpowers/plans/2026-07-15-mission-06-api-agent-runner.md`

**Interfaces:**
- Produces: Admin 执行时间线、启动/停止低量测试和运行详情接口；不返回密钥或完整 Provider 对话。

- [ ] 写失败 HTTP 测试：管理员可查看脱密运行状态；Agent 和未认证请求不能启动测试或读取其他运行详情。
- [ ] 使用 `CopyTestEnv` 将 manager env 复制到已创建的 `m06-smoke`，验证 `0600`、不覆盖和 manager 源哈希不变。
- [ ] 运行一次单 Agent、单工作包、最多 3 请求的真实 Provider API 低量测试；记录 token 数和脱密结果，不记录内容或密钥。
- [ ] 杀死测试 Worker，验证 M05 中断；重启 Supervisor，验证不重复启动有效租约。
- [ ] 扫描数据库、日志、事件、Git、进程参数和 API 响应，确认不存在测试密钥。
- [ ] 运行 `GOCACHE=/tmp/wanxiang-go-cache go test -count=1 -timeout=60s ./...` 和后端构建。
- [ ] 判断是否关联前端；若增加管理台页面，运行 `npm test -- --run && npm run build`，否则记录 `frontend_build_required: false`。
- [ ] 更新 M06 状态、测试证据、部署与 PM2 判断，中文合并并推送 `origin/main`。
