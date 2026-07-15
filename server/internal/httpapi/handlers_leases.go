package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"wanxiang-agent/server/internal/leases"
)

type leaseCredential struct {
	LeaseID      string `json:"lease_id"`
	LeaseVersion int64  `json:"lease_version"`
}

func handleAcquireLease(service *leases.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, stepID, ok := taskStepIDs(w, r)
		if !ok {
			return
		}
		agent, _ := AgentIdentity(r.Context())
		lease, err := service.Acquire(r.Context(), taskID, stepID, agent)
		writeLeaseResult(w, lease, err)
	}
}

func handleHeartbeatLease(service *leases.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, stepID, ok := taskStepIDs(w, r)
		if !ok {
			return
		}
		var input leaseCredential
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid lease request"})
			return
		}
		agent, _ := AgentIdentity(r.Context())
		lease, err := service.Heartbeat(r.Context(), leases.LeaseRef{TaskID: taskID, StepID: stepID, AgentName: agent, LeaseID: input.LeaseID, LeaseVersion: input.LeaseVersion})
		writeLeaseResult(w, lease, err)
	}
}

func handleResumeLease(service *leases.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, stepID, ok := taskStepIDs(w, r)
		if !ok {
			return
		}
		var input leaseCredential
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid lease request"})
			return
		}
		agent, _ := AgentIdentity(r.Context())
		lease, err := service.Resume(r.Context(), leases.LeaseRef{TaskID: taskID, StepID: stepID, AgentName: agent, LeaseID: input.LeaseID, LeaseVersion: input.LeaseVersion})
		writeLeaseResult(w, lease, err)
	}
}

func handleCreateCheckpoint(service *leases.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, stepID, ok := taskStepIDs(w, r)
		if !ok {
			return
		}
		var input struct {
			leaseCredential
			leases.CheckpointInput
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid checkpoint request"})
			return
		}
		agent, _ := AgentIdentity(r.Context())
		checkpoint, err := service.CreateCheckpoint(r.Context(), leases.LeaseRef{TaskID: taskID, StepID: stepID, AgentName: agent, LeaseID: input.LeaseID, LeaseVersion: input.LeaseVersion}, input.CheckpointInput)
		if err != nil {
			writeLeaseError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "checkpoint": checkpoint})
	}
}

func handleGetAgentLease(service *leases.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, stepID, ok := taskStepIDs(w, r)
		if !ok {
			return
		}
		agent, _ := AgentIdentity(r.Context())
		lease, err := service.CurrentForAgent(r.Context(), taskID, stepID, agent)
		writeLeaseResult(w, lease, err)
	}
}

func handleLeaseTimeline(service *leases.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, ok := resourceID(w, r)
		if !ok {
			return
		}
		timeline, err := service.Timeline(r.Context(), taskID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "timeline": timeline})
	}
}

func handleExtendLease(service *leases.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, stepID, ok := taskStepIDs(w, r)
		if !ok {
			return
		}
		var input struct {
			leaseCredential
			ResumeDeadline string `json:"resume_deadline"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid lease request"})
			return
		}
		deadline, err := time.Parse(time.RFC3339Nano, input.ResumeDeadline)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid resume_deadline"})
			return
		}
		actor, _ := AdminIdentity(r.Context())
		lease, err := service.ExtendResumeDeadline(r.Context(), leases.LeaseRef{TaskID: taskID, StepID: stepID, LeaseID: input.LeaseID, LeaseVersion: input.LeaseVersion, AgentName: currentLeaseAgent(r.Context(), service, taskID, stepID)}, deadline, actor)
		writeLeaseResult(w, lease, err)
	}
}

func handleFreezeLease(service *leases.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, stepID, ok := taskStepIDs(w, r)
		if !ok {
			return
		}
		var input struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		actor, _ := AdminIdentity(r.Context())
		if err := service.FreezeStep(r.Context(), taskID, stepID, actor, input.Reason); err != nil {
			writeLeaseError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleUnfreezeLease(service *leases.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, stepID, ok := taskStepIDs(w, r)
		if !ok {
			return
		}
		actor, _ := AdminIdentity(r.Context())
		lease, err := service.UnfreezeStep(r.Context(), taskID, stepID, actor)
		writeLeaseResult(w, lease, err)
	}
}

func handleReassignLease(service *leases.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, stepID, ok := taskStepIDs(w, r)
		if !ok {
			return
		}
		var input leases.ReassignInput
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid reassign request"})
			return
		}
		input.TaskID, input.StepID = taskID, stepID
		actor, _ := AdminIdentity(r.Context())
		lease, err := service.Reassign(r.Context(), input, actor)
		writeLeaseResult(w, lease, err)
	}
}

func handleGetCheckpoint(service *leases.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "checkpointID"), 10, 64)
		if err != nil || id <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid checkpoint id"})
			return
		}
		checkpoint, err := service.GetCheckpointDetail(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "checkpoint not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "checkpoint": checkpoint})
	}
}

func currentLeaseAgent(ctx context.Context, service *leases.Service, taskID, stepID int64) string {
	timeline, err := service.Timeline(ctx, taskID)
	if err != nil {
		return ""
	}
	for _, lease := range timeline.Leases {
		if lease.StepID == stepID {
			return lease.AgentName
		}
	}
	return ""
}

func taskStepIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	taskID, err1 := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	stepID, err2 := strconv.ParseInt(chi.URLParam(r, "stepID"), 10, 64)
	if err1 != nil || err2 != nil || taskID <= 0 || stepID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid task or step id"})
		return 0, 0, false
	}
	return taskID, stepID, true
}

func writeLeaseResult(w http.ResponseWriter, lease leases.Lease, err error) {
	if err != nil {
		writeLeaseError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "lease": lease})
}

func writeLeaseError(w http.ResponseWriter, err error) {
	if errors.Is(err, leases.ErrConflict) || errors.Is(err, leases.ErrRecoveryReview) {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "lease conflict"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "lease operation failed"})
}
