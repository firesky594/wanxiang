package httpapi

import (
	"encoding/json"
	"net/http"

	"wanxiang-agent/server/internal/agents"
)

type managerSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func handleManagerStatus(svc *agents.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := svc.EnsureManager(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "manager": status})
	}
}

func handleManagerSecrets(svc *agents.Service, launcher *agents.Launcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req managerSecretRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key != "MANAGER_API_KEY" || req.Value == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "MANAGER_API_KEY and value are required"})
			return
		}
		if err := svc.SaveManagerSecret(r.Context(), req.Key, req.Value); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if launcher != nil {
			if _, err := launcher.Start(r.Context()); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not start manager"})
				return
			}
		} else if _, err := svc.EnsureManager(r.Context()); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not refresh manager"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
