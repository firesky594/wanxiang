# Agent Provider v0.1.0 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-agent OpenAI and DeepSeek configuration with real provider probing.

**Architecture:** Agent env files hold private runtime configuration. A provider registry selects protocol-specific HTTP adapters, while the agent service owns validation, storage, status persistence, and launcher coordination.

**Tech Stack:** Go standard HTTP client, chi, Vue 3, Pinia, Element Plus, Vitest

## Global Constraints

- Never return or log API keys.
- Store secret files with mode `0600` and keep them out of Git.
- Default OpenAI to `https://api.openai.com/v1` and DeepSeek to `https://api.deepseek.com`.
- Allow an agent-specific base URL override.
- Do not call paid APIs on a timer.
- Keep OpenAI and DeepSeek protocol implementations separate.

---

### Task 1: Provider adapters

**Files:**
- Create: `server/internal/providers/types.go`
- Create: `server/internal/providers/openai.go`
- Create: `server/internal/providers/deepseek.go`
- Create: `server/internal/providers/providers_test.go`

- [ ] Write local-server tests for defaults, overrides, authorization, payloads, successful parsing, and sanitized provider errors.
- [ ] Run the tests and confirm they fail because the package does not exist.
- [ ] Implement the registry and separate adapters.
- [ ] Run provider and full Go tests.
- [ ] Commit the provider layer.

### Task 2: Agent configuration and launcher

**Files:**
- Modify: `server/internal/agents/types.go`
- Modify: `server/internal/agents/service.go`
- Modify: `server/internal/agents/launcher.go`
- Modify: `server/internal/agents/service_test.go`
- Modify: `server/internal/agents/runtime_test.go`

- [ ] Write tests for env validation, permissions, secret preservation, legacy manager keys, listing, and probe status.
- [ ] Run the focused tests and confirm the new behavior fails.
- [ ] Implement per-agent configuration and provider probing.
- [ ] Make launcher startup and configuration refresh use the same probe path.
- [ ] Run agent and full Go tests.
- [ ] Commit agent runtime support.

### Task 3: Admin API and Agents page

**Files:**
- Modify: `server/internal/httpapi/router.go`
- Modify: `server/internal/httpapi/handlers_agents.go`
- Modify: `server/internal/httpapi/auth_test.go`
- Modify: `web/src/api/client.ts`
- Modify: `web/src/stores/auth.ts`
- Modify: `web/src/views/Agents.vue`
- Create: `web/src/views/Agents.test.ts`

- [ ] Write authenticated API tests proving secrets never appear in responses.
- [ ] Implement list, save, and probe endpoints.
- [ ] Write frontend tests for configuration payloads and secret preservation.
- [ ] Implement the Agent model configuration form and status display.
- [ ] Run all Go and frontend tests and both builds.
- [ ] Commit the admin workflow.

### Task 4: Release v0.1.0

**Files:**
- Modify: `README.md`
- Modify: `agents/manager/agent.yaml`
- Modify: `agents/manager/env.example`

- [ ] Document provider keys, defaults, overrides, and safe secret handling.
- [ ] Build the deployed backend and frontend.
- [ ] Restart only `wanxiang-agent`, save PM2, and verify its health and logs.
- [ ] Confirm the Git diff and commit history match the approved scope.
- [ ] Create annotated tag `v0.1.0`.
