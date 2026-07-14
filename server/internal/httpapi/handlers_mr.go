package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"wanxiang-agent/server/internal/mr"
)

type createMRRequest struct {
	ProjectID    int64  `json:"project_id"`
	TaskID       int64  `json:"task_id"`
	Title        string `json:"title"`
	SourceBranch string `json:"source_branch"`
	CreatedBy    string `json:"created_by"`
}

func handleCreateMR(svc *mr.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createMRRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ProjectID == 0 || req.TaskID == 0 || req.Title == "" || req.SourceBranch == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "project_id, task_id, title, and source_branch are required"})
			return
		}
		createdBy, _ := AgentIdentity(r.Context())
		created, err := svc.Create(r.Context(), req.ProjectID, req.TaskID, req.Title, req.SourceBranch, createdBy)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "merge_request": created})
	}
}

func handleManagerMerge(svc *mr.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mrID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || mrID == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "valid mr id is required"})
			return
		}
		actor, _ := AgentIdentity(r.Context())
		if err := svc.ManagerMerge(r.Context(), mrID, actor); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
