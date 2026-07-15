package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/issues"
	"wanxiang-agent/server/internal/mr"
	"wanxiang-agent/server/internal/tasks"
)

func handleListTasks(svc *tasks.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, offset, ok := pagination(w, r)
		if !ok {
			return
		}
		items, err := svc.List(r.Context(), limit, offset)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tasks": items})
	}
}

func handleListProjects(svc *tasks.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, offset, ok := pagination(w, r)
		if !ok {
			return
		}
		items, err := svc.ListProjects(r.Context(), limit, offset)
		if err != nil {
			writeJSON(w, 500, errorBody(err))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "projects": items})
	}
}

func handleGetTask(svc *tasks.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := resourceID(w, r)
		if !ok {
			return
		}
		item, err := svc.Get(r.Context(), id)
		if errors.Is(err, tasks.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorBody(err))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": item})
	}
}

func handleUpdateTaskStatus(svc *tasks.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := resourceID(w, r)
		if !ok {
			return
		}
		var input struct {
			Status string `json:"status"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Status == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "status is required"})
			return
		}
		actor, _ := AdminIdentity(r.Context())
		item, err := svc.UpdateStatus(r.Context(), id, input.Status, actor)
		if errors.Is(err, tasks.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorBody(err))
			return
		}
		if errors.Is(err, tasks.ErrInvalidTransition) {
			writeJSON(w, http.StatusConflict, errorBody(err))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": item})
	}
}

func handleListMRs(svc *mr.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, offset, ok := pagination(w, r)
		if !ok {
			return
		}
		taskID, ok := optionalTaskID(w, r)
		if !ok {
			return
		}
		items, err := svc.AdminList(r.Context(), taskID, limit, offset)
		if err != nil {
			writeJSON(w, 500, errorBody(err))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "merge_requests": items})
	}
}

func handleGetMR(svc *mr.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := resourceID(w, r)
		if !ok {
			return
		}
		detail, err := svc.AdminDetail(r.Context(), id)
		if errors.Is(err, mr.ErrStateConflict) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "merge request not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "merge request query failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": detail})
	}
}

func handleListIssues(svc *issues.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, offset, ok := pagination(w, r)
		if !ok {
			return
		}
		taskID, ok := optionalTaskID(w, r)
		if !ok {
			return
		}
		items, err := svc.List(r.Context(), taskID, limit, offset)
		if err != nil {
			writeJSON(w, 500, errorBody(err))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "issues": items})
	}
}

func handleListEvents(bus *events.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := resourceID(w, r)
		if !ok {
			return
		}
		limit, offset, ok := pagination(w, r)
		if !ok {
			return
		}
		items, err := bus.List(r.Context(), &id, limit, offset)
		if err != nil {
			writeJSON(w, 500, errorBody(err))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "events": items})
	}
}

func pagination(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, offset := 20, 0
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "limit must be between 1 and 100"})
			return 0, 0, false
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "offset must be zero or greater"})
			return 0, 0, false
		}
	}
	return limit, offset, true
}

func optionalTaskID(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	raw := r.URL.Query().Get("task_id")
	if raw == "" {
		return nil, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "task_id must be positive"})
		return nil, false
	}
	return &id, true
}
func resourceID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "valid id is required"})
		return 0, false
	}
	return id, true
}
func errorBody(err error) map[string]any { return map[string]any{"ok": false, "error": err.Error()} }
