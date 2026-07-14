package httpapi

import (
	"encoding/json"
	"net/http"

	"wanxiang-agent/server/internal/tasks"
)

type createTaskRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

func handleCreateTask(svc *tasks.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "title is required"})
			return
		}
		actor, _ := AdminIdentity(r.Context())
		task, err := svc.CreateTask(r.Context(), req.Title, req.Description, actor)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": task})
	}
}
