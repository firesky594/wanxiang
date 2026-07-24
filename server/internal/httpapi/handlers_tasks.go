package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"wanxiang-agent/server/internal/tasks"
)

type createTaskRequest struct {
	Title          string `json:"title"`
	Description    string `json:"description"`
	ProjectID      *int64 `json:"project_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func handleCreateTask(svc *tasks.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "title is required"})
			return
		}
		actor, _ := AdminIdentity(r.Context())
		task, err := svc.CreateTaskWithInput(r.Context(), tasks.CreateTaskInput{
			Title: req.Title, Description: req.Description, ProjectID: req.ProjectID, IdempotencyKey: req.IdempotencyKey,
		}, actor)
		if err != nil {
			if errors.Is(err, tasks.ErrInvalidInput) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			if errors.Is(err, tasks.ErrProjectNotFound) {
				writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			if errors.Is(err, tasks.ErrProjectConflict) || errors.Is(err, tasks.ErrIdempotencyConflict) {
				writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": task})
	}
}
