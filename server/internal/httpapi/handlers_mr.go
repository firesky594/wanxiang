package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"wanxiang-agent/server/internal/mr"
)

func handleSubmitCompletionReport(svc *mr.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input mr.CompletionReportInput
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			writeJSON(w, http.StatusBadRequest, errorCode("invalid_report"))
			return
		}
		principal, _ := AgentPrincipal(r.Context())
		if principal.Name != input.AgentName || principal.Role != input.Role {
			writeJSON(w, http.StatusForbidden, errorCode("identity_mismatch"))
			return
		}
		if err := input.Validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, errorCode("invalid_report"))
			return
		}
		detail, err := svc.SubmitReport(r.Context(), mr.Principal{Name: principal.Name, Role: principal.Role}, input)
		if err != nil {
			writeMRError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "detail": detail})
	}
}

func handleGetAgentMR(svc *mr.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := mrID(w, r)
		if !ok {
			return
		}
		principal, _ := AgentPrincipal(r.Context())
		detail, err := svc.Detail(r.Context(), mr.Principal{Name: principal.Name, Role: principal.Role}, id)
		if err != nil {
			writeMRError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": detail})
	}
}

func handleReviewMR(svc *mr.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := mrID(w, r)
		if !ok {
			return
		}
		var input mr.ReviewInput
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			writeJSON(w, http.StatusBadRequest, errorCode("invalid_review"))
			return
		}
		principal, _ := AgentPrincipal(r.Context())
		detail, err := svc.Review(r.Context(), mr.Principal{Name: principal.Name, Role: principal.Role}, id, input)
		if err != nil {
			writeMRError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": detail})
	}
}

func handleMergeMR(svc *mr.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := mrID(w, r)
		if !ok {
			return
		}
		var input mr.MergeInput
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			writeJSON(w, http.StatusBadRequest, errorCode("invalid_merge"))
			return
		}
		principal, _ := AgentPrincipal(r.Context())
		result, err := svc.Merge(r.Context(), mr.Principal{Name: principal.Name, Role: principal.Role}, id, input)
		if err != nil {
			writeMRError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
	}
}

func mrID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, errorCode("invalid_mr_id"))
		return 0, false
	}
	return id, true
}

func writeMRError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mr.ErrIdentityMismatch):
		writeJSON(w, http.StatusForbidden, errorCode("identity_mismatch"))
	case errors.Is(err, mr.ErrLeaseInvalid):
		writeJSON(w, http.StatusConflict, errorCode("lease_invalid"))
	case errors.Is(err, mr.ErrCheckpointMismatch):
		writeJSON(w, http.StatusConflict, errorCode("checkpoint_mismatch"))
	case errors.Is(err, mr.ErrBranchOwnership):
		writeJSON(w, http.StatusConflict, errorCode("branch_ownership"))
	case errors.Is(err, mr.ErrMergeBlocked):
		writeJSON(w, http.StatusConflict, errorCode("merge_blocked"))
	case errors.Is(err, mr.ErrStateConflict):
		writeJSON(w, http.StatusConflict, errorCode("state_conflict"))
	default:
		writeJSON(w, http.StatusInternalServerError, errorCode("mr_operation_failed"))
	}
}

func errorCode(code string) map[string]any { return map[string]any{"ok": false, "error": code} }
