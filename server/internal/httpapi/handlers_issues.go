package httpapi

import (
	"encoding/json"
	"net/http"

	"wanxiang-agent/server/internal/issues"
)

func handleCreateIssue(svc *issues.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input issues.CreateIssueInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
			return
		}
		input.CreatedBy, _ = AdminIdentity(r.Context())
		issue, err := svc.Create(r.Context(), input)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "issue": issue})
	}
}
