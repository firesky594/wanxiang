package leases

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func (s *Service) ExtendResumeDeadline(ctx context.Context, ref LeaseRef, deadline time.Time, actor string) (Lease, error) {
	now := s.clock.Now().UTC()
	deadline = deadline.UTC()
	if actor == "" || !deadline.After(now) {
		return Lease{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, ref.LeaseID)
	if err != nil || !sameRef(lease.LeaseRef, ref) || lease.Status != LeaseInterrupted {
		return Lease{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `update task_step_leases set resume_deadline=?,updated_at=? where lease_id=? and lease_version=? and status='interrupted'`, formatTime(deadline), formatTime(now), ref.LeaseID, ref.LeaseVersion)
	if err != nil {
		return Lease{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Lease{}, ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update task_steps set resume_deadline=? where task_id=? and id=? and lease_id=? and lease_version=? and status='interrupted'`, formatTime(deadline), ref.TaskID, ref.StepID, ref.LeaseID, ref.LeaseVersion)
	if err != nil {
		return Lease{}, err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return Lease{}, ErrConflict
	}
	if err := insertAudit(ctx, tx, actor, "lease.extend_resume_deadline", stepTarget(ref.TaskID, ref.StepID), map[string]any{"lease_id": ref.LeaseID, "deadline": formatTime(deadline)}, now); err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	lease.ResumeDeadline = &deadline
	return lease, nil
}

func (s *Service) FreezeStep(ctx context.Context, taskID, stepID int64, actor, reason string) error {
	if actor == "" || taskID <= 0 || stepID <= 0 {
		return ErrConflict
	}
	now := s.clock.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var leaseID string
	var version int64
	if err := tx.QueryRowContext(ctx, `select lease_id,lease_version from task_steps where task_id=? and id=?`, taskID, stepID).Scan(&leaseID, &version); err != nil || leaseID == "" {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `update task_step_leases set status='frozen',revoked_at=?,revoked_reason=?,updated_at=? where lease_id=? and lease_version=? and status in ('active','interrupted')`, formatTime(now), reason, formatTime(now), leaseID, version)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update task_steps set status='blocked',lease_expires_at=null,resume_deadline=null where task_id=? and id=? and lease_id=? and lease_version=?`, taskID, stepID, leaseID, version)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return ErrConflict
	}
	if err := insertAudit(ctx, tx, actor, "lease.freeze", stepTarget(taskID, stepID), map[string]any{"lease_id": leaseID, "lease_version": version, "reason": reason}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) UnfreezeStep(ctx context.Context, taskID, stepID int64, actor string) (Lease, error) {
	if actor == "" {
		return Lease{}, ErrConflict
	}
	now := s.clock.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	var oldID, agent, branch, worktree, leaseStatus, workspaceStatus string
	var version, attempt int64
	err = tx.QueryRowContext(ctx, `select ts.lease_id,ts.lease_version,ts.agent_name,ts.attempt,l.status,pw.branch_name,pw.worktree_path,pw.status
		from task_steps ts join task_step_leases l on l.lease_id=ts.lease_id
		join project_workspaces pw on pw.task_id=ts.task_id and pw.step_id=ts.id and pw.agent_name=ts.agent_name
		where ts.task_id=? and ts.id=?`, taskID, stepID).Scan(&oldID, &version, &agent, &attempt, &leaseStatus, &branch, &worktree, &workspaceStatus)
	if err != nil || leaseStatus != string(LeaseFrozen) || workspaceStatus != "ready" {
		return Lease{}, ErrConflict
	}
	leaseID, err := randomLeaseID()
	if err != nil {
		return Lease{}, err
	}
	version++
	expires := now.Add(LeaseTTL)
	_, err = tx.ExecContext(ctx, `insert into task_step_leases(task_id,step_id,agent_name,lease_id,lease_version,status,branch_name,worktree_path,acquired_at,expires_at,last_heartbeat_at,created_at,updated_at) values(?,?,?,?,?,'active',?,?,?,?,?,?,?)`, taskID, stepID, agent, leaseID, version, branch, worktree, formatTime(now), formatTime(expires), formatTime(now), formatTime(now), formatTime(now))
	if err != nil {
		return Lease{}, err
	}
	result, err := tx.ExecContext(ctx, `update task_steps set status='in_progress',lease_id=?,lease_version=?,lease_expires_at=?,last_heartbeat_at=?,attempt=?,interrupted_at=null,resume_deadline=null where task_id=? and id=? and lease_id=?`, leaseID, version, formatTime(expires), formatTime(now), attempt+1, taskID, stepID, oldID)
	if err != nil {
		return Lease{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Lease{}, ErrConflict
	}
	if err := insertAudit(ctx, tx, actor, "lease.unfreeze", stepTarget(taskID, stepID), map[string]any{"old_lease_id": oldID, "new_lease_id": leaseID, "lease_version": version}, now); err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	heartbeat := now
	return Lease{LeaseRef: LeaseRef{TaskID: taskID, StepID: stepID, AgentName: agent, LeaseID: leaseID, LeaseVersion: version}, Status: LeaseActive, AcquiredAt: now, ExpiresAt: expires, LastHeartbeatAt: &heartbeat}, nil
}

func insertAudit(ctx context.Context, tx *sql.Tx, actor, action, target string, payload any, now time.Time) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `insert into audit_logs(actor,action,target,payload_json,created_at) values(?,?,?,?,?)`, actor, action, target, string(encoded), formatTime(now))
	return err
}

func stepTarget(taskID, stepID int64) string {
	return fmt.Sprintf("task/%d/step/%d", taskID, stepID)
}
