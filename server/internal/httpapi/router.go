package httpapi

import (
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"
	"wanxiang-agent/server/internal/agents"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/issues"
	"wanxiang-agent/server/internal/mr"
	"wanxiang-agent/server/internal/tasks"
)

type Dependencies struct {
	DB       *sql.DB
	Agents   *agents.Service
	Launcher *agents.Launcher
	Bus      *events.Bus
	Tasks    *tasks.Service
	MR       *mr.Service
	Issues   *issues.Service
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
			admin.Post("/api/admin/tasks", handleCreateTask(deps.Tasks))
		}
		if deps.Issues != nil {
			admin.Post("/api/admin/issues", handleCreateIssue(deps.Issues))
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
			agent.Post("/api/agent/mr/create", handleCreateMR(deps.MR))
			agent.Post("/api/agent/mr/{id}/merge", handleManagerMerge(deps.MR))
		}
	})
	return r
}
