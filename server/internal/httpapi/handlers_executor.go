package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"wanxiang-agent/server/internal/executor"
)

func handleListExecutorRuns(service *executor.AdminService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, _ := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64)
		runs, err := service.ListRuns(r.Context(), taskID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runs": runs})
	}
}
func handleGetExecutorRun(service *executor.AdminService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := executorRunID(w, r)
		if !ok {
			return
		}
		detail, err := service.GetRun(r.Context(), id)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": detail})
	}
}
func handleScanExecutor(service *executor.AdminService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		count, err := service.Scan(r.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "started": count})
	}
}
func handleStopExecutorRun(service *executor.AdminService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := executorRunID(w, r)
		if !ok {
			return
		}
		if err := service.StopRun(r.Context(), id); err != nil {
			writeJSON(w, http.StatusConflict, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
func executorRunID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "runID"), 10, 64)
	if err != nil || id < 1 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid run id"})
		return 0, false
	}
	return id, true
}
