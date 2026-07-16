package httpapi

import (
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"
	"wanxiang-agent/server/internal/agents"
	"wanxiang-agent/server/internal/assignments"
	"wanxiang-agent/server/internal/deliveries"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/executor"
	"wanxiang-agent/server/internal/issues"
	"wanxiang-agent/server/internal/leases"
	"wanxiang-agent/server/internal/mr"
	"wanxiang-agent/server/internal/pipelines"
	"wanxiang-agent/server/internal/tasks"
	"wanxiang-agent/server/internal/workspaces"
)

type Dependencies struct {
	DB          *sql.DB
	Agents      *agents.Service
	Launcher    *agents.Launcher
	Bus         *events.Bus
	Tasks       *tasks.Service
	MR          *mr.Service
	Issues      *issues.Service
	Assignments *assignments.Service
	Workspaces  *workspaces.Service
	Leases      *leases.Service
	Executor    *executor.AdminService
	Deliveries  *deliveries.Service
	Pipelines   *pipelines.Service
}

func NewRouter(deps Dependencies) http.Handler {
	r := chi.NewRouter()
	r.Post("/api/admin/login", handleAdminLogin(deps))
	r.Post("/api/admin/bootstrap", handleAdminBootstrap(deps))
	r.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	r.Group(func(admin chi.Router) {
		admin.Use(RequireAdmin(deps.DB))
		if deps.Agents != nil {
			admin.Get("/api/admin/manager/status", handleManagerStatus(deps.Agents))
			admin.Post("/api/admin/manager/secrets", handleManagerSecrets(deps.Agents, deps.Launcher))
			admin.Get("/api/admin/agents", handleAgentConfigs(deps.Agents))
			admin.Put("/api/admin/agents/{name}/config", handleSaveAgentConfig(deps.Agents, deps.Launcher))
			admin.Post("/api/admin/agents/{name}/probe", handleProbeAgent(deps.Agents, deps.Launcher))
		}
		if deps.Bus != nil {
			admin.Get("/api/events/stream", handleEventStream(deps.Bus))
		}
		if deps.Tasks != nil {
			admin.Get("/api/admin/projects", handleListProjects(deps.Tasks))
			admin.Post("/api/admin/tasks", handleCreateTask(deps.Tasks))
			admin.Get("/api/admin/tasks", handleListTasks(deps.Tasks))
			admin.Get("/api/admin/tasks/{id}", handleGetTask(deps.Tasks))
			admin.Patch("/api/admin/tasks/{id}/status", handleUpdateTaskStatus(deps.Tasks))
			if deps.Bus != nil {
				admin.Get("/api/admin/tasks/{id}/events", handleListEvents(deps.Bus))
			}
		}
		if deps.Assignments != nil {
			admin.Get("/api/admin/tasks/{id}/match", handleGetTaskMatch(deps.Assignments))
			admin.Put("/api/admin/tasks/{id}/match", handleOverrideTaskMatch(deps.Assignments))
		}
		if deps.Workspaces != nil {
			admin.Get("/api/admin/tasks/{id}/workspace", handleGetTaskWorkspace(deps.Workspaces))
			admin.Post("/api/admin/tasks/{id}/workspace/reconcile", handleReconcileTaskWorkspace(deps.Workspaces))
			admin.Post("/api/admin/tasks/{id}/workspace/repair", handleRepairTaskWorkspace(deps.Workspaces))
			admin.Post("/api/admin/tasks/{id}/workspace/cleanup", handleCleanupTaskWorkspace(deps.Workspaces))
		}
		if deps.Leases != nil {
			admin.Get("/api/admin/tasks/{id}/leases", handleLeaseTimeline(deps.Leases))
			admin.Post("/api/admin/tasks/{taskID}/steps/{stepID}/lease/extend", handleExtendLease(deps.Leases))
			admin.Post("/api/admin/tasks/{taskID}/steps/{stepID}/lease/freeze", handleFreezeLease(deps.Leases))
			admin.Post("/api/admin/tasks/{taskID}/steps/{stepID}/lease/unfreeze", handleUnfreezeLease(deps.Leases))
			admin.Post("/api/admin/tasks/{taskID}/steps/{stepID}/lease/reassign", handleReassignLease(deps.Leases))
			admin.Get("/api/admin/checkpoints/{checkpointID}", handleGetCheckpoint(deps.Leases))
		}
		if deps.Executor != nil {
			admin.Get("/api/admin/executor/runs", handleListExecutorRuns(deps.Executor))
			admin.Get("/api/admin/executor/runs/{runID}", handleGetExecutorRun(deps.Executor))
			admin.Post("/api/admin/executor/scan", handleScanExecutor(deps.Executor))
			admin.Post("/api/admin/executor/runs/{runID}/stop", handleStopExecutorRun(deps.Executor))
		}
		if deps.Issues != nil {
			admin.Post("/api/admin/issues", handleCreateIssue(deps.Issues))
			admin.Get("/api/admin/issues", handleListIssues(deps.Issues))
		}
		if deps.MR != nil {
			admin.Get("/api/admin/mrs", handleListMRs(deps.MR))
			admin.Get("/api/admin/mrs/{id}", handleGetMR(deps.MR))
			admin.Get("/api/admin/manager-notifications", handleListManagerNotifications(deps.MR))
		}
		if deps.Deliveries != nil {
			admin.Get("/api/admin/deliveries", handleListDeliveries(deps.Deliveries))
			admin.Get("/api/admin/deliveries/{id}", handleGetDelivery(deps.Deliveries))
			admin.Post("/api/admin/deliveries/{id}/decisions", handleDecideDelivery(deps.Deliveries))
			admin.Get("/api/admin/tasks/{id}/rework-rounds", handleListReworkRounds(deps.Deliveries))
		}
		if deps.Pipelines != nil {
			admin.Get("/api/admin/pipelines", handleListPipelines(deps.Pipelines))
			admin.Get("/api/admin/pipelines/{id}", handleGetPipeline(deps.Pipelines))
			admin.Post("/api/admin/projects/{id}/pipelines", handleStartPipeline(deps.DB, deps.Pipelines))
			admin.Post("/api/admin/pipelines/{id}/steps/{step}/confirm", handleConfirmPipeline(deps.Pipelines))
		}
	})
	r.Group(func(agent chi.Router) {
		agent.Use(RequireAgent(deps.DB))
		if deps.Agents != nil {
			agent.Post("/api/agent/heartbeat", handleAgentHeartbeat(deps.Agents))
			agent.Post("/api/agent/token-usage", handleAgentTokenUsage(deps.Agents))
			agent.Post("/api/agent/memory/write", handleAgentMemoryWrite(deps.Agents))
			agent.Post("/api/agent/logs/write", handleAgentLogWrite(deps.Agents))
		}
		if deps.MR != nil {
			agent.Post("/api/agent/completion-reports", handleSubmitCompletionReport(deps.MR))
			agent.Get("/api/agent/mrs/{id}", handleGetAgentMR(deps.MR))
			agent.Post("/api/agent/mrs/{id}/reviews", handleReviewMR(deps.MR))
			agent.Post("/api/agent/mrs/{id}/merge", handleMergeMR(deps.MR))
		}
		if deps.Leases != nil {
			agent.Post("/api/agent/tasks/{taskID}/steps/{stepID}/lease/acquire", handleAcquireLease(deps.Leases))
			agent.Post("/api/agent/tasks/{taskID}/steps/{stepID}/lease/heartbeat", handleHeartbeatLease(deps.Leases))
			agent.Post("/api/agent/tasks/{taskID}/steps/{stepID}/lease/checkpoint", handleCreateCheckpoint(deps.Leases))
			agent.Post("/api/agent/tasks/{taskID}/steps/{stepID}/lease/resume", handleResumeLease(deps.Leases))
			agent.Get("/api/agent/tasks/{taskID}/steps/{stepID}/lease", handleGetAgentLease(deps.Leases))
		}
	})
	return r
}
