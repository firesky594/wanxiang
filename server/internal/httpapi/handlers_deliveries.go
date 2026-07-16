package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"wanxiang-agent/server/internal/deliveries"
)

func deliveryID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, errorCode("invalid_delivery_id"))
		return 0, false
	}
	return id, true
}
func handleListDeliveries(s *deliveries.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var taskID *int64
		if raw := r.URL.Query().Get("task_id"); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				writeJSON(w, 400, errorCode("invalid_task_id"))
				return
			}
			taskID = &id
		}
		items, err := s.List(r.Context(), taskID, 100, 0)
		if err != nil {
			writeJSON(w, 500, errorCode("delivery_query_failed"))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "deliveries": items})
	}
}
func handleGetDelivery(s *deliveries.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := deliveryID(w, r)
		if !ok {
			return
		}
		detail, err := s.Detail(r.Context(), id)
		if errors.Is(err, deliveries.ErrNotFound) {
			writeJSON(w, 404, errorCode("delivery_not_found"))
			return
		}
		if err != nil {
			writeJSON(w, 500, errorCode("delivery_query_failed"))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "detail": detail})
	}
}
func handleDecideDelivery(s *deliveries.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := deliveryID(w, r)
		if !ok {
			return
		}
		var in deliveries.DecisionInput
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			writeJSON(w, 400, errorCode("invalid_decision"))
			return
		}
		in.CreatedBy, _ = AdminIdentity(r.Context())
		result, err := s.Decide(r.Context(), id, in)
		if err != nil {
			writeDeliveryError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "result": result})
	}
}
func handleListReworkRounds(s *deliveries.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := deliveryID(w, r)
		if !ok {
			return
		}
		items, err := s.ListRework(r.Context(), id)
		if err != nil {
			writeJSON(w, 500, errorCode("rework_query_failed"))
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "rework_rounds": items})
	}
}
func writeDeliveryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, deliveries.ErrNotFound):
		writeJSON(w, 404, errorCode("delivery_not_found"))
	case errors.Is(err, deliveries.ErrDecisionCommentRequired):
		writeJSON(w, 400, errorCode("decision_comment_required"))
	case errors.Is(err, deliveries.ErrNotReady):
		writeJSON(w, 409, errorCode("delivery_not_ready"))
	case errors.Is(err, deliveries.ErrStaleSnapshot):
		writeJSON(w, 409, errorCode("stale_snapshot"))
	case errors.Is(err, deliveries.ErrAcceptanceClosed):
		writeJSON(w, 409, errorCode("acceptance_closed"))
	default:
		writeJSON(w, 400, errorCode("invalid_decision"))
	}
}
