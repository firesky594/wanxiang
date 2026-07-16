package leases

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrConflict = errors.New("lease conflict")

type WorkspaceAuthorizer interface {
	AuthorizeAgent(context.Context, string, int64, int64, string) error
}

type Service struct {
	db         *sql.DB
	clock      Clock
	workspaces WorkspaceAuthorizer
}

func NewService(db *sql.DB, clock Clock, workspaces WorkspaceAuthorizer) *Service {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Service{db: db, clock: clock, workspaces: workspaces}
}

func (s *Service) Acquire(ctx context.Context, taskID, stepID int64, agent string) (Lease, error) {
	if taskID <= 0 || stepID <= 0 || agent == "" {
		return Lease{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()

	var taskStatus, stepStatus, owner, workspaceStatus, currentID string
	var currentVersion, attempt int64
	err = tx.QueryRowContext(ctx, `select t.status,ts.status,ta.agent_name,pw.status,ts.lease_id,ts.lease_version,ts.attempt
		from task_steps ts
		join tasks t on t.id=ts.task_id
		join task_assignments ta on ta.task_id=ts.task_id and ta.step_id=ts.id
		join project_workspaces pw on pw.task_id=ts.task_id and pw.step_id=ts.id and pw.agent_name=ta.agent_name
		where ts.task_id=? and ts.id=?`, taskID, stepID).
		Scan(&taskStatus, &stepStatus, &owner, &workspaceStatus, &currentID, &currentVersion, &attempt)
	if err != nil || taskStatus != "workspace_ready" || workspaceStatus != "ready" || owner != agent {
		return Lease{}, ErrConflict
	}
	now := s.clock.Now().UTC()
	if currentID != "" {
		current, loadErr := loadLease(ctx, tx, currentID)
		if loadErr != nil {
			return Lease{}, ErrConflict
		}
		if current.AgentName == agent && current.Status == LeaseActive && now.Before(current.ExpiresAt) {
			if err := tx.Commit(); err != nil {
				return Lease{}, err
			}
			return current, nil
		}
		return Lease{}, ErrConflict
	}
	if stepStatus != "assigned" && stepStatus != "workspace_ready" {
		return Lease{}, ErrConflict
	}

	leaseID, err := randomLeaseID()
	if err != nil {
		return Lease{}, err
	}
	version := currentVersion + 1
	expires := now.Add(LeaseTTL)
	result, err := tx.ExecContext(ctx, `update task_steps set status='in_progress',lease_id=?,lease_version=?,lease_expires_at=?,last_heartbeat_at=?,attempt=? where id=? and task_id=? and lease_id='' and lease_version=?`, leaseID, version, formatTime(expires), formatTime(now), attempt+1, stepID, taskID, currentVersion)
	if err != nil {
		return Lease{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Lease{}, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `insert into task_step_leases(task_id,step_id,agent_name,lease_id,lease_version,status,acquired_at,expires_at,last_heartbeat_at,created_at,updated_at) values(?,?,?,?,?,'active',?,?,?,?,?)`, taskID, stepID, agent, leaseID, version, formatTime(now), formatTime(expires), formatTime(now), formatTime(now), formatTime(now))
	if err != nil {
		return Lease{}, err
	}
	payload, _ := json.Marshal(map[string]any{"step_id": stepID, "lease_id": leaseID, "lease_version": version, "expires_at": formatTime(expires)})
	if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.step.lease.acquired',?,?,?)`, taskID, agent, string(payload), formatTime(now)); err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	heartbeat := now
	return Lease{LeaseRef: LeaseRef{TaskID: taskID, StepID: stepID, AgentName: agent, LeaseID: leaseID, LeaseVersion: version}, Status: LeaseActive, AcquiredAt: now, ExpiresAt: expires, LastHeartbeatAt: &heartbeat}, nil
}

func (s *Service) Heartbeat(ctx context.Context, ref LeaseRef) (Lease, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, ref.LeaseID)
	now := s.clock.Now().UTC()
	if err != nil || !sameRef(lease.LeaseRef, ref) || lease.Status != LeaseActive || !now.Before(lease.ExpiresAt) {
		return Lease{}, ErrConflict
	}
	expires := now.Add(LeaseTTL)
	result, err := tx.ExecContext(ctx, `update task_step_leases set expires_at=?,last_heartbeat_at=?,updated_at=? where lease_id=? and lease_version=? and status='active'`, formatTime(expires), formatTime(now), formatTime(now), ref.LeaseID, ref.LeaseVersion)
	if err != nil {
		return Lease{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Lease{}, ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update task_steps set lease_expires_at=?,last_heartbeat_at=? where task_id=? and id=? and lease_id=? and lease_version=?`, formatTime(expires), formatTime(now), ref.TaskID, ref.StepID, ref.LeaseID, ref.LeaseVersion)
	if err != nil {
		return Lease{}, err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return Lease{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	lease.ExpiresAt = expires
	lease.LastHeartbeatAt = &now
	return lease, nil
}

func randomLeaseID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "lease_" + hex.EncodeToString(value), nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadLease(ctx context.Context, query rowQuerier, leaseID string) (Lease, error) {
	if leaseID == "" {
		return Lease{}, sql.ErrNoRows
	}
	var lease Lease
	var status, acquired, expires string
	var heartbeat, interrupted, deadline sql.NullString
	err := query.QueryRowContext(ctx, `select task_id,step_id,agent_name,lease_id,lease_version,status,acquired_at,expires_at,last_heartbeat_at,interrupted_at,resume_deadline from task_step_leases where lease_id=?`, leaseID).
		Scan(&lease.TaskID, &lease.StepID, &lease.AgentName, &lease.LeaseID, &lease.LeaseVersion, &status, &acquired, &expires, &heartbeat, &interrupted, &deadline)
	if err != nil {
		return Lease{}, err
	}
	lease.Status = LeaseStatus(status)
	if lease.AcquiredAt, err = time.Parse(time.RFC3339Nano, acquired); err != nil {
		return Lease{}, fmt.Errorf("parse acquired_at: %w", err)
	}
	if lease.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires); err != nil {
		return Lease{}, fmt.Errorf("parse expires_at: %w", err)
	}
	if lease.LastHeartbeatAt, err = parseNullableTime(heartbeat); err != nil {
		return Lease{}, err
	}
	if lease.InterruptedAt, err = parseNullableTime(interrupted); err != nil {
		return Lease{}, err
	}
	if lease.ResumeDeadline, err = parseNullableTime(deadline); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func sameRef(left, right LeaseRef) bool {
	return left.TaskID == right.TaskID && left.StepID == right.StepID && left.AgentName == right.AgentName && left.LeaseID == right.LeaseID && left.LeaseVersion == right.LeaseVersion
}
