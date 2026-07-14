# Mission 02 Manager Planning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Consume created tasks, ask the manager model for a validated structured plan, and persist idempotent task steps and dependency edges.

**Architecture:** A planning service owns prompt construction, JSON validation, and transactional persistence. The agent service exposes a secret-safe manager chat method. A lifecycle worker polls for created tasks and records deterministic blocked states when planning fails.

**Tech Stack:** Go 1.23, SQLite, existing Provider registry, JSON, chi event bus.

## Global Constraints

- Never persist or emit Provider credentials.
- A task may have only one accepted planning result.
- Invalid model output moves the task to `blocked: planning_error` with a redacted summary.
- Planning writes steps, edges, task status, and planning event transactionally.
- Every behavior starts with a failing test.

---

### Task 1: Structured plan schema and validation

**Files:**
- Create: `server/internal/planning/types.go`
- Create: `server/internal/planning/validate.go`
- Create: `server/internal/planning/validate_test.go`

- [x] Write failing tests for valid plans, duplicate keys, missing acceptance criteria, unknown dependencies, and dependency cycles.
- [x] Implement `ParsePlan([]byte) (Plan, error)` with deterministic validation errors.
- [x] Run `go test ./internal/planning -run TestParsePlan`.

### Task 2: Manager planning request boundary

**Files:**
- Modify: `server/internal/agents/service.go`
- Modify: `server/internal/agents/service_test.go`
- Create: `server/internal/planning/prompt.go`
- Create: `server/internal/planning/prompt_test.go`

- [x] Add failing tests for manager chat and prompt content without API keys.
- [x] Add `agents.Service.ChatAgent` using existing runtime config and Provider registry.
- [x] Build the manager system/user messages from task data and `agents/manager/system_prompt.md`.
- [x] Run focused agent and planning tests.

### Task 3: Transactional and idempotent planning service

**Files:**
- Create: `server/internal/planning/service.go`
- Create: `server/internal/planning/service_test.go`
- Modify: `server/internal/tasks/service.go`

- [x] Write failing tests for created-task planning, persisted steps/edges, duplicate invocation, and invalid output blocking.
- [x] Implement task claim, model call, validation, transactional persistence, and runtime events.
- [x] Ensure a second invocation returns the accepted plan without duplicate rows or model calls.
- [x] Run `go test ./internal/planning`.

### Task 4: Lifecycle worker and recovery

**Files:**
- Create: `server/internal/planning/worker.go`
- Create: `server/internal/planning/worker_test.go`
- Modify: `server/internal/app/app.go`

- [x] Write failing tests that the worker consumes created tasks and stops with application shutdown.
- [x] Wire the worker after manager readiness without making app startup fail when manager configuration is missing.
- [x] Publish planning started, completed, and blocked events.
- [x] Run app and planning package tests.

### Task 5: Verification and Mission handoff

**Files:**
- Modify: `wanxiangAgentWorkMission.md`

- [x] Run `gofmt` and `go test ./...`.
- [x] Run `go build -o wanxiang ./cmd/wanxiang` and record whether the running backend needs replacement and restart.
- [x] Run frontend tests and build to catch API contract regressions.
- [x] Record whether this Mission changes Web code or API contracts and whether `web/dist` must be rebuilt for deployment.
- [x] Mark M02 complete with commits, tests, remaining risk, and M03 next action.
- [ ] Merge to `main`, verify again, and push `origin/main`.
