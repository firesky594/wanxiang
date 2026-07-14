package httpapi

import (
	"encoding/json"
	"net/http"

	"wanxiang-agent/server/internal/agents"
)

type agentFileWriteRequest struct {
	AgentName string `json:"agent_name"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

func handleAgentHeartbeat(svc *agents.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input agents.HeartbeatInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
			return
		}
		input.Name, _ = AgentIdentity(r.Context())
		if err := svc.Heartbeat(r.Context(), input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleAgentTokenUsage(svc *agents.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input agents.TokenUsageInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
			return
		}
		input.AgentName, _ = AgentIdentity(r.Context())
		if err := svc.RecordTokenUsage(r.Context(), input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleAgentMemoryWrite(svc *agents.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req agentFileWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "path is required"})
			return
		}
		agentName, _ := AgentIdentity(r.Context())
		if err := svc.WriteMemory(r.Context(), agentName, req.Path, req.Content); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleAgentLogWrite(svc *agents.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req agentFileWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "path is required"})
			return
		}
		agentName, _ := AgentIdentity(r.Context())
		if err := svc.WriteLog(r.Context(), agentName, req.Path, req.Content); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
