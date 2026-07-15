package httpapi

import (
	"encoding/json"
	"net/http"
	"wanxiang-agent/server/internal/workspaces"
)

func handleGetTaskWorkspace(service *workspaces.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := resourceID(w, r)
		if !ok {
			return
		}
		view, err := service.GetTask(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace": view})
	}
}
func handleReconcileTaskWorkspace(service *workspaces.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := resourceID(w, r)
		if !ok {
			return
		}
		view, err := service.ReconcileTask(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusConflict, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace": view})
	}
}
func handleRepairTaskWorkspace(service *workspaces.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := resourceID(w, r)
		if !ok {
			return
		}
		var input struct {
			Direction workspaces.RepairDirection `json:"direction"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || (input.Direction != workspaces.RepairFromDatabase && input.Direction != workspaces.RepairFromGitSnapshot) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "direction must be database or git_snapshot"})
			return
		}
		actor, _ := AdminIdentity(r.Context())
		view, err := service.RepairTask(r.Context(), id, input.Direction, actor)
		if err != nil {
			writeJSON(w, http.StatusConflict, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace": view})
	}
}
func handleCleanupTaskWorkspace(service *workspaces.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := resourceID(w, r)
		if !ok {
			return
		}
		var input struct {
			Action    string `json:"action"`
			Confirmed bool   `json:"confirmed"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || (input.Action != "request" && input.Action != "confirm") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "action must be request or confirm"})
			return
		}
		actor, _ := AdminIdentity(r.Context())
		var view workspaces.TaskWorkspace
		var err error
		if input.Action == "request" {
			view, err = service.RequestCleanup(r.Context(), id, input.Confirmed, actor)
		} else {
			view, err = service.ConfirmCleanup(r.Context(), id, actor)
		}
		if err != nil {
			writeJSON(w, http.StatusConflict, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace": view})
	}
}
