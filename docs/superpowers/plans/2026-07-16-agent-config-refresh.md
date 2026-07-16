# Agent Config Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 保持私有 env 配置持久有效，清理刷新后的陈旧缺配置状态，并提供 OpenAI/DeepSeek 默认模型。

**Architecture:** 后端以已验证的 env 为配置真源，只协调特定陈旧数据库状态；前端用 provider 映射提供表单默认值，不覆盖已持久化模型。

**Tech Stack:** Go 1.24、SQLite、Vue 3、TypeScript、Vitest。

## Global Constraints

- 密钥不得进入 API、日志、Git 或测试输出。
- `env` 权限保持 `0600`，空密钥更新保留旧值。
- OpenAI 默认 `gpt-5.2`；DeepSeek 默认 `deepseek-v4-flash`。

---

### Task 1: 后端刷新状态协调

**Files:**
- Modify: `server/internal/agents/service.go`
- Test: `server/internal/agents/service_test.go`

- [ ] 写失败测试：保存完整配置后将数据库状态模拟为 `blocked: missing_config`，调用 `GetAgentConfig` 应返回 `configured`、`secret_configured=true`，并保留 env 密钥。
- [ ] 运行 `go test ./internal/agents -run TestGetAgentConfigRepairsStaleMissingConfig -count=1`，确认 RED。
- [ ] 在 `loadRuntimeConfig` 中仅对该陈旧状态执行条件更新。
- [ ] 重跑测试确认 GREEN。

### Task 2: 前端默认模型

**Files:**
- Modify: `web/src/views/Agents.vue`
- Test: `web/src/views/Agents.test.ts`

- [ ] 写失败测试：新建 OpenAI 默认为 `gpt-5.2`，切换 DeepSeek 后为 `deepseek-v4-flash`。
- [ ] 运行 `npm test -- --run src/views/Agents.test.ts`，确认 RED。
- [ ] 增加 provider 默认模型映射并用于初始化、重置和切换。
- [ ] 重跑测试确认 GREEN。

### Task 3: 回归、提交与部署

- [ ] 运行 `go test ./...`、`go build ./...`、`npm test -- --run`、`npm run build`。
- [ ] 扫描密钥与 diff，提交中文 commit，合并并推送 main。
- [ ] 经本轮用户授权更新 `server/wanxiang`、`web/dist`，重启 `wanxiang-agent` 并验证 `/api/health`。
