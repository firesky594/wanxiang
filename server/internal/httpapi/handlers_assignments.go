package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"wanxiang-agent/server/internal/assignments"
)

func handleGetTaskMatch(svc *assignments.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := resourceID(w, r)
		if !ok {
			return
		}
		view, err := svc.GetTaskMatch(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorBody(err))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "match": view})
	}
}

func handleOverrideTaskMatch(svc *assignments.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := resourceID(w, r)
		if !ok {
			return
		}
		var input struct {
			StepID    int64  `json:"step_id"`
			AgentName string `json:"agent_name"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.StepID < 1 || input.AgentName == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "step_id and agent_name are required"})
			return
		}
		actor, _ := AdminIdentity(r.Context())
		if err := svc.Override(r.Context(), id, input.StepID, input.AgentName, actor); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, errorBody(err))
				return
			}
			writeJSON(w, http.StatusConflict, errorBody(err))
			return
		}
		view, err := svc.GetTaskMatch(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "match": view})
	}
}
