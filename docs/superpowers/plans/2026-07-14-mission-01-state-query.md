# Mission 01 State Query Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist and query tasks, projects, steps, edges, merge requests, issues, and runtime events so the management UI survives browser and service restarts.

**Architecture:** Domain services own SQL queries and state transitions. Admin HTTP handlers expose paginated list and detail resources. The Vue task store loads a persisted snapshot before SSE appends new events.

**Tech Stack:** Go 1.23, SQLite, chi, Vue 3, Pinia, TypeScript, Vitest.

## Global Constraints

- Admin query routes require a valid admin session.
- Invalid resource identifiers return HTTP 404; invalid state transitions return HTTP 409.
- Pagination uses `limit` and `offset`, with `limit` constrained to 1 through 100.
- Runtime events remain append-only and SSE deduplicates by event ID.
- Production code follows test-driven development.

---

### Task 1: Task and project query service

**Files:**
- Modify: `server/internal/tasks/types.go`
- Modify: `server/internal/tasks/service.go`
- Modify: `server/internal/tasks/service_test.go`

**Interfaces:**
- Produces: `List(ctx, limit, offset) ([]Task, error)`, `Get(ctx, id) (TaskDetail, error)`, `UpdateStatus(ctx, id, next, actor) (Task, error)`.

- [ ] Add failing tests that create two tasks, list newest first, load one task with its project, return `ErrNotFound`, and reject `created -> completed`.
- [ ] Run `GOCACHE=/tmp/wanxiang-m01-go-cache go test ./internal/tasks -run 'Test(List|Get|Update)'` and confirm the new tests fail because the methods do not exist.
- [ ] Add `Project`, `TaskStep`, `WorkflowEdge`, `TaskDetail`, `ErrNotFound`, and `ErrInvalidTransition`; implement the three methods with explicit allowed transitions.
- [ ] Re-run the focused tests and confirm they pass.

### Task 2: MR, Issue, and event history queries

**Files:**
- Modify: `server/internal/mr/service.go`
- Modify: `server/internal/mr/service_test.go`
- Modify: `server/internal/issues/service.go`
- Modify: `server/internal/issues/service_test.go`
- Modify: `server/internal/events/bus.go`
- Modify: `server/internal/events/bus_test.go`

**Interfaces:**
- Produces: `mr.Service.List`, `issues.Service.List`, and `events.Bus.List` with task filters and pagination.

- [ ] Write failing tests that verify newest-first pagination and task filtering.
- [ ] Run the three focused package test commands and confirm missing-method failures.
- [ ] Implement SQL list methods with bounded pagination and deterministic `id desc` ordering.
- [ ] Re-run focused tests and confirm they pass.

### Task 3: Admin query HTTP API

**Files:**
- Create: `server/internal/httpapi/handlers_queries.go`
- Modify: `server/internal/httpapi/router.go`
- Modify: `server/internal/httpapi/smoke_test.go`

**Interfaces:**
- Produces: `GET /api/admin/tasks`, `GET /api/admin/tasks/{id}`, `PATCH /api/admin/tasks/{id}/status`, `GET /api/admin/mrs`, `GET /api/admin/issues`, and `GET /api/admin/tasks/{id}/events`.

- [ ] Add failing HTTP tests for authentication, persisted task detail, 404, 409, pagination validation, and event history.
- [ ] Run `GOCACHE=/tmp/wanxiang-m01-go-cache go test ./internal/httpapi -run TestAdminQuery` and confirm 404 or method-not-defined failures.
- [ ] Implement handlers that map domain errors to 404/409 and malformed pagination to 400.
- [ ] Re-run the focused tests and confirm they pass.

### Task 4: Persisted frontend task store

**Files:**
- Create: `web/src/stores/tasks.ts`
- Create: `web/src/stores/tasks.test.ts`
- Modify: `web/src/api/client.ts`
- Modify: `web/src/stores/events.ts`
- Modify: `web/src/stores/events.test.ts`
- Modify: `web/src/views/Dashboard.vue`
- Modify: `web/src/views/TaskDetail.vue`

**Interfaces:**
- Produces: `useTasksStore().loadList()` and `loadDetail(id)`, plus `useEventsStore().hydrate(events)`.

- [ ] Write failing Vitest cases for persisted list/detail loading and event snapshot deduplication.
- [ ] Run `npm test -- --run src/stores/tasks.test.ts src/stores/events.test.ts` and confirm failures caused by missing stores/actions.
- [ ] Implement API types, stores, initial loading, error state, and snapshot-before-SSE ordering.
- [ ] Re-run focused frontend tests and confirm they pass.

### Task 5: Mission evidence and full verification

**Files:**
- Modify: `wanxiangAgentWorkMission.md`

- [ ] Run `gofmt` on changed Go files.
- [ ] Run `GOCACHE=/tmp/wanxiang-m01-go-cache go test ./...` and confirm zero failures.
- [ ] Run `npm test -- --run && npm run build` and confirm tests and build pass.
- [ ] Update M01 and the handoff block with commit, tests, risks, and `next_action` for M02.
- [ ] Commit the Mission implementation, merge it into `main`, re-run verification on `main`, and push `origin/main`.
